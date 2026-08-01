package workflowstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

type currentNodeFanoutTarget struct {
	BranchKey   workflow.TransitionBranchKey
	CurrentNode workflow.CurrentNode
	Node        workflow.Node
}

func completeCurrentNodeFanout(
	ctx context.Context,
	q *sqlitegen.Queries,
	definition workflow.Definition,
	source workflow.Node,
	currentSource workflow.CurrentNode,
	targets []currentNodeCompletionTarget,
	outputValues map[string]string,
	workflowVersion int64,
	group workflow.TransitionGroup,
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
		targetCurrentNode, err := materializeCompletionTargetCurrentNode(
			ctx,
			q,
			definition,
			target.Edge,
			source,
			target.Node,
			currentSource,
			outputValues,
			&branchKey,
		)
		if err != nil {
			return CurrentNodeCompletionResult{}, err
		}
		preparedTargets = append(preparedTargets, currentNodeFanoutTarget{
			BranchKey:   branchKey,
			CurrentNode: targetCurrentNode,
			Node:        target.Node,
		})
		approvalBranches = append(approvalBranches, workflow.PendingApprovalBranch{
			TransitionBranchKey: branchKey,
			Target: workflow.PendingApprovalTarget{
				CurrentNode: targetCurrentNode,
				DisplayName: workflow.NodeDisplayName(target.Node),
			},
			EffectiveEdge: target.Edge,
			ContextSourceResolution: workflow.PendingApprovalContextSourceResolution{
				SessionID: clonePendingApprovalSessionID(targetCurrentNode.SessionID),
			},
		})
		requiresApproval = requiresApproval || target.Edge.RequiresApproval
	}
	if requiresApproval {
		approval, err := newPendingApprovalWithBranches(
			currentSource,
			workflowVersion,
			group,
			workflow.NodeDisplayName(source),
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
		return CurrentNodeCompletionResult{PendingApproval: &approval}, nil
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
	if len(targets) < 2 {
		return errors.New("fan-out requires multiple target branches")
	}
	seenBranchKeys := make(map[workflow.TransitionBranchKey]struct{}, len(targets))
	for _, target := range targets {
		branchKey := workflow.TransitionBranchKey(strings.TrimSpace(string(target.BranchKey)))
		if branchKey == "" {
			return errors.New("fan-out transition branch key is required")
		}
		if _, exists := seenBranchKeys[branchKey]; exists {
			return fmt.Errorf("fan-out transition branch key %q is duplicated", branchKey)
		}
		seenBranchKeys[branchKey] = struct{}{}
		if target.CurrentNode.Reference.TaskID != source.TaskID {
			return errors.New("fan-out target task must match its source")
		}
		targetBranchKey, branchScoped := target.CurrentNode.Reference.TransitionBranchKey()
		if !branchScoped || targetBranchKey != branchKey {
			return errors.New("fan-out target branch must match its branch key")
		}
	}
	removed, err := deleteTaskCurrentNode(ctx, q, source)
	if err != nil {
		return err
	}
	if removed != 1 {
		return errors.New("fan-out source current node is no longer current")
	}
	if err := q.InsertTaskActiveFanout(ctx, string(source.TaskID)); err != nil {
		return err
	}
	for _, target := range targets {
		if err := q.InsertTaskActiveFanoutBranch(ctx, sqlitegen.InsertTaskActiveFanoutBranchParams{
			TaskID:              string(source.TaskID),
			TransitionBranchKey: string(target.BranchKey),
		}); err != nil {
			return err
		}
		if err := insertTaskCurrentNode(ctx, q, target.CurrentNode); err != nil {
			return err
		}
	}
	return nil
}
