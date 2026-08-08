package workflow_test

import (
	"testing"

	"core/server/workflow"
)

func TestResolveAssigneeSessionPolicyMatrix(t *testing.T) {
	tests := []struct {
		name       string
		mode       workflow.ContextMode
		source     workflow.ContextSource
		hasSession bool
		want       workflow.AssigneeSessionPolicy
	}{
		{name: "new session", mode: workflow.ContextModeNewSession, source: workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource}, want: workflow.AssigneeSessionPolicyEstablishTarget},
		{name: "compact and continue", mode: workflow.ContextModeCompactAndContinueSession, source: workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "review"}, want: workflow.AssigneeSessionPolicyEstablishTarget},
		{name: "continue immediate", mode: workflow.ContextModeContinueSession, source: workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource}, want: workflow.AssigneeSessionPolicyRequireTargetMatch},
		{name: "continue selected", mode: workflow.ContextModeContinueSession, source: workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "review"}, want: workflow.AssigneeSessionPolicyRequireTargetMatch},
		{name: "continue previous target", mode: workflow.ContextModeContinueSession, source: workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget}, hasSession: true, want: workflow.AssigneeSessionPolicyPreserve},
		{name: "continue previous target or new retained", mode: workflow.ContextModeContinueSession, source: workflow.ContextSource{Kind: workflow.ContextSourcePreviousTargetOrNew}, hasSession: true, want: workflow.AssigneeSessionPolicyPreserve},
		{name: "continue previous target or new fresh", mode: workflow.ContextModeContinueSession, source: workflow.ContextSource{Kind: workflow.ContextSourcePreviousTargetOrNew}, want: workflow.AssigneeSessionPolicyEstablishTarget},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := workflow.ResolveAssigneeSessionPolicy(workflow.AssigneeSessionPolicyRequest{
				ContextMode:           tt.mode,
				ContextSource:         tt.source,
				TargetSessionResolved: tt.hasSession,
			})
			if err != nil {
				t.Fatalf("ResolveAssigneeSessionPolicy: %v", err)
			}
			if got != tt.want {
				t.Fatalf("policy = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveAssigneeSessionPolicyRejectsUnsupportedValues(t *testing.T) {
	tests := []workflow.AssigneeSessionPolicyRequest{
		{ContextMode: workflow.ContextMode("invalid"), ContextSource: workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource}},
		{ContextMode: workflow.ContextModeContinueSession, ContextSource: workflow.ContextSource{Kind: workflow.ContextSourceKind("invalid")}},
	}
	for _, request := range tests {
		if _, err := workflow.ResolveAssigneeSessionPolicy(request); err == nil {
			t.Fatalf("ResolveAssigneeSessionPolicy(%+v) succeeded", request)
		}
	}
}
