package workflowstore

import (
	"context"
	"database/sql"
	"errors"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

type transitionContractContextResolutionMode uint8

const (
	transitionContractContextResolutionRequired transitionContractContextResolutionMode = iota
	transitionContractContextResolutionDeferred
)

type transitionContractTargetSessionPolicyMode uint8

const (
	transitionContractTargetSessionPolicyNotNeeded transitionContractTargetSessionPolicyMode = iota
	transitionContractTargetSessionPolicyRequired
)

func (s *Store) planTransitionParameterContract(
	ctx context.Context,
	q *sqlitegen.Queries,
	definition workflow.Definition,
	edge workflow.Edge,
	source workflow.Node,
	target workflow.Node,
	currentSource *workflow.CurrentNode,
	transitionBranchKey *workflow.TransitionBranchKey,
	manualMoveContext bool,
	requireExecutionDescriptions bool,
	contextResolutionMode transitionContractContextResolutionMode,
) (workflow.TransitionParameterContract, error) {
	sessionPolicyRequired := transitionContractTargetSessionPolicyModeForEdge(edge) == transitionContractTargetSessionPolicyRequired
	if currentSource == nil && sessionPolicyRequired {
		return workflow.TransitionParameterContract{}, nil
	}
	if !sessionPolicyRequired {
		return workflow.PlanTransitionParameterContract(workflow.TransitionParameterContractRequest{
			Edge:                         edge,
			SourceKind:                   source.Kind(),
			TargetKind:                   target.Kind(),
			TargetRole:                   workflow.NodeSubagentRole(target),
			Catalog:                      s.roleResolver,
			RequireExecutionDescriptions: requireExecutionDescriptions,
		})
	}
	contextResolution, err := resolveTransitionContext(
		ctx,
		q,
		definition,
		edge,
		currentSource.Reference.TaskID,
		currentSource,
		transitionBranchKey,
		source,
		target,
		manualMoveContext,
	)
	if err != nil {
		if contextResolutionMode != transitionContractContextResolutionDeferred ||
			(!errors.Is(err, sql.ErrNoRows) && !errors.Is(err, ErrManualMoveTransitionNotUsable)) {
			return workflow.TransitionParameterContract{}, err
		}
		contextResolution = transitionContextResolution{
			TargetSession: workflow.CreateTargetSessionIntent(),
			ActiveSource:  incomingTransitionActiveSource(currentSource),
		}
	}
	sessionID := contextResolution.targetSessionID()

	var retainedTargetRole *workflow.TargetAgentRole
	sessionPolicy := workflow.AssigneeSessionPolicyEstablishTarget
	if sessionID != nil {
		policy, err := workflow.ResolveAssigneeSessionPolicy(workflow.AssigneeSessionPolicyRequest{
			ContextMode:           edge.ContextMode,
			ContextSource:         edge.ContextSource,
			TargetSessionResolved: true,
		})
		if err != nil {
			return workflow.TransitionParameterContract{}, err
		}
		sessionPolicy = policy
		if policy == workflow.AssigneeSessionPolicyPreserve {
			selection, err := s.resolveRetainedSessionSelection(ctx, *sessionID)
			if err != nil {
				if contextResolutionMode != transitionContractContextResolutionDeferred || !errors.Is(err, sql.ErrNoRows) {
					return workflow.TransitionParameterContract{}, err
				}
				sessionID = nil
				sessionPolicy = workflow.AssigneeSessionPolicyEstablishTarget
			} else {
				role := workflow.TargetAgentRole{Identity: selection.Assignee}
				if s.roleResolver != nil {
					if resolved, ok := s.roleResolver.ResolveConfiguredRole(selection.Assignee); ok {
						role = resolved
					}
				}
				retainedTargetRole = &role
			}
		}
	}

	edgeCount := 0
	for _, candidate := range definition.Edges {
		if candidate.TransitionGroupID == edge.TransitionGroupID {
			edgeCount++
		}
	}
	return workflow.PlanTransitionParameterContract(workflow.TransitionParameterContractRequest{
		Edge:                         edge,
		SourceKind:                   source.Kind(),
		TargetKind:                   target.Kind(),
		TargetRole:                   workflow.NodeSubagentRole(target),
		RetainedTargetRole:           retainedTargetRole,
		FanOut:                       edgeCount > 1,
		TargetSessionResolved:        sessionID != nil,
		TargetSessionPolicy:          sessionPolicy,
		Catalog:                      s.roleResolver,
		RequireExecutionDescriptions: requireExecutionDescriptions,
	})
}

func transitionContractTargetSessionPolicyModeForEdge(edge workflow.Edge) transitionContractTargetSessionPolicyMode {
	for _, parameter := range edge.Parameters {
		purpose := workflow.CanonicalParameterPurpose(parameter.Purpose)
		if (purpose == workflow.ParameterPurposeTargetAssignee &&
			workflow.CanonicalAssigneeSelection(edge.AssigneeSelection) != workflow.AssigneeSelectionPreviousNode) ||
			(purpose == workflow.ParameterPurposeTargetThinking &&
				workflow.CanonicalThinkingSelection(edge.ThinkingSelection) != workflow.ThinkingSelectionPreviousNode) ||
			(purpose != workflow.ParameterPurposeTargetAssignee &&
				purpose != workflow.ParameterPurposeTargetThinking) {
			continue
		}
		if edge.ContextMode == workflow.ContextModeContinueSession {
			return transitionContractTargetSessionPolicyRequired
		}
	}
	return transitionContractTargetSessionPolicyNotNeeded
}

func transitionBranchKeyForCurrentNode(reference workflow.CurrentNodeReference) *workflow.TransitionBranchKey {
	branchKey, ok := reference.TransitionBranchKey()
	if !ok {
		return nil
	}
	return &branchKey
}
