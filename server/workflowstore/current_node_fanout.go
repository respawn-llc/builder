package workflowstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/shared/runtimeids"
)

type currentNodeFanoutTarget struct {
	BranchKey   workflow.TransitionBranchKey
	CurrentNode workflow.CurrentNode
	Node        workflow.Node
	NodeKind    workflow.NodeKind
	Invariant   *workflow.RetainedTargetInvariantDetail
}

func completeCurrentNodeFanout(
	ctx context.Context,
	q *sqlitegen.Queries,
	definition workflow.Definition,
	source workflow.Node,
	currentSource workflow.CurrentNode,
	targets []currentNodeCompletionTarget,
	commentary string,
	outputValues map[string]string,
	workflowVersion int64,
	group workflow.TransitionGroup,
	catalog workflow.TargetAgentCatalog,
	resolveRetainedSessionSelection func(context.Context, runtimeids.SessionID) (*workflow.AgentExecutionSelection, error),
	createdAt time.Time,
) (CurrentNodeCompletionResult, error) {
	if currentSource.Reference.IsBranchScoped() {
		return CurrentNodeCompletionResult{}, errors.New("nested fan-out current node completion is not supported")
	}
	preparedTargets := make([]currentNodeFanoutTarget, 0, len(targets))
	approvalBranches := make([]workflow.PendingApprovalBranch, 0, len(targets))
	requiresApproval := false
	for _, target := range targets {
		branchKey := workflow.TransitionBranchKey(strings.TrimSpace(string(target.Edge.Key)))
		if branchKey == "" {
			return CurrentNodeCompletionResult{}, errors.New("fan-out transition branch key is required")
		}
		materializedTarget, err := materializeCompletionTargetCurrentNode(
			ctx,
			q,
			definition,
			target.Edge,
			source,
			target.Node,
			catalog,
			resolveRetainedSessionSelection,
			currentSource,
			outputValues,
			commentary,
			&branchKey,
		)
		if err != nil {
			return CurrentNodeCompletionResult{}, err
		}
		targetCurrentNode := materializedTarget.CurrentNode
		if materializedTarget.Invariant != nil {
			checkRetainedTargetInvariantBeforeMutation(*materializedTarget.Invariant)
		}
		preparedTargets = append(preparedTargets, currentNodeFanoutTarget{
			BranchKey:   branchKey,
			CurrentNode: targetCurrentNode,
			Node:        target.Node,
			Invariant:   materializedTarget.Invariant,
		})
		contextResolution, err := pendingApprovalContextSourceResolution(target.Node.Kind(), targetCurrentNode)
		if err != nil {
			return CurrentNodeCompletionResult{}, err
		}
		approvalBranches = append(approvalBranches, workflow.PendingApprovalBranch{
			TransitionBranchKey: branchKey,
			Target: workflow.PendingApprovalTarget{
				CurrentNode: targetCurrentNode,
				DisplayName: workflow.NodeDisplayName(target.Node),
				NodeKind:    target.Node.Kind(),
			},
			EffectiveEdge:           target.Edge,
			ContextSourceResolution: contextResolution,
		})
		requiresApproval = requiresApproval || target.Edge.RequiresApproval
	}
	if requiresApproval {
		approval, err := newPendingApprovalWithBranches(
			currentSource,
			workflowVersion,
			group,
			workflow.NodeDisplayName(source),
			commentary,
			outputValues,
			approvalBranches,
			createdAt,
		)
		if err != nil {
			return CurrentNodeCompletionResult{}, err
		}
		if err := insertPendingApproval(ctx, q, approval); err != nil {
			return CurrentNodeCompletionResult{}, err
		}
		result := CurrentNodeCompletionResult{PendingApproval: &approval}
		for _, target := range preparedTargets {
			if target.Invariant != nil {
				result.retainedTargetInvariants = append(result.retainedTargetInvariants, *target.Invariant)
			}
		}
		return result, nil
	}
	if err := replaceCurrentNodeWithFanout(ctx, q, currentSource.Reference, preparedTargets); err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	created := make([]workflow.CurrentNode, 0, len(preparedTargets))
	for _, target := range preparedTargets {
		created = append(created, target.CurrentNode)
	}
	result := CurrentNodeCompletionResult{
		Mutation: workflow.CurrentNodeMutationResult{
			Removed: []workflow.CurrentNodeReference{currentSource.Reference},
			Created: created,
		},
	}
	for _, target := range preparedTargets {
		if target.Invariant != nil {
			result.retainedTargetInvariants = append(result.retainedTargetInvariants, *target.Invariant)
		}
	}
	for _, target := range preparedTargets {
		if target.CurrentNode.Scheduling != nil && executableNodeKind(target.Node.Kind()) {
			intent, err := newCurrentNodeAutomaticIntent(target.CurrentNode.Reference, target.Node)
			if err != nil {
				return CurrentNodeCompletionResult{}, err
			}
			result.AutomaticIntents = append(result.AutomaticIntents, intent)
		}
	}
	return result, nil
}

func replaceCurrentNodeWithFanout(
	ctx context.Context,
	q *sqlitegen.Queries,
	source workflow.CurrentNodeReference,
	targets []currentNodeFanoutTarget,
) error {
	if source.IsBranchScoped() {
		return errors.New("nested fan-out current node completion is not supported")
	}
	created := make([]workflow.CurrentNode, 0, len(targets))
	for _, target := range targets {
		created = append(created, target.CurrentNode)
	}
	if err := validateFanoutTargets(source.TaskID, created); err != nil {
		return err
	}
	removed, err := deleteTaskCurrentNode(ctx, q, source)
	if err != nil {
		return err
	}
	if removed != 1 {
		return errors.New("fan-out source current node is no longer current")
	}
	return insertFrozenTaskFanoutTargets(ctx, q, source.TaskID, targets)
}

func validateFanoutTargets(taskID workflow.TaskID, targets []workflow.CurrentNode) error {
	if len(targets) < 2 {
		return errors.New("fan-out requires multiple target branches")
	}
	seenBranchKeys := make(map[workflow.TransitionBranchKey]struct{}, len(targets))
	for _, target := range targets {
		branchKey, branchScoped := target.Reference.TransitionBranchKey()
		if !branchScoped || strings.TrimSpace(string(branchKey)) == "" {
			return errors.New("fan-out target branch must be present")
		}
		if _, exists := seenBranchKeys[branchKey]; exists {
			return fmt.Errorf("fan-out transition branch key %q is duplicated", branchKey)
		}
		seenBranchKeys[branchKey] = struct{}{}
		if target.Reference.TaskID != taskID {
			return errors.New("fan-out target task must match its source")
		}
	}
	return nil
}

func insertTaskFanoutTargets(
	ctx context.Context,
	q *sqlitegen.Queries,
	taskID workflow.TaskID,
	targets []workflow.CurrentNode,
) error {
	if err := validateFanoutTargets(taskID, targets); err != nil {
		return err
	}
	if err := q.InsertTaskActiveFanout(ctx, string(taskID)); err != nil {
		return err
	}
	for _, target := range targets {
		branchKey, _ := target.Reference.TransitionBranchKey()
		sourceKind, sourceSessionID, legacyMaterialized, err := materializedContinuationSourceColumns(target.ContinuationSource)
		if err != nil {
			return err
		}
		if err := q.InsertTaskActiveFanoutBranch(ctx, sqlitegen.InsertTaskActiveFanoutBranchParams{
			TaskID:                      string(taskID),
			TransitionBranchKey:         string(branchKey),
			ContinuationSourceKind:      sourceKind,
			ContinuationSourceSessionID: sourceSessionID,
			LegacyMaterialized:          legacyMaterialized,
		}); err != nil {
			return err
		}
		if err := insertTaskCurrentNode(ctx, q, target); err != nil {
			return err
		}
	}
	return nil
}

func insertFrozenTaskFanoutTargets(
	ctx context.Context,
	q *sqlitegen.Queries,
	taskID workflow.TaskID,
	targets []currentNodeFanoutTarget,
) error {
	created := make([]workflow.CurrentNode, 0, len(targets))
	for _, target := range targets {
		created = append(created, target.CurrentNode)
	}
	if err := validateFanoutTargets(taskID, created); err != nil {
		return err
	}
	if err := q.InsertTaskActiveFanout(ctx, string(taskID)); err != nil {
		return err
	}
	for _, target := range targets {
		branchKey, _ := target.CurrentNode.Reference.TransitionBranchKey()
		sourceKind, sourceSessionID, legacyMaterialized, err := materializedContinuationSourceColumns(target.CurrentNode.ContinuationSource)
		if err != nil {
			return err
		}
		if err := q.InsertTaskActiveFanoutBranch(ctx, sqlitegen.InsertTaskActiveFanoutBranchParams{
			TaskID:                      string(taskID),
			TransitionBranchKey:         string(branchKey),
			ContinuationSourceKind:      sourceKind,
			ContinuationSourceSessionID: sourceSessionID,
			LegacyMaterialized:          legacyMaterialized,
		}); err != nil {
			return err
		}
		nodeKind, err := target.nodeKind()
		if err != nil {
			return err
		}
		if err := insertTaskCurrentNodeWithKind(ctx, q, target.CurrentNode, nodeKind); err != nil {
			return err
		}
	}
	return nil
}

func (target currentNodeFanoutTarget) nodeKind() (workflow.NodeKind, error) {
	if target.NodeKind != "" {
		return target.NodeKind, nil
	}
	if target.Node != nil {
		return target.Node.Kind(), nil
	}
	return "", errors.New("fan-out target node kind is required")
}
