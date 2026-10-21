package main

import (
	"os"
	"path/filepath"
)

const (
	exeName     = "gcfldrive.exe"
	runValue    = "GCFLDrive"
	appTitle    = "극플드라이브 - GCFL 아카이브"
	mutexName   = `Local\GCFLDriveUI`
	systemSID   = "S-1-5-18"
	adminsSID   = "S-1-5-32-544"
	webview2Key = `SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`
)

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

var (
	installDir = filepath.Join(envOr("ProgramFiles", `C:\Program Files`), "GCFLDrive")
	installExe = filepath.Join(installDir, exeName)
	dataDir    = filepath.Join(envOr("ProgramData", `C:\ProgramData`), "GCFLDrive")
	configPath = filepath.Join(dataDir, "guard.json")
	backupExe  = filepath.Join(dataDir, "backup", exeName)
)
