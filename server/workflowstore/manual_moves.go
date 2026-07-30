package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

type ManualMovePreparation struct {
	request                 ManualMoveRequest
	target                  workflow.Node
	requiresExecutionTarget bool
}

func (p ManualMovePreparation) RequiresExecutionTarget() bool {
	return p.requiresExecutionTarget
}

func (p ManualMovePreparation) TaskID() workflow.TaskID {
	return p.request.TaskID
}

func (s *Store) ManualMoveTask(ctx context.Context, req ManualMoveRequest) (ManualMoveResult, error) {
	prepared, err := s.PrepareManualMove(ctx, req)
	if err != nil {
		return ManualMoveResult{}, err
	}
	return s.ApplyManualMove(ctx, prepared, nil)
}

func (s *Store) PrepareManualMove(ctx context.Context, req ManualMoveRequest) (ManualMovePreparation, error) {
	if strings.TrimSpace(string(req.TaskID)) == "" {
		return ManualMovePreparation{}, errors.New("task id is required")
	}
	if strings.TrimSpace(string(req.TargetNodeID)) == "" {
		return ManualMovePreparation{}, errors.New("target node id is required")
	}
	task, err := s.queries.GetTask(ctx, string(req.TaskID))
	if err != nil {
		return ManualMovePreparation{}, err
	}
	definition, _, err := s.GetDefinition(ctx, task.WorkflowID)
	if err != nil {
		return ManualMovePreparation{}, err
	}
	target, err := currentNodeDefinitionNode(definition, req.TargetNodeID)
	if err != nil {
		return ManualMovePreparation{}, err
	}
	switch target.Kind() {
	case workflow.NodeKindStart, workflow.NodeKindTerminal:
		return ManualMovePreparation{request: req, target: target}, nil
	case workflow.NodeKindAgent, workflow.NodeKindScript:
		if _, _, _, _, err := resolveManualMoveExecutablePath(ctx, s.queries, req.TaskID, definition, workflow.NodeIDOf(target)); err != nil {
			return ManualMovePreparation{}, err
		}
		return ManualMovePreparation{request: req, target: target, requiresExecutionTarget: true}, nil
	default:
		return ManualMovePreparation{}, ErrManualMoveExecutableTargetNeedsEdge
	}
}

func (s *Store) ApplyManualMove(ctx context.Context, prepared ManualMovePreparation, executionTarget *ExecutionTargetCandidate) (ManualMoveResult, error) {
	if strings.TrimSpace(string(prepared.request.TaskID)) == "" || prepared.target == nil {
		return ManualMoveResult{}, errors.New("manual move preparation is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ManualMoveResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	task, err := q.GetTask(ctx, string(prepared.request.TaskID))
	if err != nil {
		return ManualMoveResult{}, err
	}
	attentionResolution, err := taskApprovalAttentionResolution(ctx, q, prepared.request.TaskID)
	if err != nil {
		return ManualMoveResult{}, err
	}
	definition, workflowRecord, err := workflowDefinitionFromQueries(ctx, q, task.WorkflowID)
	if err != nil {
		return ManualMoveResult{}, err
	}
	targetDefinition, err := currentNodeDefinitionNode(definition, prepared.request.TargetNodeID)
	if err != nil {
		return ManualMoveResult{}, err
	}
	var currentNodes []workflow.CurrentNode
	var target workflow.CurrentNode
	if executableNodeKind(targetDefinition.Kind()) {
		var source workflow.CurrentNode
		var edge workflow.Edge
		var sourceDefinition workflow.Node
		currentNodes, source, edge, sourceDefinition, err = resolveManualMoveExecutablePath(ctx, q, prepared.request.TaskID, definition, workflow.NodeIDOf(targetDefinition))
		if err != nil {
			return ManualMoveResult{}, err
		}
		preparedOutput, err := prepareCurrentNodeCompletionRequest(CurrentNodeCompletionRequest{
			Source:       source.Reference,
			TransitionID: "manual_move",
			OutputValues: prepared.request.OutputValues,
			Commentary:   prepared.request.Commentary,
		})
		if err != nil {
			return ManualMoveResult{}, err
		}
		targetMutation, err := s.prepareExecutionTargetMutation(ctx, task, executionTarget)
		if err != nil {
			return ManualMoveResult{}, err
		}
		executionRoot := targetMutation.executionRoot
		if targetMutation.candidateToLock != nil {
			executionRoot = targetMutation.candidateToLock.Root
		}
		if targetDefinition.Kind() == workflow.NodeKindScript {
			if err := s.validateScriptNodeForExecution(ctx, q, workflow.NodeIDOf(targetDefinition), &executionRoot); err != nil {
				return ManualMoveResult{}, err
			}
		}
		target, err = materializeCompletionTargetCurrentNode(
			ctx,
			q,
			definition,
			edge,
			sourceDefinition,
			targetDefinition,
			source,
			preparedOutput.OutputValues,
			nil,
		)
		if err != nil {
			return ManualMoveResult{}, err
		}
		if err := applyPreparedExecutionTargetMutation(ctx, q, task, targetMutation, s.now().UnixMilli()); err != nil {
			return ManualMoveResult{}, err
		}
		if edge.RequiresApproval {
			group, err := manualMoveTransitionGroup(definition, edge)
			if err != nil {
				return ManualMoveResult{}, err
			}
			if _, err := q.DeleteTaskPendingApprovalsByTask(ctx, string(prepared.request.TaskID)); err != nil {
				return ManualMoveResult{}, err
			}
			approval, err := newPendingApproval(
				source,
				workflowRecord.Version,
				group,
				workflow.NodeDisplayName(sourceDefinition),
				edge,
				targetDefinition,
				target,
				preparedOutput.OutputValues,
				s.now().UTC(),
			)
			if err != nil {
				return ManualMoveResult{}, err
			}
			if err := insertPendingApproval(ctx, q, approval); err != nil {
				return ManualMoveResult{}, err
			}
			if err := touchTaskUpdatedAt(ctx, q, string(prepared.request.TaskID), s.now().UnixMilli()); err != nil {
				return ManualMoveResult{}, err
			}
			if err := tx.Commit(); err != nil {
				return ManualMoveResult{}, err
			}
			return ManualMoveResult{
				Retained:                currentNodes,
				PendingApproval:         &approval,
				TaskAttentionResolution: attentionResolution,
			}, nil
		}
	} else {
		currentNodes, err = listTaskCurrentNodes(ctx, q, prepared.request.TaskID)
		if err != nil {
			return ManualMoveResult{}, err
		}
		if len(currentNodes) == 0 {
			return ManualMoveResult{}, ErrManualMoveNoSourcePosition
		}
		if executionTarget != nil {
			return ManualMoveResult{}, errors.New("manual move to a non-executable target does not accept an execution target")
		}
		target, err = newNonExecutableCurrentNode(prepared.request.TaskID, workflow.NodeIDOf(targetDefinition))
		if err != nil {
			return ManualMoveResult{}, err
		}
	}
	if _, err := q.DeleteTaskPendingApprovalsByTask(ctx, string(prepared.request.TaskID)); err != nil {
		return ManualMoveResult{}, err
	}
	removed, err := q.DeleteTaskCurrentNodes(ctx, string(prepared.request.TaskID))
	if err != nil {
		return ManualMoveResult{}, err
	}
	if removed != int64(len(currentNodes)) {
		return ManualMoveResult{}, sql.ErrNoRows
	}
	if _, err := q.DeleteTaskActiveFanout(ctx, string(prepared.request.TaskID)); err != nil {
		return ManualMoveResult{}, err
	}
	if err := insertTaskCurrentNode(ctx, q, target); err != nil {
		return ManualMoveResult{}, err
	}
	if err := touchTaskUpdatedAt(ctx, q, string(prepared.request.TaskID), s.now().UnixMilli()); err != nil {
		return ManualMoveResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ManualMoveResult{}, err
	}
	removedReferences := make([]workflow.CurrentNodeReference, 0, len(currentNodes))
	for _, currentNode := range currentNodes {
		removedReferences = append(removedReferences, currentNode.Reference)
	}
	return ManualMoveResult{
		CurrentNodeMutationResult: workflow.CurrentNodeMutationResult{
			Removed: removedReferences,
			Created: []workflow.CurrentNode{target},
		},
		TaskAttentionResolution: attentionResolution,
	}, nil
}

func manualMoveTransitionGroup(definition workflow.Definition, edge workflow.Edge) (workflow.TransitionGroup, error) {
	for _, group := range definition.TransitionGroups {
		if group.ID == edge.TransitionGroupID {
			return group, nil
		}
	}
	return workflow.TransitionGroup{}, fmt.Errorf("manual move edge %q transition group %q is absent from workflow %q", edge.ID, edge.TransitionGroupID, definition.ID)
}

func resolveManualMoveExecutablePath(ctx context.Context, q *sqlitegen.Queries, taskID workflow.TaskID, definition workflow.Definition, targetNodeID workflow.NodeID) ([]workflow.CurrentNode, workflow.CurrentNode, workflow.Edge, workflow.Node, error) {
	currentNodes, err := listTaskCurrentNodes(ctx, q, taskID)
	if err != nil {
		return nil, workflow.CurrentNode{}, workflow.Edge{}, nil, err
	}
	if len(currentNodes) == 0 {
		return nil, workflow.CurrentNode{}, workflow.Edge{}, nil, ErrManualMoveNoSourcePosition
	}
	if len(currentNodes) != 1 || currentNodes[0].Reference.IsBranchScoped() {
		return nil, workflow.CurrentNode{}, workflow.Edge{}, nil, ErrManualMoveExecutableTargetNeedsEdge
	}
	edge, sourceDefinition, err := manualMoveExecutableEdge(definition, currentNodes[0].Reference.NodeID, targetNodeID)
	if err != nil {
		return nil, workflow.CurrentNode{}, workflow.Edge{}, nil, err
	}
	return currentNodes, currentNodes[0], edge, sourceDefinition, nil
}

func manualMoveExecutableEdge(definition workflow.Definition, sourceNodeID, targetNodeID workflow.NodeID) (workflow.Edge, workflow.Node, error) {
	source, err := currentNodeDefinitionNode(definition, sourceNodeID)
	if err != nil {
		return workflow.Edge{}, nil, err
	}
	groupSource := make(map[workflow.TransitionGroupID]workflow.NodeID, len(definition.TransitionGroups))
	for _, group := range definition.TransitionGroups {
		groupSource[group.ID] = group.SourceNodeID
	}
	var edge *workflow.Edge
	for index := range definition.Edges {
		candidate := definition.Edges[index]
		if groupSource[candidate.TransitionGroupID] != sourceNodeID || candidate.TargetNodeID != targetNodeID {
			continue
		}
		if edge != nil {
			return workflow.Edge{}, nil, ErrManualMoveExecutableTargetNeedsEdge
		}
		edge = &candidate
	}
	if edge == nil {
		return workflow.Edge{}, nil, ErrManualMoveExecutableTargetNeedsEdge
	}
	return *edge, source, nil
}
