package worktreesetup

import (
	"strings"
	"testing"
)

func TestSanitizedGitEnvironmentDropsExternalConfigOverrides(t *testing.T) {
	environment := sanitizedGitEnvironment([]string{
		"GIT_CONFIG_GLOBAL=/tmp/global.gitconfig",
		"GIT_CONFIG_SYSTEM=/tmp/system.gitconfig",
		"GIT_CONFIG_NOSYSTEM=1",
		"KEEP=value",
	})
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM":
			t.Fatalf("sanitized environment retains %q", entry)
		}
	}
	if len(environment) != 1 || environment[0] != "KEEP=value" {
		t.Fatalf("sanitized environment = %q, want [KEEP=value]", environment)
	}
}
