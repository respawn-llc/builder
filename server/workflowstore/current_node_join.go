package workflowstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

func completeCurrentNodeJoinArrival(
	ctx context.Context,
	q *sqlitegen.Queries,
	definition workflow.Definition,
	source workflow.CurrentNode,
	edge workflow.Edge,
	outputValues map[string]string,
) (CurrentNodeCompletionResult, error) {
	branchKey, branchScoped := source.Reference.TransitionBranchKey()
	if !branchScoped {
		return CurrentNodeCompletionResult{}, errors.New("join arrival requires a branch-scoped current node")
	}
	arrivalValues, err := joinArrivalValues(definition, edge, outputValues)
	if err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	arrivalValuesJSON, err := json.Marshal(arrivalValues)
	if err != nil {
		return CurrentNodeCompletionResult{}, fmt.Errorf("encode join arrival values: %w", err)
	}
	updated, err := q.UpdateTaskActiveFanoutBranchArrival(ctx, sqlitegen.UpdateTaskActiveFanoutBranchArrivalParams{
		TaskID:              string(source.Reference.TaskID),
		TransitionBranchKey: string(branchKey),
		ArrivalValuesJson:   sql.NullString{String: string(arrivalValuesJSON), Valid: true},
	})
	if err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	if updated != 1 {
		return CurrentNodeCompletionResult{}, errors.New("join arrival branch is not pending in the active fan-out")
	}
	arrivals, ready, err := currentFanoutJoinArrivals(ctx, q, source.Reference.TaskID)
	if err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	if !ready {
		removed, err := deleteTaskCurrentNode(ctx, q, source.Reference)
		if err != nil {
			return CurrentNodeCompletionResult{}, err
		}
		if removed != 1 {
			return CurrentNodeCompletionResult{}, errors.New("join arrival source current node is no longer current")
		}
		return CurrentNodeCompletionResult{
			Mutation: workflow.CurrentNodeMutationResult{
				Removed: []workflow.CurrentNodeReference{source.Reference},
			},
		}, nil
	}
	resolution, resolved := workflow.ResolveFanoutJoin(definition, currentFanoutBranchKeys(arrivals))
	if !resolved {
		return CurrentNodeCompletionResult{}, currentFanoutJoinTopologyError(definition, source.Reference.TaskID)
	}
	joinValues, err := aggregateCurrentFanoutJoinValues(definition, resolution, arrivals)
	if err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	target, err := currentFanoutJoinOutgoingTarget(definition, resolution.Join)
	if err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	joinSource, err := newNonExecutableCurrentNodeWithPriorValues(
		source.Reference.TaskID,
		workflow.NodeIDOf(resolution.Join),
		source.PriorValues,
	)
	if err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	targetCurrentNode, err := materializeCompletionTargetCurrentNode(
		ctx,
		q,
		definition,
		target.Edge,
		resolution.Join,
		target.Node,
		joinSource,
		joinValues,
		nil,
	)
	if err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	handoff, err := currentNodeCompletionHandoff(resolution.Join, target.Node)
	if err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	removed, err := deleteTaskCurrentNode(ctx, q, source.Reference)
	if err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	if removed != 1 {
		return CurrentNodeCompletionResult{}, errors.New("join arrival source current node is no longer current")
	}
	deleted, err := q.DeleteTaskActiveFanout(ctx, string(source.Reference.TaskID))
	if err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	if deleted != 1 {
		return CurrentNodeCompletionResult{}, errors.New("join arrival active fan-out is no longer current")
	}
	if err := insertTaskCurrentNode(ctx, q, targetCurrentNode); err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	result := CurrentNodeCompletionResult{
		Mutation: workflow.CurrentNodeMutationResult{
			Removed: []workflow.CurrentNodeReference{source.Reference},
			Created: []workflow.CurrentNode{targetCurrentNode},
		},
		Handoff: handoff,
	}
	if executableNodeKind(target.Node.Kind()) {
		result.AutomaticIntents = []workflow.CurrentNodeReference{targetCurrentNode.Reference}
	}
	return result, nil
}

type currentFanoutJoinArrival struct {
	BranchKey workflow.TransitionBranchKey
	Values    map[string]string
}

func currentFanoutJoinArrivals(ctx context.Context, q *sqlitegen.Queries, taskID workflow.TaskID) ([]currentFanoutJoinArrival, bool, error) {
	rows, err := q.ListTaskActiveFanoutBranches(ctx, string(taskID))
	if err != nil {
		return nil, false, err
	}
	if len(rows) < 2 {
		return nil, false, errors.New("active fan-out has fewer than two expected branches")
	}
	arrivals := make([]currentFanoutJoinArrival, 0, len(rows))
	for _, row := range rows {
		switch row.ArrivalState {
		case "pending":
			return nil, false, nil
		case "arrived":
			if !row.ArrivalValuesJson.Valid {
				return nil, false, errors.New("arrived fan-out branch has no materialized values")
			}
		default:
			return nil, false, fmt.Errorf("active fan-out branch has invalid arrival state %q", row.ArrivalState)
		}
		values := map[string]string{}
		if err := workflow.UnmarshalString(row.ArrivalValuesJson.String, &values); err != nil {
			return nil, false, fmt.Errorf("decode fan-out branch arrival values: %w", err)
		}
		arrivals = append(arrivals, currentFanoutJoinArrival{
			BranchKey: workflow.TransitionBranchKey(row.TransitionBranchKey),
			Values:    values,
		})
	}
	return arrivals, true, nil
}

func currentFanoutBranchKeys(arrivals []currentFanoutJoinArrival) []workflow.TransitionBranchKey {
	keys := make([]workflow.TransitionBranchKey, 0, len(arrivals))
	for _, arrival := range arrivals {
		keys = append(keys, arrival.BranchKey)
	}
	return keys
}

func aggregateCurrentFanoutJoinValues(
	definition workflow.Definition,
	resolution workflow.FanoutJoinResolution,
	arrivals []currentFanoutJoinArrival,
) (map[string]string, error) {
	arrivalsByBranch := make(map[workflow.TransitionBranchKey]map[string]string, len(arrivals))
	for _, arrival := range arrivals {
		arrivalsByBranch[arrival.BranchKey] = arrival.Values
	}
	branchByJoinEdge := make(map[workflow.EdgeID]workflow.TransitionBranchKey, len(resolution.BranchJoinEdges))
	for branchKey, edge := range resolution.BranchJoinEdges {
		branchByJoinEdge[edge.ID] = branchKey
	}
	derived := workflow.DeriveWiring(definition)
	// Incoming branch parameter contracts are the provider authority. A second
	// persisted provider map can drift from the executable Workflow wiring.
	providerByInput := make(map[string]workflow.EdgeID)
	for _, edge := range resolution.BranchJoinEdges {
		for _, field := range derived.RequiredProviderFieldsForJoinEdge(edge.ID) {
			inputName := strings.TrimSpace(field.Name)
			if inputName == "" {
				return nil, currentFanoutJoinTopologyError(definition, "")
			}
			if providerEdgeID, exists := providerByInput[inputName]; exists && providerEdgeID != edge.ID {
				return nil, currentFanoutJoinTopologyError(definition, "")
			}
			providerByInput[inputName] = edge.ID
		}
	}
	values := make(map[string]string)
	for _, field := range derived.JoinOutputFieldsForNode(workflow.NodeIDOf(resolution.Join)) {
		inputName := strings.TrimSpace(field.Name)
		providerEdgeID, exists := providerByInput[inputName]
		if !exists {
			return nil, currentFanoutJoinTopologyError(definition, "")
		}
		branchKey, exists := branchByJoinEdge[providerEdgeID]
		if !exists {
			return nil, currentFanoutJoinTopologyError(definition, "")
		}
		value, exists := arrivalsByBranch[branchKey][inputName]
		if !exists || strings.TrimSpace(value) == "" {
			return nil, CompletionValidationError{Issues: []CompletionValidationIssue{{
				Code:  CompletionCodeRequiredOutputMissing,
				Field: inputName,
			}}}
		}
		values[inputName] = value
	}
	return values, nil
}

func currentFanoutJoinOutgoingTarget(definition workflow.Definition, join workflow.Node) (currentNodeCompletionTarget, error) {
	groups := []workflow.TransitionGroup{}
	for _, group := range definition.TransitionGroups {
		if group.SourceNodeID == workflow.NodeIDOf(join) {
			groups = append(groups, group)
		}
	}
	if len(groups) != 1 {
		return currentNodeCompletionTarget{}, currentFanoutJoinTopologyError(definition, "")
	}
	targets := []currentNodeCompletionTarget{}
	for _, edge := range definition.Edges {
		if edge.TransitionGroupID != groups[0].ID {
			continue
		}
		target, err := currentNodeDefinitionNode(definition, edge.TargetNodeID)
		if err != nil {
			return currentNodeCompletionTarget{}, currentFanoutJoinTopologyError(definition, "")
		}
		targets = append(targets, currentNodeCompletionTarget{Edge: edge, Node: target})
	}
	if len(targets) != 1 {
		return currentNodeCompletionTarget{}, currentFanoutJoinTopologyError(definition, "")
	}
	return targets[0], nil
}

func currentFanoutJoinTopologyError(definition workflow.Definition, taskID workflow.TaskID) error {
	diagnostic := workflow.ValidationError{
		Code:       workflow.CodeInvalidFanoutJoinTopology,
		WorkflowID: definition.ID,
	}
	if strings.TrimSpace(string(taskID)) != "" {
		diagnostic.RelatedIDs = []string{string(taskID)}
	}
	return WorkflowValidationError{Diagnostics: []workflow.ValidationError{diagnostic}}
}

func joinArrivalValues(definition workflow.Definition, edge workflow.Edge, outputValues map[string]string) (map[string]string, error) {
	values := make(map[string]string)
	for _, field := range workflow.DeriveWiring(definition).RequiredProviderFieldsForJoinEdge(edge.ID) {
		value, exists := outputValues[field.Name]
		if !exists {
			return nil, fmt.Errorf("join arrival output %q is required", field.Name)
		}
		values[field.Name] = value
	}
	return values, nil
}
