package workflowrunner

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/session"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
)

// BuildWorkflowRuntimeConfig builds the runtime contract shared by live workflow
// execution and persisted workflow request inspection.
func BuildWorkflowRuntimeConfig(input workflowstore.RunStartContext, completionMode workflowruntime.CompletionMode, maxInvalidCompletionAttempts int, controller workflowruntime.Controller, taskCommentCounter workflowruntime.TaskCommentCounter) (*workflowruntime.Config, error) {
	instructions, err := BuildWorkflowTaskInstructions(input)
	if err != nil {
		return nil, err
	}
	return &workflowruntime.Config{
		RunID:                        input.Run.ID,
		Contract:                     workflowCompletionContractForRun(input.Run, input),
		CompletionMode:               completionMode,
		MaxInvalidCompletionAttempts: maxInvalidCompletionAttempts,
		Controller:                   controller,
		TaskCommentCounter:           taskCommentCounter,
		Instructions:                 instructions,
	}, nil
}

// BuildPersistedWorkflowRuntimeConfig reconstructs a workflow runtime contract
// from the marker stored on a session. It only reads workflow metadata.
func BuildPersistedWorkflowRuntimeConfig(ctx context.Context, store *workflowstore.Store, state session.WorkflowSessionState, sessionID string, maxInvalidCompletionAttempts int) (*workflowruntime.Config, error) {
	if store == nil {
		return nil, errors.New("workflow store is required")
	}
	runID := workflow.RunID(strings.TrimSpace(state.RunID))
	if runID == "" {
		return nil, errors.New("workflow session run id is required")
	}
	input, err := store.GetRunStartContext(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("load workflow run context: %w", err)
	}
	if strings.TrimSpace(input.Run.SessionID) != strings.TrimSpace(sessionID) {
		return nil, fmt.Errorf("workflow run %q is attached to session %q, not %q", runID, input.Run.SessionID, sessionID)
	}
	if taskID := strings.TrimSpace(state.TaskID); taskID != "" && taskID != string(input.Task.ID) {
		return nil, fmt.Errorf("workflow session task %q does not match run task %q", taskID, input.Task.ID)
	}
	if workflowID := strings.TrimSpace(state.WorkflowID); workflowID != "" && workflowID != string(input.Task.WorkflowID) {
		return nil, fmt.Errorf("workflow session workflow %q does not match run workflow %q", workflowID, input.Task.WorkflowID)
	}
	mode, err := workflowruntime.ParseCompletionMode(input.Run.EffectiveCompletionMode)
	if err != nil {
		return nil, fmt.Errorf("parse workflow completion mode: %w", err)
	}
	return BuildWorkflowRuntimeConfig(
		input,
		mode,
		maxInvalidCompletionAttempts,
		workflowruntime.StoreController{Store: store},
		store,
	)
}
