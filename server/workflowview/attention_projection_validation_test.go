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
