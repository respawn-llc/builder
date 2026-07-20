//go:build windows

package session

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func armEventLogWorkspaceCleanupFailure(t *testing.T, workspace string) func() {
	t.Helper()
	spoolDir := filepath.Join(workspace, eventLogMigrationSpoolDir)
	if err := os.Mkdir(spoolDir, 0o700); err != nil {
		t.Fatalf("create owned spool directory: %v", err)
	}
	path := filepath.Join(spoolDir, "active")
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatalf("encode active spool artifact path: %v", err)
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.CREATE_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatalf("lock active spool artifact against deletion: %v", err)
	}
	return func() {
		if err := windows.CloseHandle(handle); err != nil {
			t.Errorf("release active spool artifact: %v", err)
		}
		if err := os.RemoveAll(spoolDir); err != nil {
			t.Errorf("remove active spool artifact after test: %v", err)
		}
	}
}
