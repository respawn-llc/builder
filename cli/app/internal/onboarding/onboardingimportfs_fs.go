package onboarding

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func ExecuteSymlink(targetRoot string, sourcePath string, kind string, sourceLabel string) ([]string, error) {
	if err := RequireSourceDirectory(sourcePath, sourceLabel); err != nil {
		return nil, err
	}
	if err := PrepareEmptyDirectorySymlinkTarget(targetRoot, kind); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(targetRoot), 0o755); err != nil {
		return nil, fmt.Errorf("create %s parent root: %w", kind, err)
	}
	if err := os.Symlink(sourcePath, targetRoot); err != nil {
		return nil, fmt.Errorf("symlink %s: %w", sourceLabel, err)
	}
	return []string{targetRoot}, nil
}

func RollbackCreatedPaths(paths []string) error {
	var rollbackErr error
	for index := len(paths) - 1; index >= 0; index-- {
		path := strings.TrimSpace(paths[index])
		if path == "" {
			continue
		}
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("rollback import path %s: %w", path, err))
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				rollbackErr = errors.Join(rollbackErr, fmt.Errorf("rollback import path %s: %w", path, err))
			}
			continue
		}
		if err := os.RemoveAll(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("rollback import path %s: %w", path, err))
		}
	}
	return rollbackErr
}

func PrepareEmptyDirectorySymlinkTarget(path string, kind string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect %s symlink target %s: %w", kind, path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s symlink target already exists: %s", kind, path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("read %s symlink target %s: %w", kind, path, err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("%s symlink target already exists: %s", kind, path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove empty %s symlink target %s: %w", kind, path, err)
	}
	return nil
}

// ErrSourceDirectoryInvalid marks an import source path that could not be
// validated as a usable directory (missing, unreadable, or not a directory).
// Callers and tests match this with errors.Is rather than comparing rendered
// message text.
var ErrSourceDirectoryInvalid = errors.New("import source directory is invalid")

func RequireSourceDirectory(path string, label string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect %s: %w: %w", label, ErrSourceDirectoryInvalid, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory: %s: %w", label, path, ErrSourceDirectoryInvalid)
	}
	return nil
}
