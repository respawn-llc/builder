package workflowrunner

import (
	"testing"

	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestWorkflowRunContextMapsContinuationModesToTypedLaunchIntent(t *testing.T) {
	source := mustWorkflowSessionID(t, "source-session")
	tests := []struct {
		name          string
		contextMode   workflow.ContextMode
		fanOut        bool
		wantKind      serverapi.SessionLaunchIntentKind
		wantSessionID runtimeids.SessionID
		wantParentID  runtimeids.SessionID
	}{
		{
			name:        "new session",
			contextMode: workflow.ContextModeNewSession,
			wantKind:    serverapi.SessionLaunchIntentCreateNew,
		},
		{
			name:          "continue existing",
			contextMode:   workflow.ContextModeContinueSession,
			wantKind:      serverapi.SessionLaunchIntentOpenExisting,
			wantSessionID: source,
		},
		{
			name:          "compact and continue in place",
			contextMode:   workflow.ContextModeCompactAndContinueSession,
			wantKind:      serverapi.SessionLaunchIntentOpenExisting,
			wantSessionID: source,
		},
		{
			name:          "compact fan-out validates its source before cloning",
			contextMode:   workflow.ContextModeCompactAndContinueSession,
			fanOut:        true,
			wantKind:      serverapi.SessionLaunchIntentOpenExisting,
			wantSessionID: source,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req, err := sessionLaunchRequestForWorkflowRun(workflowstore.RunStartContext{
				ContextMode:     test.contextMode,
				SourceSessionID: source.String(),
				IsFanoutBranch:  test.fanOut,
			})
			if err != nil {
				t.Fatalf("sessionLaunchRequestForWorkflowRun: %v", err)
			}
			if req.Intent.Kind() != test.wantKind {
				t.Fatalf("intent kind = %q, want %q", req.Intent.Kind(), test.wantKind)
			}
			if test.wantSessionID.IsZero() {
				if _, present := req.Intent.SessionID(); present {
					t.Fatal("create intent unexpectedly carries an existing session identity")
				}
			} else if got, present := req.Intent.SessionID(); !present || got != test.wantSessionID {
				t.Fatalf("intent session = %q/%v, want %q/true", got.String(), present, test.wantSessionID.String())
			}
			if test.wantParentID.IsZero() {
				if _, present := req.Intent.ParentID(); present {
					t.Fatal("intent unexpectedly carries a parent identity")
				}
			} else if got, present := req.Intent.ParentID(); !present || got != test.wantParentID {
				t.Fatalf("intent parent = %q/%v, want %q/true", got.String(), present, test.wantParentID.String())
			}
		})
	}
}

func TestWorkflowRunContextPreservesNonDefaultAgentRoleOnCreateIntent(t *testing.T) {
	req, err := sessionLaunchRequestForWorkflowRun(workflowstore.RunStartContext{
		ContextMode: workflow.ContextModeNewSession,
		Node:        workflowstore.NodeRecord{SubagentRole: "reviewer"},
	})
	if err != nil {
		t.Fatalf("sessionLaunchRequestForWorkflowRun: %v", err)
	}
	if req.Intent.Kind() != serverapi.SessionLaunchIntentCreateNew {
		t.Fatalf("intent kind = %q, want create_new", req.Intent.Kind())
	}
	if req.Overrides.AgentRole != "reviewer" {
		t.Fatalf("agent role override = %q, want reviewer", req.Overrides.AgentRole)
	}
}

func TestWorkflowRunContextRejectsEmptyIdentityInsteadOfInferringOpenOrCreate(t *testing.T) {
	for _, input := range []workflowstore.RunStartContext{
		{
			ContextMode:     workflow.ContextModeContinueSession,
			SourceSessionID: "",
		},
		{
			ContextMode:     workflow.ContextModeCompactAndContinueSession,
			SourceSessionID: "",
		},
	} {
		if _, err := sessionLaunchRequestForWorkflowRun(input); err == nil {
			t.Fatalf("sessionLaunchRequestForWorkflowRun(%+v) succeeded; want missing identity error", input)
		}
	}
}

func mustWorkflowSessionID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q): %v", raw, err)
	}
	return id
}
