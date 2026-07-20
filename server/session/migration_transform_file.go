package session

import (
	"context"
	"errors"
	"fmt"
	"os"
)

func transformLegacyEventLogToCurrentFile(
	ctx context.Context,
	sourcePath string,
	destinationPath string,
	spoolDir string,
	storage migrationSpoolStorage,
) (result legacyMigrationTransformResult, resultErr error) {
	source, err := openRegularSessionFile(sourcePath, "legacy session event log")
	if err != nil {
		return legacyMigrationTransformResult{}, fmt.Errorf(
			"open legacy session event log: %w",
			err,
		)
	}
	sourceOpen := true
	defer func() {
		if sourceOpen {
			resultErr = errors.Join(resultErr, closeEventLogMigrationFile(
				"legacy session event log",
				source,
			))
		}
	}()
	info, err := source.Stat()
	if err != nil {
		return legacyMigrationTransformResult{}, fmt.Errorf(
			"stat legacy session event log: %w",
			err,
		)
	}

	destination, err := os.OpenFile(
		destinationPath,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		return legacyMigrationTransformResult{}, fmt.Errorf(
			"create transformed event log: %w",
			err,
		)
	}
	destinationOpen := true
	defer func() {
		if destinationOpen {
			resultErr = errors.Join(resultErr, closeEventLogMigrationFile(
				"transformed event log",
				destination,
			))
		}
	}()

	ledger := newMigrationResourceLedger()
	result, err = transformLegacyEventLogV0(
		ctx,
		source,
		info.Size(),
		destination,
		spoolDir,
		ledger,
		storage,
	)
	if err != nil {
		return result, err
	}
	if err := destination.Sync(); err != nil {
		return result, fmt.Errorf("sync transformed event log: %w", err)
	}
	destinationOpen = false
	if err := closeEventLogMigrationFile("transformed event log", destination); err != nil {
		return result, err
	}
	sourceOpen = false
	if err := closeEventLogMigrationFile("legacy session event log", source); err != nil {
		return result, err
	}
	if _, err := validateCurrentEventLogComplete(destinationPath, ledger); err != nil {
		return result, fmt.Errorf("validate transformed event log: %w", err)
	}
	return result, nil
}
