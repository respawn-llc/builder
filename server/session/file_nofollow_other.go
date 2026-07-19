//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || windows)

package session

import "os"

func openSessionFileReadOnly(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errSessionFileSymlink
	}
	return os.Open(path)
}

func isSymlinkOpenError(err error) bool {
	return err == errSessionFileSymlink
}

func syncSessionDirectory(path string) error {
	dir, err := os.Open(path)
	return syncAndClose(dir, err)
}
