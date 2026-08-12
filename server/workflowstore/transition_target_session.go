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

type transitionContextResolution struct {
	TargetSession              workflow.TargetSessionIntent
	ActiveSource               workflow.MaterializedContinuationSource
	SelectedCurrentAssociation *TaskSessionAssociation
	invariant                  *workflow.RetainedTargetInvariantDetail
}

func (r transitionContextResolution) targetSessionID() *runtimeids.SessionID {
	sessionID, ok := r.TargetSession.SessionID()
	if !ok {
		return nil
	}
	return &sessionID
}

func (r transitionContextResolution) invariantDetail() (workflow.RetainedTargetInvariantDetail, bool) {
	if r.invariant == nil {
		return workflow.RetainedTargetInvariantDetail{}, false
	}
	return *r.invariant, true
}

func resolveTransitionContext(
	ctx context.Context,
	q *sqlitegen.Queries,
	definition workflow.Definition,
	edge workflow.Edge,
	taskID workflow.TaskID,
	source *workflow.CurrentNode,
	targetBranchKey *workflow.TransitionBranchKey,
	sourceNode workflow.Node,
	targetNode workflow.Node,
	manualMoveContext bool,
) (transitionContextResolution, error) {
	if targetNode.Kind() == workflow.NodeKindTerminal {
		return transitionContextResolution{
			TargetSession: workflow.NoAgentTargetSessionIntent(),
			ActiveSource:  workflow.AbsentMaterializedContinuationSource(),
		}, nil
	}
	if edge.ContextMode == workflow.ContextModeNewSession {
		if targetNode.Kind() == workflow.NodeKindAgent {
			return transitionContextResolution{
				TargetSession: workflow.CreateTargetSessionIntent(),
				ActiveSource:  workflow.DeferredSelfMaterializedContinuationSource(),
			}, nil
		}
		return transitionContextResolution{
			TargetSession: workflow.NoAgentTargetSessionIntent(),
			ActiveSource:  incomingTransitionActiveSource(source),
		}, nil
	}
	contextSource := workflow.CanonicalContextSource(edge.ContextSource)
	if source != nil &&
		source.ContinuationSource.Kind() == workflow.MaterializedContinuationSourceLegacy &&
		(contextSource.Kind == workflow.ContextSourcePreviousTarget ||
			contextSource.Kind == workflow.ContextSourcePreviousTargetOrNew) {
		panic(fmt.Sprintf(
			"legacy Current Node %v cannot resolve retained target context for edge %q",
			source.Reference,
			edge.ID,
		))
	}
	switch contextSource.Kind {
	case workflow.ContextSourceImmediateSource:
		return resolveImmediateSourceTransitionContext(
			ctx,
			q,
			taskID,
			source,
			sourceNode,
			targetNode,
			manualMoveContext,
		)
	case workflow.ContextSourceSelectedNode:
		selected, err := currentNodeDefinitionNodeByKey(definition, contextSource.NodeKey)
		if err != nil {
			return transitionContextResolution{}, err
		}
		selectedReference, err := workflow.NewCurrentNodeReference(
			taskID,
			workflow.NodeIDOf(selected),
			selectedContextBranchKey(manualMoveContext, source),
		)
		if err != nil {
			return transitionContextResolution{}, err
		}
		association, err := currentTaskSessionForNode(ctx, q, selectedReference)
		if err != nil {
			return transitionContextResolution{}, err
		}
		return directTransitionContextResolution(targetNode, association.SessionID, &association)
	case workflow.ContextSourcePreviousTarget, workflow.ContextSourcePreviousTargetOrNew:
		return resolveRetainedTargetTransitionContext(
			ctx,
			q,
			edge,
			taskID,
			source,
			targetBranchKey,
			sourceNode,
			manualMoveContext,
		)
	default:
		if manualMoveContext {
			return transitionContextResolution{}, ErrManualMoveTransitionNotUsable
		}
		return transitionContextResolution{}, fmt.Errorf("current node completion does not yet support context source %q", contextSource.Kind)
	}
}

func resolveImmediateSourceTransitionContext(
	ctx context.Context,
	q *sqlitegen.Queries,
	taskID workflow.TaskID,
	source *workflow.CurrentNode,
	sourceNode workflow.Node,
	targetNode workflow.Node,
	manualMoveContext bool,
) (transitionContextResolution, error) {
	if source != nil &&
		source.SessionID != nil &&
		source.Reference.NodeID == workflow.NodeIDOf(sourceNode) &&
		sourceNode.Kind() == workflow.NodeKindAgent &&
		(!manualMoveContext || !source.Reference.IsBranchScoped()) {
		return directTransitionContextResolution(targetNode, *source.SessionID, nil)
	}
	if manualMoveContext {
		if sourceNode.Kind() != workflow.NodeKindAgent {
			return transitionContextResolution{}, ErrManualMoveTransitionNotUsable
		}
		sourceReference, err := workflow.NewCurrentNodeReference(taskID, workflow.NodeIDOf(sourceNode), nil)
		if err != nil {
			return transitionContextResolution{}, err
		}
		association, err := currentTaskSessionForNode(sqlitegen.WithExpectedNoRows(ctx), q, sourceReference)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) &&
				source != nil &&
				source.Reference.Equal(sourceReference) &&
				source.SessionID == nil &&
				targetNode.Kind() == workflow.NodeKindAgent {
				return transitionContextResolution{
					TargetSession: workflow.CreateTargetSessionIntent(),
					ActiveSource:  workflow.DeferredSelfMaterializedContinuationSource(),
				}, nil
			}
			return transitionContextResolution{}, err
		}
		return directTransitionContextResolution(targetNode, association.SessionID, &association)
	}
	return transitionContextResolution{}, fmt.Errorf(
		"current node completion cannot continue the immediate source session for node %q",
		workflow.NodeIDOf(sourceNode),
	)
}

func directTransitionContextResolution(
	targetNode workflow.Node,
	sessionID runtimeids.SessionID,
	association *TaskSessionAssociation,
) (transitionContextResolution, error) {
	activeSource, err := workflow.NewExactMaterializedContinuationSource(sessionID)
	if err != nil {
		return transitionContextResolution{}, err
	}
	targetSession := workflow.NoAgentTargetSessionIntent()
	if targetNode.Kind() == workflow.NodeKindAgent {
		targetSession, err = workflow.NewReuseTargetSessionIntent(sessionID)
		if err != nil {
			return transitionContextResolution{}, err
		}
	}
	return transitionContextResolution{
		TargetSession:              targetSession,
		ActiveSource:               activeSource,
		SelectedCurrentAssociation: association,
	}, nil
}

func resolveRetainedTargetTransitionContext(
	ctx context.Context,
	q *sqlitegen.Queries,
	edge workflow.Edge,
	taskID workflow.TaskID,
	source *workflow.CurrentNode,
	targetBranchKey *workflow.TransitionBranchKey,
	sourceNode workflow.Node,
	manualMoveContext bool,
) (transitionContextResolution, error) {
	targetReference, err := workflow.NewCurrentNodeReference(
		taskID,
		edge.TargetNodeID,
		nilIfManualMoveContext(manualMoveContext, targetBranchKey),
	)
	if err != nil {
		return transitionContextResolution{}, err
	}
	targetState := workflow.UnavailableRetainedTarget()
	var selected *TaskSessionAssociation
	association, err := currentTaskSessionForNode(sqlitegen.WithExpectedNoRows(ctx), q, targetReference)
	switch {
	case err == nil:
		targetState, err = workflow.NewCurrentRetainedTarget(association.SessionID, association.SourceSessionID)
		if err != nil {
			return transitionContextResolution{}, err
		}
		selected = &association
	case !errors.Is(err, sql.ErrNoRows):
		return transitionContextResolution{}, err
	default:
		historical, err := hasHistoricalTaskSessionForNode(ctx, q, targetReference)
		if err != nil {
			return transitionContextResolution{}, err
		}
		if historical {
			targetState = workflow.HistoricalRetainedTarget()
		}
	}
	activeSource := incomingTransitionActiveSource(source)
	if manualMoveContext {
		if source == nil ||
			source.Reference.IsBranchScoped() ||
			source.Reference.NodeID != workflow.NodeIDOf(sourceNode) {
			sourceReference, err := workflow.NewCurrentNodeReference(taskID, workflow.NodeIDOf(sourceNode), nil)
			if err != nil {
				return transitionContextResolution{}, err
			}
			sourceAssociation, err := currentTaskSessionForNode(ctx, q, sourceReference)
			if err != nil {
				return transitionContextResolution{}, err
			}
			activeSource, err = workflow.NewExactMaterializedContinuationSource(sourceAssociation.SessionID)
			if err != nil {
				return transitionContextResolution{}, err
			}
		}
	}
	decision, err := workflow.EvaluateRetainedTarget(workflow.RetainedTargetEvaluationRequest{
		TaskID:        taskID,
		SourceNodeID:  workflow.NodeIDOf(sourceNode),
		TargetNodeID:  edge.TargetNodeID,
		ContextSource: edge.ContextSource,
		ActiveSource:  activeSource,
		Target:        targetState,
	})
	if err != nil {
		return transitionContextResolution{}, err
	}
	var selectedCurrentAssociation *TaskSessionAssociation
	if decision.TargetSession.Kind() == workflow.TargetSessionIntentReuse {
		selectedCurrentAssociation = selected
	}
	resolution := transitionContextResolution{
		TargetSession:              decision.TargetSession,
		ActiveSource:               decision.ActiveSource,
		SelectedCurrentAssociation: selectedCurrentAssociation,
	}
	if detail, ok := decision.InvariantDetail(); ok {
		resolution.invariant = &detail
	}
	return resolution, nil
}

func incomingTransitionActiveSource(source *workflow.CurrentNode) workflow.MaterializedContinuationSource {
	if source == nil {
		return workflow.AbsentMaterializedContinuationSource()
	}
	return source.ContinuationSource
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
