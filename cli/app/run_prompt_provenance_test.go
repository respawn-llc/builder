package app

import (
	"testing"

	"core/cli/app/internal/startupconfig"
)

func TestRunPromptProvenanceCarriesKentSessionCallerAndParent(t *testing.T) {
	opts := Options{WorkspaceContextSessionID: "context-session"}
	callerID, parentID := runPromptProvenance(opts, startupconfig.CallerContext{
		Kind: startupconfig.CallerKindKentSession,
	})
	if callerID == nil || *callerID != "context-session" {
		t.Fatalf("caller session ID = %v, want context-session", callerID)
	}
	if parentID == nil || *parentID != "context-session" {
		t.Fatalf("parent session ID = %v, want context-session", parentID)
	}
}

func TestRunPromptProvenanceOmitsHumanAndMissingContext(t *testing.T) {
	tests := []struct {
		name   string
		opts   Options
		caller startupconfig.CallerContext
	}{
		{
			name:   "human caller",
			opts:   Options{WorkspaceContextSessionID: "context-session"},
			caller: startupconfig.CallerContext{Kind: startupconfig.CallerKindHuman},
		},
		{
			name:   "missing context",
			caller: startupconfig.CallerContext{Kind: startupconfig.CallerKindKentSession},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			callerID, parentID := runPromptProvenance(test.opts, test.caller)
			if callerID != nil || parentID != nil {
				t.Fatalf("provenance = %v/%v, want nil/nil", callerID, parentID)
			}
		})
	}
}
