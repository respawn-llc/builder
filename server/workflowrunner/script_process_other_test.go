//go:build !(aix || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris || windows)

package workflowrunner

import "testing"

func assertScriptProcessGone(t *testing.T, _ int) {
	t.Helper()
	t.Skip("process disappearance assertions require POSIX signal handling")
}

func assertScriptProcessAlive(t *testing.T, _ int) {
	t.Helper()
	t.Skip("process liveness assertions require POSIX signal handling")
}
