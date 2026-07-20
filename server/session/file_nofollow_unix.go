//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package session

import (
	"errors"
	"os"
	"syscall"
)

func openSessionFileReadOnly(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}

func isSymlinkOpenError(err error) bool {
	return errors.Is(err, syscall.ELOOP)
}

func syncSessionDirectory(path string) error {
	dir, err := os.Open(path)
	return syncAndClose(dir, err)
}
