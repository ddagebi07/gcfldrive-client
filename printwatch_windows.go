package main

import (
	"fmt"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ddagebi07/gcfldrive-client/internal/guard"
)

// The school allows at most guard.MaxSheetsPerJob sheets per print job, no matter
// which program prints. Every job goes through the Windows print queue, so the
// service watches the queue: a new job is held until it is fully spooled, then
// either released or cancelled with a notice on the screen.
//
// Limits of this approach (also written in the README): sheets are estimated from
// the spooler's page count, the copies and the duplex setting; a job whose data
// type has no page count (some RAW drivers) cannot be measured and is let through.

var (
	winspool          = windows.NewLazySystemDLL("winspool.drv")
	procEnumPrinters  = winspool.NewProc("EnumPrintersW")
	procOpenPrinter   = winspool.NewProc("OpenPrinterW")
	procClosePrinter  = winspool.NewProc("ClosePrinter")
	procEnumJobs      = winspool.NewProc("EnumJobsW")
	procSetJob        = winspool.NewProc("SetJobW")
	wtsapi            = windows.NewLazySystemDLL("wtsapi32.dll")
	procWTSSendMesage = wtsapi.NewProc("WTSSendMessageW")
)

const (
	printerEnumLocal       = 0x2
	printerEnumConnections = 0x4

	jobControlPause  = 1
	jobControlResume = 2
	jobControlDelete = 5

	jobStatusPaused   = 0x1
	jobStatusDeleting = 0x4
	jobStatusSpooling = 0x8
	jobStatusPrinting = 0x10
	jobStatusPrinted  = 0x80
	jobStatusDeleted  = 0x100
	jobStatusComplete = 0x1000

	dmCopies = 0x100
	dmDuplex = 0x1000

	holdTimeout = 2 * time.Minute // a job that never finishes spooling is released
)

// jobInfo2 is JOB_INFO_2W.
type jobInfo2 struct {
	JobID          uint32
	PrinterName    *uint16
	MachineName    *uint16
	UserName       *uint16
	Document       *uint16
	NotifyName     *uint16
	Datatype       *uint16
	PrintProcessor *uint16
	Parameters     *uint16
	DriverName     *uint16
	DevMode        *byte
	Status         *uint16
	SecurityDesc   uintptr
	StatusFlags    uint32
	Priority       uint32
	Position       uint32
	StartTime      uint32
	UntilTime      uint32
	TotalPages     uint32
	Size           uint32
	Submitted      windows.Systemtime
	Time           uint32
	PagesPrinted   uint32
}

type printerInfo4 struct {
	PrinterName *uint16
	ServerName  *uint16
	Attributes  uint32
}

type watched struct {
	first   time.Time
	decided bool
}

type printWatcher struct {
	active func() bool // false while the administrator's maintenance window is open
	notify func(document string, sheets int)
	mu     sync.Mutex
	seen   map[string]*watched
	notice map[string]time.Time
}

func newPrintWatcher(active func() bool, notify func(string, int)) *printWatcher {
	return &printWatcher{active: active, notify: notify, seen: map[string]*watched{}, notice: map[string]time.Time{}}
}

func (p *printWatcher) run(stop <-chan struct{}) {
	t := time.NewTicker(400 * time.Millisecond)
	defer t.Stop()
	var printers []string
	var refreshed time.Time
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		if !p.active() {
			continue
		}
		if time.Since(refreshed) > 5*time.Second {
			printers, refreshed = listPrinters(), time.Now()
		}
		live := map[string]bool{}
		for _, name := range printers {
			p.scan(name, live)
		}
		for key := range p.seen { // forget finished jobs
			if !live[key] {
				delete(p.seen, key)
			}
		}
	}
}

func listPrinters() []string {
	var needed, returned uint32
	flags := uintptr(printerEnumLocal | printerEnumConnections)
	_, _, _ = procEnumPrinters.Call(flags, 0, 4, 0, 0, uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&returned)))
	if needed == 0 {
		return nil
	}
	buf := make([]byte, needed)
	r, _, _ := procEnumPrinters.Call(flags, 0, 4, uintptr(unsafe.Pointer(&buf[0])), uintptr(needed), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&returned)))
	if r == 0 {
		return nil
	}
	infos := unsafe.Slice((*printerInfo4)(unsafe.Pointer(&buf[0])), returned)
	names := make([]string, 0, returned)
	for _, in := range infos {
		names = append(names, windows.UTF16PtrToString(in.PrinterName))
	}
	return names
}

func (p *printWatcher) scan(printer string, live map[string]bool) {
	name, err := windows.UTF16PtrFromString(printer)
	if err != nil {
		return
	}
	var h windows.Handle
	if r, _, _ := procOpenPrinter.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&h)), 0); r == 0 {
		return
	}
	defer procClosePrinter.Call(uintptr(h))

	var needed, returned uint32
	_, _, _ = procEnumJobs.Call(uintptr(h), 0, 0xFFFFFFFF, 2, 0, 0, uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&returned)))
	if needed == 0 {
		return
	}
	buf := make([]byte, needed)
	r, _, _ := procEnumJobs.Call(uintptr(h), 0, 0xFFFFFFFF, 2, uintptr(unsafe.Pointer(&buf[0])), uintptr(needed), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&returned)))
	if r == 0 || returned == 0 {
		return
	}
	for _, j := range unsafe.Slice((*jobInfo2)(unsafe.Pointer(&buf[0])), returned) {
		key := fmt.Sprintf("%s#%d", printer, j.JobID)
		live[key] = true
		p.judge(h, key, &j)
	}
}

func setJob(h windows.Handle, id uint32, cmd uintptr) {
	_, _, _ = procSetJob.Call(uintptr(h), uintptr(id), 0, 0, cmd)
}

func (p *printWatcher) judge(h windows.Handle, key string, j *jobInfo2) {
	if j.StatusFlags&(jobStatusDeleting|jobStatusDeleted|jobStatusPrinted|jobStatusComplete) != 0 {
		return
	}
	w := p.seen[key]
	if w == nil {
		w = &watched{first: time.Now()}
		p.seen[key] = w
		if j.StatusFlags&jobStatusPaused == 0 {
			setJob(h, j.JobID, jobControlPause) // hold it while it is still being spooled
		}
	}
	if w.decided {
		return
	}
	spooled := j.StatusFlags&jobStatusSpooling == 0
	if !spooled && time.Since(w.first) < holdTimeout {
		return
	}
	w.decided = true

	copies, duplex := 1, false
	if j.DevMode != nil {
		copies, duplex = readDevMode(unsafe.Slice(j.DevMode, 128))
	}
	pages := int(j.TotalPages)
	if guard.OverLimit(pages, copies, duplex) {
		setJob(h, j.JobID, jobControlDelete)
		p.warn(key, windows.UTF16PtrToString(j.Document), guard.Sheets(pages, copies, duplex))
		return
	}
	setJob(h, j.JobID, jobControlResume)
}

// readDevMode reads copies and duplex out of a DEVMODEW (fields at fixed offsets).
func readDevMode(b []byte) (copies int, duplex bool) {
	copies = 1
	fields := uint32(b[72]) | uint32(b[73])<<8 | uint32(b[74])<<16 | uint32(b[75])<<24
	if fields&dmCopies != 0 {
		if c := int(int16(uint16(b[86]) | uint16(b[87])<<8)); c > 1 {
			copies = c
		}
	}
	if fields&dmDuplex != 0 {
		duplex = int16(uint16(b[94])|uint16(b[95])<<8) > 1 // 2 = long edge, 3 = short edge
	}
	return
}

func (p *printWatcher) warn(key, document string, sheets int) {
	p.mu.Lock()
	if time.Since(p.notice[key]) < time.Minute {
		p.mu.Unlock()
		return
	}
	p.notice[key] = time.Now()
	p.mu.Unlock()
	p.notify(strings.TrimSpace(document), sheets)
}

func consoleSession() uint32 { return windows.WTSGetActiveConsoleSessionId() }

// showMessage puts a message box in a user's session (a service has no screen of its own).
func showMessage(session uint32, title, text string) {
	if session == 0xFFFFFFFF {
		return
	}
	t, _ := windows.UTF16FromString(title)
	m, _ := windows.UTF16FromString(text)
	var response uint32
	const mbOkWarningSystemModal = 0x1030
	_, _, _ = procWTSSendMesage.Call(0, uintptr(session),
		uintptr(unsafe.Pointer(&t[0])), uintptr((len(t)-1)*2),
		uintptr(unsafe.Pointer(&m[0])), uintptr((len(m)-1)*2),
		mbOkWarningSystemModal, 0, uintptr(unsafe.Pointer(&response)), 0)
}
