package workflowattention

import (
	"context"
	"errors"
	"testing"
	"time"

	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/clientui"
	"core/shared/runtimeids"
)

type questionAwaiterFunc func(context.Context, string, askquestion.AskQuestionRequest) (askquestion.AskQuestionResolution, error)

func (f questionAwaiterFunc) AwaitPromptResolution(
	ctx context.Context,
	sessionID string,
	req askquestion.AskQuestionRequest,
) (askquestion.AskQuestionResolution, error) {
	return f(ctx, sessionID, req)
}

type recordingQuestionAttention struct {
	cleared []string
	skipped []string
}

func (*recordingQuestionAttention) PrepareTaskQuestionBatch(
	askquestion.AskQuestionBatchMetadata,
	string,
	*clientui.AttentionNotificationTarget,
	string,
	time.Time,
) error {
	return nil
}

func (a *recordingQuestionAttention) MarkTaskQuestionCleared(_ askquestion.AskQuestionBatchMetadata, askID string) {
	a.cleared = append(a.cleared, askID)
}

func (a *recordingQuestionAttention) MarkTaskQuestionSkipped(batch askquestion.AskQuestionBatchMetadata) {
	a.skipped = append(a.skipped, batch.ToolCallID)
}

func TestHandleTaskQuestionDeclineKeepsPreparedSuccessorsPending(t *testing.T) {
	attention := &recordingQuestionAttention{}
	_, err := HandleTaskQuestion(
		context.Background(),
		questionAwaiterFunc(func(context.Context, string, askquestion.AskQuestionRequest) (askquestion.AskQuestionResolution, error) {
			return nil, context.Canceled
		}),
		attention,
		taskQuestionRequestForTest(t),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("HandleTaskQuestion error = %v, want context.Canceled", err)
	}
	if len(attention.cleared) != 1 || attention.cleared[0] != "ask-1" {
		t.Fatalf("cleared asks = %v, want current ask", attention.cleared)
	}
	if len(attention.skipped) != 0 {
		t.Fatalf("decline marked prepared successors skipped: %v", attention.skipped)
	}
}

func TestHandleTaskQuestionExecutionCancellationSkipsPreparedSuccessors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attention := &recordingQuestionAttention{}
	_, err := HandleTaskQuestion(
		ctx,
		questionAwaiterFunc(func(context.Context, string, askquestion.AskQuestionRequest) (askquestion.AskQuestionResolution, error) {
			return nil, context.Canceled
		}),
		attention,
		taskQuestionRequestForTest(t),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("HandleTaskQuestion error = %v, want context.Canceled", err)
	}
	if len(attention.skipped) != 1 || attention.skipped[0] != "ask-2" {
		t.Fatalf("skipped asks = %v, want prepared successor ask-2", attention.skipped)
	}
}

func taskQuestionRequestForTest(t *testing.T) TaskQuestionRequest {
	t.Helper()
	currentNode, err := workflow.NewCurrentNodeReference("task-1", "node-1", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	return TaskQuestionRequest{
		Context: TaskQuestionContext{
			Task: workflowstore.TaskRecord{
				ID:         "task-1",
				ProjectID:  "project-1",
				WorkflowID: runtimeids.NewWorkflowID(),
			},
			CurrentNode: currentNode,
			SessionID:   "session-1",
		},
		Question: askquestion.AskQuestionRequest{
			Question:   "Proceed?",
			Origin:     askquestion.AskQuestionOriginModelTool,
			RunID:      "run-1",
			StepID:     "step-1",
			ToolCallID: "ask-1",
			QuestionBatch: &askquestion.AskQuestionBatchMetadata{
				Origin:              askquestion.AskQuestionOriginModelTool,
				RunID:               "run-1",
				StepID:              "step-1",
				ToolCallID:          "ask-1",
				BatchToolCallIDs:    []string{"ask-1", "ask-2"},
				CandidateOrdinal:    0,
				PreparedPromptCount: 2,
			},
		},
	}
}
