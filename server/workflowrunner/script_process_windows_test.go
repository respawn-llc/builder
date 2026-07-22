//go:build windows

package workflowrunner

import "testing"

func assertScriptProcessGroupGone(t *testing.T, _ int) {
	t.Helper()
	t.Fatal("process group assertions require POSIX signal handling")
}
