package session

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/gofrs/flock"
)

const eventLogPersistenceLockFile = "events.jsonl.lock"

func acquireEventLogPersistenceLock(sessionDir string) (*flock.Flock, string, error) {
	lockPath := filepath.Join(sessionDir, eventLogPersistenceLockFile)
	lock := flock.New(lockPath)
	if err := lock.Lock(); err != nil {
		return nil, lockPath, fmt.Errorf(
			"acquire event-log persistence lock %s: %w",
			lockPath,
			err,
		)
	}
	return lock, lockPath, nil
}

func initializeEventLogPersistenceLock(sessionDir string) error {
	lock, lockPath, err := acquireEventLogPersistenceLock(sessionDir)
	if err != nil {
		return err
	}
	return releaseEventLogPersistenceLock(lock, lockPath)
}

func releaseEventLogPersistenceLock(lock *flock.Flock, lockPath string) error {
	if lock == nil {
		return nil
	}
	if err := lock.Close(); err != nil {
		return fmt.Errorf("release event-log persistence lock %s: %w", lockPath, err)
	}
	return nil
}

func joinEventLogPersistenceLockRelease(
	resultErr *error,
	lock *flock.Flock,
	lockPath string,
) {
	if resultErr == nil {
		return
	}
	*resultErr = errors.Join(
		*resultErr,
		releaseEventLogPersistenceLock(lock, lockPath),
	)
}
