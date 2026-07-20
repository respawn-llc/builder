//go:build windows

package session

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsFileAttributeTagInfo struct {
	FileAttributes uint32
	ReparseTag     uint32
}

func openSessionFileReadOnly(path string) (*os.File, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.FILE_GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	var info windowsFileAttributeTagInfo
	if err := windows.GetFileInformationByHandleEx(
		handle,
		windows.FileAttributeTagInfo,
		(*byte)(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(handle)
		return nil, errSessionFileSymlink
	}
	fp := os.NewFile(uintptr(handle), path)
	if fp == nil {
		_ = windows.CloseHandle(handle)
		return nil, os.ErrInvalid
	}
	return fp, nil
}

func isSymlinkOpenError(err error) bool {
	return err == errSessionFileSymlink
}

func syncSessionDirectory(path string) error {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return err
	}
	return errors.Join(windows.FlushFileBuffers(handle), windows.CloseHandle(handle))
}
