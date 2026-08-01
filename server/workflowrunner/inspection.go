package workflowrunner

import (
	"context"
	"errors"
	"fmt"

	"core/server/launch"
	"core/server/llm"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/runtimeids"
)

func BuildCurrentNodeRuntimeConfig(
	input workflowstore.CurrentNodeStartContext,
	lease sessionruntime.WorkflowExecutionLease,
	taskPromptDelivery workflowruntime.TaskPromptDelivery,
	completionMode workflowruntime.CompletionMode,
	maxInvalidCompletionAttempts int,
	useRequiredToolCalls bool,
	controller workflowruntime.Controller,
	taskCommentCounter workflowruntime.TaskCommentCounter,
) (*workflowruntime.CurrentNodeExecutionConfig, error) {
	instructions, err := BuildCurrentSessionTaskInstructions(input)
	if err != nil {
		return nil, err
	}
	return &workflowruntime.CurrentNodeExecutionConfig{
		ScopeID:                      lease.ScopeID(),
		TaskPromptDelivery:           taskPromptDelivery,
		Contract:                     workflowruntime.CompletionContract{Transitions: workflowCompletionTransitions(input.TransitionOptions, input.TransitionIDs)},
		CompletionMode:               completionMode,
		MaxInvalidCompletionAttempts: maxInvalidCompletionAttempts,
		UseAutomaticToolChoice:       !useRequiredToolCalls,
		Controller:                   controller,
		TaskCommentCounter:           taskCommentCounter,
		Instructions:                 instructions,
	}, nil
}

// PersistedWorkflowInspection is the workflow-owned prompt reconstruction for
// a persisted Session-bound Current Node.
type PersistedWorkflowInspection struct {
	Plan          launch.SessionPlan
	Prompt        *workflowruntime.PromptContract
	ExecutionRoot string
}

// BuildPersistedWorkflowInspection reconstructs the same workflow session plan
// and prompt contract that live execution uses immediately before a model turn. The
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
	executionRoot, err := requireCurrentNodeExecutionRoot(input)
	if err != nil {
		return PersistedWorkflowInspection{}, err
	}
	app.WorkspaceRoot = executionRoot.SourceWorkspaceRoot
	plan, err := launch.ResolvePromptFacingSnapshotPlan(app, sessionStore, false)
	if err != nil {
		return PersistedWorkflowInspection{}, err
	}
	mode, err := persistedInspectionCompletionMode(plan, input)
	if err != nil {
		return PersistedWorkflowInspection{}, err
	}
	instructions, err := BuildCurrentSessionTaskInstructions(input)
	if err != nil {
		return PersistedWorkflowInspection{}, err
	}
	return PersistedWorkflowInspection{
		Plan: plan,
		Prompt: &workflowruntime.PromptContract{
			Identity:               workflowruntime.CurrentNodePromptIdentity(instructions.CurrentNode),
			CompletionMode:         mode,
			UseAutomaticToolChoice: !plan.ActiveSettings.Workflow.UseRequiredToolCalls,
			Instructions:           instructions,
			Transitions:            workflowCompletionTransitions(input.TransitionOptions, input.TransitionIDs),
		},
		ExecutionRoot: executionRoot.EffectiveRoot(),
	}, nil
}

func requireCurrentNodeExecutionRoot(input workflowstore.CurrentNodeStartContext) (workflowstore.ExecutionRoot, error) {
	if input.ExecutionRoot == nil {
		return workflowstore.ExecutionRoot{}, fmt.Errorf("current workflow node %v has no execution root", input.CurrentNode.Reference)
	}
	root := *input.ExecutionRoot
	if err := root.Validate(); err != nil {
		return workflowstore.ExecutionRoot{}, fmt.Errorf("current workflow node %v has an invalid execution root: %w", input.CurrentNode.Reference, err)
	}
	return root, nil
}

func persistedInspectionCompletionMode(plan launch.SessionPlan, input workflowstore.CurrentNodeStartContext) (workflowruntime.CompletionMode, error) {
	if plan.Locked != nil && plan.Locked.WorkflowCompletionMode != nil {
		mode, err := workflowruntime.ParseCompletionMode(string(*plan.Locked.WorkflowCompletionMode))
		if err != nil {
			return "", fmt.Errorf("parse retained Session completion mode: %w", err)
		}
		return mode, nil
	}
	configured := plan.ActiveSettings.Workflow.CompletionMode
	if input.Node.CompletionMode != "" {
		configured = config.WorkflowCompletionMode(input.Node.CompletionMode)
	}
	if configured == config.WorkflowCompletionModeStructuredOutput {
		return workflowruntime.CompletionModeStructuredOutput, nil
	}
	selection := workflowruntime.CompletionModeSelection{
		ConfiguredMode:         configured,
		HasContinueSessionEdge: input.HasContinueSessionOutgoingEdge,
		ShellAvailable:         toolIDEnabled(plan.EnabledTools, "exec_command"),
	}
	if workflowCompletionModeNeedsProviderCapabilities(selection) {
		caps, ok := llm.ProviderCapabilitiesFromLockedOrOverride(plan.Locked, plan.ActiveSettings.ProviderCapabilities)
		if !ok {
			return "", errors.New("persisted workflow inspection requires a locked or configured provider capability contract for completion mode selection")
		}
		selection.ProviderCapabilities = caps
	}
	mode, err := workflowruntime.SelectCompletionMode(selection)
	if err != nil {
		return "", fmt.Errorf("select persisted workflow completion mode: %w", err)
	}
	return mode, nil
}
