package registry

import (
	"reflect"
	"testing"
	"time"

	askquestion "core/server/tools"
	"core/shared/clientui"
)

func TestResolvedPromptProjectionRemainsIdentityOnlyForQuestionAndApproval(t *testing.T) {
	createdAt := time.Unix(100, 0).UTC()
	tests := []askquestion.AskQuestionRequest{
		{
			ID:          "question-1",
			StepID:      registryTestStepID,
			Question:    "Question?",
			Suggestions: []string{"One"},
		},
		{
			ID:       "approval-1",
			StepID:   registryTestStepID,
			Question: "Approve?",
			Approval: true,
			ApprovalOptions: []askquestion.AskQuestionApprovalOption{
				{Decision: askquestion.AskQuestionApprovalDecisionDeny, Label: "Deny"},
			},
		},
	}
	for _, request := range tests {
		resolved := transcriptPendingPromptFromSnapshot("session-1", PendingPromptSnapshot{
			Request:   request,
			CreatedAt: createdAt,
		}, pendingPromptEventResolved)
		if resolved.Status != clientui.TranscriptPromptStatusResolved ||
			resolved.PromptID != clientui.PromptID(request.ID) ||
			resolved.StepID.String() != request.StepID ||
			!resolved.CreatedAt.Equal(createdAt) {
			t.Fatalf("resolved prompt projection = %+v", resolved)
		}
	}

	promptType := reflect.TypeOf(clientui.TranscriptPrompt{})
	for _, forbidden := range []string{"Answer", "Decision", "Declined", "Commentary", "Freeform"} {
		if _, exists := promptType.FieldByName(forbidden); exists {
			t.Fatalf("resolved prompt projection exposes %s", forbidden)
		}
	}
}
