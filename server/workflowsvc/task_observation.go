package workflowsvc

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"core/server/workflow"
	"core/shared/clientui"
	"core/shared/serverapi"
	"core/shared/textutil"
)

func (s *Service) ObserveWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskObservationRequest) (serverapi.WorkflowTaskObservationResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskObservationResponse{}, err
	}
	subscription, err := s.events.subscribe(req.ProjectID, nil)
	if err != nil {
		return serverapi.WorkflowTaskObservationResponse{}, err
	}
	defer func() { _ = subscription.Close() }()

	for {
		response, ready, err := s.observeWorkflowTaskSnapshot(ctx, req)
		if err != nil {
			return serverapi.WorkflowTaskObservationResponse{}, err
		}
		if ready {
			return response, nil
		}
		event, err := subscription.Next(ctx)
		if err != nil {
			return serverapi.WorkflowTaskObservationResponse{}, err
		}
		if !workflowTaskObservationEventMatches(event, req.TaskID) {
			continue
		}
	}
}

func (s *Service) observeWorkflowTaskSnapshot(ctx context.Context, req serverapi.WorkflowTaskObservationRequest) (serverapi.WorkflowTaskObservationResponse, bool, error) {
	detail, err := s.readModels.TaskDetail.GetTask(ctx, req.TaskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = serverapi.ErrWorkflowTaskNotFound
		}
		return serverapi.WorkflowTaskObservationResponse{}, false, err
	}
	if detail.Summary.ProjectID != req.ProjectID {
		return serverapi.WorkflowTaskObservationResponse{}, false, errors.New("workflow task does not belong to project")
	}
	response := serverapi.WorkflowTaskObservationResponse{
		Target: serverapi.NewRuntimeObservationTaskTarget(
			detail.Summary.ID,
			detail.Summary.ShortID,
			detail.Summary.ProjectID,
		),
	}
	approvalCache := make(map[string][]clientui.PendingApproval)
	if detail.Status.Kind == serverapi.WorkflowTaskStatusKindDone {
		response.Outcomes = []serverapi.RuntimeObservationOutcome{{
			Kind:     serverapi.RuntimeObservationOutcomeTaskDone,
			TaskDone: &serverapi.RuntimeObservationTaskDone{},
		}}
		return response, true, nil
	}
	nodeKeys := map[string]string{}
	scriptPaths := map[string]string{}
	if s.readModels.Definitions != nil {
		definition, _, err := s.readModels.Definitions.GetDefinition(ctx, detail.Summary.WorkflowID)
		if err != nil {
			return serverapi.WorkflowTaskObservationResponse{}, false, err
		}
		nodeKeys = make(map[string]string, len(definition.Nodes))
		for _, node := range definition.Nodes {
			nodeKeys[node.ID] = node.Key
			if node.ScriptPath != nil {
				scriptPaths[node.ID] = *node.ScriptPath
			}
		}
	}

	attention, err := s.readModels.Attention.ListTask(ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: req.TaskID})
	if err != nil {
		return serverapi.RuntimeObservationResponse{}, false, err
	}
	for _, item := range attention.Items {
		switch item.Kind {
		case string(serverapi.WorkflowTaskAttentionKindQuestion):
			if req.Mode != serverapi.WorkflowTaskObservationModeWatch {
				continue
			}
			outcome, ok, err := s.taskQuestionOutcome(ctx, item, nodeKeys, approvalCache)
			if err != nil {
				return serverapi.RuntimeObservationResponse{}, false, err
			}
			if ok {
				response.Outcomes = append(response.Outcomes, outcome)
			}
		case string(serverapi.WorkflowTaskAttentionKindInterrupted),
			string(serverapi.WorkflowTaskAttentionKindInterruptedCurrentNode):
			outcome, err := taskInterruptionOutcome(item, detail, nodeKeys, scriptPaths)
			if err != nil {
				return serverapi.RuntimeObservationResponse{}, false, err
			}
			if req.Mode == serverapi.WorkflowTaskObservationModeWatch ||
				outcome.Kind == serverapi.RuntimeObservationOutcomeExecutionError {
				response.Outcomes = append(response.Outcomes, outcome)
			}
		}
	}
	if reader, ok := s.readModels.TaskDetail.(interface {
		ListTaskCurrentNodes(context.Context, string) ([]workflow.CurrentNode, error)
	}); ok {
		currentNodes, err := reader.ListTaskCurrentNodes(ctx, req.TaskID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return serverapi.RuntimeObservationResponse{}, false, serverapi.ErrWorkflowTaskNotFound
			}
			return serverapi.RuntimeObservationResponse{}, false, err
		}
		for _, currentNode := range currentNodes {
			if currentNode.Scheduling == nil ||
				currentNode.Scheduling.State != workflow.CurrentNodeSchedulingInterrupted ||
				currentNode.Scheduling.Interruption == nil {
				continue
			}
			currentNodeDTO := observationCurrentNode(currentNode)
			if currentNodeHasAttentionProjection(currentNodeDTO, attention.Items) {
				continue
			}
			interruption := currentNode.Scheduling.Interruption
			reasonOverride := workflow.CurrentNodeInterruptionReason(interruption.Reason)
			outcome, err := taskInterruptionOutcomeFromDetail(
				serverapi.WorkflowAttentionItem{
					Kind:        string(serverapi.WorkflowTaskAttentionKindInterruptedCurrentNode),
					TaskID:      req.TaskID,
					CurrentNode: &currentNodeDTO,
				},
				detail,
				interruption.Detail,
				&reasonOverride,
				nodeKeys,
				scriptPaths,
			)
			if err != nil {
				return serverapi.RuntimeObservationResponse{}, false, err
			}
			if req.Mode == serverapi.WorkflowTaskObservationModeWatch ||
				outcome.Kind == serverapi.RuntimeObservationOutcomeExecutionError {
				response.Outcomes = append(response.Outcomes, outcome)
			}
		}
	}
	if len(response.Outcomes) > 0 {
		return response, true, nil
	}
	return response, false, nil
}

func observationCurrentNode(currentNode workflow.CurrentNode) serverapi.WorkflowTaskCurrentNode {
	projected := serverapi.WorkflowTaskCurrentNode{NodeID: string(currentNode.Reference.NodeID)}
	if branch, present := currentNode.Reference.TransitionBranchKey(); present {
		value := string(branch)
		projected.TransitionBranchKey = &value
	}
	if currentNode.SessionID != nil {
		value := currentNode.SessionID.String()
		projected.SessionID = &value
	}
	return projected
}

func currentNodeHasAttentionProjection(currentNode serverapi.WorkflowTaskCurrentNode, items []serverapi.WorkflowAttentionItem) bool {
	for _, item := range items {
		if item.CurrentNode == nil || item.CurrentNode.NodeID != currentNode.NodeID {
			continue
		}
		switch {
		case item.CurrentNode.TransitionBranchKey == nil && currentNode.TransitionBranchKey == nil:
			return true
		case item.CurrentNode.TransitionBranchKey != nil && currentNode.TransitionBranchKey != nil &&
			*item.CurrentNode.TransitionBranchKey == *currentNode.TransitionBranchKey:
			return true
		}
	}
	return false
}

func taskInterruptionOutcome(item serverapi.WorkflowAttentionItem, taskDetail serverapi.WorkflowTaskDetail, nodeKeys, scriptPaths map[string]string) (serverapi.RuntimeObservationOutcome, error) {
	interruptionDetail := struct {
		Code   string            `json:"Code"`
		Fields map[string]string `json:"Fields"`
	}{}
	detailJSON, _ := textutil.OptionalExact(item.DetailJSON)
	if err := workflow.UnmarshalString(detailJSON, &interruptionDetail); err != nil {
		return serverapi.RuntimeObservationOutcome{}, err
	}
	return taskInterruptionOutcomeFromDetail(
		item,
		taskDetail,
		workflow.CurrentNodeInterruptionDetail{
			Code:   interruptionDetail.Code,
			Fields: interruptionDetail.Fields,
		},
		nil,
		nodeKeys,
		scriptPaths,
	)
}

func taskInterruptionOutcomeFromDetail(
	item serverapi.WorkflowAttentionItem,
	taskDetail serverapi.WorkflowTaskDetail,
	interruptionDetail workflow.CurrentNodeInterruptionDetail,
	reasonOverride *workflow.CurrentNodeInterruptionReason,
	nodeKeys, scriptPaths map[string]string,
) (serverapi.RuntimeObservationOutcome, error) {
	reason := strings.TrimSpace(interruptionDetail.Code)
	if reasonOverride != nil && strings.TrimSpace(string(*reasonOverride)) != "" {
		reason = strings.TrimSpace(string(*reasonOverride))
	}
	if reason == "" {
		return serverapi.RuntimeObservationOutcome{}, errors.New("interruption detail has no reason")
	}
	diagnostic := ""
	if value := (workflow.CurrentNodeInterruptionDetail{
		Code:   interruptionDetail.Code,
		Fields: interruptionDetail.Fields,
	}).Diagnostic(); value != nil {
		diagnostic = *value
	}
	var diagnosticPtr *string
	if diagnostic != "" {
		diagnosticPtr = &diagnostic
	}
	var scriptPath *string
	var nodeKey *string
	if item.SessionID == nil && item.CurrentNode != nil {
		for _, script := range taskDetail.CurrentScripts {
			if script.CurrentNode.NodeID == item.CurrentNode.NodeID {
				path := script.Path
				scriptPath = &path
				break
			}
		}
		if scriptPath == nil {
			if path := strings.TrimSpace(scriptPaths[item.CurrentNode.NodeID]); path != "" {
				scriptPath = &path
			}
		}
	}
	if item.CurrentNode != nil {
		if key := strings.TrimSpace(nodeKeys[item.CurrentNode.NodeID]); key != "" {
			nodeKey = &key
		}
	}
	kind := serverapi.RuntimeObservationOutcomeExecutionError
	if reason == string(workflow.CurrentNodeInterruptionReasonUserInterrupt) ||
		reason == string(workflow.CurrentNodeInterruptionReasonRuntimeCanceled) {
		kind = serverapi.RuntimeObservationOutcomeInterrupted
	}
	if kind == serverapi.RuntimeObservationOutcomeInterrupted {
		return serverapi.RuntimeObservationOutcome{
			Kind:        kind,
			NodeKey:     nodeKey,
			SessionID:   item.SessionID,
			ScriptPath:  scriptPath,
			Interrupted: &serverapi.RuntimeObservationInterrupted{Reason: reason, Diagnostic: diagnosticPtr},
		}, nil
	}
	return serverapi.RuntimeObservationOutcome{
		Kind:           kind,
		NodeKey:        nodeKey,
		SessionID:      item.SessionID,
		ScriptPath:     scriptPath,
		ExecutionError: &serverapi.RuntimeObservationExecutionError{Reason: reason, Diagnostic: diagnosticPtr},
	}, nil
}

func (s *Service) taskQuestionOutcome(
	ctx context.Context,
	item serverapi.WorkflowAttentionItem,
	nodeKeys map[string]string,
	approvalCache map[string][]clientui.PendingApproval,
) (serverapi.RuntimeObservationOutcome, bool, error) {
	if item.Question == nil || item.QuestionID == nil {
		return serverapi.RuntimeObservationOutcome{}, false, nil
	}
	message, _ := textutil.OptionalExact(item.Message)
	question := &serverapi.RuntimeObservationQuestion{
		QuestionID: *item.QuestionID,
		Text:       strings.TrimSpace(message),
	}
	if question.Text == "" && item.Question.Kind == serverapi.WorkflowAttentionQuestionKindOrdinary {
		return serverapi.RuntimeObservationOutcome{}, false, nil
	}
	switch item.Question.Kind {
	case serverapi.WorkflowAttentionQuestionKindOrdinary:
		question.Kind = serverapi.RuntimeObservationQuestionOrdinary
		question.Suggestions = append([]string(nil), item.Question.Suggestions...)
		question.RecommendedOptionIndex = item.Question.RecommendedOptionIndex
	case serverapi.WorkflowAttentionQuestionKindApproval:
		if item.SessionID == nil || item.ApprovalID == nil || s.readModels.Approvals == nil {
			return serverapi.RuntimeObservationOutcome{}, false, nil
		}
		sessionID := *item.SessionID
		approvals, loaded := approvalCache[sessionID]
		if !loaded {
			response, err := s.readModels.Approvals.ListPendingApprovalsBySession(ctx, serverapi.ApprovalListPendingBySessionRequest{SessionID: sessionID})
			if err != nil {
				return serverapi.RuntimeObservationOutcome{}, false, err
			}
			approvals = append([]clientui.PendingApproval(nil), response.Approvals...)
			approvalCache[sessionID] = approvals
		}
		approval, ok := serverapi.FindPendingApproval(approvals, *item.ApprovalID)
		if !ok {
			return serverapi.RuntimeObservationOutcome{}, false, nil
		}
		question.Text = approval.Question
		question.Kind = serverapi.RuntimeObservationQuestionAccessRequest
		question.AccessOptions = append([]clientui.ApprovalOption(nil), approval.Options...)
	default:
		return serverapi.RuntimeObservationOutcome{}, false, nil
	}
	return serverapi.RuntimeObservationOutcome{
		Kind:      serverapi.RuntimeObservationOutcomeQuestion,
		NodeKey:   observationNodeKey(item.CurrentNode, nodeKeys),
		SessionID: item.SessionID,
		Question:  question,
	}, true, nil
}

func workflowTaskObservationEventMatches(event serverapi.WorkflowProjectEvent, taskID string) bool {
	if event.Resource != serverapi.WorkflowProjectEventResourceTask {
		return false
	}
	if event.PrimaryEntityID == taskID {
		return true
	}
	for _, relatedID := range event.RelatedIDs {
		if relatedID == taskID {
			return true
		}
	}
	return false
}

func observationNodeKey(node *serverapi.WorkflowTaskCurrentNode, nodeKeys map[string]string) *string {
	if node == nil {
		return nil
	}
	key := strings.TrimSpace(nodeKeys[node.NodeID])
	if key == "" {
		return nil
	}
	return &key
}
