package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/ddagebi07/gcfldrive-client/internal/guard"
)

// The protection works in layers; none of them is perfect alone, and a person who
// is an administrator and knows what they are doing can undo them with enough
// effort (taking ownership, running as SYSTEM, safe mode tricks). The aim is that
// a student cannot do it by accident or in a minute.
const (
	// SYSTEM only. Not even administrators can read the password hash.
	sddlDataDir = "D:PAI(A;OICI;FA;;;SY)"
	// SYSTEM may do everything; administrators and users may only read and run.
	sddlInstallDir = "D:PAI(A;OICI;FA;;;SY)(A;OICI;0x1200a9;;;BA)(A;OICI;0x1200a9;;;BU)"
	// The service cannot be stopped, paused, reconfigured or deleted by anyone but itself.
	sddlService = "D:(A;;GA;;;SY)(A;;CCLCSWLOCRRC;;;BA)(A;;CCLCSWLOCRRC;;;IU)(A;;CCLCSWLOCRRC;;;SU)"
	// Its registry key is read-only for administrators (no "Start=4", no deleting it).
	sddlServiceKey = "D:PAI(A;CI;KA;;;SY)(A;CI;KR;;;BA)(A;CI;KR;;;BU)"
	// The running window cannot be killed by an ordinary taskkill / Task Manager "End task".
	sddlProcess = "D:(D;;0x0001;;;WD)(D;;0x0800;;;WD)(A;;GA;;;WD)"
)

func setACL(name string, typ windows.SE_OBJECT_TYPE, sddl string) error {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	system, err := windows.StringToSid(systemSID)
	if err != nil {
		return err
	}
	// The owner may always rewrite the ACL, so SYSTEM must own it too.
	_ = windows.SetNamedSecurityInfo(name, typ, windows.OWNER_SECURITY_INFORMATION, system, nil, nil, nil)
	return windows.SetNamedSecurityInfo(name, typ, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

func protectProcess() error {
	sd, err := windows.SecurityDescriptorFromString(sddlProcess)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(windows.CurrentProcess(), windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// applyPolicies turns the lock-down policies on or off. Off is for the
// administrator's maintenance window and for uninstalling.
func applyPolicies(on bool) {
	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`, registry.SET_VALUE)
	if err == nil {
		for _, name := range []string{"DisableTaskMgr", "DisableRegistryTools"} {
			if on {
				_ = key.SetDWordValue(name, 1)
			} else {
				_ = key.DeleteValue(name)
			}
		}
		key.Close()
	}
	run, _, err := registry.CreateKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err == nil {
		if on {
			_ = run.SetStringValue(runValue, fmt.Sprintf(`"%s" --ui`, installExe))
		} else {
			_ = run.DeleteValue(runValue)
		}
		run.Close()
	}
	for _, set := range []string{"Minimal", "Network"} { // keep the service running in safe mode
		path := `SYSTEM\CurrentControlSet\Control\SafeBoot\` + set + `\` + guard.ServiceName
		if on {
			if k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, path, registry.SET_VALUE); err == nil {
				_ = k.SetStringValue("", "Service")
				k.Close()
			}
		} else {
			_ = registry.DeleteKey(registry.LOCAL_MACHINE, path)
		}
	}
}

// harden (re)applies everything the service guards: the files, their ACLs and the policies.
func harden() {
	// A deleted or replaced program is put back from the backup copy.
	if fi, err := os.Stat(installExe); err != nil || !sameSize(installExe, backupExe, fi) {
		_ = copyFile(backupExe, installExe)
	}
	_ = setACL(dataDir, windows.SE_FILE_OBJECT, sddlDataDir)
	_ = setACL(installDir, windows.SE_FILE_OBJECT, sddlInstallDir)
	_ = setACL(guard.ServiceName, windows.SE_SERVICE, sddlService)
	_ = setACL(`MACHINE\SYSTEM\CurrentControlSet\Services\`+guard.ServiceName, windows.SE_REGISTRY_KEY, sddlServiceKey)
	applyPolicies(true)
}

func sameSize(a, b string, ai os.FileInfo) bool {
	bi, err := os.Stat(b)
	return err != nil || bi.Size() == ai.Size()
}

func enableDebugPrivilege() {
	var tok windows.Token
	if windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok) != nil {
		return
	}
	defer tok.Close()
	var luid windows.LUID
	if windows.LookupPrivilegeValue(nil, windows.StringToUTF16Ptr("SeDebugPrivilege"), &luid) != nil {
		return
	}
	p := windows.Tokenprivileges{PrivilegeCount: 1}
	p.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
	_ = windows.AdjustTokenPrivileges(tok, false, &p, 0, nil, nil)
}
