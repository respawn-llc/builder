package app

import (
	"context"
	"strings"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

func (c *sessionRuntimeClient) sessionRuntimeBoundary() {}

func (c *sessionRuntimeClient) ReadChatSettings() (serverapi.ChatSettings, error) {
	sessionID, err := runtimeids.ParseSessionID(strings.TrimSpace(c.sessionID))
	if err != nil {
		return serverapi.ChatSettings{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), uiRuntimeControlTimeout)
	defer cancel()
	response, err := c.chatSettings.ReadChatSettings(ctx, serverapi.ChatSettingsReadRequest{
		Target: serverapi.SessionChatSettingsTarget(sessionID),
	})
	if err != nil {
		return serverapi.ChatSettings{}, err
	}
	return response.Settings, nil
}

func (c *sessionRuntimeClient) MutateChatSettings(operation serverapi.ChatSettingsMutationOperation) (serverapi.ChatSettingsMutationResponse, error) {
	sessionID, err := runtimeids.ParseSessionID(strings.TrimSpace(c.sessionID))
	if err != nil {
		return serverapi.ChatSettingsMutationResponse{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), uiRuntimeControlTimeout)
	defer cancel()
	response, err := c.chatSettings.MutateChatSettings(ctx, serverapi.ChatSettingsMutationRequest{
		Target:    serverapi.SessionChatSettingsTarget(sessionID),
		Operation: operation,
	})
	if err != nil {
		return serverapi.ChatSettingsMutationResponse{}, err
	}
	return response, nil
}

func runtimeContextUsageFromChatContext(contextFacts serverapi.ChatContext) clientui.RuntimeContextUsage {
	return clientui.RuntimeContextUsage{
		UsedTokens:               int(contextFacts.UsedTokens),
		WindowTokens:             int(contextFacts.ContextWindowTokens),
		AutomaticThresholdTokens: int(contextFacts.AutomaticThresholdTokens),
		HasAutomaticThreshold:    true,
	}
}

func runtimeRequestCallWithID[T any](ctx context.Context, c *sessionRuntimeClient, appendWarning bool, requestID string, call func(ctx context.Context, requestID string) (T, error)) (T, error) {
	return retryRuntimeUnavailableCall(ctx, c.recoverRuntimeConnectionWithWarning, appendWarning, func() (T, error) {
		return call(ctx, requestID)
	})
}

func runtimeRequestCallNoResult(ctx context.Context, c *sessionRuntimeClient, call func(ctx context.Context, requestID string) error) error {
	_, err := runtimeRequestCall(ctx, c, true, func(ctx context.Context, requestID string) (struct{}, error) {
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

func (c *sessionRuntimeClient) ShowGoal() (*clientui.RuntimeGoal, error) {
	resp, err := runtimeControlCall(c, false, func(ctx context.Context, _ string) (serverapi.RuntimeGoalShowResponse, error) {
		return c.controls.ShowGoal(ctx, serverapi.RuntimeGoalShowRequest{SessionID: c.sessionID})
	})
	if err != nil {
		return nil, err
	}
	return runtimeGoalFromResponse(resp), nil
}

func (c *sessionRuntimeClient) SetGoal(objective string) (clientui.GoalMutationResult, error) {
	resp, err := runtimeControlCall(c, true, func(ctx context.Context, requestID string) (serverapi.RuntimeGoalMutationResponse, error) {
		return c.controls.SetGoal(ctx, serverapi.RuntimeGoalSetRequest{ClientRequestID: requestID, SessionID: c.sessionID, Objective: objective, Actor: "user"})
	})
	return clientui.GoalMutationResult(resp), err
}

func (c *sessionRuntimeClient) PauseGoal() (clientui.GoalMutationResult, error) {
	return c.setGoalStatus(func(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalMutationResponse, error) {
		return c.controls.PauseGoal(ctx, req)
	})
}

func (c *sessionRuntimeClient) ResumeGoal() (clientui.GoalMutationResult, error) {
	return c.setGoalStatus(func(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalMutationResponse, error) {
		return c.controls.ResumeGoal(ctx, req)
	})
}

func (c *sessionRuntimeClient) CompleteGoal() (clientui.GoalMutationResult, error) {
	return c.setGoalStatus(func(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalMutationResponse, error) {
		return c.controls.CompleteGoal(ctx, req)
	})
}

func (c *sessionRuntimeClient) ClearGoal() (clientui.GoalMutationResult, error) {
	resp, err := runtimeControlCall(c, true, func(ctx context.Context, requestID string) (serverapi.RuntimeGoalMutationResponse, error) {
		return c.controls.ClearGoal(ctx, serverapi.RuntimeGoalClearRequest{ClientRequestID: requestID, SessionID: c.sessionID, Actor: "user"})
	})
	return clientui.GoalMutationResult(resp), err
}

func (c *sessionRuntimeClient) setGoalStatus(call func(context.Context, serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalMutationResponse, error)) (clientui.GoalMutationResult, error) {
	resp, err := runtimeControlCall(c, true, func(ctx context.Context, requestID string) (serverapi.RuntimeGoalMutationResponse, error) {
		return call(ctx, serverapi.RuntimeGoalStatusRequest{ClientRequestID: requestID, SessionID: c.sessionID, Actor: "user"})
	})
	return clientui.GoalMutationResult(resp), err
}

func runtimeGoalFromResponse(resp serverapi.RuntimeGoalShowResponse) *clientui.RuntimeGoal {
	return &clientui.RuntimeGoal{Goal: resp.Goal, Availability: &resp.Availability}
}

func cloneRuntimeGoal(goal *clientui.RuntimeGoal) *clientui.RuntimeGoal {
	if goal == nil {
		return nil
	}
	cloned := *goal
	if goal.Goal != nil {
		core := *goal.Goal
		cloned.Goal = &core
	}
	if goal.Availability != nil {
		availability := *goal.Availability
		cloned.Availability = &availability
	}
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
	requestID := req.ClientRequestID.String()
	resp, err := runtimeRequestCallWithID(ctx, c, true, requestID, func(ctx context.Context, id string) (serverapi.RuntimeSubmitUserTurnResponse, error) {
		return c.controls.SubmitUserTurn(ctx, serverapi.RuntimeSubmitUserTurnRequest{
			ClientRequestID: id,
			SessionID:       c.sessionID,
			Input:           req.Input,
		})
	})
	return userTurnSubmissionFromResponse(resp, runtimeSubmitInputText(req), requestID), err
}

func runtimeSubmitInputText(input clientui.RuntimeSubmitRequest) string {
	text, err := input.Input.CanonicalHistoryText()
	if err != nil {
		panic("runtime submit input must validate before projection: " + err.Error())
	}
	return text
}

func userTurnSubmissionFromResponse(resp serverapi.RuntimeSubmitUserTurnResponse, text string, requestID string) clientui.UserTurnSubmission {
	submission := clientui.UserTurnSubmission{
		Message:    resp.Message,
		ResultKind: resp.ResultKind,
	}
	if resp.Steered && strings.TrimSpace(resp.QueueItemID) != "" {
		submission.Queued = clientui.QueuedUserMessage{ID: resp.QueueItemID, Text: text, ClientRequestID: requestID}
	}
	return submission
}

func (c *sessionRuntimeClient) RunUserShell(ctx context.Context, req clientui.RuntimeShellRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	return runtimeRequestCallNoResult(ctx, c, func(ctx context.Context, requestID string) error {
		return c.controls.SubmitUserShellCommand(ctx, serverapi.RuntimeSubmitUserShellCommandRequest{ClientRequestID: requestID, SessionID: c.sessionID, Command: req.Command})
	})
}

func (c *sessionRuntimeClient) CompactRuntime(ctx context.Context, req clientui.RuntimeCompactRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	return c.controls.CompactContext(ctx, serverapi.RuntimeCompactContextRequest{ClientRequestID: uuid.NewString(), SessionID: c.sessionID, Args: req.Args})
}

func (c *sessionRuntimeClient) Interrupt() error {
	_, err := c.interruptRuntimeCandidate()
	return err
}

func (c *sessionRuntimeClient) interruptRuntimeCandidate() (runtimeTupleCandidate, error) {
	resp, err := runtimeControlCall(c, true, func(ctx context.Context, requestID string) (serverapi.RuntimeInterruptResponse, error) {
		return c.controls.Interrupt(ctx, serverapi.RuntimeInterruptRequest{ClientRequestID: requestID, SessionID: c.sessionID})
	})
	if err != nil {
		return runtimeTupleCandidate{}, err
	}
	candidate := runtimeTupleCandidate{
		Version:  resp.Version,
		Activity: resp.Activity,
	}
	return candidate, nil
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
