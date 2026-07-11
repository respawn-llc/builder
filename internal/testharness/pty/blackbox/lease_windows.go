//go:build windows

package blackbox

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

var errLeaseContended = errors.New("artifact lease contended")

type fileLease struct {
	file *os.File
}

func acquireFileLease(path string) (*fileLease, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open artifact lease %s: %w", path, err)
	}
	handle := windows.Handle(file.Fd())
	if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{}); err != nil {
		closeErr := file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			if closeErr != nil {
				return nil, fmt.Errorf("%w; close contended artifact lease: %v", errLeaseContended, closeErr)
			}
			return nil, errLeaseContended
		}
		if closeErr != nil {
			return nil, fmt.Errorf("acquire artifact lease: %w; close lease: %v", err, closeErr)
		}
		return nil, fmt.Errorf("acquire artifact lease: %w", err)
	}
	return &fileLease{file: file}, nil
}

func (l *fileLease) release() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &windows.Overlapped{})
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil && closeErr != nil {
		return fmt.Errorf("release artifact lease: %w; close lease: %v", unlockErr, closeErr)
	}
	if unlockErr != nil {
		return fmt.Errorf("release artifact lease: %w", unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close artifact lease: %w", closeErr)
	}
	return nil
}
