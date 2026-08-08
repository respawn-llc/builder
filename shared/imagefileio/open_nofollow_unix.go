//go:build darwin || linux

package imagefileio

import (
	"os"
	"syscall"
)

func openReadOnlyNoFollow(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func setReadBlocking(file *os.File) error {
	return syscall.SetNonblock(int(file.Fd()), false)
}
