package patch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type patchFileState struct {
	Exists     bool
	Content    []string
	Mode       os.FileMode
	NewPath    string
	Original   string
	StagedPath string
}

type fileSnapshot struct {
	Exists bool
	Mode   os.FileMode
	Data   []byte
}

type committedWrite struct {
	Path   string
	Before fileSnapshot
}

type removedSource struct {
	Path   string
	Before fileSnapshot
}

func sortedCommitStates(state map[string]*patchFileState) []*patchFileState {
	out := make([]*patchFileState, 0, len(state))
	for _, s := range state {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].NewPath < out[j].NewPath
	})
	return out
}

func cleanupStagedFiles(states []*patchFileState) {
	for _, s := range states {
		if strings.TrimSpace(s.StagedPath) == "" {
			continue
		}
		_ = os.Remove(s.StagedPath)
	}
}

func commitStagedFiles(states []*patchFileState, deleteTargets map[string]struct{}) error {
	committed := make([]committedWrite, 0, len(states))
	removed := make([]removedSource, 0, len(states)*2)
	removedPaths := make(map[string]struct{}, len(deleteTargets)+len(states))
	rollback := func() error {
		var rollbackErr error
		for i := len(committed) - 1; i >= 0; i-- {
			entry := committed[i]
			if err := restoreSnapshot(entry.Path, entry.Before); err != nil {
				rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore target %s: %w", entry.Path, err))
			}
		}
		for i := len(removed) - 1; i >= 0; i-- {
			entry := removed[i]
			if err := restoreSnapshot(entry.Path, entry.Before); err != nil {
				rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore moved source %s: %w", entry.Path, err))
			}
		}
		return rollbackErr
	}

	removePath := func(path string, label string) error {
		if _, seen := removedPaths[path]; seen {
			return nil
		}
		before, err := captureSnapshot(path)
		if err != nil {
			primary := fmt.Errorf("snapshot %s %s: %w", label, path, err)
			if rollbackErr := rollback(); rollbackErr != nil {
				return errors.Join(primary, fmt.Errorf("rollback failed: %w", rollbackErr))
			}
			return primary
		}
		removedPaths[path] = struct{}{}
		if !before.Exists {
			return nil
		}
		if err := os.Remove(path); err != nil {
			primary := fmt.Errorf("remove %s %s: %w", label, path, err)
			if rollbackErr := rollback(); rollbackErr != nil {
				return errors.Join(primary, fmt.Errorf("rollback failed: %w", rollbackErr))
			}
			return primary
		}
		removed = append(removed, removedSource{Path: path, Before: before})
		return nil
	}

	deletePaths := make([]string, 0, len(deleteTargets))
	for path := range deleteTargets {
		deletePaths = append(deletePaths, path)
	}
	sort.Strings(deletePaths)
	for _, path := range deletePaths {
		if err := removePath(path, "delete target"); err != nil {
			return err
		}
	}

	for _, s := range states {
		if s.NewPath != s.Original {
			if err := removePath(s.Original, "moved source"); err != nil {
				return err
			}
		}
	}

	for _, s := range states {
		if err := os.MkdirAll(filepath.Dir(s.NewPath), 0o755); err != nil {
			primary := fmt.Errorf("create parent dir for %s: %w", s.NewPath, err)
			if rollbackErr := rollback(); rollbackErr != nil {
				return errors.Join(primary, fmt.Errorf("rollback failed: %w", rollbackErr))
			}
			return primary
		}
		before, err := captureSnapshot(s.NewPath)
		if err != nil {
			primary := fmt.Errorf("snapshot target %s: %w", s.NewPath, err)
			if rollbackErr := rollback(); rollbackErr != nil {
				return errors.Join(primary, fmt.Errorf("rollback failed: %w", rollbackErr))
			}
			return primary
		}
		if err := os.Rename(s.StagedPath, s.NewPath); err != nil {
			primary := fmt.Errorf("commit write %s: %w", s.NewPath, err)
			if rollbackErr := rollback(); rollbackErr != nil {
				return errors.Join(primary, fmt.Errorf("rollback failed: %w", rollbackErr))
			}
			return primary
		}
		committed = append(committed, committedWrite{Path: s.NewPath, Before: before})
	}

	return nil
}

func createStagedFile(targetPath string, data []byte, mode os.FileMode) (string, error) {
	stageDir, err := nearestExistingDirectory(filepath.Dir(targetPath))
	if err != nil {
		return "", err
	}
	pattern := ".kent-patch-*"
	if base := strings.TrimSpace(filepath.Base(targetPath)); base != "" && base != "." && base != string(filepath.Separator) {
		pattern = ".kent-patch-" + base + "-*"
	}
	file, err := os.CreateTemp(stageDir, pattern)
	if err != nil {
		return "", err
	}
	path := file.Name()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func nearestExistingDirectory(path string) (string, error) {
	current := filepath.Clean(path)
	for {
		info, err := os.Stat(current)
		if err == nil {
			if info.IsDir() {
				return current, nil
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		next := filepath.Dir(current)
		if next == current {
			return "", fmt.Errorf("no existing directory ancestor for %s", path)
		}
		current = next
	}
}

func captureSnapshot(path string) (fileSnapshot, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileSnapshot{}, nil
	}
	if err != nil {
		return fileSnapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return fileSnapshot{}, fmt.Errorf("not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fileSnapshot{}, err
	}
	return fileSnapshot{Exists: true, Mode: info.Mode().Perm(), Data: data}, nil
}

func restoreSnapshot(path string, snapshot fileSnapshot) error {
	if !snapshot.Exists {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".kent.rollback.tmp"
	if err := os.WriteFile(tmp, snapshot.Data, snapshot.Mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
