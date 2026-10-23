package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/ddagebi07/gcfldrive-client/internal/drive"
	"github.com/ddagebi07/gcfldrive-client/internal/guard"
)

var (
	user32                = windows.NewLazySystemDLL("user32.dll")
	procSetWindowLong     = user32.NewProc(windowLongName())
	procCallWindowProc    = user32.NewProc("CallWindowProcW")
	procGetSystemMenu     = user32.NewProc("GetSystemMenu")
	procEnableMenuItem    = user32.NewProc("EnableMenuItem")
	procSetWindowPos      = user32.NewProc("SetWindowPos")
	procShowWindow        = user32.NewProc("ShowWindow")
	procSetForeground     = user32.NewProc("SetForegroundWindow")
	procGetMetrics        = user32.NewProc("GetSystemMetrics")
	procGetWindowRect     = user32.NewProc("GetWindowRect")
	procGetLastInputInfo  = user32.NewProc("GetLastInputInfo")
	procGetDpiForSystem   = user32.NewProc("GetDpiForSystem")
	procMessageBox        = user32.NewProc("MessageBoxW")
	procGetWindowLongRead = user32.NewProc(windowLongReadName())
	procIsIconic          = user32.NewProc("IsIconic")
	procGetForeground     = user32.NewProc("GetForegroundWindow")
	kernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procGetTickCount      = kernel32.NewProc("GetTickCount")
)

func windowLongName() string {
	if unsafe.Sizeof(uintptr(0)) == 8 {
		return "SetWindowLongPtrW"
	}
	return "SetWindowLongW"
}
func windowLongReadName() string {
	if unsafe.Sizeof(uintptr(0)) == 8 {
		return "GetWindowLongPtrW"
	}
	return "GetWindowLongW"
}

const (
	gwlStyle   = ^uintptr(15) // -16
	gwlWndProc = ^uintptr(3)  // -4

	wmClose      = 0x0010
	wmSysCommand = 0x0112
	scClose      = 0xF060
	scMask       = 0xFFF0

	wsPopup        = 0x80000000
	wsVisible      = 0x10000000
	swMinimize     = 6
	swRestore      = 9
	smCxScreen     = 0
	smCyScreen     = 1
	swpNoZOrder    = 0x0004
	swpFrameChange = 0x0020
	swpShowWindow  = 0x0040
	mfByCommand    = 0x0
	mfGrayed       = 0x1

	clientWidth  = 904 // the size of the screens we were given
	clientHeight = 627

	// After this long without input the window comes up in front of other programs.
	raiseAfter = 30 * time.Second
	// A student who walks away from the PC is asked to use 극플드라이브 after this long.
	nudgeAfter = 3 * time.Minute
	// After "다른 방식 이용" the nudge waits this long.
	nudgeGrace = 10 * time.Minute
)

type uiHost struct {
	w    webview2.WebView
	hwnd uintptr
	srv  *drive.Server

	mu         sync.Mutex
	graceUntil time.Time
	fullscreen bool
	savedStyle uintptr
	savedRect  [4]int32
	allowExit  atomic.Bool

	cameForNotice atomic.Bool // the window was minimized and only came up to show a print warning
}

func (h *uiHost) NoticeDone() {
	if h.cameForNotice.Swap(false) {
		h.w.Dispatch(func() { _, _, _ = procShowWindow.Call(h.hwnd, swMinimize) })
	}
}

// pollNotices asks the service for cancelled print jobs and shows each one in the window.
func (h *uiHost) pollNotices() {
	for range time.Tick(time.Second) {
		resp, err := askGuardResp(guard.Request{Op: "poll-notice"})
		if err == nil && resp.Notice == nil && resp.Version != "" && resp.Version != version && h.restartable() {
			// The service was updated; the service starts the window again from the new file.
			h.Exit()
			return
		}
		if err != nil || resp.Notice == nil {
			continue
		}
		raw, _ := json.Marshal(resp.Notice)
		h.w.Dispatch(func() {
			if iconic, _, _ := procIsIconic.Call(h.hwnd); iconic != 0 {
				h.cameForNotice.Store(true)
				_, _, _ = procShowWindow.Call(h.hwnd, swRestore)
			}
			hwndTopmost, hwndNoTopmost := ^uintptr(0), ^uintptr(1)
			const flags = 0x0001 | 0x0002 | 0x0040 // NOSIZE | NOMOVE | SHOWWINDOW
			_, _, _ = procSetWindowPos.Call(h.hwnd, hwndTopmost, 0, 0, 0, 0, flags)
			if !h.isFullscreen() {
				_, _, _ = procSetWindowPos.Call(h.hwnd, hwndNoTopmost, 0, 0, 0, 0, flags)
			}
			_, _, _ = procSetForeground.Call(h.hwnd)
			h.w.Eval("window.showPrintNotice(" + string(raw) + ")")
		})
	}
}

// restartable reports whether the window may close for an update without
// getting in anybody's way: nobody signed in and nobody touched the PC for a while.
func (h *uiHost) restartable() bool {
	return !h.srv.SignedIn() && h.idle() >= time.Minute
}

func (h *uiHost) isFullscreen() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fullscreen
}

func (h *uiHost) OtherWay() {
	h.mu.Lock()
	h.graceUntil = time.Now().Add(nudgeGrace)
	h.mu.Unlock()
	h.w.Dispatch(func() {
		h.setFullscreen(false)
		_, _, _ = procShowWindow.Call(h.hwnd, swMinimize)
	})
}

func (h *uiHost) Exit() {
	h.allowExit.Store(true)
	h.w.Dispatch(func() { h.w.Destroy() })
}

// setFullscreen must run on the window's thread (use Dispatch).
func (h *uiHost) setFullscreen(on bool) {
	h.mu.Lock()
	if h.fullscreen == on {
		h.mu.Unlock()
		return
	}
	h.fullscreen = on
	h.mu.Unlock()
	if on {
		style, _, _ := procGetWindowLongRead.Call(h.hwnd, gwlStyle)
		h.savedStyle = style
		var r struct{ l, t, r, b int32 }
		_, _, _ = procGetWindowRect.Call(h.hwnd, uintptr(unsafe.Pointer(&r)))
		h.savedRect = [4]int32{r.l, r.t, r.r, r.b}
		_, _, _ = procShowWindow.Call(h.hwnd, swRestore)
		_, _, _ = procSetWindowLong.Call(h.hwnd, gwlStyle, wsPopup|wsVisible)
		sw, _, _ := procGetMetrics.Call(smCxScreen)
		sh, _, _ := procGetMetrics.Call(smCyScreen)
		hwndTopmost := ^uintptr(0) // -1
		_, _, _ = procSetWindowPos.Call(h.hwnd, hwndTopmost, 0, 0, sw, sh, swpFrameChange|swpShowWindow)
		_, _, _ = procSetForeground.Call(h.hwnd)
	} else {
		_, _, _ = procSetWindowLong.Call(h.hwnd, gwlStyle, h.savedStyle)
		hwndNoTopmost := ^uintptr(1) // -2
		r := h.savedRect
		_, _, _ = procSetWindowPos.Call(h.hwnd, hwndNoTopmost, uintptr(r[0]), uintptr(r[1]), uintptr(r[2]-r[0]), uintptr(r[3]-r[1]), swpFrameChange|swpShowWindow)
	}
	h.srv.SetFullscreen(on)
}

func (h *uiHost) idle() time.Duration {
	var info struct{ size, tick uint32 }
	info.size = uint32(unsafe.Sizeof(info))
	if r, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		return 0
	}
	now, _, _ := procGetTickCount.Call()
	return time.Duration(uint32(now)-info.tick) * time.Millisecond
}

// watch runs the screen-saver-like nudge: when nobody has touched the PC for a
// while and nobody is signed in, the PIN screen takes the whole screen.
func (h *uiHost) watch(sess *drive.Session) {
	raised := false // once per idle stretch, so it does not fight a person who sends it back
	for range time.Tick(2 * time.Second) {
		signedIn := sess.SignedIn()
		h.mu.Lock()
		full, grace := h.fullscreen, h.graceUntil
		h.mu.Unlock()
		idle := h.idle()
		if idle < raiseAfter {
			raised = false
		}
		switch {
		case full && signedIn:
			h.w.Dispatch(func() { h.setFullscreen(false) })
		case !full && !signedIn && time.Now().After(grace) && idle >= nudgeAfter:
			h.w.Dispatch(func() { h.setFullscreen(true) })
		case !full && !signedIn && !raised && time.Now().After(grace) && idle >= raiseAfter:
			// Nobody signed in (a signed-in student may be printing from the photo viewer).
			raised = true
			h.w.Dispatch(h.raise)
		}
	}
}

// raise brings the window in front of every other window without making it
// stay there. It must run on the window's thread (use Dispatch).
func (h *uiHost) raise() {
	if fg, _, _ := procGetForeground.Call(); fg == h.hwnd {
		return
	}
	if iconic, _, _ := procIsIconic.Call(h.hwnd); iconic != 0 {
		_, _, _ = procShowWindow.Call(h.hwnd, swRestore)
	}
	// Windows may refuse SetForegroundWindow to a program nobody clicked, so
	// briefly making it topmost is what actually puts it on top.
	hwndTopmost, hwndNoTopmost := ^uintptr(0), ^uintptr(1)
	const flags = 0x0001 | 0x0002 | 0x0040 // NOSIZE | NOMOVE | SHOWWINDOW
	_, _, _ = procSetWindowPos.Call(h.hwnd, hwndTopmost, 0, 0, 0, 0, flags)
	_, _, _ = procSetWindowPos.Call(h.hwnd, hwndNoTopmost, 0, 0, 0, 0, flags)
	_, _, _ = procSetForeground.Call(h.hwnd)
}

func webview2Installed() bool {
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		for _, path := range []string{webview2Key, `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`} {
			if k, err := registry.OpenKey(root, path, registry.QUERY_VALUE); err == nil {
				v, _, err := k.GetStringValue("pv")
				k.Close()
				if err == nil && v != "" && v != "0.0.0.0" {
					return true
				}
			}
		}
	}
	return false
}

func messageBox(title, text string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	_, _, _ = procMessageBox.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x10)
}

func dpiScale() float64 {
	if r, _, _ := procGetDpiForSystem.Call(); r != 0 {
		return float64(r) / 96
	}
	return 1
}

func runUI(server string) int {
	// one window per sign-in
	if _, err := windows.CreateMutex(nil, false, windows.StringToUTF16Ptr(mutexName)); err == windows.ERROR_ALREADY_EXISTS {
		return 0
	}
	if !webview2Installed() {
		messageBox(appTitle, "Microsoft Edge WebView2 런타임이 없어 화면을 열 수 없어요.\n관리자에게 알려 주세요.")
		return 3
	}
	_ = protectProcess()

	// A fresh browser profile each start, thrown away on exit, so nothing a student does stays on the PC.
	profile := filepath.Join(os.TempDir(), fmt.Sprintf("gcfldrive-%d", os.Getpid()))
	_ = os.RemoveAll(profile)
	defer os.RemoveAll(profile)

	clearOpened()
	defer clearOpened()

	sess := drive.NewSession(drive.SessionTimeout)
	sess.OnLogout(clearOpened)
	host := &uiHost{}
	srv := drive.NewServer(drive.NewAPI(server), sess, host, pipeGuard{})
	host.srv = srv
	go srv.SyncSettings(5 * time.Minute)
	url, err := srv.Listen()
	if err != nil {
		messageBox(appTitle, "화면을 시작하지 못했어요: "+err.Error())
		return 2
	}

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		DataPath: profile, AutoFocus: true,
		WindowOptions: webview2.WindowOptions{Title: appTitle, Width: 920, Height: 700, Center: true},
	})
	if w == nil {
		messageBox(appTitle, "화면을 만들지 못했어요. WebView2 런타임을 확인해 주세요.")
		return 3
	}
	defer w.Destroy()
	host.w, host.hwnd = w, uintptr(w.Window())

	scale := dpiScale()
	w.SetTitle(appTitle)
	w.SetSize(int(clientWidth*scale), int(clientHeight*scale), webview2.HintFixed)
	centerWindow(host.hwnd)
	lockClose(host)
	go host.watch(sess)
	go host.pollNotices()

	w.Navigate(url)
	w.Run()
	return 0
}

func centerWindow(hwnd uintptr) {
	var r struct{ l, t, r, b int32 }
	_, _, _ = procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	sw, _, _ := procGetMetrics.Call(smCxScreen)
	sh, _, _ := procGetMetrics.Call(smCyScreen)
	x, y := (int32(sw)-(r.r-r.l))/2, (int32(sh)-(r.b-r.t))/2
	_, _, _ = procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), 0, 0, swpNoZOrder|0x0001) // SWP_NOSIZE
}

// lockClose makes the window impossible to close without the administrator
// password: every WM_CLOSE / SC_CLOSE (the X button, Alt+F4, taskbar "Close
// window", a polite taskkill) is swallowed and opens the password dialog instead.
// Only the administrator's menu ends the program.
func lockClose(h *uiHost) {
	var orig uintptr
	proc := windows.NewCallback(func(hwnd, msg, wp, lp uintptr) uintptr {
		if !h.allowExit.Load() {
			if msg == wmClose || (msg == wmSysCommand && wp&scMask == scClose) {
				h.w.Eval("window.openAdmin && window.openAdmin()") // closing needs the administrator password
				return 0
			}
		}
		r, _, _ := procCallWindowProc.Call(orig, hwnd, msg, wp, lp)
		return r
	})
	orig, _, _ = procSetWindowLong.Call(h.hwnd, gwlWndProc, proc)
}
