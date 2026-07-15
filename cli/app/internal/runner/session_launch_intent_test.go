package runner

import (
	"context"
	"testing"

	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestSessionLifecycleOptionsUseExplicitTypedInitialIntent(t *testing.T) {
	selected := mustRunnerSessionID(t, "selected-session")
	parent := mustRunnerSessionID(t, "parent-session")

	tests := []struct {
		name          string
		request       Request[NoStartupOptions]
		wantKind      serverapi.SessionLaunchIntentKind
		wantSessionID runtimeids.SessionID
		wantParentID  runtimeids.SessionID
	}{
		{
			name:          "open selected session",
			request:       Request[NoStartupOptions]{SessionID: selected.String()},
			wantKind:      serverapi.SessionLaunchIntentOpenExisting,
			wantSessionID: selected,
		},
		{
			name: "create for non-default agent role",
			request: Request[NoStartupOptions]{
				AgentRole: "reviewer",
			},
			wantKind: serverapi.SessionLaunchIntentCreateNew,
		},
		{
			name: "create with workspace context parent",
			request: Request[NoStartupOptions]{
				WorkspaceContextSessionID: parent.String(),
			},
			wantKind:     serverapi.SessionLaunchIntentCreateNew,
			wantParentID: parent,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opts, err := SessionLifecycleOptionsFor(test.request)
			if err != nil {
				t.Fatalf("SessionLifecycleOptionsFor: %v", err)
			}
			if (*opts.Intent).Kind() != test.wantKind {
				t.Fatalf("intent kind = %q, want %q", (*opts.Intent).Kind(), test.wantKind)
			}
			if test.wantSessionID.IsZero() {
				if _, present := (*opts.Intent).SessionID(); present {
					t.Fatal("create intent unexpectedly has an existing session identity")
				}
			} else if got, present := (*opts.Intent).SessionID(); !present || got != test.wantSessionID {
				t.Fatalf("session identity = %q/%v, want %q/true", got.String(), present, test.wantSessionID.String())
			}
			if test.wantParentID.IsZero() {
				if _, present := (*opts.Intent).ParentID(); present {
					t.Fatal("intent unexpectedly has a parent")
				}
			} else if got, present := (*opts.Intent).ParentID(); !present || got != test.wantParentID {
				t.Fatalf("parent identity = %q/%v, want %q/true", got.String(), present, test.wantParentID.String())
			}
		})
	}
}

func TestSessionLifecycleOptionsRejectInvalidIdentityWithoutEmptyStringInference(t *testing.T) {
	for _, request := range []Request[NoStartupOptions]{
		{SessionID: "   "},
		{WorkspaceContextSessionID: "   "},
	} {
		if _, err := SessionLifecycleOptionsFor(request); err == nil {
			t.Fatalf("SessionLifecycleOptionsFor(%+v) succeeded; want invalid identity error", request)
		}
	}
}

func TestRunInteractivePassesTypedInitialIntentToLifecycle(t *testing.T) {
	server := &fakeServer{}
	err := RunInteractive(t.Context(), Request[NoStartupOptions]{
		AgentRole: "reviewer",
	}, Dependencies[*fakeServer, struct{}, NoStartupOptions]{
		NewAuthInteractor: func() struct{} { return struct{}{} },
		StartSessionServer: func(_ context.Context, _ Request[NoStartupOptions], _ struct{}, _ bool) (*fakeServer, error) {
			return server, nil
		},
		RunSessionLifecycle: func(_ context.Context, _ *fakeServer, _ struct{}, opts SessionLifecycleOptions) error {
			if (*opts.Intent).Kind() != serverapi.SessionLaunchIntentCreateNew {
				t.Fatalf("intent kind = %q, want create_new", (*opts.Intent).Kind())
			}
			if opts.Overrides.AgentRole != "reviewer" {
				t.Fatalf("agent role override = %q, want reviewer", opts.Overrides.AgentRole)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("RunInteractive: %v", err)
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
