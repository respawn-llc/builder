//go:build aix || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package app

import (
	"os/signal"
	"syscall"
)

func ignoreLifecycleHookHelperTermination() {
	signal.Ignore(syscall.SIGTERM)
}
