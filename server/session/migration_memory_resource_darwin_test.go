//go:build darwin

package session

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func migrationMemoryLimitResource() int {
	// Darwin aliases RLIMIT_RSS to RLIMIT_AS and rejects lowering the limit after
	// the Go runtime has reserved its virtual arena. The child therefore verifies
	// the kernel-reported peak resident set after the bounded transform.
	return -1
}

func assertMigrationResidentMemoryWithinLimit() error {
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		return fmt.Errorf("read migration child resident memory: %w", err)
	}
	if usage.Maxrss > migrationResidentMemoryOracleBytes {
		return fmt.Errorf(
			"peak resident memory %d exceeds runtime-adjusted oracle %d for %d-byte migration working-set contract",
			usage.Maxrss,
			migrationResidentMemoryOracleBytes,
			migrationHardMemoryLimitBytes,
		)
	}
	return nil
}
