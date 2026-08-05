package workflow

import "testing"

func TestResolveProtectedParameterConsumptionPolicies(t *testing.T) {
	tests := []struct {
		name           string
		edge           Edge
		source         NodeKind
		target         NodeKind
		fanout         bool
		targetResolved bool
		roleCount      int
		thinking       ThinkingCapability
		wantAssignee   ProtectedParameterConsumptionPolicy
		wantThinking   ProtectedParameterConsumptionPolicy
	}{
		{
			name:         "disabled selectors reject unknown protected values",
			edge:         Edge{},
			source:       NodeKindAgent,
			target:       NodeKindAgent,
			roleCount:    2,
			thinking:     ThinkingCapability{ReasoningCapable: true, Finite: true, Levels: []string{"low", "high"}},
			wantAssignee: ProtectedParameterConsumptionRejectUnknown,
			wantThinking: ProtectedParameterConsumptionRejectUnknown,
		},
		{
			name:         "many roles and thinking levels require validation",
			edge:         Edge{AssigneeSelection: AssigneeSelectionPreviousNode, ThinkingSelection: ThinkingSelectionPreviousNode},
			source:       NodeKindAgent,
			target:       NodeKindAgent,
			roleCount:    2,
			thinking:     ThinkingCapability{ReasoningCapable: true, Finite: true, Levels: []string{"low", "high"}},
			wantAssignee: ProtectedParameterConsumptionRequiredValidate,
			wantThinking: ProtectedParameterConsumptionRequiredValidate,
		},
		{
			name:         "sole role and finite one level ignore authorized values",
			edge:         Edge{AssigneeSelection: AssigneeSelectionPreviousNode, ThinkingSelection: ThinkingSelectionPreviousNode},
			source:       NodeKindScript,
			target:       NodeKindAgent,
			roleCount:    1,
			thinking:     ThinkingCapability{ReasoningCapable: true, Finite: true, Levels: []string{"high"}},
			wantAssignee: ProtectedParameterConsumptionIgnoreAuthorized,
			wantThinking: ProtectedParameterConsumptionIgnoreAuthorized,
		},
		{
			name:           "retained target role ignores supplied assignee",
			edge:           Edge{AssigneeSelection: AssigneeSelectionPreviousNode, ContextMode: ContextModeContinueSession, ContextSource: ContextSource{Kind: ContextSourcePreviousTargetOrNew}},
			source:         NodeKindAgent,
			target:         NodeKindAgent,
			targetResolved: true,
			roleCount:      2,
			thinking:       ThinkingCapability{ReasoningCapable: true, Finite: true, Levels: []string{"low", "high"}},
			wantAssignee:   ProtectedParameterConsumptionIgnoreAuthorized,
			wantThinking:   ProtectedParameterConsumptionRejectUnknown,
		},
		{
			name:         "fanout and non-agent targets reject unknown",
			edge:         Edge{AssigneeSelection: AssigneeSelectionPreviousNode, ThinkingSelection: ThinkingSelectionPreviousNode},
			source:       NodeKindAgent,
			target:       NodeKindAgent,
			fanout:       true,
			roleCount:    2,
			thinking:     ThinkingCapability{ReasoningCapable: true, Finite: true, Levels: []string{"low", "high"}},
			wantAssignee: ProtectedParameterConsumptionRejectUnknown,
			wantThinking: ProtectedParameterConsumptionRejectUnknown,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ResolveProtectedParameterConsumption(ProtectedParameterConsumptionRequest{
				Edge:                  test.edge,
				SourceKind:            test.source,
				TargetKind:            test.target,
				FanOut:                test.fanout,
				TargetSessionResolved: test.targetResolved,
				TargetSessionPolicy: func() AssigneeSessionPolicy {
					if test.targetResolved {
						return AssigneeSessionPolicyPreserve
					}
					return AssigneeSessionPolicyEstablishTarget
				}(),
				ExplicitCallableRoles: test.roleCount,
				Thinking:              test.thinking,
			})
			if got.Assignee != test.wantAssignee || got.Thinking != test.wantThinking {
				t.Fatalf("policies = %+v, want assignee=%q thinking=%q", got, test.wantAssignee, test.wantThinking)
			}
		})
	}
}

func TestCompactionWithResolvedTargetSessionStillRequiresAssigneeSelection(t *testing.T) {
	got := ResolveProtectedParameterConsumption(ProtectedParameterConsumptionRequest{
		Edge: Edge{
			AssigneeSelection: AssigneeSelectionPreviousNode,
			ContextMode:       ContextModeCompactAndContinueSession,
		},
		SourceKind:            NodeKindAgent,
		TargetKind:            NodeKindAgent,
		TargetSessionResolved: true,
		TargetSessionPolicy:   AssigneeSessionPolicyEstablishTarget,
		ExplicitCallableRoles: 2,
	})
	if got.Assignee != ProtectedParameterConsumptionRequiredValidate {
		t.Fatalf("assignee policy = %q, want %q", got.Assignee, ProtectedParameterConsumptionRequiredValidate)
	}
}
