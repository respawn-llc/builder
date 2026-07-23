package app

import "testing"

func TestSanitizedGitEnvRemovesRepositoryScopedVariables(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"HOME=/tmp/home",
		"GIT_DIR=/tmp/repo/.git",
		"GIT_WORK_TREE=/tmp/repo",
		"GIT_COMMON_DIR=/tmp/common",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0=.githooks",
	}

	got := sanitizedGitEnv(base)
	for _, entry := range got {
		if entry == "PATH=/usr/bin" || entry == "HOME=/tmp/home" {
			continue
		}
		t.Fatalf("unexpected git-scoped env entry retained: %q", entry)
	}
	if len(got) != 2 {
		t.Fatalf("expected only non-git env entries to remain, got %v", got)
	}
}
