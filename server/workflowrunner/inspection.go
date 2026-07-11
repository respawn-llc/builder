package workflowrunner

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/launch"
	"core/server/session"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/config"
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
	input, err := loadPersistedWorkflowRunInput(ctx, store, state, sessionID)
	if err != nil {
		return nil, err
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

// PersistedWorkflowInspection is the workflow-owned runtime reconstruction for
// a persisted session, including the run's authoritative worktree root.
type PersistedWorkflowInspection struct {
	Plan         launch.SessionPlan
	Runtime      *workflowruntime.Config
	WorktreeRoot string
}

// BuildPersistedWorkflowInspection reconstructs the same workflow session plan
// and runtime contract that Starter uses immediately before a model turn. The
// supplied session store controls persistence, so callers can use a fileless
// store for read-only inspection.
func BuildPersistedWorkflowInspection(ctx context.Context, app config.App, sessionStore *session.Store, store *workflowstore.Store) (PersistedWorkflowInspection, error) {
	if sessionStore == nil || sessionStore.Meta().WorkflowSession == nil {
		return PersistedWorkflowInspection{}, errors.New("workflow session state is required")
	}
	state := *sessionStore.Meta().WorkflowSession
	input, err := loadPersistedWorkflowRunInput(ctx, store, state, sessionStore.Meta().SessionID)
	if err != nil {
		return PersistedWorkflowInspection{}, err
	}
	app.WorkspaceRoot = strings.TrimSpace(input.WorkspaceRoot)
	overrides := workflowRunPromptOverrides(input.Node.SubagentRole)
	plan, err := launch.ResolvePromptFacingSnapshotPlan(app, sessionStore, overrides.HasAny())
	if err != nil {
		return PersistedWorkflowInspection{}, err
	}
	plan, _, err = applyWorkflowSessionPromptOverrides(plan, input)
	if err != nil {
		return PersistedWorkflowInspection{}, err
	}
	mode, err := workflowruntime.ParseCompletionMode(input.Run.EffectiveCompletionMode)
	if err != nil {
		return PersistedWorkflowInspection{}, fmt.Errorf("parse workflow completion mode: %w", err)
	}
	runtimeConfig, err := BuildWorkflowRuntimeConfig(
		input,
		mode,
		plan.ActiveSettings.Workflow.MaxInvalidCompletionAttempts,
		workflowruntime.StoreController{Store: store},
		store,
	)
	if err != nil {
		return PersistedWorkflowInspection{}, err
	}
	return PersistedWorkflowInspection{
		Plan:         plan,
		Runtime:      runtimeConfig,
		WorktreeRoot: input.WorktreeRoot,
	}, nil
}

func loadPersistedWorkflowRunInput(ctx context.Context, store *workflowstore.Store, state session.WorkflowSessionState, sessionID string) (workflowstore.RunStartContext, error) {
	if store == nil {
		return workflowstore.RunStartContext{}, errors.New("workflow store is required")
	}
	runID := workflow.RunID(strings.TrimSpace(state.RunID))
	if runID == "" {
		return workflowstore.RunStartContext{}, errors.New("workflow session run id is required")
	}
	input, err := store.GetRunStartContext(ctx, runID)
	if err != nil {
		return workflowstore.RunStartContext{}, fmt.Errorf("load workflow run context: %w", err)
	}
	if strings.TrimSpace(input.Run.SessionID) != strings.TrimSpace(sessionID) {
		return workflowstore.RunStartContext{}, fmt.Errorf("workflow run %q is attached to session %q, not %q", runID, input.Run.SessionID, sessionID)
	}
	if taskID := strings.TrimSpace(state.TaskID); taskID != "" && taskID != string(input.Task.ID) {
		return workflowstore.RunStartContext{}, fmt.Errorf("workflow session task %q does not match run task %q", taskID, input.Task.ID)
	}
	if workflowID := strings.TrimSpace(state.WorkflowID); workflowID != "" && workflowID != string(input.Task.WorkflowID) {
		return workflowstore.RunStartContext{}, fmt.Errorf("workflow session workflow %q does not match run workflow %q", workflowID, input.Task.WorkflowID)
	}
	return input, nil
}
