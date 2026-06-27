package app

import (
	"context"
	"strings"

	"core/shared/clientui"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

func (c *sessionRuntimeClient) SetSessionName(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), uiRuntimeControlTimeout)
	defer cancel()
	if err := c.retryControlCallNoResult(ctx, func() error {
		return c.controls.SetSessionName(ctx, serverapi.RuntimeSetSessionNameRequest{ClientRequestID: uuid.NewString(), SessionID: c.sessionID, Name: name})
	}); err != nil {
		return err
	}
	c.patchMainView(func(view *clientui.RuntimeMainView) {
		view.Session.SessionName = name
	})
	return nil
}

func (c *sessionRuntimeClient) SetThinkingLevel(level string) error {
	ctx, cancel := context.WithTimeout(context.Background(), uiRuntimeControlTimeout)
	defer cancel()
	if err := c.retryControlCallNoResult(ctx, func() error {
		return c.controls.SetThinkingLevel(ctx, serverapi.RuntimeSetThinkingLevelRequest{ClientRequestID: uuid.NewString(), SessionID: c.sessionID, Level: level})
	}); err != nil {
		return err
	}
	c.patchMainView(func(view *clientui.RuntimeMainView) {
		view.Status.ThinkingLevel = level
	})
	return nil
}

func (c *sessionRuntimeClient) SetFastModeEnabled(enabled bool) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), uiRuntimeControlTimeout)
	defer cancel()
	resp, err := retryRuntimeUnavailableCall(ctx, c.recoverRuntimeConnectionWithWarning, true, func() (serverapi.RuntimeSetFastModeEnabledResponse, error) {
		return c.controls.SetFastModeEnabled(ctx, serverapi.RuntimeSetFastModeEnabledRequest{ClientRequestID: uuid.NewString(), SessionID: c.sessionID, Enabled: enabled})
	})
	if err == nil {
		c.patchMainView(func(view *clientui.RuntimeMainView) {
			view.Status.FastModeEnabled = enabled
		})
	}
	return resp.Changed, err
}

func (c *sessionRuntimeClient) SetReviewerEnabled(enabled bool) (bool, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), uiRuntimeControlTimeout)
	defer cancel()
	resp, err := retryRuntimeUnavailableCall(ctx, c.recoverRuntimeConnectionWithWarning, true, func() (serverapi.RuntimeSetReviewerEnabledResponse, error) {
		return c.controls.SetReviewerEnabled(ctx, serverapi.RuntimeSetReviewerEnabledRequest{ClientRequestID: uuid.NewString(), SessionID: c.sessionID, Enabled: enabled})
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
	ctx, cancel := context.WithTimeout(context.Background(), uiRuntimeControlTimeout)
	defer cancel()
	resp, err := retryRuntimeUnavailableCall(ctx, c.recoverRuntimeConnectionWithWarning, true, func() (serverapi.RuntimeSetAutoCompactionEnabledResponse, error) {
		return c.controls.SetAutoCompactionEnabled(ctx, serverapi.RuntimeSetAutoCompactionEnabledRequest{ClientRequestID: uuid.NewString(), SessionID: c.sessionID, Enabled: enabled})
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
	ctx, cancel := context.WithTimeout(context.Background(), uiRuntimeControlTimeout)
	defer cancel()
	resp, err := retryRuntimeUnavailableCall(ctx, c.recoverRuntimeConnectionWithWarning, true, func() (serverapi.RuntimeSetQuestionsEnabledResponse, error) {
		return c.controls.SetQuestionsEnabled(ctx, serverapi.RuntimeSetQuestionsEnabledRequest{ClientRequestID: uuid.NewString(), SessionID: c.sessionID, Enabled: enabled})
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
	ctx, cancel := context.WithTimeout(context.Background(), uiRuntimeControlTimeout)
	defer cancel()
	resp, err := retryRuntimeUnavailableCall(ctx, c.recoverRuntimeConnectionWithWarning, false, func() (serverapi.RuntimeGoalShowResponse, error) {
		return c.controls.ShowGoal(ctx, serverapi.RuntimeGoalShowRequest{SessionID: c.sessionID})
	})
	if err != nil {
		return nil, err
	}
	goal := runtimeGoalFromAPI(resp.Goal)
	c.patchMainView(func(view *clientui.RuntimeMainView) {
		view.Status.Goal = cloneRuntimeGoal(goal)
	})
	return goal, nil
}

func (c *sessionRuntimeClient) SetGoal(objective string) (*clientui.RuntimeGoal, error) {
	ctx, cancel := context.WithTimeout(context.Background(), uiRuntimeControlTimeout)
	defer cancel()
	resp, err := retryRuntimeUnavailableCall(ctx, c.recoverRuntimeConnectionWithWarning, true, func() (serverapi.RuntimeGoalShowResponse, error) {
		return c.controls.SetGoal(ctx, serverapi.RuntimeGoalSetRequest{ClientRequestID: uuid.NewString(), SessionID: c.sessionID, Objective: objective, Actor: "user"})
	})
	if err != nil {
		return nil, err
	}
	goal := runtimeGoalFromAPI(resp.Goal)
	c.patchMainView(func(view *clientui.RuntimeMainView) {
		view.Status.Goal = cloneRuntimeGoal(goal)
	})
	return goal, nil
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

func (c *sessionRuntimeClient) ClearGoal() (*clientui.RuntimeGoal, error) {
	ctx, cancel := context.WithTimeout(context.Background(), uiRuntimeControlTimeout)
	defer cancel()
	resp, err := retryRuntimeUnavailableCall(ctx, c.recoverRuntimeConnectionWithWarning, true, func() (serverapi.RuntimeGoalShowResponse, error) {
		return c.controls.ClearGoal(ctx, serverapi.RuntimeGoalClearRequest{ClientRequestID: uuid.NewString(), SessionID: c.sessionID, Actor: "user"})
	})
	if err != nil {
		return nil, err
	}
	goal := runtimeGoalFromAPI(resp.Goal)
	c.patchMainView(func(view *clientui.RuntimeMainView) {
		view.Status.Goal = cloneRuntimeGoal(goal)
	})
	return goal, nil
}

func (c *sessionRuntimeClient) setGoalStatus(call func(context.Context, serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error)) (*clientui.RuntimeGoal, error) {
	ctx, cancel := context.WithTimeout(context.Background(), uiRuntimeControlTimeout)
	defer cancel()
	resp, err := retryRuntimeUnavailableCall(ctx, c.recoverRuntimeConnectionWithWarning, true, func() (serverapi.RuntimeGoalShowResponse, error) {
		return call(ctx, serverapi.RuntimeGoalStatusRequest{ClientRequestID: uuid.NewString(), SessionID: c.sessionID, Actor: "user"})
	})
	if err != nil {
		return nil, err
	}
	goal := runtimeGoalFromAPI(resp.Goal)
	c.patchMainView(func(view *clientui.RuntimeMainView) {
		view.Status.Goal = cloneRuntimeGoal(goal)
	})
	return goal, nil
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
	ctx, cancel := context.WithTimeout(context.Background(), uiRuntimeControlTimeout)
	defer cancel()
	return c.retryControlCallNoResult(ctx, func() error {
		return c.controls.AppendCommittedEntry(ctx, serverapi.RuntimeAppendCommittedEntryRequest{ClientRequestID: uuid.NewString(), SessionID: c.sessionID, Role: role, Text: text, NoticeID: strings.TrimSpace(noticeID)})
	})
}

func (c *sessionRuntimeClient) SubmitUserMessage(ctx context.Context, text string) (string, error) {
	requestID := uuid.NewString()
	resp, err := retryRuntimeUnavailableCall(ctx, c.recoverRuntimeConnectionWithWarning, true, func() (serverapi.RuntimeSubmitUserTurnResponse, error) {
		return c.controls.SubmitUserTurn(ctx, serverapi.RuntimeSubmitUserTurnRequest{ClientRequestID: requestID, SessionID: c.sessionID, Text: text})
	})
	return resp.Message, err
}

func (c *sessionRuntimeClient) SubmitUserMessageWithPromptHistoryRecorded(ctx context.Context, text string) (string, error) {
	requestID := uuid.NewString()
	resp, err := retryRuntimeUnavailableCall(ctx, c.recoverRuntimeConnectionWithWarning, true, func() (serverapi.RuntimeSubmitUserTurnResponse, error) {
		return c.controls.SubmitUserTurn(ctx, serverapi.RuntimeSubmitUserTurnRequest{ClientRequestID: requestID, SessionID: c.sessionID, Text: text, PromptHistoryRecorded: true})
	})
	return resp.Message, err
}

func (c *sessionRuntimeClient) SubmitUserShellCommand(ctx context.Context, command string) error {
	return c.retryControlCallNoResult(ctx, func() error {
		return c.controls.SubmitUserShellCommand(ctx, serverapi.RuntimeSubmitUserShellCommandRequest{ClientRequestID: uuid.NewString(), SessionID: c.sessionID, Command: command})
	})
}

func (c *sessionRuntimeClient) CompactContext(ctx context.Context, args string) error {
	return c.retryControlCallNoResult(ctx, func() error {
		return c.controls.CompactContext(ctx, serverapi.RuntimeCompactContextRequest{ClientRequestID: uuid.NewString(), SessionID: c.sessionID, Args: args})
	})
}

func (c *sessionRuntimeClient) HasQueuedUserWork() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), uiRuntimeControlTimeout)
	defer cancel()
	resp, err := retryRuntimeUnavailableCall(ctx, c.recoverRuntimeConnectionWithWarning, false, func() (serverapi.RuntimeHasQueuedUserWorkResponse, error) {
		return c.controls.HasQueuedUserWork(ctx, serverapi.RuntimeHasQueuedUserWorkRequest{SessionID: c.sessionID})
	})
	if err != nil {
		return false, err
	}
	return resp.HasQueuedUserWork, nil
}

func (c *sessionRuntimeClient) SubmitQueuedUserMessages(ctx context.Context) (string, error) {
	resp, err := retryRuntimeUnavailableCall(ctx, c.recoverRuntimeConnectionWithWarning, true, func() (serverapi.RuntimeSubmitQueuedUserMessagesResponse, error) {
		return c.controls.SubmitQueuedUserMessages(ctx, serverapi.RuntimeSubmitQueuedUserMessagesRequest{ClientRequestID: uuid.NewString(), SessionID: c.sessionID})
	})
	return resp.Message, err
}

func (c *sessionRuntimeClient) Interrupt() error {
	ctx, cancel := context.WithTimeout(context.Background(), uiRuntimeControlTimeout)
	defer cancel()
	return c.retryControlCallNoResult(ctx, func() error {
		return c.controls.Interrupt(ctx, serverapi.RuntimeInterruptRequest{ClientRequestID: uuid.NewString(), SessionID: c.sessionID})
	})
}

func (c *sessionRuntimeClient) QueueUserMessage(text string) (clientui.QueuedUserMessage, error) {
	return c.QueueUserMessageWithClientRequestID(text, uuid.NewString())
}

func (c *sessionRuntimeClient) QueueUserMessageWithClientRequestID(text string, clientRequestID string) (clientui.QueuedUserMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), uiRuntimeControlTimeout)
	defer cancel()
	requestID := strings.TrimSpace(clientRequestID)
	if requestID == "" {
		requestID = uuid.NewString()
	}
	resp, err := retryRuntimeUnavailableCall(ctx, c.recoverRuntimeConnectionWithWarning, true, func() (serverapi.RuntimeQueueUserMessageResponse, error) {
		return c.controls.QueueUserMessage(ctx, serverapi.RuntimeQueueUserMessageRequest{ClientRequestID: requestID, SessionID: c.sessionID, Text: text})
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
	ctx, cancel := context.WithTimeout(context.Background(), uiRuntimeControlTimeout)
	defer cancel()
	resp, err := retryRuntimeUnavailableCall(ctx, c.recoverRuntimeConnectionWithWarning, true, func() (serverapi.RuntimeDiscardQueuedUserMessageResponse, error) {
		return c.controls.DiscardQueuedUserMessage(ctx, serverapi.RuntimeDiscardQueuedUserMessageRequest{ClientRequestID: uuid.NewString(), SessionID: c.sessionID, QueueItemID: queueItemID})
	})
	if err != nil {
		return false
	}
	return resp.Discarded
}

func (c *sessionRuntimeClient) RecordPromptHistory(text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), uiRuntimeControlTimeout)
	defer cancel()
	requestID := uuid.NewString()
	return c.retryControlCallNoResult(ctx, func() error {
		return c.controls.RecordPromptHistory(ctx, serverapi.RuntimeRecordPromptHistoryRequest{ClientRequestID: requestID, SessionID: c.sessionID, Text: text})
	})
}
