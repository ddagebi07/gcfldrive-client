package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"

	"github.com/ddagebi07/gcfldrive-client/internal/drive"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if isService, _ := svc.IsWindowsService(); isService {
		if err := runService(); err != nil {
			return 1
		}
		return 0
	}
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	server := drive.DefaultServer
	if v := os.Getenv("GCFLDRIVE_SERVER"); v != "" {
		server = v
	}
	if cmd == "install" || cmd == "uninstall" || cmd == "resume" || cmd == "help" {
		attachConsole()
	}
	switch cmd {
	case "", "--ui", "ui":
		return runUI(server)
	case "install":
		return cliInstall(args[1:])
	case "uninstall":
		return cliUninstall()
	case "resume":
		return cliResume()
	default:
		fmt.Println("사용법: gcfldrive.exe [install | uninstall | resume]")
		return 2
	}
}

// attachConsole lets the (windowless) program talk in the console it was started from.
func attachConsole() {
	const attachParentProcess = ^uintptr(0)
	_, _, _ = windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole").Call(attachParentProcess)
	if f, err := os.OpenFile("CONOUT$", os.O_RDWR, 0); err == nil {
		os.Stdout, os.Stderr = f, f
	}
	if f, err := os.OpenFile("CONIN$", os.O_RDWR, 0); err == nil {
		os.Stdin = f
	}
}
