package session

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

type scheduledSessionArtifactKind uint8

const (
	scheduledSessionFile scheduledSessionArtifactKind = iota + 1
	scheduledSessionDirectory
)

type scheduledSessionArtifact struct {
	path string
	kind scheduledSessionArtifactKind
}

type SessionArtifactRemovalSchedule struct {
	artifacts []scheduledSessionArtifact
}

type SessionArtifactPreflightError struct {
	Path string
	Err  error
}

func (e *SessionArtifactPreflightError) Error() string {
	return fmt.Sprintf("preflight Session artifact removal %s: %v", e.Path, e.Err)
}

func (e *SessionArtifactPreflightError) Unwrap() error {
	return e.Err
}

type SessionArtifactRemovalError struct {
	RemainingPath string
	Err           error
}

func (e *SessionArtifactRemovalError) Error() string {
	return fmt.Sprintf("remove Session artifact %s: %v", e.RemainingPath, e.Err)
}

func (e *SessionArtifactRemovalError) Unwrap() error {
	return e.Err
}

func PreflightSessionArtifactRemoval(sessionDir string) (SessionArtifactRemovalSchedule, error) {
	if sessionDir == "" {
		return SessionArtifactRemovalSchedule{}, errors.New("Session directory is required")
	}
	if !filepath.IsAbs(sessionDir) {
		return SessionArtifactRemovalSchedule{}, errors.New("Session directory must be absolute")
	}
	sessionDir = filepath.Clean(sessionDir)
	migrationDir := filepath.Join(sessionDir, eventLogMigrationWorkspaceDir)
	schedule := SessionArtifactRemovalSchedule{
		artifacts: []scheduledSessionArtifact{
			{path: filepath.Join(sessionDir, eventsFile), kind: scheduledSessionFile},
			{path: filepath.Join(sessionDir, eventLogPersistenceLockFile), kind: scheduledSessionFile},
			{path: filepath.Join(sessionDir, appendRecoveryFile), kind: scheduledSessionFile},
			{path: RunLogPath(sessionDir), kind: scheduledSessionFile},
			{path: filepath.Join(migrationDir, eventLogMigrationWorkspaceMarkerFile), kind: scheduledSessionFile},
			{path: filepath.Join(migrationDir, eventLogMigrationStagedLogFile), kind: scheduledSessionFile},
			{path: filepath.Join(migrationDir, eventLogMigrationReadyMarkerFile), kind: scheduledSessionFile},
			{path: migrationDir, kind: scheduledSessionDirectory},
			{path: sessionDir, kind: scheduledSessionDirectory},
		},
	}
	for _, artifact := range schedule.artifacts {
		if err := preflightScheduledSessionArtifact(artifact); err != nil {
			return SessionArtifactRemovalSchedule{}, &SessionArtifactPreflightError{
				Path: artifact.path,
				Err:  err,
			}
		}
	}
	return schedule, nil
}

func preflightScheduledSessionArtifact(artifact scheduledSessionArtifact) error {
	info, err := os.Lstat(artifact.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if artifact.kind == scheduledSessionFile && info.IsDir() {
		return syscall.EISDIR
	}
	return preflightDirectoryMutation(filepath.Dir(artifact.path))
}

func RemovePreflightedSessionArtifacts(schedule SessionArtifactRemovalSchedule) error {
	if len(schedule.artifacts) == 0 {
		return errors.New("Session artifact removal schedule is required")
	}
	for _, artifact := range schedule.artifacts {
		var err error
		if artifact.kind == scheduledSessionDirectory {
			err = removeKnownSessionDirectoryIfEmpty(artifact.path)
		} else {
			err = os.Remove(artifact.path)
			if errors.Is(err, os.ErrNotExist) {
				err = nil
			}
		}
		if err != nil {
			return &SessionArtifactRemovalError{
				RemainingPath: artifact.path,
				Err:           err,
			}
		}
	}
	return nil
}

func removeKnownSessionDirectoryIfEmpty(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		empty, emptyErr := directoryEmpty(path)
		if emptyErr == nil && !empty {
			return nil
		}
		if emptyErr != nil && !errors.Is(emptyErr, syscall.ENOTDIR) {
			return emptyErr
		}
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) ||
		errors.Is(err, syscall.ENOTEMPTY) ||
		errors.Is(err, syscall.EEXIST) {
		return nil
	}
	return err
}

func directoryEmpty(path string) (bool, error) {
	dir, err := os.Open(path)
	if err != nil {
		return false, err
	}
	_, readErr := dir.Readdirnames(1)
	closeErr := dir.Close()
	switch {
	case errors.Is(readErr, io.EOF):
		return true, closeErr
	case readErr != nil:
		return false, errors.Join(readErr, closeErr)
	default:
		return false, closeErr
	}
}
