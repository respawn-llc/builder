package workflowrunner

import (
	"context"
	"errors"
	"fmt"

	"core/server/launch"
	"core/server/session"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/runtimeids"
)

// BuildWorkflowRuntimeConfig builds the runtime contract shared by live workflow
// execution and persisted workflow request inspection.
func BuildWorkflowRuntimeConfig(input workflowstore.RunStartContext, completionMode workflowruntime.CompletionMode, maxInvalidCompletionAttempts int, useRequiredToolCalls bool, controller workflowruntime.Controller, taskCommentCounter workflowruntime.TaskCommentCounter) (*workflowruntime.Config, error) {
	instructions, err := BuildWorkflowTaskInstructions(input)
	if err != nil {
		return nil, err
	}
	return &workflowruntime.Config{
		RunID:                        input.Run.ID,
		Contract:                     workflowCompletionContractForRun(input.Run, input),
		CompletionMode:               completionMode,
		MaxInvalidCompletionAttempts: maxInvalidCompletionAttempts,
		UseAutomaticToolChoice:       !useRequiredToolCalls,
		Controller:                   controller,
		TaskCommentCounter:           taskCommentCounter,
		Instructions:                 instructions,
	}, nil
}

// PersistedWorkflowInspection is the workflow-owned runtime reconstruction for
// a persisted session, including the run's authoritative execution root.
type PersistedWorkflowInspection struct {
	Plan          launch.SessionPlan
	Runtime       *workflowruntime.Config
	ExecutionRoot string
}

func optionalRunCompletionMode(mode *string) string {
	if mode == nil {
		return ""
	}
	return *mode
}

// BuildPersistedWorkflowInspection reconstructs the same workflow session plan
// and runtime contract that Starter uses immediately before a model turn. The
// supplied session store controls persistence, so callers can use a fileless
// store for read-only inspection.
func BuildPersistedWorkflowInspection(ctx context.Context, app config.App, sessionStore *session.Store, store *workflowstore.Store) (PersistedWorkflowInspection, error) {
	if sessionStore == nil {
		return PersistedWorkflowInspection{}, errors.New("session store is required")
	}
	sessionID, err := runtimeids.ParseSessionID(sessionStore.Meta().SessionID)
	if err != nil {
		return PersistedWorkflowInspection{}, fmt.Errorf("parse persisted session id: %w", err)
	}
	if store == nil {
		return PersistedWorkflowInspection{}, errors.New("workflow store is required")
	}
	input, err := store.ResolveCurrentSessionStartContext(ctx, sessionID)
	if err != nil {
		return PersistedWorkflowInspection{}, fmt.Errorf("resolve persisted workflow context: %w", err)
	}
	executionRoot, err := requireRunExecutionRoot(input)
	if err != nil {
		return PersistedWorkflowInspection{}, err
	}
	app.WorkspaceRoot = executionRoot.SourceWorkspaceRoot
	overrides := workflowRunPromptOverrides(input.Node.SubagentRole)
	plan, err := launch.ResolvePromptFacingSnapshotPlan(app, sessionStore, overrides.HasAny())
	if err != nil {
		return PersistedWorkflowInspection{}, err
	}
	plan, _, err = applyWorkflowSessionPromptOverrides(plan, input)
	if err != nil {
		return PersistedWorkflowInspection{}, err
	}
	mode, err := workflowruntime.ParseCompletionMode(optionalRunCompletionMode(input.Run.EffectiveCompletionMode))
	if err != nil {
		return PersistedWorkflowInspection{}, fmt.Errorf("parse workflow completion mode: %w", err)
	}
	runtimeConfig, err := BuildWorkflowRuntimeConfig(
		input,
		mode,
		plan.ActiveSettings.Workflow.MaxInvalidCompletionAttempts,
		plan.ActiveSettings.Workflow.UseRequiredToolCalls,
		workflowruntime.StoreController{Store: store},
		store,
	)
	if err != nil {
		return PersistedWorkflowInspection{}, err
	}
	return PersistedWorkflowInspection{
		Plan:          plan,
		Runtime:       runtimeConfig,
		ExecutionRoot: executionRoot.EffectiveRoot(),
	}, nil
}
