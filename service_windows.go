package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/ddagebi07/gcfldrive-client/internal/drive"
	"github.com/ddagebi07/gcfldrive-client/internal/guard"
)

// guardService is the protection service: it runs as SYSTEM from boot, starts
// the window in whoever signs in, starts it again whenever it is closed, keeps
// the lock-down in place, watches the print queue, and is the only one that can
// uninstall the program (after checking the administrator password).
type guardService struct {
	cfg     *guard.Config
	limiter *guard.Limiter

	mu           sync.Mutex
	pausedUntil  time.Time
	uninstalling bool

	notices     []queuedNotice
	lastLaunch  time.Time
	fastExits   int
	backoffTill time.Time
}

type queuedNotice struct {
	guard.Notice
	at time.Time
}

// pushNotice queues a print warning for the window to show in its own screen.
func (g *guardService) pushNotice(document string, sheets int) {
	g.mu.Lock()
	g.notices = append(g.notices, queuedNotice{Notice: guard.Notice{Document: document, Sheets: sheets, Max: guard.MaxSheets()}, at: time.Now()})
	g.mu.Unlock()
}

func (g *guardService) popNotice() *guard.Notice {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.notices) == 0 {
		return nil
	}
	n := g.notices[0].Notice
	g.notices = g.notices[1:]
	return &n
}

// flushStaleNotices falls back to a plain Windows message box when the window did not pick a warning up.
func (g *guardService) flushStaleNotices() {
	g.mu.Lock()
	var stale []guard.Notice
	for len(g.notices) > 0 && time.Since(g.notices[0].at) > 8*time.Second {
		stale = append(stale, g.notices[0].Notice)
		g.notices = g.notices[1:]
	}
	g.mu.Unlock()
	for _, n := range stale {
		showMessage(consoleSession(), "출력 제한", fmt.Sprintf("한 번에 종이 %d장까지만 출력할 수 있어요.\n\n'%s'은(는) 약 %d장이라 출력을 취소했어요.", n.Max, n.Document, n.Sheets))
	}
}

// syncLimit keeps the paper limit in step with what the librarian set at /drive-admin.
// The last value is kept on disk so a PC that boots without a network still enforces it.
func syncLimit(server string) {
	cache := filepath.Join(dataDir, "limit.json")
	if raw, err := os.ReadFile(cache); err == nil {
		var v struct{ MaxSheets int }
		if json.Unmarshal(raw, &v) == nil {
			guard.SetMaxSheets(v.MaxSheets)
		}
	}
	if v := os.Getenv("GCFLDRIVE_SERVER"); v != "" {
		server = v
	}
	api := drive.NewAPI(server)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		if n, err := api.MaxSheets(ctx); err == nil {
			guard.SetMaxSheets(n)
			raw, _ := json.Marshal(map[string]int{"MaxSheets": n})
			_ = os.WriteFile(cache, raw, 0o600)
		}
		cancel()
		time.Sleep(5 * time.Minute)
	}
}

func runService() error {
	return svc.Run(guard.ServiceName, &guardService{limiter: guard.NewLimiter()})
}

func (g *guardService) paused() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return time.Now().Before(g.pausedUntil)
}

func (g *guardService) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	raw, err := os.ReadFile(configPath)
	if err == nil {
		g.cfg = &guard.Config{}
		err = json.Unmarshal(raw, g.cfg)
	}
	if err != nil {
		return false, 1 // not installed properly; the SCM will restart us, which is harmless
	}
	enableDebugPrivilege()
	harden()
	go syncLimit(drive.DefaultServer)

	ln, err := listenGuard()
	if err == nil {
		go serveGuard(ln, g.handle)
		defer ln.Close()
	}
	stop := make(chan struct{})
	defer close(stop)
	go newPrintWatcher(func() bool { return !g.paused() }, g.pushNotice).run(stop)

	// Not accepting Stop: nobody, administrators included, can stop the service from outside.
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptShutdown | svc.AcceptSessionChange}
	tick := time.NewTicker(1500 * time.Millisecond)
	defer tick.Stop()
	ticks := 0
	for {
		select {
		case <-tick.C:
			ticks++
			g.tick(ticks%20 == 0)
		case c := <-requests:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.SessionChange:
				g.tick(false)
			case svc.Shutdown:
				return false, 0
			}
		}
		if g.isUninstalling() {
			return false, 0
		}
	}
}

func (g *guardService) isUninstalling() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.uninstalling
}

func (g *guardService) tick(rehard bool) {
	g.flushStaleNotices()
	g.mu.Lock()
	until, gone := g.pausedUntil, g.uninstalling
	g.mu.Unlock()
	if gone {
		return
	}
	if time.Now().Before(until) {
		return
	}
	if !until.IsZero() { // the maintenance window just ended
		g.mu.Lock()
		g.pausedUntil = time.Time{}
		g.mu.Unlock()
		rehard = true
	}
	if rehard {
		harden()
	}
	session := windows.WTSGetActiveConsoleSessionId()
	if session == 0xFFFFFFFF || uiRunning(session) || time.Now().Before(g.backoffTill) {
		return
	}
	if !g.lastLaunch.IsZero() && time.Since(g.lastLaunch) < 15*time.Second {
		// it closed right after starting (broken WebView2?): don't spin
		if g.fastExits++; g.fastExits >= 3 {
			g.backoffTill, g.fastExits = time.Now().Add(time.Minute), 0
		}
	} else {
		g.fastExits = 0
	}
	if launchUI(session) == nil {
		g.lastLaunch = time.Now()
	}
}

// handle answers one request from the window or the command line.
func (g *guardService) handle(req guard.Request) guard.Response {
	switch req.Op {
	case "ping":
		return guard.Response{OK: true}
	case "poll-notice":
		return guard.Response{OK: true, Notice: g.popNotice()}
	case "resume":
		g.mu.Lock()
		g.pausedUntil = time.Time{}
		g.mu.Unlock()
		harden()
		return guard.Response{OK: true}
	case "check", "maintenance", "uninstall":
	default:
		return guard.Response{Error: "알 수 없는 요청이에요."}
	}
	if err := g.limiter.Allow(); err != nil {
		return guard.Response{Error: err.Error()}
	}
	ok := g.cfg.Verify(req.Password)
	if penalty := g.limiter.Record(ok); !ok {
		time.Sleep(penalty)
		return guard.Response{Error: "비밀번호가 맞지 않아요."}
	}
	switch req.Op {
	case "maintenance":
		minutes := min(max(req.Minutes, 1), 60)
		g.mu.Lock()
		g.pausedUntil = time.Now().Add(time.Duration(minutes) * time.Minute)
		g.mu.Unlock()
		applyPolicies(false)
	case "uninstall":
		go g.uninstall()
	}
	return guard.Response{OK: true}
}

// uninstall removes everything. It runs inside the service because only SYSTEM may.
func (g *guardService) uninstall() {
	g.mu.Lock()
	g.uninstalling = true
	g.mu.Unlock()
	applyPolicies(false)
	killUI()
	_ = setACL(installDir, windows.SE_FILE_OBJECT, "D:PAI(A;OICI;FA;;;SY)")
	if m, err := mgr.Connect(); err == nil {
		if s, err := m.OpenService(guard.ServiceName); err == nil {
			_ = s.Delete()
			s.Close()
		}
		m.Disconnect()
	}
	// The files are in use until this process ends, so a detached command removes them a moment later.
	script := fmt.Sprintf(`ping -n 4 127.0.0.1 >nul & rd /s /q "%s" & rd /s /q "%s"`, installDir, dataDir)
	cmd := exec.Command("cmd.exe", "/C", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP}
	_ = cmd.Start()
}

// ---- processes -----------------------------------------------------------------

// uiProcesses lists the running windows (other copies of this program outside the service's own session 0).
func uiProcesses() (pids []uint32, sessions []uint32) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, nil
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	self := uint32(os.Getpid())
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if e.ProcessID == self || !strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), exeName) {
			continue
		}
		var s uint32
		if windows.ProcessIdToSessionId(e.ProcessID, &s) == nil && s != 0 {
			pids, sessions = append(pids, e.ProcessID), append(sessions, s)
		}
	}
	return
}

func uiRunning(session uint32) bool {
	_, sessions := uiProcesses()
	for _, s := range sessions {
		if s == session {
			return true
		}
	}
	return false
}

func killUI() {
	pids, _ := uiProcesses()
	for _, pid := range pids {
		if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid); err == nil {
			_ = windows.TerminateProcess(h, 0)
			windows.CloseHandle(h)
		}
	}
}

// launchUI starts the window in the signed-in user's session with their own (unelevated) token.
func launchUI(session uint32) error {
	var token windows.Token
	if err := windows.WTSQueryUserToken(session, &token); err != nil {
		return err // nobody is signed in yet
	}
	defer token.Close()
	var env *uint16
	if err := windows.CreateEnvironmentBlock(&env, token, false); err != nil {
		return err
	}
	defer windows.DestroyEnvironmentBlock(env)

	desktop, _ := windows.UTF16PtrFromString(`winsta0\default`)
	app, _ := windows.UTF16PtrFromString(installExe)
	cmdline, _ := windows.UTF16PtrFromString(fmt.Sprintf(`"%s" --ui`, installExe))
	dir, _ := windows.UTF16PtrFromString(installDir)
	si := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{})), Desktop: desktop}
	var pi windows.ProcessInformation
	err := windows.CreateProcessAsUser(token, app, cmdline, nil, nil, false, windows.CREATE_UNICODE_ENVIRONMENT, env, dir, &si, &pi)
	if err != nil {
		return err
	}
	windows.CloseHandle(pi.Thread)
	windows.CloseHandle(pi.Process)
	return nil
}
