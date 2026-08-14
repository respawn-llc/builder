package workflowsvc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"core/server/workflow"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/serverapi"
	"core/shared/textutil"
)

func (s *Service) ObserveWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskObservationRequest) (serverapi.WorkflowTaskObservationResponse, error) {
	if err := apicontract.ClassifyRequestValidation(req.Validate()); err != nil {
		return serverapi.WorkflowTaskObservationResponse{}, err
	}
	sub, err := s.events.subscribe(req.ProjectID, nil)
	if err != nil {
		return serverapi.WorkflowTaskObservationResponse{}, err
	}
	defer func() { _ = sub.Close() }()

	for {
		response, ready, err := s.observeWorkflowTask(ctx, req)
		if err != nil || ready {
			return response, err
		}
		for {
			event, err := sub.Next(ctx)
			if err != nil {
				return serverapi.WorkflowTaskObservationResponse{}, normalizeTaskObservationError(err)
			}
			if event.Resource == serverapi.WorkflowProjectEventResourceTask &&
				(event.PrimaryEntityID == req.TaskID || slices.Contains(event.RelatedIDs, req.TaskID)) {
				break
			}
		}
	}
}

func normalizeTaskObservationError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return serverapi.ErrWorkflowTaskNotFound
	}
	if errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: task observation event stream closed: %v", serverapi.ErrStreamFailed, err)
	}
	return serverapi.NormalizeStreamError(err)
}

func (s *Service) observeWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskObservationRequest) (serverapi.WorkflowTaskObservationResponse, bool, error) {
	detail, err := s.readModels.TaskDetail.GetTask(ctx, req.TaskID)
	if err != nil {
		return serverapi.WorkflowTaskObservationResponse{}, false, normalizeTaskObservationError(err)
	}
	if detail.Summary.ProjectID != req.ProjectID {
		return serverapi.WorkflowTaskObservationResponse{}, false, errors.New("workflow task does not belong to project")
	}
	response := serverapi.WorkflowTaskObservationResponse{TaskID: detail.Summary.ID, TaskShortID: detail.Summary.ShortID}
	if detail.Status.Kind == serverapi.WorkflowTaskStatusKindDone {
		response.Outcomes = []serverapi.WorkflowTaskObservationOutcome{{Kind: serverapi.WorkflowTaskObservationDone}}
		return response, true, nil
	}

	definition, _, err := s.readModels.Definitions.GetDefinition(ctx, detail.Summary.WorkflowID)
	if err != nil {
		return serverapi.WorkflowTaskObservationResponse{}, false, err
	}
	nodeKeys := make(map[string]string, len(definition.Nodes))
	nodes := make(map[string]serverapi.WorkflowNode, len(definition.Nodes))
	for _, node := range definition.Nodes {
		nodeKeys[node.ID] = node.Key
		nodes[node.ID] = node
	}
	currentNodes, err := s.readModels.TaskDetail.ListCurrentNodes(ctx, req.TaskID)
	if err != nil {
		return serverapi.WorkflowTaskObservationResponse{}, false, normalizeTaskObservationError(err)
	}
	for _, currentNode := range currentNodes {
		if currentNode.Scheduling == nil || currentNode.Scheduling.Interruption == nil {
			continue
		}
		outcome, err := taskCurrentNodeFailure(currentNode, nodes, nodeKeys)
		if err != nil {
			return serverapi.WorkflowTaskObservationResponse{}, false, err
		}
		if req.Mode == serverapi.WorkflowTaskObservationWatch ||
			outcome.Kind == serverapi.WorkflowTaskObservationExecutionError ||
			outcome.Kind == serverapi.WorkflowTaskObservationInterrupted {
			response.Outcomes = append(response.Outcomes, outcome)
		}
	}

	attention, err := s.readModels.Attention.ListTaskByID(ctx, req.TaskID)
	if err != nil {
		return serverapi.WorkflowTaskObservationResponse{}, false, normalizeTaskObservationError(err)
	}
	if req.Mode == serverapi.WorkflowTaskObservationWatch {
		approvalCache := make(map[string][]clientui.PendingApproval)
		for _, item := range attention.Items {
			if item.Kind != string(serverapi.WorkflowTaskAttentionKindQuestion) {
				continue
			}
			outcome, ok, err := s.taskQuestion(ctx, item, nodeKeys, approvalCache)
			if err != nil {
				return serverapi.WorkflowTaskObservationResponse{}, false, err
			}
			if ok {
				response.Outcomes = append(response.Outcomes, outcome)
			}
		}
	}
	return response, len(response.Outcomes) > 0, nil
}

func (s *Service) taskQuestion(
	ctx context.Context,
	item serverapi.WorkflowAttentionItem,
	keys map[string]string,
	cache map[string][]clientui.PendingApproval,
) (serverapi.WorkflowTaskObservationOutcome, bool, error) {
	if item.Question == nil {
		return serverapi.WorkflowTaskObservationOutcome{}, false, nil
	}
	if err := item.Question.Validate(); err != nil {
		return serverapi.WorkflowTaskObservationOutcome{}, false, err
	}
	sessionID := item.Question.SessionID.String()
	if strings.TrimSpace(sessionID) == "" {
		return serverapi.WorkflowTaskObservationOutcome{}, false, nil
	}
	questionID := string(item.Question.PromptID)
	if strings.TrimSpace(questionID) == "" {
		return serverapi.WorkflowTaskObservationOutcome{}, false, nil
	}

	var question serverapi.ObservationQuestion
	questionKind := item.Question.Kind
	switch questionKind {
	case serverapi.WorkflowAttentionQuestionKindOrdinary:
		text, _ := textutil.OptionalExact(item.Message)
		if strings.TrimSpace(text) == "" {
			return serverapi.WorkflowTaskObservationOutcome{}, false, nil
		}
		ask := clientui.PendingAsk{
			PromptID:               item.Question.PromptID,
			SessionID:              item.Question.SessionID,
			StepID:                 item.Question.StepID,
			Question:               text,
			Suggestions:            append([]string(nil), item.Question.Suggestions...),
			RecommendedOptionIndex: item.Question.RecommendedOptionIndex,
			CreatedAt:              time.UnixMilli(item.OccurredAtUnixMs).UTC(),
		}
		question.Ask = &ask
	case serverapi.WorkflowAttentionQuestionKindApproval:
		approvals, ok := cache[sessionID]
		if !ok {
			list, err := s.readModels.Approvals.ListPendingApprovalsBySession(ctx, serverapi.ApprovalListPendingBySessionRequest{SessionID: sessionID})
			if err != nil {
				return serverapi.WorkflowTaskObservationOutcome{}, false, err
			}
			approvals = append([]clientui.PendingApproval(nil), list.Approvals...)
			cache[sessionID] = approvals
		}
		var approval *clientui.PendingApproval
		for index := range approvals {
			candidate := &approvals[index]
			if candidate.PromptID == item.Question.PromptID &&
				candidate.SessionID == item.Question.SessionID &&
				candidate.StepID == item.Question.StepID {
				approval = candidate
				break
			}
		}
		if approval == nil {
			return serverapi.WorkflowTaskObservationOutcome{}, false, nil
		}
		question.Approval = approval
	default:
		return serverapi.WorkflowTaskObservationOutcome{}, false, nil
	}
	outcomeSessionID := item.Question.SessionID.String()
	return serverapi.WorkflowTaskObservationOutcome{
		Kind:      serverapi.WorkflowTaskObservationQuestion,
		SessionID: &outcomeSessionID,
		NodeKey:   nodeKey(item.CurrentNode, keys),
		Question:  &question,
	}, true, nil
}

func taskCurrentNodeFailure(
	currentNode workflow.CurrentNode,
	nodes map[string]serverapi.WorkflowNode,
	keys map[string]string,
) (serverapi.WorkflowTaskObservationOutcome, error) {
	interruption := currentNode.Scheduling.Interruption
	if interruption == nil {
		return serverapi.WorkflowTaskObservationOutcome{}, errors.New("current node interruption is required")
	}
	reason := strings.TrimSpace(string(interruption.Reason))
	if reason == "" {
		return serverapi.WorkflowTaskObservationOutcome{}, errors.New("task interruption reason is required")
	}
	failure := &serverapi.RuntimeLiveWatchFailure{Reason: strings.TrimSpace(interruption.Detail.Code)}
	if failure.Reason == "" {
		failure.Reason = reason
	}
	failure.Diagnostic = interruption.Detail.Diagnostic()
	kind := serverapi.WorkflowTaskObservationExecutionError
	if interruption.Reason == workflow.CurrentNodeInterruptionReasonUserInterrupt ||
		interruption.Reason == workflow.CurrentNodeInterruptionReasonRuntimeCanceled {
		kind = serverapi.WorkflowTaskObservationInterrupted
	}
	var sessionID *string
	if currentNode.SessionID != nil {
		value := currentNode.SessionID.String()
		sessionID = &value
	}
	var scriptPath *string
	if node, ok := nodes[string(currentNode.Reference.NodeID)]; ok && node.ScriptPath != nil {
		value := *node.ScriptPath
		if strings.TrimSpace(value) != "" {
			scriptPath = &value
			sessionID = nil
		}
	}
	return serverapi.WorkflowTaskObservationOutcome{
		Kind:       kind,
		SessionID:  sessionID,
		ScriptPath: scriptPath,
		NodeKey:    nodeKey(&serverapi.WorkflowTaskCurrentNode{NodeID: string(currentNode.Reference.NodeID)}, keys),
		Failure:    failure,
	}, nil
}

func nodeKey(node *serverapi.WorkflowTaskCurrentNode, keys map[string]string) *string {
	if node == nil {
		return nil
	}
	key := strings.TrimSpace(keys[node.NodeID])
	if key == "" {
		return nil
	}
	return &key
}
