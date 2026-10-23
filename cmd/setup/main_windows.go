// Command setup is the one-file installer: gcfldrive-setup.exe carries
// gcfldrive.exe inside, asks for administrator rights, and runs its install.
// build.sh builds it after gcfldrive.exe (the program is embedded from payload/).
//
//	gcfldrive-setup.exe                  # install with the default administrator password
//	gcfldrive-setup.exe --set-password   # install with a password typed in
//	gcfldrive-setup.exe uninstall        # remove (asks for the administrator password)
package main

import (
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

//go:embed payload/gcfldrive.exe
var program []byte

const webview2Installer = "MicrosoftEdgeWebView2RuntimeInstallerX86.exe"

func main() {
	code := run()
	fmt.Print("\n엔터 키를 누르면 창이 닫혀요.")
	_, _ = fmt.Scanln()
	os.Exit(code)
}

func run() int {
	_ = windows.SetConsoleOutputCP(65001) // 한글이 깨지지 않게
	_ = windows.SetConsoleCP(65001)
	if !windows.GetCurrentProcessToken().IsElevated() {
		return relaunchAsAdmin()
	}
	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"install"}
	} else if args[0] != "uninstall" {
		args = append([]string{"install"}, args...)
	}

	dir, err := os.MkdirTemp("", "gcfldrive-setup-")
	if err != nil {
		fmt.Println("임시 폴더를 만들지 못했어요:", err)
		return 1
	}
	defer os.RemoveAll(dir)
	exe := filepath.Join(dir, "gcfldrive.exe")
	if err := os.WriteFile(exe, program, 0o755); err != nil {
		fmt.Println("프로그램을 풀지 못했어요:", err)
		return 1
	}
	// install looks for the WebView2 installer next to the program it runs from.
	self, _ := os.Executable()
	if src := filepath.Join(filepath.Dir(self), webview2Installer); fileExists(src) {
		_ = copyFile(src, filepath.Join(dir, webview2Installer))
	}

	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode()
		}
		fmt.Println("설치를 시작하지 못했어요:", err)
		return 1
	}
	return 0
}

// relaunchAsAdmin shows the Windows "allow this app to make changes" prompt and
// starts this installer again with administrator rights in its own window.
func relaunchAsAdmin() int {
	self, err := os.Executable()
	if err != nil {
		fmt.Println("프로그램 위치를 찾지 못했어요:", err)
		return 1
	}
	var params string
	for _, a := range os.Args[1:] {
		params += ` "` + strings.ReplaceAll(a, `"`, ``) + `"`
	}
	var p *uint16
	if params != "" {
		p = windows.StringToUTF16Ptr(params)
	}
	err = windows.ShellExecute(0, windows.StringToUTF16Ptr("runas"), windows.StringToUTF16Ptr(self), p, nil, windows.SW_SHOWNORMAL)
	if err != nil {
		fmt.Println("관리자 권한이 필요해요. 설치 파일을 마우스 오른쪽 버튼으로 눌러 '관리자 권한으로 실행'해 주세요.")
		return 1
	}
	os.Exit(0) // the elevated copy carries on in its own window
	return 0
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
