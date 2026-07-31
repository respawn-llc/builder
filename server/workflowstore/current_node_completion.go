package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/shared/runtimeids"
)

type CurrentNodeCompletionRequest struct {
	Source       workflow.CurrentNodeReference
	TransitionID string
	OutputValues map[string]string
	Commentary   string
}

type CurrentNodeCompletionResult struct {
	Mutation         workflow.CurrentNodeMutationResult
	Handoff          CompletionHandoff
	AutomaticIntents []workflow.CurrentNodeReference
	PendingApproval  *workflow.PendingApproval
}

// ErrCurrentNodeCompletionSelectorAmbiguous means a completion selector
// matches several Current Nodes. Callers must choose a narrower Session or
// Task selector; Current Nodes are intentionally not external completion
// selectors.
var ErrCurrentNodeCompletionSelectorAmbiguous = errors.New("current node completion selector is ambiguous")

type IdleCurrentNodeSelector struct {
	TaskID    *workflow.TaskID
	SessionID *runtimeids.SessionID
}

// ResolveIdleExecutableCurrentNode selects exactly one current executable node
// for forced completion. It only considers ready or interrupted nodes with no
// pending Approval; admitted work is deliberately excluded because it may
// still be preparing a live Exact Execution Scope.
func (s *Store) ResolveIdleExecutableCurrentNode(ctx context.Context, selector IdleCurrentNodeSelector) (workflow.CurrentNode, error) {
	if (selector.TaskID == nil) == (selector.SessionID == nil) {
		return workflow.CurrentNode{}, errors.New("exactly one idle current node selector is required")
	}
	var taskID workflow.TaskID
	if selector.TaskID != nil {
		taskID = *selector.TaskID
		if strings.TrimSpace(string(taskID)) == "" {
			return workflow.CurrentNode{}, errors.New("task id is required")
		}
	} else {
		if selector.SessionID == nil || selector.SessionID.IsZero() {
			return workflow.CurrentNode{}, errors.New("session id is required")
		}
		taskIDs, err := s.queries.ListSessionWorkflowTaskIDs(ctx, selector.SessionID.String())
		if err != nil {
			return workflow.CurrentNode{}, err
		}
		if len(taskIDs) != 1 || !taskIDs[0].Valid || strings.TrimSpace(taskIDs[0].String) == "" {
			return workflow.CurrentNode{}, sql.ErrNoRows
		}
		taskID = workflow.TaskID(taskIDs[0].String)
	}
	task, err := s.queries.GetTask(ctx, string(taskID))
	if err != nil {
		return workflow.CurrentNode{}, err
	}
	definition, _, err := s.GetDefinition(ctx, workflow.WorkflowID(task.WorkflowID))
	if err != nil {
		return workflow.CurrentNode{}, err
	}
	currentNodes, err := s.ListCurrentNodes(ctx, taskID)
	if err != nil {
		return workflow.CurrentNode{}, err
	}
	matches := make([]workflow.CurrentNode, 0, len(currentNodes))
	for _, currentNode := range currentNodes {
		if selector.SessionID != nil && (currentNode.SessionID == nil || *currentNode.SessionID != *selector.SessionID) {
			continue
		}
		if currentNode.Scheduling == nil ||
			(currentNode.Scheduling.State != workflow.CurrentNodeSchedulingReady &&
				currentNode.Scheduling.State != workflow.CurrentNodeSchedulingInterrupted) {
			continue
		}
		node, err := currentNodeDefinitionNode(definition, currentNode.Reference.NodeID)
		if err != nil {
			return workflow.CurrentNode{}, err
		}
		if !executableNodeKind(node.Kind()) {
			continue
		}
		eligible, err := s.IsCurrentNodeExecutionEligible(ctx, currentNode.Reference)
		if err != nil {
			return workflow.CurrentNode{}, err
		}
		if eligible {
			matches = append(matches, currentNode)
		}
	}
	switch len(matches) {
	case 0:
		return workflow.CurrentNode{}, sql.ErrNoRows
	case 1:
		return matches[0], nil
	default:
		return workflow.CurrentNode{}, ErrCurrentNodeCompletionSelectorAmbiguous
	}
}

func (s *Store) CompleteCurrentNode(ctx context.Context, req CurrentNodeCompletionRequest) (CurrentNodeCompletionResult, error) {
	prepared, err := prepareCurrentNodeCompletionRequest(req)
	if err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	task, err := s.queries.GetTask(ctx, string(prepared.Source.TaskID))
	if err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	definition, workflowRecord, err := s.GetDefinition(ctx, workflow.WorkflowID(task.WorkflowID))
	if err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	if err := s.preflightInitialExecution(definition); err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	source, err := currentNodeDefinitionNode(definition, prepared.Source.NodeID)
	if err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	if !executableNodeKind(source.Kind()) {
		return CurrentNodeCompletionResult{}, errors.New("current node is not executable")
	}
	group, targets, err := currentNodeCompletionTransition(definition, source, prepared.TransitionID)
	if err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	if issues := currentNodeCompletionOutputIssues(definition, group, prepared.OutputValues); len(issues) > 0 {
		return CurrentNodeCompletionResult{}, CompletionValidationError{Issues: issues}
	}
	nowTime := s.now().UTC()
	now := nowTime.UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	currentSource, err := currentNodeForReference(ctx, q, prepared.Source)
	if err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	if _, pending, err := currentNodePendingApprovalID(ctx, q, currentSource.Reference); err != nil {
		return CurrentNodeCompletionResult{}, err
	} else if pending {
		return CurrentNodeCompletionResult{}, ErrCurrentNodePendingApproval
	}
	if len(targets) > 1 {
		result, err := completeCurrentNodeFanout(
			ctx,
			q,
			definition,
			source,
			currentSource,
			targets,
			prepared.OutputValues,
			workflowRecord.Version,
			group,
			nowTime,
		)
		if err != nil {
			return CurrentNodeCompletionResult{}, err
		}
		if err := touchTaskUpdatedAt(ctx, q, string(prepared.Source.TaskID), now); err != nil {
			return CurrentNodeCompletionResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return CurrentNodeCompletionResult{}, err
		}
		return result, nil
	}
	target := targets[0]
	if target.Node.Kind() == workflow.NodeKindJoin {
		result, err := completeCurrentNodeJoinArrival(
			ctx,
			q,
			definition,
			currentSource,
			target.Edge,
			prepared.OutputValues,
		)
		if err != nil {
			return CurrentNodeCompletionResult{}, err
		}
		if err := touchTaskUpdatedAt(ctx, q, string(prepared.Source.TaskID), now); err != nil {
			return CurrentNodeCompletionResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return CurrentNodeCompletionResult{}, err
		}
		return result, nil
	}
	targetCurrentNode, err := materializeCompletionTargetCurrentNode(
		ctx,
		q,
		definition,
		target.Edge,
		source,
		target.Node,
		currentSource,
		prepared.OutputValues,
		currentNodeReferenceBranchKey(currentSource.Reference),
	)
	if err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	if target.Edge.RequiresApproval {
		approval, err := newPendingApproval(
			currentSource,
			workflowRecord.Version,
			group,
			workflow.NodeDisplayName(source),
			target.Edge,
			target.Node,
			targetCurrentNode,
			prepared.OutputValues,
			nowTime,
		)
		if err != nil {
			return CurrentNodeCompletionResult{}, err
		}
		if err := insertPendingApproval(ctx, q, approval); err != nil {
			return CurrentNodeCompletionResult{}, err
		}
		if err := touchTaskUpdatedAt(ctx, q, string(prepared.Source.TaskID), now); err != nil {
			return CurrentNodeCompletionResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return CurrentNodeCompletionResult{}, err
		}
		return CurrentNodeCompletionResult{PendingApproval: &approval}, nil
	}
	handoff, err := currentNodeCompletionHandoff(source, target.Node)
	if err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	removed, err := deleteTaskCurrentNode(ctx, q, prepared.Source)
	if err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	if removed != 1 {
		return CurrentNodeCompletionResult{}, sql.ErrNoRows
	}
	if err := insertTaskCurrentNode(ctx, q, targetCurrentNode); err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	if err := touchTaskUpdatedAt(ctx, q, string(prepared.Source.TaskID), now); err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	result := CurrentNodeCompletionResult{
		Mutation: workflow.CurrentNodeMutationResult{
			Removed: []workflow.CurrentNodeReference{prepared.Source},
			Created: []workflow.CurrentNode{targetCurrentNode},
		},
		Handoff: handoff,
	}
	if executableNodeKind(target.Node.Kind()) {
		result.AutomaticIntents = []workflow.CurrentNodeReference{targetCurrentNode.Reference}
	}
	return result, nil
}

func prepareCurrentNodeCompletionRequest(req CurrentNodeCompletionRequest) (CurrentNodeCompletionRequest, error) {
	if err := req.Source.Validate(); err != nil {
		return CurrentNodeCompletionRequest{}, err
	}
	req.TransitionID = strings.TrimSpace(req.TransitionID)
	if len(req.Commentary) > workflow.MaxCommentaryBytes {
		return CurrentNodeCompletionRequest{}, CompletionValidationError{Issues: []CompletionValidationIssue{{
			Code:    CompletionCodeCommentaryTooLarge,
			Field:   "commentary",
			Message: "commentary is too large",
		}}}
	}
	req.Commentary = strings.TrimSpace(req.Commentary)
	req.OutputValues = cloneCurrentNodeOutputValues(req.OutputValues)
	issues := []CompletionValidationIssue{}
	for _, name := range sortedStringKeys(req.OutputValues) {
		value := req.OutputValues[name]
		if strings.TrimSpace(name) == "" {
			issues = append(issues, CompletionValidationIssue{Code: CompletionCodeOutputFieldRequired, Message: "output field name is required"})
		}
		if len(value) > workflow.MaxOutputValueBytes {
			issues = append(issues, CompletionValidationIssue{Code: CompletionCodeOutputTooLarge, Field: strings.TrimSpace(name), Message: "output field is too large"})
		}
	}
	if len(issues) > 0 {
		return CurrentNodeCompletionRequest{}, CompletionValidationError{Issues: issues}
	}
	return req, nil
}

func cloneCurrentNodeOutputValues(values map[string]string) map[string]string {
	if len(values) == 0 {
		return map[string]string{}
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func sortedStringKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func currentNodeDefinitionNode(definition workflow.Definition, nodeID workflow.NodeID) (workflow.Node, error) {
	for _, node := range definition.Nodes {
		if workflow.NodeIDOf(node) == nodeID {
			return node, nil
		}
	}
	return nil, fmt.Errorf("current node %q is absent from workflow %q", nodeID, definition.ID)
}

func currentNodeDefinitionEnteringEdge(
	definition workflow.Definition,
	currentNode workflow.CurrentNode,
) (workflow.Edge, error) {
	if currentNode.EnteredByEdgeID == nil {
		return workflow.Edge{}, fmt.Errorf("current workflow node %v has no entering edge", currentNode.Reference)
	}
	for _, edge := range definition.Edges {
		if edge.ID != *currentNode.EnteredByEdgeID {
			continue
		}
		if edge.TargetNodeID != currentNode.Reference.NodeID {
			return workflow.Edge{}, fmt.Errorf(
				"current workflow node %v entering edge %q targets node %q",
				currentNode.Reference,
				edge.ID,
				edge.TargetNodeID,
			)
		}
		return edge, nil
	}
	return workflow.Edge{}, fmt.Errorf(
		"current workflow node %v entering edge %q is absent from workflow %q",
		currentNode.Reference,
		*currentNode.EnteredByEdgeID,
		definition.ID,
	)
}

type currentNodeCompletionTarget struct {
	Edge workflow.Edge
	Node workflow.Node
}

func currentNodeCompletionTransition(definition workflow.Definition, source workflow.Node, transitionID string) (workflow.TransitionGroup, []currentNodeCompletionTarget, error) {
	var selected *workflow.TransitionGroup
	for _, group := range definition.TransitionGroups {
		if group.SourceNodeID != workflow.NodeIDOf(source) {
			continue
		}
		if transitionID == "" {
			if selected != nil {
				return workflow.TransitionGroup{}, nil, CompletionValidationError{Issues: []CompletionValidationIssue{{
					Code:    CompletionCodeTransitionIDRequired,
					Field:   "transition_id",
					Message: "transition id is required when current node has multiple transitions",
				}}}
			}
		} else if string(group.TransitionID) != transitionID {
			continue
		}
		candidate := group
		selected = &candidate
		if transitionID != "" {
			break
		}
	}
	if selected == nil {
		if transitionID == "" {
			return workflow.TransitionGroup{}, nil, CompletionValidationError{Issues: []CompletionValidationIssue{{
				Code:    CompletionCodeNoOutgoingTransition,
				Field:   "transition_id",
				Message: "current node has no outgoing transition",
			}}}
		}
		return workflow.TransitionGroup{}, nil, CompletionValidationError{Issues: []CompletionValidationIssue{{
			Code:    CompletionCodeInvalidTransitionID,
			Field:   "transition_id",
			Message: fmt.Sprintf("transition %q is not available for current node", transitionID),
		}}}
	}
	targets := make([]currentNodeCompletionTarget, 0, 1)
	for _, edge := range definition.Edges {
		if edge.TransitionGroupID != selected.ID {
			continue
		}
		target, err := currentNodeDefinitionNode(definition, edge.TargetNodeID)
		if err != nil {
			return workflow.TransitionGroup{}, nil, err
		}
		targets = append(targets, currentNodeCompletionTarget{Edge: edge, Node: target})
	}
	if len(targets) == 0 {
		return workflow.TransitionGroup{}, nil, errors.New("current node completion transition has no target edges")
	}
	return *selected, targets, nil
}

func currentNodeCompletionOutputIssues(definition workflow.Definition, group workflow.TransitionGroup, values map[string]string) []CompletionValidationIssue {
	wiring := workflow.DeriveWiring(definition)
	required := wiring.CurrentNodeOutputFieldsForTransitionGroup(group.ID)
	for _, edge := range definition.Edges {
		if edge.TransitionGroupID != group.ID {
			continue
		}
		target, err := currentNodeDefinitionNode(definition, edge.TargetNodeID)
		if err != nil || target.Kind() != workflow.NodeKindJoin {
			continue
		}
		for _, field := range wiring.RequiredProviderFieldsForJoinEdge(edge.ID) {
			if !completionOutputFieldPresent(required, field.Name) {
				required = append(required, field)
			}
		}
	}
	known := make(map[string]struct{}, len(required))
	for _, field := range required {
		name := strings.TrimSpace(field.Name)
		if name != "" {
			known[name] = struct{}{}
		}
	}
	issues := []CompletionValidationIssue{}
	for _, name := range sortedStringKeys(values) {
		field := strings.TrimSpace(name)
		if field == "" {
			continue
		}
		if _, exists := known[field]; !exists {
			issues = append(issues, CompletionValidationIssue{Code: CompletionCodeUnknownOutputField, Field: field, Message: "output field is not declared by source node"})
		}
	}
	for _, field := range required {
		name := strings.TrimSpace(field.Name)
		if name != "" && strings.TrimSpace(values[name]) == "" {
			issues = append(issues, CompletionValidationIssue{Code: CompletionCodeRequiredOutputMissing, Field: name, Message: "required output is missing"})
		}
	}
	return issues
}

func completionOutputFieldPresent(fields []workflow.OutputField, name string) bool {
	for _, field := range fields {
		if strings.TrimSpace(field.Name) == strings.TrimSpace(name) {
			return true
		}
	}
	return false
}

func materializeCompletionTargetCurrentNode(
	ctx context.Context,
	q *sqlitegen.Queries,
	definition workflow.Definition,
	edge workflow.Edge,
	source workflow.Node,
	target workflow.Node,
	currentSource workflow.CurrentNode,
	outputValues map[string]string,
	transitionBranchKey *workflow.TransitionBranchKey,
) (workflow.CurrentNode, error) {
	wiring := workflow.DeriveWiring(definition)
	currentInputValues := make(map[string]string)
	for _, binding := range wiring.CurrentNodeInputBindingsForEdge(edge.ID) {
		value, exists := outputValues[binding.Field]
		if !exists {
			return workflow.CurrentNode{}, CompletionValidationError{Issues: []CompletionValidationIssue{{
				Code:    CompletionCodeRequiredOutputMissing,
				Field:   strings.TrimSpace(binding.Field),
				Message: "required output is missing",
			}}}
		}
		currentInputValues[binding.Name] = value
	}
	priorValues := currentSource.PriorValues.Clone()
	sourceKey := workflow.NodeKey(source)
	var sourceTransitionKey workflow.ModelKey
	for _, group := range definition.TransitionGroups {
		if group.ID == edge.TransitionGroupID {
			sourceTransitionKey = workflow.ModelKey(group.TransitionID)
			break
		}
	}
	if sourceTransitionKey == "" {
		return workflow.CurrentNode{}, fmt.Errorf("transition group %q is absent", edge.TransitionGroupID)
	}
	for _, requirement := range wiring.PriorParameterRequirementsForNode(workflow.NodeIDOf(target)) {
		var value string
		var exists bool
		if requirement.ProviderNode == sourceKey && requirement.TransitionKey == sourceTransitionKey {
			value, exists = outputValues[requirement.ParameterName]
		} else {
			value, exists = currentSource.PriorValues.TransitionParameter(requirement.TransitionKey, requirement.ParameterName)
			if !exists {
				recoveredValue, recovered, recoveryErr := currentNodeEnteringTransitionParameterValue(
					definition,
					wiring,
					currentSource,
					requirement,
				)
				if recoveryErr != nil {
					return workflow.CurrentNode{}, recoveryErr
				}
				value, exists = recoveredValue, recovered
			}
		}
		if !exists {
			return workflow.CurrentNode{}, CompletionValidationError{Issues: []CompletionValidationIssue{{
				Code:    CompletionCodeRequiredOutputMissing,
				Field:   strings.TrimSpace(requirement.ParameterName),
				Message: "required prior Transition parameter is missing",
			}}}
		}
		priorValues.SetTransitionParameter(requirement.TransitionKey, requirement.ParameterName, value)
	}
	sessionID, err := completionTargetSession(ctx, q, definition, edge, currentSource, transitionBranchKey)
	if err != nil {
		return workflow.CurrentNode{}, err
	}
	return completionTargetCurrentNode(
		currentSource.Reference.TaskID,
		target,
		transitionBranchKey,
		currentInputValues,
		priorValues,
		sessionID,
		edge.ID,
	)
}

func currentNodeEnteringTransitionParameterValue(
	definition workflow.Definition,
	wiring workflow.DerivedWiring,
	currentNode workflow.CurrentNode,
	requirement workflow.PriorTransitionParameterRequirement,
) (string, bool, error) {
	if currentNode.EnteredByEdgeID == nil {
		return "", false, nil
	}
	enteringEdge, err := currentNodeDefinitionEnteringEdge(definition, currentNode)
	if err != nil {
		return "", false, err
	}
	var enteringGroup *workflow.TransitionGroup
	for index := range definition.TransitionGroups {
		if definition.TransitionGroups[index].ID == enteringEdge.TransitionGroupID {
			enteringGroup = &definition.TransitionGroups[index]
			break
		}
	}
	if enteringGroup == nil {
		return "", false, fmt.Errorf(
			"current node %v entering edge %q references absent transition group %q",
			currentNode.Reference,
			enteringEdge.ID,
			enteringEdge.TransitionGroupID,
		)
	}
	if workflow.ModelKey(enteringGroup.TransitionID) != requirement.TransitionKey {
		return "", false, nil
	}
	provider, err := currentNodeDefinitionNode(definition, enteringGroup.SourceNodeID)
	if err != nil {
		return "", false, fmt.Errorf(
			"resolve entering transition source for current node %v: %w",
			currentNode.Reference,
			err,
		)
	}
	if workflow.NodeKey(provider) != requirement.ProviderNode {
		return "", false, nil
	}
	for _, binding := range wiring.CurrentNodeInputBindingsForEdge(enteringEdge.ID) {
		if binding.Field != requirement.ParameterName {
			continue
		}
		value, exists := currentNode.CurrentInputValues[binding.Name]
		return value, exists, nil
	}
	return "", false, nil
}

func completionTargetSession(
	ctx context.Context,
	q *sqlitegen.Queries,
	definition workflow.Definition,
	edge workflow.Edge,
	source workflow.CurrentNode,
	targetBranchKey *workflow.TransitionBranchKey,
) (*runtimeids.SessionID, error) {
	if edge.ContextMode == workflow.ContextModeNewSession {
		return nil, nil
	}
	contextSource := workflow.CanonicalContextSource(edge.ContextSource)
	switch contextSource.Kind {
	case workflow.ContextSourceImmediateSource:
		if source.SessionID == nil {
			return nil, errors.New("immediate source continuation requires a source current node session")
		}
		sessionID := *source.SessionID
		return &sessionID, nil
	case workflow.ContextSourceSelectedNode:
		selected, err := currentNodeDefinitionNodeByKey(definition, contextSource.NodeKey)
		if err != nil {
			return nil, err
		}
		selectedReference, err := workflow.NewCurrentNodeReference(
			source.Reference.TaskID,
			workflow.NodeIDOf(selected),
			currentNodeReferenceBranchKey(source.Reference),
		)
		if err != nil {
			return nil, err
		}
		association, err := latestTaskSessionForNode(ctx, q, selectedReference)
		if err != nil {
			return nil, err
		}
		sessionID := association.SessionID
		return &sessionID, nil
	case workflow.ContextSourcePreviousTarget, workflow.ContextSourcePreviousTargetOrNew:
		targetReference, err := workflow.NewCurrentNodeReference(
			source.Reference.TaskID,
			edge.TargetNodeID,
			targetBranchKey,
		)
		if err != nil {
			return nil, err
		}
		association, err := latestTaskSessionForNode(ctx, q, targetReference)
		if err != nil {
			if contextSource.Kind == workflow.ContextSourcePreviousTargetOrNew && errors.Is(err, sql.ErrNoRows) {
				return nil, nil
			}
			return nil, err
		}
		sessionID := association.SessionID
		return &sessionID, nil
	default:
		return nil, fmt.Errorf("current node completion does not yet support context source %q", contextSource.Kind)
	}
}

func currentNodeDefinitionNodeByKey(definition workflow.Definition, key workflow.ModelKey) (workflow.Node, error) {
	for _, node := range definition.Nodes {
		if workflow.NodeKey(node) == key {
			return node, nil
		}
	}
	return nil, fmt.Errorf("context source node %q is absent from workflow %q", key, definition.ID)
}

func completionTargetCurrentNode(
	taskID workflow.TaskID,
	target workflow.Node,
	transitionBranchKey *workflow.TransitionBranchKey,
	currentInputValues map[string]string,
	priorValues workflow.MaterializedPriorValues,
	sessionID *runtimeids.SessionID,
	enteredByEdgeID workflow.EdgeID,
) (workflow.CurrentNode, error) {
	var scheduling *workflow.CurrentNodeScheduling
	switch target.Kind() {
	case workflow.NodeKindAgent, workflow.NodeKindScript:
		scheduling = &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady}
	case workflow.NodeKindTerminal:
		scheduling = nil
	default:
		return workflow.CurrentNode{}, fmt.Errorf("current node completion cannot target %s node", target.Kind())
	}
	reference, err := workflow.NewCurrentNodeReference(taskID, workflow.NodeIDOf(target), transitionBranchKey)
	if err != nil {
		return workflow.CurrentNode{}, err
	}
	return workflow.NewCurrentNodeWithEntry(reference, &enteredByEdgeID, currentInputValues, priorValues, sessionID, scheduling)
}

func currentNodeReferenceBranchKey(reference workflow.CurrentNodeReference) *workflow.TransitionBranchKey {
	branchKey, present := reference.TransitionBranchKey()
	if !present {
		return nil
	}
	return &branchKey
}

func currentNodeCompletionHandoff(source workflow.Node, target workflow.Node) (CompletionHandoff, error) {
	sourceDisplayName := strings.TrimSpace(workflow.NodeDisplayName(source))
	if sourceDisplayName == "" {
		return CompletionHandoff{}, fmt.Errorf("current node completion source %q has a blank display name", workflow.NodeIDOf(source))
	}
	targetDisplayName := strings.TrimSpace(workflow.NodeDisplayName(target))
	if targetDisplayName == "" {
		return CompletionHandoff{}, fmt.Errorf("current node completion target %q has a blank display name", workflow.NodeIDOf(target))
	}
	return CompletionHandoff{
		SourceNodeDisplayName:  sourceDisplayName,
		DestinationDisplayName: targetDisplayName,
	}, nil
}
