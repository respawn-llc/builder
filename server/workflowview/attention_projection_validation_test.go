package workflowview

import (
	"context"
	"errors"
	"testing"

	"core/shared/serverapi"
	"core/shared/textutil"
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
	askID := "ask-1"
	resolver := newPendingQuestionResolver(nil, staticPendingPromptSource{
		sessionID: {{
			ID:                     askID,
			Question:               "Continue?",
			Suggestions:            []string{"Yes"},
			RecommendedOptionIndex: textutil.Value(2),
		}},
	})

	_, _, err := (&Attention{}).itemFromCandidate(t.Context(), attentionProjectionQuestionCandidate(sessionID, askID), resolver)
	var validationErr serverapi.WorkflowRequestValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("itemFromCandidate error = %T %v, want WorkflowRequestValidationError", err, err)
	}
	if validationErr.Code != serverapi.WorkflowRequestErrorInvalidValue || validationErr.Field != "recommended_option_index" {
		t.Fatalf("validation error = %+v, want invalid recommended option", validationErr)
	}
}

func TestAttentionProjectionPropagatesQuestionMetadataSourceFailures(t *testing.T) {
	sessionID := "session-1"
	askID := "ask-1"
	tests := []struct {
		name              string
		pendingPromptFail bool
	}{
		{
			name: "transcript source failure",
		},
		{
			name:              "pending prompt source failure",
			pendingPromptFail: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sourceErr := errors.New(tt.name)
			source := failingQuestionMetadataSource{err: sourceErr}
			resolver := newPendingQuestionResolver(source, nil)
			if tt.pendingPromptFail {
				resolver = newPendingQuestionResolver(nil, source)
			}
			_, include, err := (&Attention{}).itemFromCandidate(
				t.Context(),
				attentionProjectionQuestionCandidate(sessionID, askID),
				resolver,
			)
			if !errors.Is(err, sourceErr) {
				t.Fatalf("itemFromCandidate error = %v, want source error %v", err, sourceErr)
			}
			if include {
				t.Fatal("itemFromCandidate included a question after metadata source failure")
			}
		})
	}
}

func attentionProjectionQuestionCandidate(sessionID string, askID string) attentionCandidateRow {
	return attentionCandidateRow{
		kind:       "question",
		id:         "question:" + askID,
		projectID:  "project-1",
		workflowID: "workflow-1",
		taskID:     textutil.Value("task-1"),
		shortID:    textutil.Value("KENT-1"),
		title:      textutil.Value("Task"),
		runID:      textutil.Value("run-1"),
		sessionID:  textutil.Value(sessionID),
		askID:      textutil.Value(askID),
	}
}

type failingQuestionMetadataSource struct {
	err error
}

func (s failingQuestionMetadataSource) SessionNewestActiveSegmentQuestions(context.Context, string) ([]PendingQuestionTranscriptEntry, error) {
	return nil, s.err
}

func (s failingQuestionMetadataSource) ListPendingPrompts(string) ([]PendingPromptSnapshot, error) {
	return nil, s.err
}
