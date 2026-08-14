package workflowsvc

import (
	"context"
	"errors"
	"path/filepath"

	"core/server/metadata"
	"core/server/session"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/sessioncontract"
)

func applyManualMoveForWorkflowServiceTest(
	ctx context.Context,
	store *workflowstore.Store,
	prepared workflowstore.ManualMovePreparation,
	candidate *workflowstore.ExecutionTargetCandidate,
) (workflowstore.ManualMoveResult, error) {
	return store.ApplyManualMoveWithTargetAssignments(
		ctx,
		prepared,
		candidate,
		func(ctx context.Context, inputs []workflowstore.CurrentNodeStartContext) (workflowstore.ManualMoveTargetAssignmentPreparation, error) {
			assignments := make([]workflowstore.ManualMoveTargetAssignment, 0, len(inputs))
			for _, input := range inputs {
				if input.CurrentNode.AgentExecutionSelection == nil {
					if input.Node.Kind != workflow.NodeKindScript {
						return workflowstore.ManualMoveTargetAssignmentPreparation{}, errors.New("test Manual Move target execution shape is inconsistent")
					}
					continue
				}
				if input.Node.Kind != workflow.NodeKindAgent || input.ExecutionRoot == nil {
					return workflowstore.ManualMoveTargetAssignmentPreparation{}, errors.New("test Manual Move Agent target execution context is incomplete")
				}
				sessionID, err := workflowServiceManualMoveSession(ctx, input)
				if err != nil {
					return workflowstore.ManualMoveTargetAssignmentPreparation{}, err
				}
				assignments = append(assignments, workflowstore.ManualMoveTargetAssignment{
					CurrentNode: input.CurrentNode.Reference,
					SessionID:   sessionID,
				})
			}
			return workflowstore.ManualMoveTargetAssignmentPreparation{Assignments: assignments}, nil
		},
	)
}

func workflowServiceManualMoveSession(
	ctx context.Context,
	input workflowstore.CurrentNodeStartContext,
) (runtimeids.SessionID, error) {
	if input.CurrentNode.SessionID != nil {
		return *input.CurrentNode.SessionID, nil
	}
	cfg, err := config.Load(input.ExecutionRoot.SourceWorkspaceRoot, config.LoadOptions{})
	if err != nil {
		return runtimeids.SessionID{}, err
	}
	metadataStore, err := metadata.Open(cfg.PersistenceRoot)
	if err != nil {
		return runtimeids.SessionID{}, err
	}
	defer func() { _ = metadataStore.Close() }()
	sessionRoot := filepath.Join(cfg.PersistenceRoot, "projects", input.Task.ProjectID, "sessions")
	sessionStore, err := session.Create(
		sessionRoot,
		filepath.Base(input.ExecutionRoot.SourceWorkspaceRoot),
		input.ExecutionRoot.SourceWorkspaceRoot,
		sessioncontract.SessionCategorySubagent,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		return runtimeids.SessionID{}, err
	}
	if err := sessionStore.EnsureDurable(); err != nil {
		return runtimeids.SessionID{}, err
	}
	if _, err := metadataStore.ResolvePersistedSession(ctx, sessionStore.Meta().SessionID); err != nil {
		return runtimeids.SessionID{}, err
	}
	return runtimeids.ParseSessionID(sessionStore.Meta().SessionID)
}
