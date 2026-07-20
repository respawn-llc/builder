package shell

import (
	"os"
	"testing"
)

func TestManagerClosePreservesBackgroundLogDirectory(t *testing.T) {
	manager, err := NewManager()
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	tempDir := manager.TempDir()
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })

	if err := manager.Close(); err != nil {
		t.Fatalf("close manager: %v", err)
	}
	if _, err := os.Stat(tempDir); err != nil {
		t.Fatalf("ordinary manager close changed log-directory lifetime: %v", err)
	}
}

func TestCloseOwnedManagerRemovesBackgroundLogDirectory(t *testing.T) {
	manager, err := NewManager()
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	tempDir := manager.TempDir()
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })

	if err := CloseOwnedManager(manager); err != nil {
		t.Fatalf("close owned manager: %v", err)
	}
	if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
		t.Fatalf("owned manager log directory remains after close: %v", err)
	}
}
