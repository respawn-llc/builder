package filemode

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
)

type EventLogAppendBlocker struct {
	mu       sync.Mutex
	path     string
	contents []byte
	mode     os.FileMode
	restored bool
}

func BlockEventLogAppends(path string) (*EventLogAppendBlocker, error) {
	if path == "" {
		return nil, errors.New("event-log append blocker requires a path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat event log before blocking appends: %w", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read event log before blocking appends: %w", err)
	}
	if err := os.Remove(path); err != nil {
		return nil, fmt.Errorf("remove event log before blocking appends: %w", err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return nil, errors.Join(
				fmt.Errorf("replace event log with directory: %w", err),
				fmt.Errorf("remove failed event-log blocker artifact: %w", removeErr),
			)
		}
		if restoreErr := os.WriteFile(path, contents, info.Mode().Perm()); restoreErr != nil {
			return nil, errors.Join(
				fmt.Errorf("replace event log with directory: %w", err),
				fmt.Errorf("restore event log after blocker setup failure: %w", restoreErr),
			)
		}
		return nil, fmt.Errorf("replace event log with directory: %w", err)
	}
	return &EventLogAppendBlocker{
		path:     path,
		contents: contents,
		mode:     info.Mode(),
	}, nil
}

func MustBlockEventLogAppends(t testing.TB, path string) *EventLogAppendBlocker {
	t.Helper()
	blocker, err := BlockEventLogAppends(path)
	if err != nil {
		t.Fatalf("block event-log appends: %v", err)
	}
	t.Cleanup(func() {
		if err := blocker.Restore(); err != nil {
			t.Errorf("restore event log: %v", err)
		}
	})
	return blocker
}

func (b *EventLogAppendBlocker) Restore() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.restored {
		return nil
	}
	if err := os.Remove(b.path); err != nil {
		return fmt.Errorf("remove event-log blocker directory: %w", err)
	}
	if err := os.WriteFile(b.path, b.contents, b.mode.Perm()); err != nil {
		return fmt.Errorf("restore event log: %w", err)
	}
	b.restored = true
	return nil
}
