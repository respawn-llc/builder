//go:build !windows

package blackbox

import (
	"errors"
	"fmt"
	"os"
	"syscall"
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
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		closeErr := file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
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
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
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
