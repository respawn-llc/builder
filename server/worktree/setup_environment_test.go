package worktree

import (
	"strings"
	"testing"
)

type setupEnvironmentPolicyCase struct {
	name                          string
	canonicalize                  setupEnvironmentKeyCanonicalizer
	wantMixedCaseSessionPreserved bool
}

func setupEnvironmentPolicies() []setupEnvironmentPolicyCase {
	return []setupEnvironmentPolicyCase{
		{name: "non-windows exact case", canonicalize: caseSensitiveSetupEnvironmentKey, wantMixedCaseSessionPreserved: true},
		{name: "windows case insensitive", canonicalize: caseInsensitiveSetupEnvironmentKey, wantMixedCaseSessionPreserved: false},
	}
}

func setupEnvironmentPayload(sessionID *string) setupScriptPayload {
	return setupScriptPayload{
		SourceWorkspaceRoot: "/source",
		BranchName:          "task-123",
		WorktreeRoot:        "/worktree",
		SessionID:           sessionID,
		ProjectID:           "project-1",
		WorkspaceID:         "workspace-1",
		WorktreeID:          "worktree-1",
	}
}

func assertMixedCaseSessionPolicy(t *testing.T, env []string, preserved bool) {
	t.Helper()
	mixedCase, found := setupEnvironmentEntry(t, env, "Kent_Worktree_Session_Id", caseSensitiveSetupEnvironmentKey)
	if preserved && (!found || mixedCase != "mixed-case-stale-session") {
		t.Fatalf("mixed-case session = %q, present=%t, want preserved value", mixedCase, found)
	}
	if !preserved && found {
		t.Fatalf("mixed-case session = %q, want removed", mixedCase)
	}
}

func TestBuildSetupEnvironmentReplacesReservedValuesWithPlatformKeyPolicy(t *testing.T) {
	sessionID := "current-session"
	payload := setupEnvironmentPayload(&sessionID)
	payload.CreatedBranch = true
	inherited := []string{
		"PATH=/bin",
		"UNRELATED=value",
		"KENT_WORKTREE_ROOT=stale-root",
		"KENT_WORKTREE_SESSION_ID=stale-session",
		"Kent_Worktree_Session_Id=mixed-case-stale-session",
		"KENT_WORKTREE_NOT_RESERVED=preserve",
	}

	for _, tt := range setupEnvironmentPolicies() {
		t.Run(tt.name, func(t *testing.T) {
			env, err := buildSetupEnvironment(inherited, payload, tt.canonicalize)
			if err != nil {
				t.Fatalf("buildSetupEnvironment: %v", err)
			}
			if got, found := setupEnvironmentEntry(t, env, setupEnvironmentKeyWorktreeRoot, tt.canonicalize); !found || got != payload.WorktreeRoot {
				t.Fatalf("worktree root = %q, present=%t, want %q", got, found, payload.WorktreeRoot)
			}
			if got, found := setupEnvironmentEntry(t, env, setupEnvironmentKeySessionID, tt.canonicalize); !found || got != sessionID {
				t.Fatalf("session id = %q, present=%t, want %q", got, found, sessionID)
			}
			if got, found := setupEnvironmentEntry(t, env, "UNRELATED", tt.canonicalize); !found || got != "value" {
				t.Fatalf("unrelated value = %q, present=%t, want value", got, found)
			}
			if got, found := setupEnvironmentEntry(t, env, "KENT_WORKTREE_NOT_RESERVED", tt.canonicalize); !found || got != "preserve" {
				t.Fatalf("non-reserved Kent value = %q, present=%t, want preserve", got, found)
			}
			assertMixedCaseSessionPolicy(t, env, tt.wantMixedCaseSessionPreserved)
		})
	}
}

func TestBuildSetupEnvironmentOmitsAbsentSessionAcrossPlatformPolicies(t *testing.T) {
	payload := setupEnvironmentPayload(nil)
	inherited := []string{
		"PATH=/bin",
		"KENT_WORKTREE_SESSION_ID=stale-session",
		"Kent_Worktree_Session_Id=mixed-case-stale-session",
	}

	for _, tt := range setupEnvironmentPolicies() {
		t.Run(tt.name, func(t *testing.T) {
			env, err := buildSetupEnvironment(inherited, payload, tt.canonicalize)
			if err != nil {
				t.Fatalf("buildSetupEnvironment: %v", err)
			}
			if got, found := setupEnvironmentEntry(t, env, setupEnvironmentKeySessionID, tt.canonicalize); found {
				t.Fatalf("session id = %q, want absent", got)
			}
			assertMixedCaseSessionPolicy(t, env, tt.wantMixedCaseSessionPreserved)
			if got, found := setupEnvironmentEntry(t, env, "PATH", tt.canonicalize); !found || got != "/bin" {
				t.Fatalf("PATH = %q, present=%t, want /bin", got, found)
			}
		})
	}
}

func TestNormalizeSetupSessionIDRejectsEmptyPresentIdentity(t *testing.T) {
	for _, sessionID := range []string{"", " \t "} {
		if _, err := normalizeSetupSessionID(&sessionID); err == nil {
			t.Fatalf("normalizeSetupSessionID accepted invalid present identity %q", sessionID)
		}
	}
}

func setupEnvironmentEntry(t *testing.T, env []string, key string, canonicalize setupEnvironmentKeyCanonicalizer) (string, bool) {
	t.Helper()
	values := setupEnvironmentValues(env, key, canonicalize)
	if len(values) > 1 {
		t.Fatalf("environment key %q has duplicate values %q", key, values)
	}
	if len(values) == 0 {
		return "", false
	}
	return values[0], true
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
