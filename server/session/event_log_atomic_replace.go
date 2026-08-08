//go:build !windows

package session

import (
	"fmt"
	"os"
)

func atomicallyReplaceEventLog(stagedPath, eventsPath string) error {
	if err := os.Rename(stagedPath, eventsPath); err != nil {
		return fmt.Errorf("install staged event log: %w", err)
	}
	return nil
}
