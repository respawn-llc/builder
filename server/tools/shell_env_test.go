package tools

import (
	"strings"
	"testing"

	"core/shared/sessionenv"
)

func envMap(t *testing.T, in []string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(in))
	for _, entry := range in {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			t.Fatalf("invalid env entry: %q", entry)
		}
		if _, exists := out[key]; exists {
			t.Fatalf("duplicate env key: %s", key)
		}
		out[key] = value
	}
	return out
}

func TestEnrichAppliesAgentShellDefaults(t *testing.T) {
	env := envMap(t, EnrichShellEnv([]string{
		"AGENT=other",
		"TERM=xterm-256color",
		"DOCKER_CLI_HINTS=true",
		"BUILDKIT_PROGRESS=auto",
		"COMPOSE_PROGRESS=auto",
		"COMPOSE_ANSI=always",
		"npm_config_progress=true",
		"YARN_ENABLE_PROGRESS_BARS=true",
		"KEEP=1",
	}))

	want := map[string]string{
		"AGENT":                     "kent",
		"TERM":                      "dumb",
		"CI":                        "1",
		"NO_COLOR":                  "1",
		"GIT_TERMINAL_PROMPT":       "0",
		"DOCKER_CLI_HINTS":          "false",
		"BUILDKIT_PROGRESS":         "plain",
		"COMPOSE_PROGRESS":          "plain",
		"COMPOSE_ANSI":              "never",
		"npm_config_progress":       "false",
		"YARN_ENABLE_PROGRESS_BARS": "false",
		"KEEP":                      "1",
	}
	for key, wantValue := range want {
		if env[key] != wantValue {
			t.Fatalf("%s = %q, want %q", key, env[key], wantValue)
		}
	}
}

func TestEnrichForSessionInjectsSessionIDEnv(t *testing.T) {
	env := envMap(t, EnrichShellEnvForSession([]string{"PATH=/bin", "KEEP=1"}, " session-1 "))
	if got := env[sessionenv.SessionIDEnv]; got != "session-1" {
		t.Fatalf("%s = %q, want session-1", sessionenv.SessionIDEnv, got)
	}
	if env["KEEP"] != "1" {
		t.Fatalf("KEEP = %q, want 1", env["KEEP"])
	}
}

func TestEnrichForSessionOverridesExistingSessionIDEnv(t *testing.T) {
	env := envMap(t, EnrichShellEnvForSession([]string{sessionenv.SessionIDEnv + "=old"}, "new"))
	if got := env[sessionenv.SessionIDEnv]; got != "new" {
		t.Fatalf("%s = %q, want new", sessionenv.SessionIDEnv, got)
	}
}

func TestEnrichForSessionOmitsBlankSessionID(t *testing.T) {
	env := envMap(t, EnrichShellEnvForSession(nil, " \n\t"))
	if _, exists := env[sessionenv.SessionIDEnv]; exists {
		t.Fatalf("%s should be omitted for blank session id", sessionenv.SessionIDEnv)
	}
}
