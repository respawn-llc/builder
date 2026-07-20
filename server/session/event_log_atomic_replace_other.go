//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || windows)

package session

import (
	"errors"
	"fmt"
	"os"
)

func atomicallyReplaceEventLog(stagedPath, eventsPath string) error {
	if err := os.Rename(stagedPath, eventsPath); err != nil {
		return fmt.Errorf("install staged event log: %w", err)
	}
	return nil
}

func syncEventLogDirectory(dir string) (resultErr error) {
	fp, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open event-log directory for sync: %w", err)
	}
	defer func() {
		if closeErr := fp.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close event-log directory: %w", closeErr))
		}
	}()
	if err := fp.Sync(); err != nil {
		return fmt.Errorf("sync event-log directory: %w", err)
	}
	return nil
}
