package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

func (s *Store) CompleteCurrentNode(ctx context.Context, req CurrentNodeCompletionRequest) (CurrentNodeCompletionResult, error) {
	prepared, err := prepareCurrentNodeCompletionRequest(req)
	if err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	task, err := s.queries.GetTask(ctx, string(prepared.Source.TaskID))
	if err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	if task.CanceledAtUnixMs.Valid {
		return CurrentNodeCompletionResult{}, ErrTaskCanceled
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
	if req.TransitionID == "" {
		return CurrentNodeCompletionRequest{}, CompletionValidationError{Issues: []CompletionValidationIssue{{
			Code:    CompletionCodeTransitionIDRequired,
			Field:   "transition_id",
			Message: "transition id is required",
		}}}
	}
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

func currentNodeDefinitionNode(definition workflow.Definition, nodeID workflow.NodeID) (workflow.Node, error) {
	for _, node := range definition.Nodes {
		if workflow.NodeIDOf(node) == nodeID {
			return node, nil
		}
	}
	return nil, fmt.Errorf("current node %q is absent from workflow %q", nodeID, definition.ID)
}

type currentNodeCompletionTarget struct {
	Edge workflow.Edge
	Node workflow.Node
}

func currentNodeCompletionTransition(definition workflow.Definition, source workflow.Node, transitionID string) (workflow.TransitionGroup, []currentNodeCompletionTarget, error) {
	var selected *workflow.TransitionGroup
	for _, group := range definition.TransitionGroups {
		if group.SourceNodeID != workflow.NodeIDOf(source) || string(group.TransitionID) != transitionID {
			continue
		}
		candidate := group
		selected = &candidate
		break
	}
	if selected == nil {
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
	required := workflow.DeriveWiring(definition).CurrentNodeOutputFieldsForTransitionGroup(group.ID)
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
			return workflow.CurrentNode{}, fmt.Errorf("completion output %q required to materialize current input %q", binding.Field, binding.Name)
		}
		currentInputValues[binding.Name] = value
	}
	priorNodeValues := make(map[string]map[string]string)
	sourceKey := string(workflow.NodeKey(source))
	for _, requirement := range wiring.PriorNodeValueRequirementsForNode(workflow.NodeIDOf(target)) {
		nodeKey := string(requirement.NodeKey)
		var value string
		var exists bool
		if nodeKey == sourceKey {
			value, exists = outputValues[requirement.OutputName]
		} else {
			value, exists = currentSource.PriorNodeValues[nodeKey][requirement.OutputName]
		}
		if !exists {
			return workflow.CurrentNode{}, fmt.Errorf(
				"completion cannot materialize prior value %q from node %q for target %q",
				requirement.OutputName,
				nodeKey,
				workflow.NodeIDOf(target),
			)
		}
		if priorNodeValues[nodeKey] == nil {
			priorNodeValues[nodeKey] = make(map[string]string)
		}
		priorNodeValues[nodeKey][requirement.OutputName] = value
	}
	sessionID, err := completionTargetSession(ctx, q, definition, edge, currentSource)
	if err != nil {
		return workflow.CurrentNode{}, err
	}
	return completionTargetCurrentNode(
		currentSource.Reference.TaskID,
		target,
		transitionBranchKey,
		currentInputValues,
		priorNodeValues,
		sessionID,
	)
}

func completionTargetSession(
	ctx context.Context,
	q *sqlitegen.Queries,
	definition workflow.Definition,
	edge workflow.Edge,
	source workflow.CurrentNode,
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
			currentNodeReferenceBranchKey(source.Reference),
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
	priorNodeValues map[string]map[string]string,
	sessionID *runtimeids.SessionID,
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
	return workflow.NewCurrentNodeWithMaterializedValues(reference, currentInputValues, priorNodeValues, sessionID, scheduling)
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
