//go:build windows

package main

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	userenvDLL            = windows.NewLazySystemDLL("userenv.dll")
	procLoadUserProfile   = userenvDLL.NewProc("LoadUserProfileW")
	procUnloadUserProfile = userenvDLL.NewProc("UnloadUserProfile")
)

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

const piNoUI = 0x00000001

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
