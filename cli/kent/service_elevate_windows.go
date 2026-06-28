//go:build windows

package main

import (
	brand "core/shared/config"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	shell32DLL         = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteEx = shell32DLL.NewProc("ShellExecuteExW")
)

const (
	seeMaskNoCloseProcess = 0x00000040
	swShowNormal          = 1
)

// shellExecuteInfo mirrors SHELLEXECUTEINFOW; ShellExecuteEx writes the launched
// process handle into the trailing process field when SEE_MASK_NOCLOSEPROCESS is
// set.
type shellExecuteInfo struct {
	cbSize        uint32
	mask          uint32
	hwnd          uintptr
	verb          *uint16
	file          *uint16
	parameters    *uint16
	directory     *uint16
	show          int32
	instApp       uintptr
	idList        uintptr
	class         *uint16
	hkeyClass     uintptr
	hotKey        uint32
	iconOrMonitor uintptr
	process       windows.Handle
}

// elevateServiceAction re-launches the current command elevated when the action
// requires Administrator (install/uninstall) and this process is not elevated.
// It returns (exitCode, true) when an elevated child handled the action, or
// (0, false) when the caller should proceed in-process (already elevated, or the
// action needs no elevation). Start/stop/restart/status are never elevated: the
// install grants the user a service DACL so they run unprivileged.
func elevateServiceAction(action serviceAction) (int, bool) {
	if action != serviceActionInstall && action != serviceActionUninstall {
		return 0, false
	}
	if windows.GetCurrentProcessToken().IsElevated() {
		return 0, false
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1, true
	}
	verbPtr, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1, true
	}
	exePtr, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1, true
	}
	paramsPtr, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(os.Args[1:]))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1, true
	}
	info := shellExecuteInfo{
		mask:       seeMaskNoCloseProcess,
		verb:       verbPtr,
		file:       exePtr,
		parameters: paramsPtr,
		show:       swShowNormal,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))

	ret, _, callErr := procShellExecuteEx.Call(uintptr(unsafe.Pointer(&info)))
	if ret == 0 {
		fmt.Fprintf(os.Stderr, "elevation was declined or failed; run `%s service %s` from an Administrator terminal: %v\n", brand.Command, action, callErr)
		return 1, true
	}
	if info.process == 0 {
		return 0, true
	}
	defer func() { _ = windows.CloseHandle(info.process) }()
	if _, err := windows.WaitForSingleObject(info.process, windows.INFINITE); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1, true
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.process, &code); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1, true
	}
	return int(code), true
}
