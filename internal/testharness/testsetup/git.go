package testsetup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type repositorySeedEntry struct {
	relativePath string
	mode         fs.FileMode
	directory    bool
	contents     []byte
}

var cleanRepositorySeed struct {
	once    sync.Once
	entries []repositorySeedEntry
	err     error
}

func InitializeGitRepository(t *testing.T, root string) {
	t.Helper()
	entries, err := repositorySeed()
	if err != nil {
		t.Fatalf("prepare clean git repository seed: %v", err)
	}
	if err := materializeRepository(entries, root); err != nil {
		t.Fatalf("materialize clean git repository: %v", err)
	}
}

func repositorySeed() ([]repositorySeedEntry, error) {
	cleanRepositorySeed.once.Do(func() {
		cleanRepositorySeed.entries, cleanRepositorySeed.err = createRepositorySeed()
	})
	return cleanRepositorySeed.entries, cleanRepositorySeed.err
}

func createRepositorySeed() ([]repositorySeedEntry, error) {
	root, err := os.MkdirTemp("", "kent-test-clean-git-repository-*")
	if err != nil {
		return nil, fmt.Errorf("create temporary clean git repository seed: %w", err)
	}

	initializeErr := initializeRepository(root)
	var entries []repositorySeedEntry
	if initializeErr == nil {
		entries, initializeErr = snapshotRepository(root)
	}
	cleanupErr := os.RemoveAll(root)
	if cleanupErr != nil {
		cleanupErr = fmt.Errorf("remove temporary clean git repository seed %q: %w", root, cleanupErr)
	}
	if initializeErr != nil || cleanupErr != nil {
		return nil, errors.Join(initializeErr, cleanupErr)
	}
	return entries, nil
}

func initializeRepository(root string) error {
	if _, err := runGit(root, "init", "-q"); err != nil {
		return fmt.Errorf("initialize clean git repository seed: %w", err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("root\n"), 0o644); err != nil {
		return fmt.Errorf("write clean git repository seed README: %w", err)
	}
	if _, err := runGit(root, "add", "README.md"); err != nil {
		return fmt.Errorf("stage clean git repository seed README: %w", err)
	}
	if _, err := runGit(root, "commit", "-q", "-m", "init"); err != nil {
		return fmt.Errorf("commit clean git repository seed: %w", err)
	}
	return nil
}

func snapshotRepository(root string) ([]repositorySeedEntry, error) {
	var entries []repositorySeedEntry
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relativePath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			entries = append(entries, repositorySeedEntry{
				relativePath: relativePath,
				mode:         info.Mode().Perm(),
				directory:    true,
			})
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("clean git repository seed contains unsupported file %q with mode %v", path, entry.Type())
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		entries = append(entries, repositorySeedEntry{
			relativePath: relativePath,
			mode:         info.Mode().Perm(),
			contents:     contents,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("snapshot clean git repository seed: %w", err)
	}
	return entries, nil
}

func materializeRepository(entries []repositorySeedEntry, root string) error {
	for _, entry := range entries {
		path := filepath.Join(root, entry.relativePath)
		if entry.directory {
			if err := os.MkdirAll(path, entry.mode); err != nil {
				return fmt.Errorf("create clean git repository directory %q: %w", path, err)
			}
			continue
		}
		if err := os.WriteFile(path, entry.contents, entry.mode); err != nil {
			return fmt.Errorf("write clean git repository file %q: %w", path, err)
		}
	}
	return nil
}

func RunGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	output, err := runGit(dir, args...)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return output
}

func runGit(dir string, args ...string) (string, error) {
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(sanitizedGitEnvironment(os.Environ()),
		"GIT_AUTHOR_NAME=kent-test",
		"GIT_AUTHOR_EMAIL=kent-test@example.invalid",
		"GIT_COMMITTER_NAME=kent-test",
		"GIT_COMMITTER_EMAIL=kent-test@example.invalid",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func sanitizedGitEnvironment(environment []string) []string {
	managedKeys := map[string]struct{}{
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": {},
		"GIT_AUTHOR_DATE":                  {},
		"GIT_AUTHOR_EMAIL":                 {},
		"GIT_AUTHOR_NAME":                  {},
		"GIT_COMMITTER_DATE":               {},
		"GIT_COMMITTER_EMAIL":              {},
		"GIT_COMMITTER_NAME":               {},
		"GIT_COMMON_DIR":                   {},
		"GIT_CONFIG":                       {},
		"GIT_CONFIG_COUNT":                 {},
		"GIT_CONFIG_GLOBAL":                {},
		"GIT_CONFIG_NOSYSTEM":              {},
		"GIT_CONFIG_PARAMETERS":            {},
		"GIT_CONFIG_SYSTEM":                {},
		"GIT_DIR":                          {},
		"GIT_GLOB_PATHSPECS":               {},
		"GIT_GRAFT_FILE":                   {},
		"GIT_ICASE_PATHSPECS":              {},
		"GIT_IMPLICIT_WORK_TREE":           {},
		"GIT_INDEX_FILE":                   {},
		"GIT_INTERNAL_SUPER_PREFIX":        {},
		"GIT_LITERAL_PATHSPECS":            {},
		"GIT_NAMESPACE":                    {},
		"GIT_NOGLOB_PATHSPECS":             {},
		"GIT_NO_REPLACE_OBJECTS":           {},
		"GIT_OBJECT_DIRECTORY":             {},
		"GIT_PREFIX":                       {},
		"GIT_REPLACE_REF_BASE":             {},
		"GIT_SHALLOW_FILE":                 {},
		"GIT_WORK_TREE":                    {},
	}
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			key = entry
		}
		if _, managed := managedKeys[key]; !managed {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}
