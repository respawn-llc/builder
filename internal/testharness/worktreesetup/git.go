package worktreesetup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"core/shared/config"
)

func InitializeGitRepository(t *testing.T, root string) {
	t.Helper()
	RunGit(t, root, "init", "-q")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("root\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	RunGit(t, root, "add", "README.md")
	RunGit(t, root, "commit", "-q", "-m", "init")
	canonicalRoot, err := config.CanonicalWorkspaceRoot(root)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	if got := RunGit(t, root, "rev-parse", "--show-toplevel"); got != canonicalRoot {
		t.Fatalf("git top-level = %q, want %q", got, canonicalRoot)
	}
}

func RunGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
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
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
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
