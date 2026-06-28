//go:build windows

package main

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// userenv.dll wrappers that x/sys/windows does not vendor. The supervisor loads
// the target user's profile before launching the server so that HKCU, DPAPI,
// the Windows Credential Manager, and %USERPROFILE%-relative config (.gitconfig,
// .ssh) resolve exactly as they do for an interactive logon.
var (
	userenvDLL            = windows.NewLazySystemDLL("userenv.dll")
	procLoadUserProfile   = userenvDLL.NewProc("LoadUserProfileW")
	procUnloadUserProfile = userenvDLL.NewProc("UnloadUserProfile")
)

// profileInfo mirrors PROFILEINFOW. hProfile is an output handle that must be
// passed back to UnloadUserProfile.
type profileInfo struct {
	Size        uint32
	Flags       uint32
	UserName    *uint16
	ProfilePath *uint16
	DefaultPath *uint16
	ServerName  *uint16
	PolicyPath  *uint16
	Profile     windows.Handle
}

const piNoUI = 0x00000001 // PI_NOUI: do not display a progress UI

// loadUserProfile mounts the user's registry hive/profile for the logon token,
// returning the profile handle to unload on shutdown.
func loadUserProfile(token windows.Token, username string) (windows.Handle, error) {
	namePtr, err := windows.UTF16PtrFromString(username)
	if err != nil {
		return 0, fmt.Errorf("encode profile user name: %w", err)
	}
	info := profileInfo{
		Flags:    piNoUI,
		UserName: namePtr,
	}
	info.Size = uint32(unsafe.Sizeof(info))
	ret, _, callErr := procLoadUserProfile.Call(uintptr(token), uintptr(unsafe.Pointer(&info)))
	if ret == 0 {
		return 0, fmt.Errorf("LoadUserProfile: %w", callErr)
	}
	return info.Profile, nil
}

// unloadUserProfile releases a profile handle returned by loadUserProfile.
func unloadUserProfile(token windows.Token, profile windows.Handle) error {
	if profile == 0 {
		return nil
	}
	ret, _, callErr := procUnloadUserProfile.Call(uintptr(token), uintptr(profile))
	if ret == 0 {
		return fmt.Errorf("UnloadUserProfile: %w", callErr)
	}
	return nil
}
