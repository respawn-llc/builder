package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
)

func (c *sessionRuntimeClient) SetSessionName(name string) error {
	if err := runtimeControlCallNoResult(c, func(ctx context.Context) error {
		return c.controls.SetSessionName(ctx, serverapi.RuntimeSetSessionNameRequest{SessionID: c.sessionID, Name: name})
	}); err != nil {
		return err
	}
	c.patchMainView(func(view *clientui.RuntimeMainView) {
		view.Session.SessionName = name
	})
	return nil
}

func (c *sessionRuntimeClient) SetThinkingLevel(level string) error {
	if err := runtimeControlCallNoResult(c, func(ctx context.Context) error {
		return c.controls.SetThinkingLevel(ctx, serverapi.RuntimeSetThinkingLevelRequest{SessionID: c.sessionID, Level: level})
	}); err != nil {
		return err
	}
	c.patchMainView(func(view *clientui.RuntimeMainView) {
		view.Status.ThinkingLevel = level
	})
	return nil
}

func (c *sessionRuntimeClient) SetFastModeEnabled(enabled bool) (bool, error) {
	resp, err := runtimeControlCall(c, true, func(ctx context.Context) (serverapi.RuntimeSetFastModeEnabledResponse, error) {
		return c.controls.SetFastModeEnabled(ctx, serverapi.RuntimeSetFastModeEnabledRequest{SessionID: c.sessionID, Enabled: enabled})
	})
	if err == nil {
		c.patchMainView(func(view *clientui.RuntimeMainView) {
			view.Status.FastModeEnabled = enabled
		})
	}
	return resp.Changed, err
}

func (c *sessionRuntimeClient) SetReviewerEnabled(enabled bool) (bool, string, error) {
	resp, err := runtimeControlCall(c, true, func(ctx context.Context) (serverapi.RuntimeSetReviewerEnabledResponse, error) {
		return c.controls.SetReviewerEnabled(ctx, serverapi.RuntimeSetReviewerEnabledRequest{SessionID: c.sessionID, Enabled: enabled})
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
	resp, err := runtimeControlCall(c, true, func(ctx context.Context) (serverapi.RuntimeSetAutoCompactionEnabledResponse, error) {
		return c.controls.SetAutoCompactionEnabled(ctx, serverapi.RuntimeSetAutoCompactionEnabledRequest{SessionID: c.sessionID, Enabled: enabled})
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
	resp, err := runtimeControlCall(c, true, func(ctx context.Context) (serverapi.RuntimeSetQuestionsEnabledResponse, error) {
		return c.controls.SetQuestionsEnabled(ctx, serverapi.RuntimeSetQuestionsEnabledRequest{SessionID: c.sessionID, Enabled: enabled})
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
	resp, err := runtimeControlCall(c, false, func(ctx context.Context) (serverapi.RuntimeGoalShowResponse, error) {
		return c.controls.ShowGoal(ctx, serverapi.RuntimeGoalShowRequest{SessionID: c.sessionID})
	})
	if err != nil {
		return nil, err
	}
	return runtimeGoalFromCore(resp.Goal), nil
}

func (c *sessionRuntimeClient) SetGoal(objective string) (*clientui.RuntimeGoal, error) {
	resp, err := runtimeControlCall(c, true, func(ctx context.Context) (serverapi.RuntimeGoalShowResponse, error) {
		return c.controls.SetGoal(ctx, serverapi.RuntimeGoalSetRequest{SessionID: c.sessionID, Objective: objective, Actor: "user"})
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
	resp, err := runtimeControlCall(c, true, func(ctx context.Context) (serverapi.RuntimeGoalShowResponse, error) {
		return c.controls.ClearGoal(ctx, serverapi.RuntimeGoalClearRequest{SessionID: c.sessionID, Actor: "user"})
	})
	if err != nil {
		return nil, err
	}
	return c.applyGoalResponse(resp), nil
}

func (c *sessionRuntimeClient) setGoalStatus(call func(context.Context, serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error)) (*clientui.RuntimeGoal, error) {
	resp, err := runtimeControlCall(c, true, func(ctx context.Context) (serverapi.RuntimeGoalShowResponse, error) {
		return call(ctx, serverapi.RuntimeGoalStatusRequest{SessionID: c.sessionID, Actor: "user"})
	})
	if err != nil {
		return nil, err
	}
	return c.applyGoalResponse(resp), nil
}

func (c *sessionRuntimeClient) applyGoalResponse(resp serverapi.RuntimeGoalShowResponse) *clientui.RuntimeGoal {
	var goal *clientui.RuntimeGoal
	c.patchMainView(func(view *clientui.RuntimeMainView) {
		goal = runtimeGoalFromCorePreservingAvailability(resp.Goal, view.Status.Goal)
		view.Status.Goal = cloneRuntimeGoal(goal)
	})
	return goal
}

func runtimeGoalFromCorePreservingAvailability(goal *clientui.Goal, current *clientui.RuntimeGoal) *clientui.RuntimeGoal {
	projected := runtimeGoalFromCore(goal)
	if current == nil || current.Availability == nil {
		return projected
	}
	if projected == nil {
		projected = &clientui.RuntimeGoal{}
	}
	availability := *current.Availability
	projected.Availability = &availability
	return projected
}

func runtimeGoalFromCore(goal *clientui.Goal) *clientui.RuntimeGoal {
	if goal == nil {
		return nil
	}
	core := *goal
	return &clientui.RuntimeGoal{
		Goal: &core,
	}
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
	return runtimeControlCallNoResult(c, func(ctx context.Context) error {
		return c.controls.AppendCommittedEntry(ctx, serverapi.RuntimeAppendCommittedEntryRequest{SessionID: c.sessionID, Role: role, Text: text, NoticeID: strings.TrimSpace(noticeID)})
	})
}

func (c *sessionRuntimeClient) SubmitRuntimeInput(ctx context.Context, req clientui.RuntimeSubmitRequest) (clientui.UserTurnSubmission, error) {
	if err := req.Validate(); err != nil {
		return clientui.UserTurnSubmission{}, err
	}
	resp, err := runtimeRequestCall(ctx, c, true, func(ctx context.Context) (serverapi.RuntimeSubmitUserTurnResponse, error) {
		return c.controls.SubmitUserTurn(ctx, serverapi.RuntimeSubmitUserTurnRequest{
			SessionID: c.sessionID,
			Input:     req.Input,
		})
	})
	return userTurnSubmissionFromResponse(resp, runtimeSubmitInputText(req)), err
}

func runtimeSubmitInputText(input clientui.RuntimeSubmitRequest) string {
	text, err := input.Input.CanonicalHistoryText()
	if err != nil {
		panic("runtime submit input must validate before projection: " + err.Error())
	}
	return text
}

func userTurnSubmissionFromResponse(resp serverapi.RuntimeSubmitUserTurnResponse, text string) clientui.UserTurnSubmission {
	submission := clientui.UserTurnSubmission{
		Message:    resp.Message,
		ResultKind: resp.ResultKind,
	}
	if resp.Steered && strings.TrimSpace(resp.QueueItemID) != "" {
		submission.Queued = clientui.QueuedUserMessage{ID: resp.QueueItemID, Text: text}
	}
	return submission
}

func (c *sessionRuntimeClient) RunUserShell(ctx context.Context, req clientui.RuntimeShellRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	_, err := runtimeRequestCall(ctx, c, true, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, c.controls.SubmitUserShellCommand(ctx, serverapi.RuntimeSubmitUserShellCommandRequest{SessionID: c.sessionID, Command: req.Command})
	})
	return err
}

func (c *sessionRuntimeClient) CompactRuntime(ctx context.Context, req clientui.RuntimeCompactRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	_, err := runtimeRequestCall(ctx, c, true, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, c.controls.CompactContext(ctx, serverapi.RuntimeCompactContextRequest{
			SessionID: c.sessionID,
			RequestID: req.RequestID,
			Admission: req.Admission,
		})
	})
	return err
}

func (c *sessionRuntimeClient) Interrupt() error {
	_, err := c.interruptRuntimeCandidate()
	return err
}

func (c *sessionRuntimeClient) interruptRuntimeCandidate() (runtimeTupleCandidate, error) {
	resp, err := runtimeControlCall(c, true, func(ctx context.Context) (serverapi.RuntimeInterruptResponse, error) {
		return c.controls.Interrupt(ctx, serverapi.RuntimeInterruptRequest{SessionID: c.sessionID})
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

func (c *sessionRuntimeClient) RemovePendingWork(queueItemID string) bool {
	pendingWork, ok := c.controls.(apicontract.RuntimePendingWorkService)
	if !ok {
		return false
	}
	itemID, err := runtimeids.ParseQueueItemID(queueItemID)
	if err != nil {
		return false
	}
	_, err = runtimeControlCall(c, true, func(ctx context.Context) (serverapi.RuntimeRemovePendingWorkResponse, error) {
		return pendingWork.RemovePendingWork(ctx, serverapi.RuntimeRemovePendingWorkRequest{SessionID: c.sessionID, ItemID: itemID})
	})
	return err == nil
}

func (c *sessionRuntimeClient) ListPendingWork(sessionID runtimeids.SessionID) (runtimeinput.PendingWork, error) {
	if c == nil {
		return runtimeinput.PendingWork{}, errors.New("runtime client is required")
	}
	if sessionID.IsZero() {
		return runtimeinput.PendingWork{}, errors.New("Pending Work Session ID is required")
	}
	if sessionID.String() != c.sessionID {
		return runtimeinput.PendingWork{}, fmt.Errorf(
			"Pending Work Session ID %q does not match runtime client Session %q",
			sessionID.String(),
			c.sessionID,
		)
	}
	pendingWork, ok := c.controls.(apicontract.RuntimePendingWorkService)
	if !ok {
		return runtimeinput.PendingWork{}, errors.New("runtime Pending Work service is unavailable")
	}
	resp, err := runtimeControlCall(c, false, func(ctx context.Context) (serverapi.RuntimeListPendingWorkResponse, error) {
		return pendingWork.ListPendingWork(ctx, serverapi.RuntimeListPendingWorkRequest{SessionID: sessionID.String()})
	})
	if err != nil {
		return runtimeinput.PendingWork{}, err
	}
	if err := resp.Validate(); err != nil {
		return runtimeinput.PendingWork{}, fmt.Errorf("validate Pending Work list response: %w", err)
	}
	return resp.PendingWork, nil
}

func (c *sessionRuntimeClient) RecordPromptHistory(text string) error {
	return runtimeControlCallNoResult(c, func(ctx context.Context) error {
		return c.controls.RecordPromptHistory(ctx, serverapi.RuntimeRecordPromptHistoryRequest{SessionID: c.sessionID, Text: text})
	})
}
