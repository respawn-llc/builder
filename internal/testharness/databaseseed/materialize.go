package databaseseed

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func (s Seed) Materialize(persistenceRoot string, databaseRelativePath string) (resultErr error) {
	if persistenceRoot == "" {
		return errors.New("database persistence root is required")
	}
	if databaseRelativePath == "" || !filepath.IsLocal(databaseRelativePath) {
		return fmt.Errorf("invalid database relative path %q", databaseRelativePath)
	}
	cleanRelativePath := filepath.Clean(databaseRelativePath)
	if cleanRelativePath == "." {
		return fmt.Errorf("invalid database relative path %q", databaseRelativePath)
	}
	if err := os.MkdirAll(persistenceRoot, 0o755); err != nil {
		return fmt.Errorf("create persistence root: %w", err)
	}
	root, err := os.OpenRoot(persistenceRoot)
	if err != nil {
		return fmt.Errorf("open persistence root: %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()
	if databaseDirectory := filepath.Dir(cleanRelativePath); databaseDirectory != "." {
		if err := root.MkdirAll(databaseDirectory, 0o755); err != nil {
			return fmt.Errorf("create database directory: %w", err)
		}
	}
	databaseFile, err := root.OpenFile(cleanRelativePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, s.mode)
	if err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	_, writeErr := databaseFile.Write(s.contents)
	closeErr := databaseFile.Close()
	if writeErr != nil {
		writeErr = fmt.Errorf("write database: %w", writeErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close database: %w", closeErr)
	}
	return errors.Join(writeErr, closeErr)
}
