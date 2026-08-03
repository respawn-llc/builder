package client

import (
	"testing"

	"core/shared/clientui"
)

func TestPlanApprovalCommentary(t *testing.T) {
	tests := []struct {
		name       string
		decision   clientui.ApprovalDecision
		commentary string
		want       []ApprovalCommentaryEffect
	}{
		{
			name:       "deny keeps commentary on approval",
			decision:   clientui.ApprovalDecisionDeny,
			commentary: "reason",
			want: []ApprovalCommentaryEffect{
				{Kind: ApprovalCommentaryEffectApproval, Commentary: "reason"},
			},
		},
		{
			name:       "allow once submits commentary before approval",
			decision:   clientui.ApprovalDecisionAllowOnce,
			commentary: "context",
			want: []ApprovalCommentaryEffect{
				{Kind: ApprovalCommentaryEffectRuntimeInput, Commentary: "context"},
				{Kind: ApprovalCommentaryEffectApproval},
			},
		},
		{
			name:       "allow session ignores blank commentary",
			decision:   clientui.ApprovalDecisionAllowSession,
			commentary: " \t ",
			want: []ApprovalCommentaryEffect{
				{Kind: ApprovalCommentaryEffectApproval},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := PlanApprovalCommentary(test.decision, test.commentary)
			if err != nil {
				t.Fatalf("PlanApprovalCommentary: %v", err)
			}
			if len(got) != len(test.want) {
				t.Fatalf("effects = %#v, want %#v", got, test.want)
			}
			for index := range test.want {
				if got[index] != test.want[index] {
					t.Fatalf("effect %d = %#v, want %#v", index, got[index], test.want[index])
				}
			}
		})
	}
}
