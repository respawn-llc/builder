package databaseseed

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (s Seed) Materialize(persistenceRoot string, databaseRelativePath string) error {
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
	canonicalPersistenceRoot, err := createAndResolveDirectory(persistenceRoot)
	if err != nil {
		return err
	}
	canonicalDatabaseDirectory, err := ensureDirectoryWithinRoot(
		canonicalPersistenceRoot,
		filepath.Dir(cleanRelativePath),
	)
	if err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}
	databasePath := filepath.Join(canonicalDatabaseDirectory, filepath.Base(cleanRelativePath))
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

func createAndResolveDirectory(path string) (string, error) {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return "", fmt.Errorf("create persistence root: %w", err)
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve persistence root: %w", err)
	}
	return resolvedPath, nil
}

func ensureDirectoryWithinRoot(root string, relativeDirectory string) (string, error) {
	if relativeDirectory == "." {
		return root, nil
	}

	components := strings.Split(relativeDirectory, string(filepath.Separator))
	directory := root
	for index, component := range components {
		candidate := filepath.Join(directory, component)
		_, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			missingDirectory := filepath.Join(directory, strings.Join(components[index:], string(filepath.Separator)))
			if err := os.MkdirAll(missingDirectory, 0o755); err != nil {
				return "", err
			}
			resolvedDirectory, err := filepath.EvalSymlinks(missingDirectory)
			if err != nil {
				return "", err
			}
			if !directoryWithinRoot(root, resolvedDirectory) {
				return "", fmt.Errorf("%q escapes persistence root", relativeDirectory)
			}
			return resolvedDirectory, nil
		}
		if err != nil {
			return "", err
		}

		resolvedDirectory, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(resolvedDirectory)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("%q is not a directory", component)
		}
		if !directoryWithinRoot(root, resolvedDirectory) {
			return "", fmt.Errorf("%q escapes persistence root", relativeDirectory)
		}
		directory = resolvedDirectory
	}
	return directory, nil
}

func directoryWithinRoot(root string, directory string) bool {
	relativeDirectory, err := filepath.Rel(root, directory)
	return err == nil && filepath.IsLocal(relativeDirectory)
}
