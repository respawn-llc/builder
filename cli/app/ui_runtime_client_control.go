package app

import (
	"context"
	"strings"

	"core/shared/clientui"
	"core/shared/serverapi"
)

func (c *sessionRuntimeClient) sessionRuntimeBoundary() {}

func runtimeRequestCallWithID[T any](ctx context.Context, c *sessionRuntimeClient, appendWarning bool, requestID string, call func(ctx context.Context, requestID string) (T, error)) (T, error) {
	return retryRuntimeUnavailableCall(ctx, c.recoverRuntimeConnectionWithWarning, appendWarning, func() (T, error) {
		return call(ctx, requestID)
	})
}

func runtimeRequestCallNoResultWithID(ctx context.Context, c *sessionRuntimeClient, requestID string, call func(ctx context.Context, requestID string) error) error {
	_, err := runtimeRequestCallWithID(ctx, c, true, requestID, func(ctx context.Context, requestID string) (struct{}, error) {
		return struct{}{}, call(ctx, requestID)
	})
	return err
}

func (c *sessionRuntimeClient) SetSessionName(name string) error {
	if err := runtimeControlCallNoResult(c, func(ctx context.Context, requestID string) error {
		return c.controls.SetSessionName(ctx, serverapi.RuntimeSetSessionNameRequest{ClientRequestID: requestID, SessionID: c.sessionID, Name: name})
	}); err != nil {
		return err
	}
	c.patchMainView(func(view *clientui.RuntimeMainView) {
		view.Session.SessionName = name
	})
	return nil
}

func (c *sessionRuntimeClient) SetThinkingLevel(level string) error {
	if err := runtimeControlCallNoResult(c, func(ctx context.Context, requestID string) error {
		return c.controls.SetThinkingLevel(ctx, serverapi.RuntimeSetThinkingLevelRequest{ClientRequestID: requestID, SessionID: c.sessionID, Level: level})
	}); err != nil {
		return err
	}
	c.patchMainView(func(view *clientui.RuntimeMainView) {
		view.Status.ThinkingLevel = level
	})
	return nil
}

func (c *sessionRuntimeClient) SetFastModeEnabled(enabled bool) (bool, error) {
	resp, err := runtimeControlCall(c, true, func(ctx context.Context, requestID string) (serverapi.RuntimeSetFastModeEnabledResponse, error) {
		return c.controls.SetFastModeEnabled(ctx, serverapi.RuntimeSetFastModeEnabledRequest{ClientRequestID: requestID, SessionID: c.sessionID, Enabled: enabled})
	})
	if err == nil {
		c.patchMainView(func(view *clientui.RuntimeMainView) {
			view.Status.FastModeEnabled = enabled
		})
	}
	return resp.Changed, err
}

func (c *sessionRuntimeClient) SetReviewerEnabled(enabled bool) (bool, string, error) {
	resp, err := runtimeControlCall(c, true, func(ctx context.Context, requestID string) (serverapi.RuntimeSetReviewerEnabledResponse, error) {
		return c.controls.SetReviewerEnabled(ctx, serverapi.RuntimeSetReviewerEnabledRequest{ClientRequestID: requestID, SessionID: c.sessionID, Enabled: enabled})
	})
	if err == nil {
		c.patchMainView(func(view *clientui.RuntimeMainView) {
			view.Status.ReviewerFrequency = resp.Mode
			view.Status.ReviewerEnabled = resp.Mode != "" && resp.Mode != "off"
		})
	}
	return resp.Changed, resp.Mode, err
}

func (c *sessionRuntimeClient) SetAutoCompactionEnabled(enabled bool) (bool, bool, error) {
	resp, err := runtimeControlCall(c, true, func(ctx context.Context, requestID string) (serverapi.RuntimeSetAutoCompactionEnabledResponse, error) {
		return c.controls.SetAutoCompactionEnabled(ctx, serverapi.RuntimeSetAutoCompactionEnabledRequest{ClientRequestID: requestID, SessionID: c.sessionID, Enabled: enabled})
	})
	if err != nil {
		return false, false, err
	}
	c.patchMainView(func(view *clientui.RuntimeMainView) {
		view.Status.AutoCompactionEnabled = resp.Enabled
	})
	return resp.Changed, resp.Enabled, nil
}

func (c *sessionRuntimeClient) SetQuestionsEnabled(enabled bool) (bool, error) {
	resp, err := runtimeControlCall(c, true, func(ctx context.Context, requestID string) (serverapi.RuntimeSetQuestionsEnabledResponse, error) {
		return c.controls.SetQuestionsEnabled(ctx, serverapi.RuntimeSetQuestionsEnabledRequest{ClientRequestID: requestID, SessionID: c.sessionID, Enabled: enabled})
	})
	if err != nil {
		return false, err
	}
	c.patchMainView(func(view *clientui.RuntimeMainView) {
		view.Status.QuestionsEnabled = resp.Enabled
	})
	return resp.Changed, nil
}

func (c *sessionRuntimeClient) ShowGoal() (*clientui.RuntimeGoal, error) {
	resp, err := runtimeControlCall(c, false, func(ctx context.Context, _ string) (serverapi.RuntimeGoalShowResponse, error) {
		return c.controls.ShowGoal(ctx, serverapi.RuntimeGoalShowRequest{SessionID: c.sessionID})
	})
	if err != nil {
		return nil, err
	}
	return c.applyGoalResponse(resp), nil
}

func (c *sessionRuntimeClient) SetGoal(objective string) (*clientui.RuntimeGoal, error) {
	resp, err := runtimeControlCall(c, true, func(ctx context.Context, requestID string) (serverapi.RuntimeGoalShowResponse, error) {
		return c.controls.SetGoal(ctx, serverapi.RuntimeGoalSetRequest{ClientRequestID: requestID, SessionID: c.sessionID, Objective: objective, Actor: "user"})
	})
	if err != nil {
		return nil, err
	}
	return c.applyGoalResponse(resp), nil
}

func (c *sessionRuntimeClient) PauseGoal() (*clientui.RuntimeGoal, error) {
	return c.setGoalStatus(func(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
		return c.controls.PauseGoal(ctx, req)
	})
}

func (c *sessionRuntimeClient) ResumeGoal() (*clientui.RuntimeGoal, error) {
	return c.setGoalStatus(func(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
		return c.controls.ResumeGoal(ctx, req)
	})
}

func (c *sessionRuntimeClient) CompleteGoal() (*clientui.RuntimeGoal, error) {
	return c.setGoalStatus(func(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
		return c.controls.CompleteGoal(ctx, req)
	})
}

func (c *sessionRuntimeClient) ClearGoal() (*clientui.RuntimeGoal, error) {
	resp, err := runtimeControlCall(c, true, func(ctx context.Context, requestID string) (serverapi.RuntimeGoalShowResponse, error) {
		return c.controls.ClearGoal(ctx, serverapi.RuntimeGoalClearRequest{ClientRequestID: requestID, SessionID: c.sessionID, Actor: "user"})
	})
	if err != nil {
		return nil, err
	}
	return c.applyGoalResponse(resp), nil
}

func (c *sessionRuntimeClient) setGoalStatus(call func(context.Context, serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error)) (*clientui.RuntimeGoal, error) {
	resp, err := runtimeControlCall(c, true, func(ctx context.Context, requestID string) (serverapi.RuntimeGoalShowResponse, error) {
		return call(ctx, serverapi.RuntimeGoalStatusRequest{ClientRequestID: requestID, SessionID: c.sessionID, Actor: "user"})
	})
	if err != nil {
		return nil, err
	}
	return c.applyGoalResponse(resp), nil
}

func (c *sessionRuntimeClient) applyGoalResponse(resp serverapi.RuntimeGoalShowResponse) *clientui.RuntimeGoal {
	goal := runtimeGoalFromAPI(resp.Goal)
	c.patchMainView(func(view *clientui.RuntimeMainView) {
		view.Status.Goal = cloneRuntimeGoal(goal)
	})
	return goal
}

func runtimeGoalFromAPI(goal *serverapi.RuntimeGoal) *clientui.RuntimeGoal {
	if goal == nil {
		return nil
	}
	return &clientui.RuntimeGoal{
		ID:        goal.ID,
		Objective: goal.Objective,
		Status:    clientui.RuntimeGoalStatus(strings.TrimSpace(goal.Status)),
		Suspended: goal.Suspended,
	}
}

func cloneRuntimeGoal(goal *clientui.RuntimeGoal) *clientui.RuntimeGoal {
	if goal == nil {
		return nil
	}
	cloned := *goal
	return &cloned
}

func (c *sessionRuntimeClient) AppendCommittedEntry(role, text string) error {
	return c.AppendCommittedEntryWithNoticeID(role, text, "")
}

func (c *sessionRuntimeClient) AppendCommittedEntryWithNoticeID(role, text, noticeID string) error {
	return runtimeControlCallNoResult(c, func(ctx context.Context, requestID string) error {
		return c.controls.AppendCommittedEntry(ctx, serverapi.RuntimeAppendCommittedEntryRequest{ClientRequestID: requestID, SessionID: c.sessionID, Role: role, Text: text, NoticeID: strings.TrimSpace(noticeID)})
	})
}

func (c *sessionRuntimeClient) SubmitRuntimeInput(ctx context.Context, req clientui.RuntimeSubmitRequest) (clientui.UserTurnSubmission, error) {
	if err := req.Validate(); err != nil {
		return clientui.UserTurnSubmission{}, err
	}
	resp, err := runtimeRequestCallWithID(ctx, c, true, req.OperationRef.ClientRequestID, func(ctx context.Context, id string) (serverapi.RuntimeSubmitUserTurnResponse, error) {
		return c.controls.SubmitUserTurn(ctx, serverapi.RuntimeSubmitUserTurnRequest{
			ClientRequestID:                 id,
			SessionID:                       c.sessionID,
			Text:                            req.Text,
			OperationRef:                    req.OperationRef,
			PreSubmitCompactionOperationRef: req.PreSubmitCompactionOperationRef,
		})
	})
	return userTurnSubmissionFromResponse(resp, req.Text, req.OperationRef.ClientRequestID), err
}

func userTurnSubmissionFromResponse(resp serverapi.RuntimeSubmitUserTurnResponse, text string, requestID string) clientui.UserTurnSubmission {
	submission := clientui.UserTurnSubmission{Message: resp.Message}
	if resp.Steered && strings.TrimSpace(resp.QueueItemID) != "" {
		submission.Queued = clientui.QueuedUserMessage{ID: resp.QueueItemID, Text: text, ClientRequestID: requestID}
	}
	return submission
}

func (c *sessionRuntimeClient) RunUserShell(ctx context.Context, req clientui.RuntimeShellRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	return runtimeRequestCallNoResultWithID(ctx, c, req.OperationRef.ClientRequestID, func(ctx context.Context, requestID string) error {
		return c.controls.SubmitUserShellCommand(ctx, serverapi.RuntimeSubmitUserShellCommandRequest{ClientRequestID: requestID, SessionID: c.sessionID, Command: req.Command, OperationRef: req.OperationRef})
	})
}

func (c *sessionRuntimeClient) CompactRuntime(ctx context.Context, req clientui.RuntimeCompactRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	return runtimeRequestCallNoResultWithID(ctx, c, req.OperationRef.ClientRequestID, func(ctx context.Context, requestID string) error {
		return c.controls.CompactContext(ctx, serverapi.RuntimeCompactContextRequest{ClientRequestID: requestID, SessionID: c.sessionID, Args: req.Args, OperationRef: req.OperationRef})
	})
}

func (c *sessionRuntimeClient) HasQueuedUserWork() (bool, error) {
	resp, err := runtimeControlCall(c, false, func(ctx context.Context, _ string) (serverapi.RuntimeHasQueuedUserWorkResponse, error) {
		return c.controls.HasQueuedUserWork(ctx, serverapi.RuntimeHasQueuedUserWorkRequest{SessionID: c.sessionID})
	})
	if err != nil {
		return false, err
	}
	return resp.HasQueuedUserWork, nil
}

func (c *sessionRuntimeClient) SubmitRuntimeQueued(ctx context.Context, req clientui.RuntimeSubmitQueuedRequest) (string, error) {
	if err := req.Validate(); err != nil {
		return "", err
	}
	resp, err := runtimeRequestCallWithID(ctx, c, true, req.OperationRef.ClientRequestID, func(ctx context.Context, requestID string) (serverapi.RuntimeSubmitQueuedUserMessagesResponse, error) {
		return c.controls.SubmitQueuedUserMessages(ctx, serverapi.RuntimeSubmitQueuedUserMessagesRequest{ClientRequestID: requestID, SessionID: c.sessionID, OperationRef: req.OperationRef})
	})
	return resp.Message, err
}

func (c *sessionRuntimeClient) Interrupt() error {
	return c.InterruptWithPendingRefs(nil)
}

func (c *sessionRuntimeClient) InterruptWithPendingRefs(refs []clientui.RuntimeOperationRef) error {
	return c.interruptWithTarget(nil, refs)
}

func (c *sessionRuntimeClient) InterruptWithTarget(target clientui.RuntimeOperationRef, refs []clientui.RuntimeOperationRef) error {
	if err := target.Validate(); err != nil {
		return err
	}
	return c.interruptWithTarget(&target, refs)
}

func (c *sessionRuntimeClient) interruptWithTarget(target *clientui.RuntimeOperationRef, refs []clientui.RuntimeOperationRef) error {
	resp, err := runtimeControlCall(c, true, func(ctx context.Context, requestID string) (serverapi.RuntimeInterruptResponse, error) {
		return c.controls.Interrupt(ctx, serverapi.RuntimeInterruptRequest{ClientRequestID: requestID, SessionID: c.sessionID, TargetOperationRef: target, PendingOperationRefs: refs})
	})
	if err != nil {
		return err
	}
	c.patchVersionedRuntimeActivity(runtimeActivitySnapshotPatch{
		Version:             resp.Version,
		Activity:            resp.Activity,
		InputReconciliation: resp.InputReconciliation,
	})
	return nil
}

func (c *sessionRuntimeClient) QueueRuntimeUserMessage(req clientui.RuntimeQueueUserMessageRequest) (clientui.QueuedUserMessage, error) {
	if err := req.Validate(); err != nil {
		return clientui.QueuedUserMessage{}, err
	}
	requestID := strings.TrimSpace(req.OperationRef.ClientRequestID)
	resp, err := runtimeControlCall(c, true, func(ctx context.Context, _ string) (serverapi.RuntimeQueueUserMessageResponse, error) {
		return c.controls.QueueUserMessage(ctx, serverapi.RuntimeQueueUserMessageRequest{ClientRequestID: requestID, SessionID: c.sessionID, OperationRef: req.OperationRef, Text: req.Text})
	})
	if err != nil {
		c.notifyConnectionState(err)
		return clientui.QueuedUserMessage{}, err
	}
	responseClientRequestID := strings.TrimSpace(resp.ClientRequestID)
	if responseClientRequestID == "" {
		responseClientRequestID = requestID
	}
	return clientui.QueuedUserMessage{ID: resp.QueueItemID, Text: resp.Text, ClientRequestID: responseClientRequestID}, nil
}

func (c *sessionRuntimeClient) DiscardQueuedUserMessage(queueItemID string) bool {
	resp, err := runtimeControlCall(c, true, func(ctx context.Context, requestID string) (serverapi.RuntimeDiscardQueuedUserMessageResponse, error) {
		return c.controls.DiscardQueuedUserMessage(ctx, serverapi.RuntimeDiscardQueuedUserMessageRequest{ClientRequestID: requestID, SessionID: c.sessionID, QueueItemID: queueItemID})
	})
	if err != nil {
		return false
	}
	return resp.Discarded
}

func (c *sessionRuntimeClient) RecordPromptHistory(text string) error {
	return runtimeControlCallNoResult(c, func(ctx context.Context, requestID string) error {
		return c.controls.RecordPromptHistory(ctx, serverapi.RuntimeRecordPromptHistoryRequest{ClientRequestID: requestID, SessionID: c.sessionID, Text: text})
	})
}
