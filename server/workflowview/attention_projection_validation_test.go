package workflowview

import (
	"context"
	"errors"
	"testing"

	"core/shared/serverapi"
)

func TestAttentionCandidateProjectionReturnsTypedValidationErrors(t *testing.T) {
	tests := []struct {
		name  string
		row   attentionCandidateRow
		field string
		code  string
	}{
		{
			name:  "unknown kind",
			row:   attentionCandidateRow{kind: "unknown", id: "candidate-1"},
			field: "kind",
			code:  serverapi.WorkflowRequestErrorInvalidMode,
		},
		{
			name:  "missing task identity",
			row:   attentionCandidateRow{kind: "approval", id: "candidate-2"},
			field: "task_id",
			code:  serverapi.WorkflowRequestErrorRequired,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := (&Attention{}).itemFromCandidate(context.Background(), tt.row, nil)
			var validationErr serverapi.WorkflowRequestValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("itemFromCandidate error = %T %v, want WorkflowRequestValidationError", err, err)
			}
			if validationErr.Field != tt.field || validationErr.Code != tt.code {
				t.Fatalf("validation error = %+v, want code=%q field=%q", validationErr, tt.code, tt.field)
			}
		})
	}
}

func TestAttentionProjectionReturnsTypedErrorForInvalidQuestionRecommendation(t *testing.T) {
	sessionID := "session-1"
	taskID := "task-1"
	shortID := "KENT-1"
	title := "Task"
	runID := "run-1"
	askID := "ask-1"
	resolver := newPendingQuestionResolver(nil, staticPendingPromptSource{
		sessionID: {{
			ID:                     askID,
			Question:               "Continue?",
			Suggestions:            []string{"Yes"},
			RecommendedOptionIndex: attentionProjectionTestPointer(2),
		}},
	})

	_, _, err := (&Attention{}).itemFromCandidate(t.Context(), attentionCandidateRow{
		kind:       "question",
		id:         "question:" + askID,
		projectID:  "project-1",
		workflowID: "workflow-1",
		taskID:     &taskID,
		shortID:    &shortID,
		title:      &title,
		runID:      &runID,
		sessionID:  &sessionID,
		askID:      &askID,
	}, resolver)
	var validationErr serverapi.WorkflowRequestValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("itemFromCandidate error = %T %v, want WorkflowRequestValidationError", err, err)
	}
	if validationErr.Code != serverapi.WorkflowRequestErrorInvalidValue || validationErr.Field != "recommended_option_index" {
		t.Fatalf("validation error = %+v, want invalid recommended option", validationErr)
	}
}

func attentionProjectionTestPointer(value int) *int {
	return &value
}
