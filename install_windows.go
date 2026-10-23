package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	if !clearLeftovers(self) {
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

// clearLeftovers removes what an earlier install left behind when its service is
// already gone (a failed uninstall, a half-finished install). Those folders are
// owned by SYSTEM and closed to administrators, so they are taken over first.
func clearLeftovers(self string) bool {
	dirs := []string{installDir, dataDir}
	found := false
	for _, d := range dirs {
		if _, err := os.Lstat(d); err == nil {
			found = true
		}
	}
	if !found {
		return true
	}
	fmt.Println("전에 설치했던 파일을 정리하고 있어요…")
	// Administrators may take ownership and kill the protected window (it denies termination to everyone else).
	enablePrivilege("SeTakeOwnershipPrivilege")
	enablePrivilege("SeRestorePrivilege")
	enableDebugPrivilege()
	killUI()
	time.Sleep(500 * time.Millisecond)
	for _, d := range dirs {
		if err := takeOver(d); err != nil {
			fmt.Printf("%s 의 권한을 가져오지 못했어요: %v\n", d, err)
			return false
		}
		if inside(self, d) {
			fmt.Printf("설치 폴더(%s) 안에서는 설치할 수 없어요. 다른 폴더에 받은 gcfldrive.exe 로 설치해 주세요.\n", d)
			return false
		}
		if err := os.RemoveAll(d); err != nil {
			setAsideInUse(d)
		}
	}
	return true
}

// takeOver makes the administrators the owner of dir with full control, and lets
// that control flow down to everything inside (Windows rewrites the children's
// inherited entries), so the folder can be deleted.
func takeOver(dir string) error {
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		return nil
	}
	admins, err := windows.StringToSid(adminsSID)
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION, admins, nil, nil, nil); err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:AI(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// setAsideInUse handles files that are still open (a program that could not be
// stopped): each is renamed out of the way so the new copy can take its name,
// and Windows deletes it at the next restart.
func setAsideInUse(dir string) {
	_ = filepath.WalkDir(dir, func(path string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		if os.Remove(path) == nil {
			return nil
		}
		aside := fmt.Sprintf("%s.old-%d", path, time.Now().UnixNano())
		if os.Rename(path, aside) == nil {
			path = aside
		}
		p, _ := windows.UTF16PtrFromString(path)
		_ = windows.MoveFileEx(p, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT)
		return nil
	})
}

func inside(path, dir string) bool {
	rel, err := filepath.Rel(strings.ToLower(dir), strings.ToLower(path)) // Windows paths ignore case
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
