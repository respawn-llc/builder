package app

import (
	"context"
	"errors"
	"strings"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

func (c *sessionRuntimeClient) sessionRuntimeBoundary() {}

func (c *sessionRuntimeClient) ReadChatSettings() (serverapi.ChatSettings, error) {
	if c == nil || c.chatSettings == nil {
		return serverapi.ChatSettings{}, errors.New("Chat settings service is unavailable")
	}
	sessionID, err := runtimeids.ParseSessionID(strings.TrimSpace(c.sessionID))
	if err != nil {
		return serverapi.ChatSettings{}, err
	}
	response, err := c.chatSettings.ReadChatSettings(context.Background(), serverapi.ChatSettingsReadRequest{
		Target: serverapi.SessionChatSettingsTarget(sessionID),
	})
	if err != nil {
		return serverapi.ChatSettings{}, err
	}
	return response.Settings, nil
}

func (c *sessionRuntimeClient) MutateChatSettings(operation serverapi.ChatSettingsMutationOperation) (serverapi.ChatSettingsMutationResponse, error) {
	if c == nil || c.chatSettings == nil {
		return serverapi.ChatSettingsMutationResponse{}, errors.New("Chat settings service is unavailable")
	}
	sessionID, err := runtimeids.ParseSessionID(strings.TrimSpace(c.sessionID))
	if err != nil {
		return serverapi.ChatSettingsMutationResponse{}, err
	}
	response, err := c.chatSettings.MutateChatSettings(context.Background(), serverapi.ChatSettingsMutationRequest{
		Target:    serverapi.SessionChatSettingsTarget(sessionID),
		Operation: operation,
	})
	if err != nil {
		return serverapi.ChatSettingsMutationResponse{}, err
	}
	c.patchMainView(func(view *clientui.RuntimeMainView) {
		view.Status.ThinkingLevel = response.Settings.SelectedAgent.Thinking
		view.Status.ReviewerFrequency = string(response.Settings.Supervisor.Value)
		view.Status.ReviewerEnabled = response.Settings.Supervisor.Value != serverapi.ChatSettingsSupervisorOff
		view.Status.FastModeEnabled = response.Settings.Fast != nil && response.Settings.Fast.Value
		view.Status.QuestionsEnabled = response.Settings.Questions.Enabled
		view.Status.AutoCompactionEnabled = response.Settings.AutoCompaction.Stored
		view.Status.CompactionMode = string(response.Settings.AutoCompaction.Policy)
	})
	return response, nil
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
