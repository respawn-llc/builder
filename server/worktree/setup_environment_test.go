package worktree

import (
	"strings"
	"testing"
)

func TestBuildSetupEnvironmentReplacesReservedValuesWithPlatformKeyPolicy(t *testing.T) {
	sessionID := "current-session"
	payload := setupScriptPayload{
		SourceWorkspaceRoot: "/source",
		BranchName:          "task-123",
		WorktreeRoot:        "/worktree",
		SessionID:           &sessionID,
		ProjectID:           "project-1",
		WorkspaceID:         "workspace-1",
		WorktreeID:          "worktree-1",
		CreatedBranch:       true,
	}
	inherited := []string{
		"PATH=/bin",
		"UNRELATED=value",
		"KENT_WORKTREE_ROOT=stale-root",
		"KENT_WORKTREE_SESSION_ID=stale-session",
		"Kent_Worktree_Session_Id=mixed-case-stale-session",
		"KENT_WORKTREE_NOT_RESERVED=preserve",
	}

	tests := []struct {
		name                          string
		canonicalize                  setupEnvironmentKeyCanonicalizer
		wantMixedCaseSessionPreserved bool
	}{
		{
			name:                          "non-windows exact case",
			canonicalize:                  caseSensitiveSetupEnvironmentKey,
			wantMixedCaseSessionPreserved: true,
		},
		{
			name:                          "windows case insensitive",
			canonicalize:                  caseInsensitiveSetupEnvironmentKey,
			wantMixedCaseSessionPreserved: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := buildSetupEnvironment(inherited, payload, tt.canonicalize)
			if err != nil {
				t.Fatalf("buildSetupEnvironment: %v", err)
			}
			if got := setupEnvironmentValue(t, env, setupEnvironmentKeyWorktreeRoot, tt.canonicalize); got != payload.WorktreeRoot {
				t.Fatalf("worktree root = %q, want %q", got, payload.WorktreeRoot)
			}
			if got := setupEnvironmentValue(t, env, setupEnvironmentKeySessionID, tt.canonicalize); got != sessionID {
				t.Fatalf("session id = %q, want %q", got, sessionID)
			}
			if got := setupEnvironmentValue(t, env, "UNRELATED", tt.canonicalize); got != "value" {
				t.Fatalf("unrelated value = %q, want value", got)
			}
			if got := setupEnvironmentValue(t, env, "KENT_WORKTREE_NOT_RESERVED", tt.canonicalize); got != "preserve" {
				t.Fatalf("non-reserved Kent value = %q, want preserve", got)
			}
			mixedCase := setupEnvironmentValue(t, env, "Kent_Worktree_Session_Id", caseSensitiveSetupEnvironmentKey)
			if tt.wantMixedCaseSessionPreserved && mixedCase != "mixed-case-stale-session" {
				t.Fatalf("mixed-case session = %q, want preserved value", mixedCase)
			}
			if !tt.wantMixedCaseSessionPreserved && mixedCase != "" {
				t.Fatalf("mixed-case session = %q, want removed", mixedCase)
			}
		})
	}
}

func TestBuildSetupEnvironmentOmitsAbsentSessionAcrossPlatformPolicies(t *testing.T) {
	payload := setupScriptPayload{
		SourceWorkspaceRoot: "/source",
		BranchName:          "task-123",
		WorktreeRoot:        "/worktree",
		ProjectID:           "project-1",
		WorkspaceID:         "workspace-1",
		WorktreeID:          "worktree-1",
	}
	inherited := []string{
		"PATH=/bin",
		"KENT_WORKTREE_SESSION_ID=stale-session",
		"Kent_Worktree_Session_Id=mixed-case-stale-session",
	}

	for _, tt := range []struct {
		name                          string
		canonicalize                  setupEnvironmentKeyCanonicalizer
		wantMixedCaseSessionPreserved bool
	}{
		{name: "non-windows exact case", canonicalize: caseSensitiveSetupEnvironmentKey, wantMixedCaseSessionPreserved: true},
		{name: "windows case insensitive", canonicalize: caseInsensitiveSetupEnvironmentKey, wantMixedCaseSessionPreserved: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env, err := buildSetupEnvironment(inherited, payload, tt.canonicalize)
			if err != nil {
				t.Fatalf("buildSetupEnvironment: %v", err)
			}
			if got := setupEnvironmentValue(t, env, setupEnvironmentKeySessionID, tt.canonicalize); got != "" {
				t.Fatalf("session id = %q, want absent", got)
			}
			mixedCase := setupEnvironmentValue(t, env, "Kent_Worktree_Session_Id", caseSensitiveSetupEnvironmentKey)
			if tt.wantMixedCaseSessionPreserved && mixedCase != "mixed-case-stale-session" {
				t.Fatalf("mixed-case session = %q, want preserved value", mixedCase)
			}
			if !tt.wantMixedCaseSessionPreserved && mixedCase != "" {
				t.Fatalf("mixed-case session = %q, want removed", mixedCase)
			}
			if got := setupEnvironmentValue(t, env, "PATH", tt.canonicalize); got != "/bin" {
				t.Fatalf("PATH = %q, want /bin", got)
			}
		})
	}
}

func setupEnvironmentValue(t *testing.T, env []string, key string, canonicalize setupEnvironmentKeyCanonicalizer) string {
	t.Helper()
	values := setupEnvironmentValues(env, key, canonicalize)
	if len(values) > 1 {
		t.Fatalf("environment key %q has duplicate values %q", key, values)
	}
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func setupEnvironmentValues(env []string, key string, canonicalize setupEnvironmentKeyCanonicalizer) []string {
	want := canonicalize(key)
	var values []string
	for _, entry := range env {
		entryKey, value, found := strings.Cut(entry, "=")
		if found && canonicalize(entryKey) == want {
			values = append(values, value)
		}
	}
	return values
}
