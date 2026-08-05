package workflowstore

import (
	"context"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
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
) (workflow.TransitionParameterContract, error) {
	if currentSource == nil {
		return workflow.TransitionParameterContract{}, nil
	}
	requiresProtectedPolicy := false
	for _, parameter := range edge.Parameters {
		if purpose := workflow.CanonicalParameterPurpose(parameter.Purpose); purpose == workflow.ParameterPurposeTargetAssignee || purpose == workflow.ParameterPurposeTargetThinking {
			requiresProtectedPolicy = true
			break
		}
	}
	if !requiresProtectedPolicy {
		return workflow.PlanTransitionParameterContract(workflow.TransitionParameterContractRequest{
			Edge:                         edge,
			SourceKind:                   source.Kind(),
			TargetKind:                   target.Kind(),
			TargetRole:                   workflow.NodeSubagentRole(target),
			Catalog:                      s.roleResolver,
			RequireExecutionDescriptions: requireExecutionDescriptions,
		})
	}
	sessionID, err := resolveTransitionTargetSession(
		ctx,
		q,
		definition,
		edge,
		currentSource.Reference.TaskID,
		currentSource,
		transitionBranchKey,
		source,
		manualMoveContext,
	)
	if err != nil {
		return workflow.TransitionParameterContract{}, err
	}

	var retainedTargetRole *workflow.TargetAgentRole
	if sessionID != nil {
		policy, err := workflow.ResolveAssigneeSessionPolicy(workflow.AssigneeSessionPolicyRequest{
			ContextMode:           edge.ContextMode,
			ContextSource:         edge.ContextSource,
			TargetSessionResolved: true,
		})
		if err != nil {
			return workflow.TransitionParameterContract{}, err
		}
		if policy == workflow.AssigneeSessionPolicyPreserve {
			selection, err := s.resolveRetainedSessionSelection(ctx, *sessionID)
			if err != nil {
				return workflow.TransitionParameterContract{}, err
			}
			role := workflow.TargetAgentRole{Identity: selection.Assignee}
			if s.roleResolver != nil {
				if resolved, ok := s.roleResolver.ResolveConfiguredRole(selection.Assignee); ok {
					role = resolved
				}
			}
			retainedTargetRole = &role
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
		Catalog:                      s.roleResolver,
		RequireExecutionDescriptions: requireExecutionDescriptions,
	})
}

func transitionBranchKeyForCurrentNode(reference workflow.CurrentNodeReference) *workflow.TransitionBranchKey {
	branchKey, ok := reference.TransitionBranchKey()
	if !ok {
		return nil
	}
	return &branchKey
}
