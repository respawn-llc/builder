//go:build linux

package session

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func migrationMemoryLimitResource() int {
	// Linux RLIMIT_AS and RLIMIT_DATA both account runtime/toolchain mappings
	// that are unrelated to the fixture's resident working set. Go 1.26 can
	// therefore fail small allocations while RSS is still below the contract.
	// Use the kernel-reported peak resident set as the cross-toolchain oracle,
	// matching the Darwin path.
	return -1
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
