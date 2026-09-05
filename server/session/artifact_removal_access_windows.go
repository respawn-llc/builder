//go:build windows

package session

import (
	"io/fs"
	"os"
)

func preflightDirectoryMutation(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o222 == 0 {
		return fs.ErrPermission
	}
	return nil
}
