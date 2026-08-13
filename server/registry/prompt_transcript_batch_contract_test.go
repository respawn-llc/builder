package registry

import (
	"reflect"
	"testing"
	"time"

	askquestion "core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
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
	stepID, err := runtimeids.ParseStepID(registryTestStepID)
	if err != nil {
		t.Fatalf("ParseStepID: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID("session-1")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	for _, request := range tests {
		resolved := transcriptPendingPromptFromSnapshot("session-1", PendingPromptSnapshot{
			Request:   request,
			CreatedAt: createdAt,
			SessionID: sessionID,
			PromptID:  clientui.PromptID(request.ID),
			StepID:    stepID,
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
