//go:build windows

package workflowrunner

import "testing"

func assertScriptProcessGone(t *testing.T, _ int) {
	t.Helper()
	t.Fatal("process group assertions require POSIX signal handling")
}

func assertScriptProcessAlive(t *testing.T, _ int) {
	t.Helper()
	t.Fatal("process liveness assertions require POSIX signal handling")
}
