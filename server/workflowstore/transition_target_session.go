package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/shared/runtimeids"
)

func resolveTransitionTargetSession(
	ctx context.Context,
	q *sqlitegen.Queries,
	definition workflow.Definition,
	edge workflow.Edge,
	taskID workflow.TaskID,
	source *workflow.CurrentNode,
	targetBranchKey *workflow.TransitionBranchKey,
	sourceNode workflow.Node,
	manualMoveContext bool,
) (*runtimeids.SessionID, error) {
	if edge.ContextMode == workflow.ContextModeNewSession {
		return nil, nil
	}
	contextSource := workflow.CanonicalContextSource(edge.ContextSource)
	switch contextSource.Kind {
	case workflow.ContextSourceImmediateSource:
		if source != nil &&
			source.SessionID != nil &&
			source.Reference.NodeID == workflow.NodeIDOf(sourceNode) &&
			sourceNode.Kind() == workflow.NodeKindAgent &&
			(!manualMoveContext || !source.Reference.IsBranchScoped()) {
			sessionID := *source.SessionID
			return &sessionID, nil
		}
		if manualMoveContext {
			if sourceNode.Kind() != workflow.NodeKindAgent {
				return nil, ErrManualMoveTransitionNotUsable
			}
			sourceReference, err := workflow.NewCurrentNodeReference(taskID, workflow.NodeIDOf(sourceNode), nil)
			if err != nil {
				return nil, err
			}
			association, err := currentTaskSessionForNode(ctx, q, sourceReference)
			if err != nil {
				return nil, err
			}
			sessionID := association.SessionID
			return &sessionID, nil
		}
		return nil, fmt.Errorf(
			"current node completion cannot continue the immediate source session for node %q",
			workflow.NodeIDOf(sourceNode),
		)
	case workflow.ContextSourceSelectedNode:
		selected, err := currentNodeDefinitionNodeByKey(definition, contextSource.NodeKey)
		if err != nil {
			return nil, err
		}
		selectedReference, err := workflow.NewCurrentNodeReference(
			taskID,
			workflow.NodeIDOf(selected),
			selectedContextBranchKey(manualMoveContext, source),
		)
		if err != nil {
			return nil, err
		}
		association, err := currentTaskSessionForNode(ctx, q, selectedReference)
		if err != nil {
			return nil, err
		}
		sessionID := association.SessionID
		return &sessionID, nil
	case workflow.ContextSourcePreviousTarget, workflow.ContextSourcePreviousTargetOrNew:
		targetReference, err := workflow.NewCurrentNodeReference(
			taskID,
			edge.TargetNodeID,
			nilIfManualMoveContext(manualMoveContext, targetBranchKey),
		)
		if err != nil {
			return nil, err
		}
		association, err := currentTaskSessionForNode(ctx, q, targetReference)
		if err != nil {
			if contextSource.Kind == workflow.ContextSourcePreviousTargetOrNew && errors.Is(err, sql.ErrNoRows) {
				return nil, nil
			}
			return nil, err
		}
		sessionID := association.SessionID
		return &sessionID, nil
	default:
		if manualMoveContext {
			return nil, ErrManualMoveTransitionNotUsable
		}
		return nil, fmt.Errorf("current node completion does not yet support context source %q", contextSource.Kind)
	}
}

func nilIfManualMoveContext(manualMoveContext bool, branchKey *workflow.TransitionBranchKey) *workflow.TransitionBranchKey {
	if manualMoveContext {
		return nil
	}
	return branchKey
}

func selectedContextBranchKey(manualMoveContext bool, source *workflow.CurrentNode) *workflow.TransitionBranchKey {
	if manualMoveContext || source == nil {
		return nil
	}
	return currentNodeReferenceBranchKey(source.Reference)
}

func currentNodeDefinitionNodeByKey(definition workflow.Definition, key workflow.ModelKey) (workflow.Node, error) {
	for _, node := range definition.Nodes {
		if workflow.NodeKey(node) == key {
			return node, nil
		}
	}
	return nil, fmt.Errorf("context source node %q is absent from workflow %q", key, definition.ID)
}
