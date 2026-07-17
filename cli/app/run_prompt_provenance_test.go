package app

import (
	"testing"

	"core/cli/app/internal/startupconfig"
)

func TestRunPromptCallerSessionIDCarriesKentSessionCaller(t *testing.T) {
	opts := Options{WorkspaceContextSessionID: "context-session"}
	callerID := runPromptCallerSessionID(opts, startupconfig.CallerContext{
		Kind: startupconfig.CallerKindKentSession,
	})
	if callerID == nil || *callerID != "context-session" {
		t.Fatalf("caller session ID = %v, want context-session", callerID)
	}
}

func TestRunPromptCallerSessionIDOmittedForHumanAndMissingContext(t *testing.T) {
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
			callerID := runPromptCallerSessionID(test.opts, test.caller)
			if callerID != nil {
				t.Fatalf("caller session ID = %v, want nil", callerID)
			}
		})
	}
}
