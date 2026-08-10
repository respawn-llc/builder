package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/metadata/sqlitelifecyclegen"
	"core/server/session"
	"core/server/workflow"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type CurrentNodeCompletionRequest struct {
	Source       workflow.CurrentNodeReference
	TransitionID string
	OutputValues map[string]string
	Commentary   string
}

// CurrentNodeAutomaticIntent is a volatile successor start produced by a
// committed completion. The executable Node kind is captured from the same
// Workflow definition used to materialize the successor.
type CurrentNodeAutomaticIntent struct {
	CurrentNode workflow.CurrentNodeReference
	NodeKind    workflow.NodeKind
}

type CurrentNodeCompletionResult struct {
	Mutation               workflow.CurrentNodeMutationResult
	Handoff                CompletionHandoff
	AutomaticIntents       []CurrentNodeAutomaticIntent
	PendingApproval        *workflow.PendingApproval
	SessionReuse           *workflow.SessionReuseAnalysisInput
	PostCompletionEligible bool
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
	definition, _, err := s.GetDefinition(ctx, task.WorkflowID)
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
	prepared, err := s.PrepareCurrentNodeCompletion(ctx, req)
	if err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	result := prepared.Result()
	if err := prepared.Commit(); err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	if err := s.PublishCurrentNodeCompletion(ctx, req.Source.TaskID, result); err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	return result, nil
}

func (s *Store) PrepareCurrentNodeCompletion(
	ctx context.Context,
	req CurrentNodeCompletionRequest,
) (PreparedCurrentNodeCompletion, error) {
	prepared, err := prepareCurrentNodeCompletionRequest(req)
	if err != nil {
		return nil, err
	}
	task, err := s.queries.GetTask(ctx, string(prepared.Source.TaskID))
	if err != nil {
		return nil, err
	}
	definition, workflowRecord, err := s.GetDefinition(ctx, task.WorkflowID)
	if err != nil {
		return nil, err
	}
	if err := s.preflightInitialExecution(definition); err != nil {
		return nil, err
	}
	source, err := currentNodeDefinitionNode(definition, prepared.Source.NodeID)
	if err != nil {
		return nil, err
	}
	if !executableNodeKind(source.Kind()) {
		return nil, errors.New("current node is not executable")
	}
	group, targets, err := currentNodeCompletionTransition(definition, source, prepared.TransitionID)
	if err != nil {
		return nil, err
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	closeConnection := true
	defer func() {
		if closeConnection {
			_ = connection.Close()
		}
	}()
	lifecycle := sqlitelifecyclegen.New(connection)
	if err := lifecycle.SetBusyTimeout15Seconds(ctx); err != nil {
		return nil, err
	}
	restoreTimeout := true
	defer func() {
		if restoreTimeout {
			_ = lifecycle.SetBusyTimeout5Seconds(context.Background())
		}
	}()
	if err := lifecycle.BeginImmediate(ctx); err != nil {
		return nil, err
	}
	transactionOpen := true
	defer func() {
		if transactionOpen {
			_ = lifecycle.Rollback(context.Background())
		}
	}()
	nowTime := s.now().UTC()
	now := nowTime.UnixMilli()
	q := sqlitegen.New(connection)
	currentSource, err := currentNodeForReference(ctx, q, prepared.Source)
	if err != nil {
		return nil, err
	}
	if _, pending, err := currentNodePendingApprovalID(ctx, q, currentSource.Reference); err != nil {
		return nil, err
	} else if pending {
		return nil, ErrCurrentNodePendingApproval
	}
	issues, err := s.currentNodeCompletionOutputIssues(ctx, q, definition, group, source, targets, currentSource, prepared.OutputValues)
	if err != nil {
		return nil, err
	}
	if len(issues) > 0 {
		return nil, CompletionValidationError{Issues: issues}
	}
	var result CurrentNodeCompletionResult
	if len(targets) > 1 {
		result, err = completeCurrentNodeFanout(
			ctx,
			q,
			definition,
			source,
			currentSource,
			targets,
			prepared.Commentary,
			prepared.OutputValues,
			workflowRecord.Version,
			group,
			s.roleResolver,
			s.resolveRetainedSessionSelection,
			nowTime,
		)
		if err != nil {
			return nil, err
		}
		result.SessionReuse = newSessionReuseAnalysisInput(definition, currentSource, completionTargetEdges(targets))
		result.PostCompletionEligible = source.Kind() == workflow.NodeKindAgent
		if err := touchTaskUpdatedAt(ctx, q, string(prepared.Source.TaskID), now); err != nil {
			return nil, err
		}
		transactionOpen = false
		restoreTimeout = false
		closeConnection = false
		return newPreparedCurrentNodeCompletion(ctx, connection, lifecycle, result), nil
	}
	target := targets[0]
	if target.Node.Kind() == workflow.NodeKindJoin {
		result, err = completeCurrentNodeJoinArrival(
			ctx,
			q,
			definition,
			currentSource,
			target.Edge,
			prepared.OutputValues,
			s.roleResolver,
			s.resolveRetainedSessionSelection,
		)
		if err != nil {
			return nil, err
		}
		if err := touchTaskUpdatedAt(ctx, q, string(prepared.Source.TaskID), now); err != nil {
			return nil, err
		}
		result.SessionReuse = newSessionReuseAnalysisInput(definition, currentSource, []workflow.Edge{target.Edge})
		result.PostCompletionEligible = source.Kind() == workflow.NodeKindAgent
		transactionOpen = false
		restoreTimeout = false
		closeConnection = false
		return newPreparedCurrentNodeCompletion(ctx, connection, lifecycle, result), nil
	}
	targetCurrentNode, err := materializeCompletionTargetCurrentNode(
		ctx,
		q,
		definition,
		target.Edge,
		source,
		target.Node,
		s.roleResolver,
		s.resolveRetainedSessionSelection,
		currentSource,
		prepared.OutputValues,
		prepared.Commentary,
		currentNodeReferenceBranchKey(currentSource.Reference),
	)
	if err != nil {
		return nil, err
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
			prepared.Commentary,
			prepared.OutputValues,
			nowTime,
		)
		if err != nil {
			return nil, err
		}
		if err := insertPendingApproval(ctx, q, approval); err != nil {
			return nil, err
		}
		if err := touchTaskUpdatedAt(ctx, q, string(prepared.Source.TaskID), now); err != nil {
			return nil, err
		}
		result = CurrentNodeCompletionResult{
			PendingApproval:        &approval,
			SessionReuse:           newSessionReuseAnalysisInput(definition, currentSource, []workflow.Edge{target.Edge}),
			PostCompletionEligible: source.Kind() == workflow.NodeKindAgent,
		}
		transactionOpen = false
		restoreTimeout = false
		closeConnection = false
		return newPreparedCurrentNodeCompletion(ctx, connection, lifecycle, result), nil
	}
	handoff, err := currentNodeCompletionHandoff(source, target.Node)
	if err != nil {
		return nil, err
	}
	removed, err := deleteTaskCurrentNode(ctx, q, prepared.Source)
	if err != nil {
		return nil, err
	}
	if removed != 1 {
		return nil, sql.ErrNoRows
	}
	if err := insertTaskCurrentNode(ctx, q, targetCurrentNode); err != nil {
		return nil, err
	}
	if err := touchTaskUpdatedAt(ctx, q, string(prepared.Source.TaskID), now); err != nil {
		return nil, err
	}
	result = CurrentNodeCompletionResult{
		Mutation: workflow.CurrentNodeMutationResult{
			Removed: []workflow.CurrentNodeReference{prepared.Source},
			Created: []workflow.CurrentNode{targetCurrentNode},
		},
		Handoff:                handoff,
		SessionReuse:           newSessionReuseAnalysisInput(definition, currentSource, []workflow.Edge{target.Edge}),
		PostCompletionEligible: source.Kind() == workflow.NodeKindAgent,
	}
	if executableNodeKind(target.Node.Kind()) {
		intent, err := newCurrentNodeAutomaticIntent(targetCurrentNode.Reference, target.Node)
		if err != nil {
			return nil, err
		}
		result.AutomaticIntents = []CurrentNodeAutomaticIntent{intent}
	}
	transactionOpen = false
	restoreTimeout = false
	closeConnection = false
	return newPreparedCurrentNodeCompletion(ctx, connection, lifecycle, result), nil
}

func (s *Store) PublishCurrentNodeCompletion(
	ctx context.Context,
	taskID workflow.TaskID,
	result CurrentNodeCompletionResult,
) error {
	if len(result.Mutation.Removed) == 0 {
		return nil
	}
	return s.publishCurrentNodeTaskEvent(ctx, taskID, serverapi.WorkflowProjectEventActionCompleted)
}

func completionTargetEdges(targets []currentNodeCompletionTarget) []workflow.Edge {
	edges := make([]workflow.Edge, 0, len(targets))
	for _, target := range targets {
		edges = append(edges, target.Edge)
	}
	return edges
}

func newSessionReuseAnalysisInput(
	definition workflow.Definition,
	currentSource workflow.CurrentNode,
	acceptedBranches []workflow.Edge,
) *workflow.SessionReuseAnalysisInput {
	return &workflow.SessionReuseAnalysisInput{
		Workflow:             definition,
		AcceptedBranches:     append([]workflow.Edge(nil), acceptedBranches...),
		CompletedCurrentNode: currentSource,
	}
}

func prepareCurrentNodeCompletionRequest(req CurrentNodeCompletionRequest) (CurrentNodeCompletionRequest, error) {
	if err := req.Source.Validate(); err != nil {
		return CurrentNodeCompletionRequest{}, err
	}
	req.TransitionID = strings.TrimSpace(req.TransitionID)
	if err := validateCommentarySize(req.Commentary); err != nil {
		return CurrentNodeCompletionRequest{}, err
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

func validateCommentarySize(commentary string) error {
	if len(commentary) > workflow.MaxCommentaryBytes {
		return CompletionValidationError{Issues: []CompletionValidationIssue{{
			Code:    CompletionCodeCommentaryTooLarge,
			Field:   "commentary",
			Message: "commentary is too large",
		}}}
	}
	return nil
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

func newCurrentNodeAutomaticIntent(reference workflow.CurrentNodeReference, node workflow.Node) (CurrentNodeAutomaticIntent, error) {
	if err := reference.Validate(); err != nil {
		return CurrentNodeAutomaticIntent{}, err
	}
	if node == nil {
		return CurrentNodeAutomaticIntent{}, errors.New("automatic intent target node is required")
	}
	kind := node.Kind()
	if !executableNodeKind(kind) {
		return CurrentNodeAutomaticIntent{}, fmt.Errorf("automatic intent target node %q has non-executable kind %q", reference.NodeID, kind)
	}
	return CurrentNodeAutomaticIntent{CurrentNode: reference, NodeKind: kind}, nil
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

type transitionTargetValueResolver func(providerNode workflow.ModelKey, transitionKey workflow.ModelKey, outputName string) (string, bool)

type transitionTargetMaterializationRequest struct {
	Definition                      workflow.Definition
	Edge                            workflow.Edge
	Source                          workflow.Node
	Target                          workflow.Node
	Catalog                         workflow.TargetAgentCatalog
	ResolveRetainedSessionSelection func(context.Context, runtimeids.SessionID) (*workflow.AgentExecutionSelection, error)
	ContextTaskID                   workflow.TaskID
	ContextCurrentSource            *workflow.CurrentNode
	ManualMoveContext               bool
	PriorValues                     workflow.MaterializedPriorValues
	Value                           transitionTargetValueResolver
	TransitionBranchKey             *workflow.TransitionBranchKey
}

func materializeTransitionTargetCurrentNode(
	ctx context.Context,
	q *sqlitegen.Queries,
	request transitionTargetMaterializationRequest,
) (workflow.CurrentNode, error) {
	definition := request.Definition
	edge := request.Edge
	source := request.Source
	target := request.Target
	if strings.TrimSpace(string(request.ContextTaskID)) == "" {
		if request.ManualMoveContext {
			return workflow.CurrentNode{}, ErrManualMoveTransitionNotUsable
		}
		return workflow.CurrentNode{}, errors.New("current node completion requires task id")
	}
	transitionGroup, err := transitionGroupForEdge(definition, edge)
	if err != nil {
		return workflow.CurrentNode{}, err
	}
	sourceTransitionKey := workflow.ModelKey(transitionGroup.TransitionID)
	wiring := workflow.DeriveWiringWithCatalog(definition, request.Catalog)
	currentInputValues := make(map[string]string)
	for _, binding := range wiring.CurrentNodeInputBindingsForEdge(edge.ID) {
		providerNode, err := transitionTargetInputProviderNodeKey(definition, wiring, source, binding.Field)
		if err != nil {
			return workflow.CurrentNode{}, err
		}
		value, exists := request.Value(providerNode, sourceTransitionKey, binding.Field)
		if !exists {
			if parameter, protected := transitionParameterByKey(edge, binding.Field); protected &&
				workflow.CanonicalParameterPurpose(parameter.Purpose) != workflow.ParameterPurposeOrdinary {
				continue
			}
			return workflow.CurrentNode{}, CompletionValidationError{Issues: []CompletionValidationIssue{{
				Code:    CompletionCodeRequiredOutputMissing,
				Field:   strings.TrimSpace(binding.Field),
				Message: "required output is missing",
			}}}
		}
		currentInputValues[binding.Name] = value
	}
	priorValues := request.PriorValues.Clone()
	for _, requirement := range wiring.PriorParameterRequirementsForNode(workflow.NodeIDOf(target)) {
		value, exists := request.Value(requirement.ProviderNode, requirement.TransitionKey, requirement.ParameterName)
		if !exists {
			return workflow.CurrentNode{}, CompletionValidationError{Issues: []CompletionValidationIssue{{
				Code:    CompletionCodeRequiredOutputMissing,
				Field:   strings.TrimSpace(requirement.ParameterName),
				Message: "required prior Transition parameter is missing",
			}}}
		}
		priorValues.SetTransitionParameter(requirement.TransitionKey, requirement.ParameterName, value)
	}
	sessionID, err := resolveTransitionTargetSession(
		ctx,
		q,
		definition,
		edge,
		request.ContextTaskID,
		request.ContextCurrentSource,
		request.TransitionBranchKey,
		request.Source,
		request.ManualMoveContext,
	)
	if err != nil {
		return workflow.CurrentNode{}, err
	}
	var retainedSessionSelection *workflow.AgentExecutionSelection
	if sessionID != nil && request.ResolveRetainedSessionSelection != nil {
		policy, err := workflow.ResolveAssigneeSessionPolicy(workflow.AssigneeSessionPolicyRequest{
			ContextMode:           edge.ContextMode,
			ContextSource:         edge.ContextSource,
			TargetSessionResolved: true,
		})
		if err != nil {
			return workflow.CurrentNode{}, err
		}
		if policy == workflow.AssigneeSessionPolicyPreserve {
			retainedSessionSelection, err = request.ResolveRetainedSessionSelection(ctx, *sessionID)
			if err != nil {
				return workflow.CurrentNode{}, err
			}
		}
	}
	selection, err := materializeTargetAgentSelection(
		request,
		sourceTransitionKey,
		retainedSessionSelection,
	)
	if err != nil {
		return workflow.CurrentNode{}, err
	}
	for _, parameter := range edge.Parameters {
		if workflow.CanonicalParameterPurpose(parameter.Purpose) == workflow.ParameterPurposeOrdinary {
			continue
		}
		value, exists := request.Value(workflow.NodeKey(source), sourceTransitionKey, parameter.Key)
		if exists {
			priorValues.SetTransitionParameter(sourceTransitionKey, parameter.Key, value)
		}
	}
	return completionTargetCurrentNode(
		request.ContextTaskID,
		target,
		request.TransitionBranchKey,
		currentInputValues,
		priorValues,
		sessionID,
		edge.ID,
		selection,
	)
}

func materializeTargetAgentSelection(
	request transitionTargetMaterializationRequest,
	transitionKey workflow.ModelKey,
	retainedSessionSelection *workflow.AgentExecutionSelection,
) (*workflow.AgentExecutionSelection, error) {
	if request.Target.Kind() != workflow.NodeKindAgent {
		return nil, nil
	}
	var submittedRole string
	var submittedThinking string
	var thinkingDescription string
	for _, parameter := range request.Edge.Parameters {
		purpose := workflow.CanonicalParameterPurpose(parameter.Purpose)
		if purpose != workflow.ParameterPurposeTargetAssignee &&
			purpose != workflow.ParameterPurposeTargetThinking {
			continue
		}
		value, exists := request.Value(workflow.NodeKey(request.Source), transitionKey, parameter.Key)
		if exists {
			switch purpose {
			case workflow.ParameterPurposeTargetAssignee:
				submittedRole = value
			case workflow.ParameterPurposeTargetThinking:
				submittedThinking = value
			}
		}
		if purpose == workflow.ParameterPurposeTargetThinking {
			thinkingDescription = parameter.Description
		}
	}
	plan, err := workflow.PlanTransitionSelection(workflow.TransitionParameterContractRequest{
		Edge:       request.Edge,
		SourceKind: request.Source.Kind(),
		TargetKind: request.Target.Kind(),
		TargetRole: workflow.NodeSubagentRole(request.Target),
		Catalog:    request.Catalog,
		Materialization: &workflow.TransitionSelectionMaterializationRequest{
			FallbackRole:        workflow.NodeSubagentRole(request.Target),
			SubmittedRole:       submittedRole,
			SubmittedThinking:   submittedThinking,
			ThinkingDescription: thinkingDescription,
			RetainedSession:     retainedSessionSelection,
		},
	})
	if err != nil {
		var selectionErr workflow.TargetAgentSelectionError
		if errors.As(err, &selectionErr) {
			code := string(selectionErr.Code)
			if selectionErr.Code == workflow.TargetAgentSelectionErrorNoSelectableRoles {
				code = string(workflow.TargetAgentSelectionErrorUnavailableRole)
			}
			return nil, CompletionValidationError{Issues: []CompletionValidationIssue{{
				Code:    code,
				Message: selectionErr.Error(),
			}}}
		}
		return nil, err
	}
	if plan.ExecutionSelection == nil {
		return nil, errors.New("transition selection planner omitted Agent execution selection")
	}
	return plan.ExecutionSelection, nil
}

func transitionParameterByKey(edge workflow.Edge, key string) (workflow.Parameter, bool) {
	for _, parameter := range edge.Parameters {
		if parameter.Key == key {
			return parameter, true
		}
	}
	return workflow.Parameter{}, false
}

func materializeCompletionTargetCurrentNode(
	ctx context.Context,
	q *sqlitegen.Queries,
	definition workflow.Definition,
	edge workflow.Edge,
	source workflow.Node,
	target workflow.Node,
	catalog workflow.TargetAgentCatalog,
	resolveRetainedSessionSelection func(context.Context, runtimeids.SessionID) (*workflow.AgentExecutionSelection, error),
	currentSource workflow.CurrentNode,
	outputValues map[string]string,
	commentary string,
	transitionBranchKey *workflow.TransitionBranchKey,
) (workflow.CurrentNode, error) {
	wiring := workflow.DeriveWiringWithCatalog(definition, catalog)
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
	value := func(providerNode, transitionKey workflow.ModelKey, outputName string) (string, bool) {
		if transitionKey == sourceTransitionKey &&
			sourceTransitionKey != "" &&
			(providerNode == sourceKey || source.Kind() == workflow.NodeKindJoin) {
			if outputName == workflow.RuntimePromptParameterCommentary {
				return commentary, true
			}
			if resolved, exists := outputValues[outputName]; exists {
				return resolved, true
			}
		}
		if resolved, exists := currentSource.PriorValues.TransitionParameter(transitionKey, outputName); exists {
			return resolved, true
		}
		for _, requirement := range wiring.PriorParameterRequirementsForNode(workflow.NodeIDOf(target)) {
			if requirement.ProviderNode != providerNode ||
				requirement.TransitionKey != transitionKey ||
				requirement.ParameterName != outputName {
				continue
			}
			resolved, exists, err := currentNodeEnteringTransitionParameterValue(definition, wiring, currentSource, requirement)
			if err != nil {
				return "", false
			}
			if exists {
				return resolved, true
			}
		}
		return "", false
	}
	targetCurrentNode, err := materializeTransitionTargetCurrentNode(ctx, q, transitionTargetMaterializationRequest{
		Definition:                      definition,
		Edge:                            edge,
		Source:                          source,
		Target:                          target,
		Catalog:                         catalog,
		ResolveRetainedSessionSelection: resolveRetainedSessionSelection,
		ContextTaskID:                   currentSource.Reference.TaskID,
		ContextCurrentSource:            &currentSource,
		PriorValues:                     currentSource.PriorValues,
		Value:                           value,
		TransitionBranchKey:             transitionBranchKey,
	})
	if err != nil {
		return workflow.CurrentNode{}, err
	}
	if targetCurrentNode.CurrentInputValues == nil {
		targetCurrentNode.CurrentInputValues = make(map[string]string)
	}
	targetCurrentNode.CurrentInputValues[workflow.RuntimePromptParameterCommentary] = commentary
	return targetCurrentNode, nil
}

func (s *Store) resolveRetainedSessionSelection(ctx context.Context, sessionID runtimeids.SessionID) (*workflow.AgentExecutionSelection, error) {
	record, err := s.metadata.ResolvePersistedSession(ctx, sessionID.String())
	if err != nil {
		return nil, err
	}
	role := workflow.DefaultAgentRole
	if record.Meta != nil {
		if persisted := session.ContinuationAgentRole(*record.Meta); persisted != nil {
			role = *persisted
		}
	}
	selection, err := workflow.NewAgentExecutionSelection(role, nil, workflow.AssigneeOriginRetainedSession)
	if err != nil {
		return nil, fmt.Errorf("materialize retained Agent session selection: %w", err)
	}
	return &selection, nil
}

func transitionTargetInputProviderNodeKey(
	definition workflow.Definition,
	derived workflow.DerivedWiring,
	source workflow.Node,
	fieldName string,
) (workflow.ModelKey, error) {
	if source.Kind() != workflow.NodeKindJoin {
		return workflow.NodeKey(source), nil
	}
	for _, edge := range definition.Edges {
		if edge.TargetNodeID != workflow.NodeIDOf(source) {
			continue
		}
		for _, field := range derived.RequiredProviderFieldsForJoinEdge(edge.ID) {
			if strings.TrimSpace(field.Name) != strings.TrimSpace(fieldName) {
				continue
			}
			group, err := transitionGroupForEdge(definition, edge)
			if err != nil {
				return "", err
			}
			provider, err := currentNodeDefinitionNode(definition, group.SourceNodeID)
			if err != nil {
				return "", err
			}
			return workflow.NodeKey(provider), nil
		}
	}
	return "", fmt.Errorf("manual move Join output %q has no provider", fieldName)
}

func transitionGroupForEdge(definition workflow.Definition, edge workflow.Edge) (workflow.TransitionGroup, error) {
	for _, group := range definition.TransitionGroups {
		if group.ID == edge.TransitionGroupID {
			return group, nil
		}
	}
	return workflow.TransitionGroup{}, fmt.Errorf("transition group %q is absent", edge.TransitionGroupID)
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

func completionTargetCurrentNode(
	taskID workflow.TaskID,
	target workflow.Node,
	transitionBranchKey *workflow.TransitionBranchKey,
	currentInputValues map[string]string,
	priorValues workflow.MaterializedPriorValues,
	sessionID *runtimeids.SessionID,
	enteredByEdgeID workflow.EdgeID,
	selection *workflow.AgentExecutionSelection,
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
	currentNode, err := workflow.NewCurrentNodeWithExecutionSelection(
		reference,
		currentInputValues,
		priorValues,
		sessionID,
		scheduling,
		selection,
	)
	if err != nil {
		return workflow.CurrentNode{}, err
	}
	currentNode.EnteredByEdgeID = &enteredByEdgeID
	return currentNode, nil
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
