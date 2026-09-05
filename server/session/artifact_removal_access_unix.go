//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || zos

package session

import "golang.org/x/sys/unix"

func preflightDirectoryMutation(path string) error {
	return unix.Access(path, unix.W_OK|unix.X_OK)
}
