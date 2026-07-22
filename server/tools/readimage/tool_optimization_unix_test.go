//go:build darwin || linux

package readimage

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCall_FIFOPathReturnsToolError(t *testing.T) {
	workspace := t.TempDir()
	fifoPath := filepath.Join(workspace, "pipe.png")
	if err := syscall.Mkfifo(fifoPath, 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}

	tool := newReadImageTestTool(t, workspace, true)
	result := callReadImageTool(t, tool, "call-fifo", `{"path":"pipe.png"}`)
	if !result.IsError {
		t.Fatalf("expected FIFO path to be rejected")
	}
	if got := toolError(t, result); !strings.Contains(got, "not a regular file") {
		t.Fatalf("expected regular file error, got %q", got)
	}
}
