package databaseseed

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func Create(prefix string, databaseRelativePath string, initialize func(string) error) (seed Seed, resultErr error) {
	if initialize == nil {
		return Seed{}, errors.New("database seed initializer is required")
	}
	root, err := os.MkdirTemp("", prefix)
	if err != nil {
		return Seed{}, fmt.Errorf("create database seed root: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(root); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("remove database seed root %q: %w", root, err))
		}
	}()

	if err := initialize(root); err != nil {
		return Seed{}, err
	}
	databasePath := filepath.Join(root, databaseRelativePath)
	for _, suffix := range []string{"-wal", "-shm"} {
		sidecarPath := databasePath + suffix
		_, err := os.Stat(sidecarPath)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return Seed{}, fmt.Errorf("stat database seed sidecar %q: %w", sidecarPath, err)
		default:
			return Seed{}, fmt.Errorf("database seed sidecar remained after close: %q", sidecarPath)
		}
	}
	info, err := os.Stat(databasePath)
	if err != nil {
		return Seed{}, fmt.Errorf("stat database seed: %w", err)
	}
	contents, err := os.ReadFile(databasePath)
	if err != nil {
		return Seed{}, fmt.Errorf("read database seed: %w", err)
	}
	return Seed{contents: contents, mode: info.Mode().Perm()}, nil
}
