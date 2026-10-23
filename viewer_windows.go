package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// openedDir holds the pictures handed to the Windows viewer. It is emptied on
// logout, at start and on exit, so nothing a student opened stays on the PC.
func openedDir() string { return filepath.Join(os.TempDir(), "gcfldrive-open") }

func clearOpened() { _ = os.RemoveAll(openedDir()) }

// OpenFile writes the picture to a temporary folder and opens it with its
// default program (Photos), where the student prints it. The print service
// still holds every job to the sheet limit.
func (h *uiHost) OpenFile(name string, body []byte) error {
	if err := os.MkdirAll(openedDir(), 0o700); err != nil {
		return errors.New("파일을 열 준비를 하지 못했어요.")
	}
	dir, err := os.MkdirTemp(openedDir(), "") // the same name twice must not clash with a file the viewer still has open
	if err != nil {
		return errors.New("파일을 열 준비를 하지 못했어요.")
	}
	file := filepath.Join(dir, safeFileName(name))
	if err := os.WriteFile(file, body, 0o600); err != nil {
		return errors.New("파일을 열 준비를 하지 못했어요.")
	}
	done := make(chan error, 1)
	h.w.Dispatch(func() { // the window's thread has COM set up, which ShellExecute wants
		done <- windows.ShellExecute(windows.Handle(h.hwnd), windows.StringToUTF16Ptr("open"),
			windows.StringToUTF16Ptr(file), nil, windows.StringToUTF16Ptr(dir), windows.SW_SHOWNORMAL)
	})
	if err := <-done; err != nil {
		_ = os.RemoveAll(dir)
		return errors.New("이 파일을 열 수 있는 프로그램이 없어요. 선생님께 알려 주세요.")
	}
	return nil
}

// safeFileName keeps the name (and so the extension the viewer goes by) but
// drops anything Windows does not allow in a file name.
func safeFileName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "/", `\`))
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimRight(name, ". ")
	if name == "" || strings.HasPrefix(name, ".") {
		name = "file" + name
	}
	return name
}
