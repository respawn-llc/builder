//go:build windows

package session

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func atomicallyReplaceEventLog(stagedPath, eventsPath string) error {
	staged, err := windows.UTF16PtrFromString(stagedPath)
	if err != nil {
		return fmt.Errorf("encode staged event-log path: %w", err)
	}
	target, err := windows.UTF16PtrFromString(eventsPath)
	if err != nil {
		return fmt.Errorf("encode event-log path: %w", err)
	}
	if err := windows.MoveFileEx(
		staged,
		target,
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	); err != nil {
		return fmt.Errorf("install staged event log: %w", err)
	}
	return nil
}

// MoveFileEx with MOVEFILE_WRITE_THROUGH commits the rename before returning.
// Windows does not expose a portable directory fsync through os.File.
func syncEventLogDirectory(string) error {
	return nil
}
