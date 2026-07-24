package databaseseed

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func (s Seed) Materialize(persistenceRoot string, databaseRelativePath string) error {
	databasePath := filepath.Join(persistenceRoot, databaseRelativePath)
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o755); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}
	databaseFile, err := os.OpenFile(databasePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, s.mode)
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
