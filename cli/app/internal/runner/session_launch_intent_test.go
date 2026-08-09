package runner

import (
	"testing"

	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestSessionLifecycleOptionsUseExplicitTypedInitialIntent(t *testing.T) {
	selected := mustRunnerSessionID(t, "selected-session")
	parent := mustRunnerSessionID(t, "parent-session")

	tests := []struct {
		name          string
		request       Request
		wantKind      serverapi.SessionLaunchIntentKind
		wantSessionID runtimeids.SessionID
		wantParentID  runtimeids.SessionID
	}{
		{
			name:          "open selected session",
			request:       Request{SessionID: selected.String()},
			wantKind:      serverapi.SessionLaunchIntentOpenExisting,
			wantSessionID: selected,
		},
		{
			name: "create for non-default agent role",
			request: Request{
				AgentRole: runnerStringPtr("reviewer"),
			},
			wantKind: serverapi.SessionLaunchIntentCreateNew,
		},
		{
			name: "create with workspace context parent",
			request: Request{
				WorkspaceContextSessionID: parent.String(),
			},
			wantKind:     serverapi.SessionLaunchIntentCreateNew,
			wantParentID: parent,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			intent, _, err := SessionLifecycleOptionsFor(test.request)
			if err != nil {
				t.Fatalf("SessionLifecycleOptionsFor: %v", err)
			}
			if intent.Kind() != test.wantKind {
				t.Fatalf("intent kind = %q, want %q", intent.Kind(), test.wantKind)
			}
			if test.wantSessionID.IsZero() {
				if _, present := intent.SessionID(); present {
					t.Fatal("create intent unexpectedly has an existing session identity")
				}
			} else if got, present := intent.SessionID(); !present || got != test.wantSessionID {
				t.Fatalf("session identity = %q/%v, want %q/true", got.String(), present, test.wantSessionID.String())
			}
			origin, hasOrigin := intent.CreateOrigin()
			if test.wantParentID.IsZero() {
				if hasOrigin && origin.Kind() == serverapi.SessionCreateOriginParentAgent {
					t.Fatal("intent unexpectedly has a parent-agent origin")
				}
			} else if got, present := origin.SessionID(); !hasOrigin || origin.Kind() != serverapi.SessionCreateOriginParentAgent || !present || got != test.wantParentID {
				t.Fatalf("parent-agent identity = %q/%v, want %q/true", got.String(), present, test.wantParentID.String())
			}
		})
	}
}

func TestSessionLifecycleOptionsRejectInvalidIdentityWithoutEmptyStringInference(t *testing.T) {
	for _, request := range []Request{
		{SessionID: "   "},
		{WorkspaceContextSessionID: "   "},
	} {
		if _, _, err := SessionLifecycleOptionsFor(request); err == nil {
			t.Fatalf("SessionLifecycleOptionsFor(%+v) succeeded; want invalid identity error", request)
		}
	}
}

func mustRunnerSessionID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q): %v", raw, err)
	}
	return id
}
