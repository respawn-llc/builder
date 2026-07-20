//go:build linux

package session

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func migrationMemoryLimitResource() int {
	// RLIMIT_AS includes the Go runtime, executable, and shared-library address
	// mappings, so the same bounded workload can fail before allocating its
	// working set as toolchain mapping strategy changes. RLIMIT_DATA applies the
	// hard ceiling to heap and anonymous data mappings; peak RSS is checked
	// separately after the fixture.
	return unix.RLIMIT_DATA
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
