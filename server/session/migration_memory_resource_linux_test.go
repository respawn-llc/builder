//go:build linux

package session

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func migrationMemoryLimitResource() int {
	// Resident pages are a subset of virtual address space, so a 128 MiB hard
	// RLIMIT_AS is a conservative hard upper bound on resident memory.
	return unix.RLIMIT_AS
}

func assertMigrationResidentMemoryWithinLimit() error {
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		return fmt.Errorf("read migration child resident memory: %w", err)
	}
	maxResidentBytes := usage.Maxrss << 10
	if maxResidentBytes > migrationHardMemoryLimitBytes {
		return fmt.Errorf(
			"peak resident memory %d exceeds limit %d",
			maxResidentBytes,
			migrationHardMemoryLimitBytes,
		)
	}
	return nil
}
