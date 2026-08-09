package app

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"
	"testing"

	"core/server/llm"
	"core/shared/clientui"
	"core/shared/serverapi"
	"core/shared/textutil"
)

type runtimeControlFakeClient struct {
	status                clientui.RuntimeStatus
	sessionView           clientui.RuntimeSessionView
	mainView              clientui.RuntimeMainView
	cachedMainView        clientui.RuntimeMainView
	hasCachedMainView     bool
	setSessionNameArg     string
	setThinkingLevelArg   string
	setFastModeArg        bool
	setFastModeCalls      int
	setAutoCompactCalls   int
	goal                  *clientui.RuntimeGoal
	showGoalCalls         int
	setGoalArg            string
	pauseGoalCalls        int
	resumeGoalCalls       int
	clearGoalCalls        int
	appendCalls           int
	appendedRole          string
	appendedText          string
	submitText            string
	submitCalls           int
	submitOperationRef    clientui.RuntimeOperationRef
	preSubmitOperationRef clientui.RuntimeOperationRef
	submitResult          string
	interruptCalls        int
	interruptPendingRefs  []clientui.RuntimeOperationRef
	interruptTargetRef    *clientui.RuntimeOperationRef
	submitQueuedID        string
	discardQueuedID       string
	discardQueuedCalls    int
	discardQueuedResult   bool
	recordedPromptHistory string
	refreshMainViewCalls  int
	err                   error
	appendErr             error
	submitErr             error
	interruptErr          error
	collaborative         bool
}

func TestUserTurnSubmissionFromResponsePreservesMessagePresence(t *testing.T) {
	t.Parallel()
	blank := ""
	blankKind := clientui.UserTurnResultKindSilentFinal
	withBlank := userTurnSubmissionFromResponse(
		serverapi.RuntimeSubmitUserTurnResponse{Message: &blank, ResultKind: &blankKind},
		"turn",
		"request",
	)
	if withBlank.Message == nil || *withBlank.Message != "" {
		t.Fatalf("blank submission message = %v, want present empty message", withBlank.Message)
	}
	if withBlank.ResultKind == nil || *withBlank.ResultKind != clientui.UserTurnResultKindSilentFinal {
		t.Fatalf("blank submission result kind = %v, want silent final", withBlank.ResultKind)
	}

	withoutMessage := userTurnSubmissionFromResponse(
		serverapi.RuntimeSubmitUserTurnResponse{},
		"turn",
		"request",
	)
	if withoutMessage.Message != nil {
		t.Fatalf("omitted submission message = %v, want absent", withoutMessage.Message)
	}
}

type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return false }

func (f *runtimeControlFakeClient) MainView() clientui.RuntimeMainView {
	if f.mainView.Session.SessionID != "" || f.mainView.Status.ThinkingLevel != "" || f.mainView.Activity.State != "" || f.mainView.Status.WorkflowSession != nil {
		return f.mainView
	}
	return clientui.RuntimeMainView{Status: f.status, Session: f.sessionView}
}
func (f *runtimeControlFakeClient) IsCollaborativeRuntime() bool { return f.collaborative }
func (f *runtimeControlFakeClient) CachedMainView() (clientui.RuntimeMainView, bool) {
	if f.hasCachedMainView {
		return f.cachedMainView, true
	}
	return f.MainView(), true
}
func (f *runtimeControlFakeClient) RefreshMainView() (clientui.RuntimeMainView, error) {
	f.refreshMainViewCalls++
	return f.MainView(), f.err
}
func (f *runtimeControlFakeClient) Status() clientui.RuntimeStatus { return f.status }
func (f *runtimeControlFakeClient) SessionView() clientui.RuntimeSessionView {
	return f.sessionView
}
func (f *runtimeControlFakeClient) SetSessionName(name string) error {
	f.setSessionNameArg = name
	return f.err
}
func (f *runtimeControlFakeClient) SetThinkingLevel(level string) error {
	f.setThinkingLevelArg = level
	return f.err
}
func (f *runtimeControlFakeClient) SetFastModeEnabled(enabled bool) (bool, error) {
	f.setFastModeArg = enabled
	f.setFastModeCalls++
	f.status.FastModeEnabled = enabled
	return true, f.err
}
func (f *runtimeControlFakeClient) SetReviewerEnabled(enabled bool) (bool, string, error) {
	return true, "edits", f.err
}
func (f *runtimeControlFakeClient) SetAutoCompactionEnabled(enabled bool) (bool, bool, error) {
	f.setAutoCompactCalls++
	return true, enabled, f.err
}
func (f *runtimeControlFakeClient) SetQuestionsEnabled(enabled bool) (bool, error) {
	return true, f.err
}
func (f *runtimeControlFakeClient) ShowGoal() (*clientui.RuntimeGoal, error) {
	f.showGoalCalls++
	return cloneRuntimeGoal(f.goal), f.err
}
func (f *runtimeControlFakeClient) SetGoal(objective string) (*clientui.RuntimeGoal, error) {
	f.setGoalArg = objective
	f.goal = &clientui.RuntimeGoal{ID: "goal-1", Objective: objective, Status: "active"}
	return cloneRuntimeGoal(f.goal), f.err
}
func (f *runtimeControlFakeClient) PauseGoal() (*clientui.RuntimeGoal, error) {
	f.pauseGoalCalls++
	if f.goal == nil {
		f.goal = &clientui.RuntimeGoal{ID: "goal-1", Objective: "objective"}
	}
	f.goal.Status = "paused"
	return cloneRuntimeGoal(f.goal), f.err
}
func (f *runtimeControlFakeClient) ResumeGoal() (*clientui.RuntimeGoal, error) {
	f.resumeGoalCalls++
	if f.goal == nil {
		f.goal = &clientui.RuntimeGoal{ID: "goal-1", Objective: "objective"}
	}
	f.goal.Status = "active"
	return cloneRuntimeGoal(f.goal), f.err
}
func (f *runtimeControlFakeClient) CompleteGoal() (*clientui.RuntimeGoal, error) {
	if f.goal == nil {
		f.goal = &clientui.RuntimeGoal{ID: "goal-1", Objective: "objective"}
	}
	f.goal.Status = "complete"
	return cloneRuntimeGoal(f.goal), f.err
}
func (f *runtimeControlFakeClient) ClearGoal() (*clientui.RuntimeGoal, error) {
	f.clearGoalCalls++
	f.goal = nil
	return nil, f.err
}
func (f *runtimeControlFakeClient) AppendCommittedEntry(role, text string) error {
	return f.AppendCommittedEntryWithNoticeID(role, text, "")
}
func (f *runtimeControlFakeClient) AppendCommittedEntryWithNoticeID(role, text, noticeID string) error {
	f.appendCalls++
	f.appendedRole = role
	f.appendedText = text
	if f.appendErr != nil {
		return f.appendErr
	}
	return f.err
}
func (f *runtimeControlFakeClient) submitUserMessage(_ context.Context, text string) (clientui.UserTurnSubmission, error) {
	f.submitText = text
	result := clientui.UserTurnSubmission{Message: textutil.Value(f.submitResult)}
	if f.submitErr != nil {
		return result, f.submitErr
	}
	return result, f.err
}
func (f *runtimeControlFakeClient) SubmitRuntimeInput(ctx context.Context, req clientui.RuntimeSubmitRequest) (clientui.UserTurnSubmission, error) {
	f.submitCalls++
	f.submitOperationRef = req.OperationRef
	f.preSubmitOperationRef = req.PreSubmitCompactionOperationRef
	text := runtimeSubmitInputText(req)
	submission, err := f.submitUserMessage(ctx, text)
	if err == nil && strings.TrimSpace(f.submitQueuedID) != "" {
		submission.Queued = clientui.QueuedUserMessage{
			ID:              strings.TrimSpace(f.submitQueuedID),
			Text:            text,
			ClientRequestID: req.OperationRef.ClientRequestID.String(),
		}
	}
	return submission, err
}
func (f *runtimeControlFakeClient) submitUserShellCommand(_ context.Context, command string) error {
	return f.err
}
func (f *runtimeControlFakeClient) RunUserShell(ctx context.Context, req clientui.RuntimeShellRequest) error {
	return f.submitUserShellCommand(ctx, req.Command)
}
func (f *runtimeControlFakeClient) compactContext(_ context.Context, args string) error {
	_ = args
	return f.err
}
func (f *runtimeControlFakeClient) CompactRuntime(ctx context.Context, req clientui.RuntimeCompactRequest) error {
	return f.compactContext(ctx, req.Args)
}
func (f *runtimeControlFakeClient) Interrupt() error {
	f.interruptCalls++
	if f.interruptErr != nil {
		return f.interruptErr
	}
	return f.err
}
func (f *runtimeControlFakeClient) InterruptWithPendingRefs(refs []clientui.RuntimeOperationRef) error {
	f.interruptCalls++
	f.interruptPendingRefs = append([]clientui.RuntimeOperationRef(nil), refs...)
	f.interruptTargetRef = nil
	if f.interruptErr != nil {
		return f.interruptErr
	}
	return f.err
}
func (f *runtimeControlFakeClient) InterruptWithTarget(target clientui.RuntimeOperationRef, refs []clientui.RuntimeOperationRef) error {
	f.interruptCalls++
	f.interruptPendingRefs = append([]clientui.RuntimeOperationRef(nil), refs...)
	f.interruptTargetRef = &target
	if f.interruptErr != nil {
		return f.interruptErr
	}
	return f.err
}
func (f *runtimeControlFakeClient) DiscardQueuedUserMessage(queueItemID string) bool {
	f.discardQueuedCalls++
	f.discardQueuedID = queueItemID
	if f.discardQueuedResult {
		return true
	}
	return false
}
func (f *runtimeControlFakeClient) RecordPromptHistory(text string) error {
	f.recordedPromptHistory = text
	return f.err
}

func TestThinkingQueryUsesStatusOnly(t *testing.T) {
	disableTransientStatusClearForTest(t)

	client := &runtimeControlFakeClient{status: clientui.RuntimeStatus{ThinkingLevel: "medium"}}
	m := newProjectedTestUIModel(client)

	next, cmd := m.inputController().handleThinkingLevelCommand("")
	updated := next.(*uiModel)
	for _, msg := range collectCmdMessages(t, cmd) {
		next, _ = updated.Update(msg)
		updated = next.(*uiModel)
	}

	if client.appendCalls != 0 {
		t.Fatalf("thinking query must not append transcript entries, got %d append calls", client.appendCalls)
	}
	if updated.transientStatus == "" || updated.transientStatusKind != uiStatusNoticeInfo {
		t.Fatalf("thinking query should surface an info status notice, got status=%q kind=%v", updated.transientStatus, updated.transientStatusKind)
	}
}

func TestThinkingSetWithoutRuntimeUsesStatusOnly(t *testing.T) {
	disableTransientStatusClearForTest(t)

	m := newProjectedStaticUIModel()

	next, cmd := m.inputController().handleThinkingLevelCommand("low")
	updated := next.(*uiModel)
	for _, msg := range collectCmdMessages(t, cmd) {
		next, _ = updated.Update(msg)
		updated = next.(*uiModel)
	}

	if updated.thinkingLevel != "low" {
		t.Fatalf("thinking level = %q, want low", updated.thinkingLevel)
	}
	if updated.transientStatus == "" || updated.transientStatusKind != uiStatusNoticeSuccess {
		t.Fatalf("thinking set should surface a success status notice, got status=%q kind=%v", updated.transientStatus, updated.transientStatusKind)
	}
}

func TestThinkingRuntimeCompletionUsesStatusOnly(t *testing.T) {
	disableTransientStatusClearForTest(t)

	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client)
	cmd := m.runtimeControlCommand(runtimeControlSetThinkingLevel, "high", false, "")
	msgs := collectCmdMessages(t, cmd)

	var done runtimeControlDoneMsg
	for _, msg := range msgs {
		if typed, ok := msg.(runtimeControlDoneMsg); ok {
			done = typed
		}
	}
	next, cmd := m.Update(done)
	updated := next.(*uiModel)
	for _, msg := range collectCmdMessages(t, cmd) {
		next, _ = updated.Update(msg)
		updated = next.(*uiModel)
	}

	if client.appendCalls != 0 {
		t.Fatalf("thinking runtime completion must not append transcript entries, got %d append calls", client.appendCalls)
	}
	if updated.thinkingLevel != "high" {
		t.Fatalf("thinking level = %q, want high", updated.thinkingLevel)
	}
	if updated.transientStatus == "" || updated.transientStatusKind != uiStatusNoticeSuccess {
		t.Fatalf("thinking runtime completion should surface a success status notice, got status=%q kind=%v", updated.transientStatus, updated.transientStatusKind)
	}
}

func TestRuntimeControlCompletionsAreScopedPerOperation(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client)
	m.startupCmds = nil

	sessionCmd := m.runtimeControlCommand(runtimeControlSetSessionName, "incident triage", false, "")
	thinkingCmd := m.runtimeControlCommand(runtimeControlSetThinkingLevel, "high", false, "")
	sessionMsgs := collectCmdMessages(t, sessionCmd)
	thinkingMsgs := collectCmdMessages(t, thinkingCmd)

	var sessionDone runtimeControlDoneMsg
	for _, msg := range sessionMsgs {
		if typed, ok := msg.(runtimeControlDoneMsg); ok {
			sessionDone = typed
		}
	}
	var thinkingDone runtimeControlDoneMsg
	for _, msg := range thinkingMsgs {
		if typed, ok := msg.(runtimeControlDoneMsg); ok {
			thinkingDone = typed
		}
	}

	next, _ := m.Update(thinkingDone)
	updated := next.(*uiModel)
	next, _ = updated.Update(sessionDone)
	updated = next.(*uiModel)
	if updated.thinkingLevel != "high" || updated.sessionName != "incident triage" {
		t.Fatalf("expected independent completions to apply, session=%q thinking=%q", updated.sessionName, updated.thinkingLevel)
	}
}
func TestRuntimeControlStaleSessionCompletionClearsPendingToggle(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client)
	m.startupCmds = nil
	m.sessionID = "session-old"
	m.fastModeAvailable = true

	cmd := m.runtimeControlCommand(runtimeControlSetFastMode, "", true, "")
	msgs := collectCmdMessages(t, cmd)
	var done runtimeControlDoneMsg
	for _, msg := range msgs {
		if typed, ok := msg.(runtimeControlDoneMsg); ok {
			done = typed
		}
	}

	m.sessionID = "session-new"
	_, blockedCmd := m.inputController().handleFastModeCommand("on")
	if blockedCmd == nil {
		t.Fatal("expected new-session fast toggle to start even while old-session command is in flight")
	}
	_ = collectCmdMessages(t, blockedCmd)
	if client.setFastModeArg != true {
		t.Fatalf("new-session bare fast toggle should target true from cached state, got %t", client.setFastModeArg)
	}

	next, _ := m.Update(done)
	updated := next.(*uiModel)
	pending, exists := updated.runtimeControlPending[runtimeControlSetFastMode]
	if !exists || pending.sessionID != "session-new" {
		t.Fatalf("expected stale-session completion to preserve new-session pending toggle, got %+v", updated.runtimeControlPending)
	}
	_, nextCmd := updated.inputController().handleFastModeCommand("off")
	if nextCmd != nil {
		t.Fatal("expected new-session follow-up target to coalesce without a concurrent command")
	}
	if pending := updated.runtimeControlPending[runtimeControlSetFastMode]; pending.desiredEnabled {
		t.Fatalf("expected coalesced new-session desired target to be false, got %+v", pending)
	}
}

func TestSubmitErrorShowsTransientStatusWithoutPersisting(t *testing.T) {
	disableTransientStatusClearForTest(t)

	client := &runtimeControlFakeClient{}
	m := newProjectedStaticUIModel()
	m.engine = client
	m.setRuntimeActivityBusyForTest(true)
	m.activeSubmit = activeSubmitState{token: 1, text: "prompt", stepID: "step-1"}

	next, cmd := m.Update(submitDoneMsg{token: 1, submittedText: "prompt", err: errors.New("submit failed")})
	updated := next.(*uiModel)

	if updated.activity != uiActivityError {
		t.Fatalf("expected error activity, got %v", updated.activity)
	}
	for _, msg := range collectCmdMessages(t, cmd) {
		next, _ = updated.Update(msg)
		updated = next.(*uiModel)
	}
	if client.appendedRole != "" || client.appendedText != "" {
		t.Fatalf("engine is sole persister: client must not persist a run-error entry, got role=%q text=%q", client.appendedRole, client.appendedText)
	}
	if updated.transientStatus == "" || updated.transientStatusKind != uiStatusNoticeError {
		t.Fatalf("expected a transient error status for a submit failure, got status=%q kind=%v", updated.transientStatus, updated.transientStatusKind)
	}
}

func TestRuntimeControlMarksDisconnectOnTransportError(t *testing.T) {
	client := &runtimeControlFakeClient{submitErr: io.EOF}
	m := newProjectedTestUIModel(client)

	if _, err := m.submitRuntimeUserMessage(context.Background(), "prompt"); !errors.Is(err, io.EOF) {
		t.Fatalf("submit runtime user message err = %v, want EOF", err)
	}
	if !m.runtimeDisconnectStatusVisible() {
		t.Fatal("expected runtime disconnect notice after transport error")
	}
}

func TestRuntimeControlClearsDisconnectOnReachableServerError(t *testing.T) {
	client := &runtimeControlFakeClient{submitErr: &llm.APIStatusError{StatusCode: 429, Body: "rate limit"}}
	m := newProjectedTestUIModel(client)
	m.setRuntimeDisconnected(true)

	if _, err := m.submitRuntimeUserMessage(context.Background(), "prompt"); err == nil {
		t.Fatal("expected submit runtime user message error")
	}
	if m.runtimeDisconnectStatusVisible() {
		t.Fatal("expected reachable server error to clear disconnect notice")
	}
}

func TestRuntimeControlTimeoutDoesNotMarkDisconnect(t *testing.T) {
	client := &runtimeControlFakeClient{submitErr: context.DeadlineExceeded}
	m := newProjectedTestUIModel(client)

	if _, err := m.submitRuntimeUserMessage(context.Background(), "prompt"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("submit runtime user message err = %v, want deadline exceeded", err)
	}
	if m.runtimeDisconnectStatusVisible() {
		t.Fatal("did not expect timeout to mark disconnect")
	}
}

func TestRuntimeControlTimeoutDoesNotClearExistingDisconnect(t *testing.T) {
	client := &runtimeControlFakeClient{submitErr: context.DeadlineExceeded}
	m := newProjectedTestUIModel(client)
	m.setRuntimeDisconnected(true)

	if _, err := m.submitRuntimeUserMessage(context.Background(), "prompt"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("submit runtime user message err = %v, want deadline exceeded", err)
	}
	if !m.runtimeDisconnectStatusVisible() {
		t.Fatal("expected timeout not to clear existing disconnect notice")
	}
}

func TestRuntimeControlURLTimeoutDoesNotMarkDisconnect(t *testing.T) {
	client := &runtimeControlFakeClient{submitErr: &url.Error{Op: "Get", URL: "http://example.test", Err: timeoutNetError{}}}
	m := newProjectedTestUIModel(client)

	if _, err := m.submitRuntimeUserMessage(context.Background(), "prompt"); err == nil {
		t.Fatal("expected submit runtime user message error")
	}
	if m.runtimeDisconnectStatusVisible() {
		t.Fatal("did not expect URL timeout to mark disconnect")
	}
}

func TestRuntimeControlOpTimeoutDoesNotMarkDisconnect(t *testing.T) {
	client := &runtimeControlFakeClient{submitErr: &net.OpError{Op: "read", Net: "tcp", Err: timeoutNetError{}}}
	m := newProjectedTestUIModel(client)

	if _, err := m.submitRuntimeUserMessage(context.Background(), "prompt"); err == nil {
		t.Fatal("expected submit runtime user message error")
	}
	if m.runtimeDisconnectStatusVisible() {
		t.Fatal("did not expect op timeout to mark disconnect")
	}
}
