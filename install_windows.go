package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
	"golang.org/x/term"

	"github.com/ddagebi07/gcfldrive-client/internal/guard"
)

func isAdmin() bool { return windows.GetCurrentProcessToken().IsElevated() }

func readPassword(prompt string) (string, error) {
	fmt.Print(prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	return string(b), err
}

func serviceExists() bool {
	m, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer m.Disconnect()
	s, err := m.OpenService(guard.ServiceName)
	if err != nil {
		return false
	}
	s.Close()
	return true
}

// cliInstall is run once per PC by an administrator ("관리자 권한으로 실행").
func cliInstall(args []string) int {
	if !isAdmin() {
		fmt.Println("관리자 권한이 필요해요. install.cmd를 마우스 오른쪽 버튼으로 '관리자 권한으로 실행'해 주세요.")
		return 1
	}
	if serviceExists() {
		fmt.Println("이미 설치되어 있어요. 다시 설치하려면 먼저 gcfldrive.exe uninstall 로 제거해 주세요.")
		return 1
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Println("프로그램 위치를 찾지 못했어요:", err)
		return 1
	}

	cfg := guard.DefaultConfig()
	if len(args) > 0 && args[0] == "--set-password" {
		pw, err := readPassword(fmt.Sprintf("관리자 비밀번호를 정해 주세요 (%d자 이상): ", guard.MinPasswordLength))
		if err != nil {
			fmt.Println("비밀번호를 읽지 못했어요.")
			return 1
		}
		if again, _ := readPassword("한 번 더 입력해 주세요: "); pw != again {
			fmt.Println("두 비밀번호가 달라요.")
			return 1
		}
		if cfg, err = guard.NewConfig(pw); err != nil {
			fmt.Println(err)
			return 1
		}
	}

	if !webview2Installed() {
		installer := filepath.Join(filepath.Dir(self), "MicrosoftEdgeWebView2RuntimeInstallerX86.exe")
		if _, err := os.Stat(installer); err != nil {
			fmt.Println("WebView2 런타임이 없어요. MicrosoftEdgeWebView2RuntimeInstallerX86.exe 를 이 프로그램 옆에 두고 다시 실행해 주세요.")
			return 1
		}
		fmt.Println("WebView2 런타임을 설치하고 있어요…")
		if err := exec.Command(installer, "/silent", "/install").Run(); err != nil || !webview2Installed() {
			fmt.Println("WebView2 런타임을 설치하지 못했어요.")
			return 1
		}
	}

	step := func(what string, err error) bool {
		if err != nil {
			fmt.Printf("%s 실패: %v\n", what, err)
			return false
		}
		return true
	}
	if !step("폴더 만들기", os.MkdirAll(installDir, 0o755)) || !step("폴더 만들기", os.MkdirAll(dataDir, 0o755)) ||
		!step("프로그램 복사", copyFile(self, installExe)) || !step("백업 복사", copyFile(self, backupExe)) {
		return 1
	}
	raw, _ := json.Marshal(cfg)
	if !step("설정 저장", os.WriteFile(configPath, raw, 0o600)) {
		return 1
	}

	m, err := mgr.Connect()
	if !step("서비스 관리자 연결", err) {
		return 1
	}
	defer m.Disconnect()
	s, err := m.CreateService(guard.ServiceName, installExe, mgr.Config{
		DisplayName: guard.ServiceDisplayName, StartType: mgr.StartAutomatic,
		Description: "극플드라이브 창을 항상 띄우고 출력 매수 제한을 지킵니다.",
	})
	if !step("서비스 만들기", err) {
		return 1
	}
	defer s.Close()
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: time.Second}, {Type: mgr.ServiceRestart, Delay: time.Second}, {Type: mgr.ServiceRestart, Delay: 5 * time.Second},
	}, 86400)
	_ = s.SetRecoveryActionsOnNonCrashFailures(true)

	if !step("서비스 시작", s.Start()) {
		return 1
	}
	// The service now applies the ACLs and policies itself; give it a moment so the message below is true.
	time.Sleep(3 * time.Second)
	fmt.Println("설치했어요. 로그인하면 극플드라이브 창이 자동으로 떠요.")
	fmt.Println("종료하거나 제거하려면 창을 닫으려 하거나(Alt+F4) 화면 아래 'Powered by' 로고를 7번 눌러 관리자 비밀번호를 입력하세요.")
	return 0
}

// cliUninstall removes the program after the service has checked the password.
func cliUninstall() int {
	if !serviceExists() {
		fmt.Println("설치되어 있지 않아요.")
		return 1
	}
	pw, err := readPassword("관리자 비밀번호: ")
	if err != nil {
		return 1
	}
	if err := askGuard(guard.Request{Op: "uninstall", Password: pw}); err != nil {
		fmt.Println(err)
		return 1
	}
	fmt.Println("제거하고 있어요. 잠시 뒤에 모두 사라져요.")
	return 0
}

// cliResume ends a maintenance window early (no password needed: it only turns protection back on).
func cliResume() int {
	if err := askGuard(guard.Request{Op: "resume"}); err != nil {
		fmt.Println(err)
		return 1
	}
	fmt.Println("보호를 다시 켰어요.")
	return 0
}
