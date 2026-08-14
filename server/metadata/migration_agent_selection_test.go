package metadata

import (
	"testing"

	"core/server/workflow"
)

func TestMigrationAgentExecutionSelectionUsesCanonicalSessionPolicy(t *testing.T) {
	tests := []struct {
		name           string
		mode           workflow.ContextMode
		source         workflow.ContextSourceKind
		targetResolved bool
		targetBound    bool
		fallbackRole   string
		sessionRole    string
		wantRole       string
		wantOrigin     workflow.AssigneeOrigin
	}{
		{
			name:         "new session uses node fallback",
			mode:         workflow.ContextModeNewSession,
			source:       workflow.ContextSourceImmediateSource,
			fallbackRole: "fallback",
			sessionRole:  "retained",
			wantRole:     "fallback",
			wantOrigin:   workflow.AssigneeOriginConfiguredFallback,
		},
		{
			name:           "compact uses node fallback",
			mode:           workflow.ContextModeCompactAndContinueSession,
			source:         workflow.ContextSourcePreviousTarget,
			targetResolved: true,
			fallbackRole:   "fallback",
			sessionRole:    "retained",
			wantRole:       "fallback",
			wantOrigin:     workflow.AssigneeOriginConfiguredFallback,
		},
		{
			name:           "immediate continuation preserves session role",
			mode:           workflow.ContextModeContinueSession,
			source:         workflow.ContextSourceImmediateSource,
			targetResolved: true,
			fallbackRole:   "fallback",
			sessionRole:    "retained",
			wantRole:       "retained",
			wantOrigin:     workflow.AssigneeOriginRetainedSession,
		},
		{
			name:           "previous target preserves session role",
			mode:           workflow.ContextModeContinueSession,
			source:         workflow.ContextSourcePreviousTarget,
			targetResolved: true,
			fallbackRole:   "fallback",
			sessionRole:    "retained",
			wantRole:       "retained",
			wantOrigin:     workflow.AssigneeOriginRetainedSession,
		},
		{
			name:         "previous target or new without session uses fallback",
			mode:         workflow.ContextModeContinueSession,
			source:       workflow.ContextSourcePreviousTargetOrNew,
			fallbackRole: "fallback",
			sessionRole:  "retained",
			wantRole:     "fallback",
			wantOrigin:   workflow.AssigneeOriginConfiguredFallback,
		},
		{
			name:           "previous target or new with session preserves role",
			mode:           workflow.ContextModeContinueSession,
			source:         workflow.ContextSourcePreviousTargetOrNew,
			targetResolved: true,
			fallbackRole:   "fallback",
			sessionRole:    "retained",
			wantRole:       "retained",
			wantOrigin:     workflow.AssigneeOriginRetainedSession,
		},
		{
			name:         "exact started target binding preserves role",
			mode:         workflow.ContextModeContinueSession,
			source:       workflow.ContextSourceImmediateSource,
			targetBound:  true,
			fallbackRole: "fallback",
			sessionRole:  "retained",
			wantRole:     "retained",
			wantOrigin:   workflow.AssigneeOriginRetainedSession,
		},
		{
			name:           "missing retained role uses default",
			mode:           workflow.ContextModeContinueSession,
			source:         workflow.ContextSourcePreviousTarget,
			targetResolved: true,
			fallbackRole:   "fallback",
			wantRole:       workflow.DefaultAgentRole,
			wantOrigin:     workflow.AssigneeOriginRetainedSession,
		},
		{
			name:           "unavailable retained role is preserved",
			mode:           workflow.ContextModeContinueSession,
			source:         workflow.ContextSourcePreviousTarget,
			targetResolved: true,
			fallbackRole:   "fallback",
			sessionRole:    "removed_role",
			wantRole:       "removed_role",
			wantOrigin:     workflow.AssigneeOriginRetainedSession,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selection, err := migrationAgentExecutionSelection(
				test.mode,
				workflow.ContextSource{Kind: test.source},
				test.targetResolved,
				test.targetBound,
				test.fallbackRole,
				test.sessionRole,
			)
			if err != nil {
				t.Fatalf("migrationAgentExecutionSelection: %v", err)
			}
			if selection.Assignee != test.wantRole || selection.Origin != test.wantOrigin || selection.Thinking != nil {
				t.Fatalf("selection = %+v, want role=%q origin=%q absent thinking", selection, test.wantRole, test.wantOrigin)
			}
		})
	}
}

func TestMigrationAgentExecutionSelectionRejectsMalformedRows(t *testing.T) {
	tests := []struct {
		name     string
		mode     workflow.ContextMode
		source   workflow.ContextSource
		fallback string
	}{
		{name: "missing fallback", mode: workflow.ContextModeNewSession, source: workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource}},
		{name: "invalid mode", mode: workflow.ContextMode("bad"), source: workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource}, fallback: "fallback"},
		{name: "invalid source", mode: workflow.ContextModeNewSession, source: workflow.ContextSource{Kind: workflow.ContextSourceKind("bad")}, fallback: "fallback"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := migrationAgentExecutionSelection(test.mode, test.source, false, false, test.fallback, ""); err == nil {
				t.Fatal("migrationAgentExecutionSelection succeeded")
			}
		})
	}
}
