package app

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"core/server/llm"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
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
	submitInput           runtimeinput.Input
	submitCalls           int
	submitResult          string
	interruptCalls        int
	submitQueuedID        string
	discardQueuedID       string
	discardQueuedCalls    int
	discardQueuedResult   bool
	recordedPromptHistory string
	refreshMainViewCalls  int
	compactCalls          int
	compactArgs           string
	compactErr            error
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
		serverapi.RuntimeSubmitUserTurnResponse{Message: &blank, ResultKind: blankKind},
		"turn",
		"request",
	)
	if withBlank.Message == nil || *withBlank.Message != "" {
		t.Fatalf("blank submission message = %v, want present empty message", withBlank.Message)
	}
	if withBlank.ResultKind != clientui.UserTurnResultKindSilentFinal {
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
func (f *runtimeControlFakeClient) ReadChatSettings() (serverapi.ChatSettings, error) {
	return f.chatSettings(), f.err
}
func (f *runtimeControlFakeClient) MutateChatSettings(operation serverapi.ChatSettingsMutationOperation) (chatSettingsMutationResult, error) {
	settings := f.chatSettings()
	switch operation.Kind {
	case serverapi.ChatSettingsMutationThinking:
		settings.SelectedAgent.Thinking = *operation.Value
	case serverapi.ChatSettingsMutationFast:
		settings.Fast.Value = *operation.Enabled
	case serverapi.ChatSettingsMutationSupervisor:
		settings.Supervisor.Value = serverapi.ChatSettingsSupervisorValue(*operation.Value)
	case serverapi.ChatSettingsMutationQuestions:
		settings.Questions.Enabled = *operation.Enabled
	case serverapi.ChatSettingsMutationAutoCompaction:
		settings.AutoCompaction.Stored = *operation.Enabled
	}
	response := serverapi.ChatSettingsMutationResponse{
		Result:   serverapi.NewChatSettingsMutationApplied(true),
		Settings: settings,
		Context: serverapi.ChatContext{
			ContextWindowTokens:      100,
			UsedTokens:               10,
			RemainingTokens:          90,
			AutomaticThresholdTokens: 80,
			AutoCompactionEnabled:    settings.AutoCompaction.Stored,
			CompactionMode:           serverapi.ChatContextCompactionModeLocal,
		},
	}
	f.status.ThinkingLevel = response.Settings.SelectedAgent.Thinking
	f.status.ReviewerFrequency = string(response.Settings.Supervisor.Value)
	f.status.ReviewerEnabled = response.Settings.Supervisor.Value != serverapi.ChatSettingsSupervisorOff
	f.status.FastModeAvailable = response.Settings.Fast != nil
	f.status.FastModeEnabled = response.Settings.Fast != nil && response.Settings.Fast.Value
	f.status.QuestionsEnabled = response.Settings.Questions.Enabled
	f.status.AutoCompactionEnabled = response.Context.AutoCompactionEnabled
	f.status.CompactionMode = string(response.Context.CompactionMode)
	f.status.CompactionCount = int(response.Context.CompletedCompactionCount)
	f.status.ContextUsage = clientui.RuntimeContextUsage{
		UsedTokens:               int(response.Context.UsedTokens),
		WindowTokens:             int(response.Context.ContextWindowTokens),
		AutomaticThresholdTokens: int(response.Context.AutomaticThresholdTokens),
		HasAutomaticThreshold:    true,
	}
	if f.err != nil {
		return chatSettingsMutationResult{}, f.err
	}
	return chatSettingsMutationResult{
		response:   response,
		projection: projectChatSettingsMutation(response, f.MainView()),
	}, nil
}
func (f *runtimeControlFakeClient) chatSettings() serverapi.ChatSettings {
	return serverapi.ChatSettings{
		SelectedAgent: serverapi.ChatSettingsAgentSummary{Role: "default", Thinking: f.status.ThinkingLevel},
		Supervisor: serverapi.ChatSettingsSupervisor{
			Value:    serverapi.ChatSettingsSupervisorValue(f.status.ReviewerFrequency),
			Baseline: serverapi.ChatSettingsSupervisorAfterEdits,
		},
		Fast:           &serverapi.ChatSettingsFast{Value: f.status.FastModeEnabled},
		Questions:      serverapi.ChatSettingsQuestions{Enabled: f.status.QuestionsEnabled},
		AutoCompaction: serverapi.ChatSettingsAutoCompaction{Stored: f.status.AutoCompactionEnabled},
	}
}
func (f *runtimeControlFakeClient) ShowGoal() (*clientui.RuntimeGoal, error) {
	f.showGoalCalls++
	return cloneRuntimeGoal(f.goal), f.err
}
func (f *runtimeControlFakeClient) SetGoal(objective string) (clientui.GoalMutationResult, error) {
	f.setGoalArg = objective
	f.goal = runtimeControlTestGoal(objective, clientui.RuntimeGoalStatusActive)
	return clientui.GoalMutationResult{Goal: f.goal.Goal}, f.err
}
func (f *runtimeControlFakeClient) PauseGoal() (clientui.GoalMutationResult, error) {
	f.pauseGoalCalls++
	if f.goal == nil {
		f.goal = runtimeControlTestGoal("objective", clientui.RuntimeGoalStatusActive)
	}
	f.goal.Status = "paused"
	return clientui.GoalMutationResult{Goal: f.goal.Goal}, f.err
}
func (f *runtimeControlFakeClient) ResumeGoal() (clientui.GoalMutationResult, error) {
	f.resumeGoalCalls++
	if f.goal == nil {
		f.goal = runtimeControlTestGoal("objective", clientui.RuntimeGoalStatusActive)
	}
	f.goal.Status = "active"
	return clientui.GoalMutationResult{Goal: f.goal.Goal}, f.err
}
func (f *runtimeControlFakeClient) CompleteGoal() (clientui.GoalMutationResult, error) {
	if f.goal == nil {
		f.goal = runtimeControlTestGoal("objective", clientui.RuntimeGoalStatusActive)
	}
	f.goal.Status = "complete"
	return clientui.GoalMutationResult{Goal: f.goal.Goal}, f.err
}
func (f *runtimeControlFakeClient) ClearGoal() (clientui.GoalMutationResult, error) {
	f.clearGoalCalls++
	f.goal = nil
	return clientui.GoalMutationResult{}, f.err
}
func runtimeControlTestGoal(objective string, status clientui.RuntimeGoalStatus) *clientui.RuntimeGoal {
	now := time.Unix(1, 0)
	return &clientui.RuntimeGoal{Goal: &clientui.Goal{ID: "goal-1", Objective: objective, Status: status, CreatedAt: now, UpdatedAt: now}}
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
	f.submitInput = req.Input
	text := runtimeSubmitInputText(req)
	submission, err := f.submitUserMessage(ctx, text)
	if err == nil && strings.TrimSpace(f.submitQueuedID) != "" {
		submission.Queued = clientui.QueuedUserMessage{
			ID:              strings.TrimSpace(f.submitQueuedID),
			Text:            text,
			ClientRequestID: req.ClientRequestID.String(),
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
	f.compactCalls++
	f.compactArgs = args
	if f.compactErr != nil {
		return f.compactErr
	}
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

func TestRuntimeInterruptNotAcceptedClearsPendingAttempt(t *testing.T) {
	client := &runtimeControlFakeClient{
		interruptErr: serverapi.NewRuntimeCommandNotAcceptedError(errors.New("no active Agent Turn")),
	}
	m := newProjectedClosedUIModel(client)
	m.sessionID = "session-1"
	m.setRuntimeActivityBusyForTest(true)
	m.activeSubmit = activeSubmitState{
		token: 1,
		text:  "keep me",
	}

	cmd := m.inputController().interruptBusyRuntime()
	if !m.hasPendingInterrupt() {
		t.Fatal("interrupt attempt was not marked pending")
	}
	done := cmd().(runtimeControlDoneMsg)
	next, _ := m.Update(done)
	updated := next.(*uiModel)

	if updated.hasPendingInterrupt() {
		t.Fatal("not-accepted interrupt remained pending")
	}
	if updated.activeSubmit.token != 1 || updated.activeSubmit.text != "keep me" {
		t.Fatalf("not-accepted interrupt changed active submit: %+v", updated.activeSubmit)
	}
}

func TestRuntimeInterruptRestoresPendingInputsBeforeComposerDraftInSubmissionOrder(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedClosedUIModel(client)
	steerOne := "  steer one\nline\t "
	queueTwo := "\tqueue two  "
	steerThree := "steer  three\n\ninside"
	queueFour := " queue four "
	draft := "  existing draft\t"
	_ = m.queueInjectedInput(steerOne)
	m.queueInput(queueTwo)
	_ = m.queueInjectedInput(steerThree)
	m.queueInput(queueFour)
	stopped := clientui.QueuedUserMessageFailureStopped
	bindSteer := func(index int) clientui.TranscriptQueuedMessageState {
		t.Helper()
		requestID, err := runtimeids.ParseRuntimeClientRequestID(m.injectedQueue[index].ClientRequestID)
		if err != nil {
			t.Fatalf("parse queued client request id: %v", err)
		}
		queueID := runtimeids.NewQueueItemID()
		text := m.injectedQueue[index].Text
		m.registerSteeredQueuedUserMessage(clientui.QueuedUserMessage{
			ID:              queueID.String(),
			Text:            text,
			ClientRequestID: requestID.String(),
		})
		return clientui.TranscriptQueuedMessageState{
			ClientRequestID: requestID,
			QueueItemID:     queueID,
			Status:          clientui.QueuedUserMessageFailed,
			FailureReason:   &stopped,
			Text:            &text,
		}
	}
	firstFailure := bindSteer(0)
	secondFailure := bindSteer(1)
	testSetMainInput(m, draft)
	activeText := "active turn already visible in the transcript"
	m.activeSubmit = activeSubmitState{
		token:  1,
		text:   activeText,
		origin: activeSubmitOriginQueued,
	}
	m.setPendingInterrupt(true)

	next, interruptedCmd := m.inputController().handleSubmitDone(newSubmitDoneMsg(1, "", activeText, context.Canceled))
	m = next.(*uiModel)
	for _, msg := range collectCmdMessages(t, interruptedCmd) {
		m = updateUIModel(t, m, msg)
	}
	if got := testMainInput(m); got != draft {
		t.Fatalf("interrupted active Submit changed composer before interrupt response = %q", got)
	}
	if len(m.injectedQueue) != 2 || len(m.queued) != 2 {
		t.Fatalf("interrupted active Submit consumed pending inputs before response: injected=%d queued=%d", len(m.injectedQueue), len(m.queued))
	}
	if client.discardQueuedCalls != 0 {
		t.Fatalf("interrupted active Submit dispatched %d queued-message discards before response", client.discardQueuedCalls)
	}
	stoppedCmd := m.applyTranscriptQueuedMessageState(firstFailure)
	if stoppedCmd == nil {
		t.Fatal("stopped queue event did not acknowledge the observed interruption")
	}
	for _, msg := range collectCmdMessages(t, stoppedCmd) {
		m = updateUIModel(t, m, msg)
	}

	want := strings.Join([]string{steerOne, queueTwo, steerThree, queueFour, draft}, "\n\n")
	if got := testMainInput(m); got != want {
		t.Fatalf("restored composer = %q, want %q", got, want)
	}
	if len(m.injectedQueue) != 0 || len(m.queued) != 0 {
		t.Fatalf("pending input state remained after restoration: injected=%+v queued=%+v", m.injectedQueue, m.queued)
	}
	if m.hasPendingInterrupt() {
		t.Fatal("interrupt remained pending after local restoration")
	}
	accepted := secondFailure
	accepted.Status = clientui.QueuedUserMessageAccepted
	accepted.FailureReason = nil
	m.applyTranscriptQueuedMessageState(accepted)
	m.applyTranscriptQueuedMessageState(secondFailure)
	if got := testMainInput(m); got != want {
		t.Fatalf("late queue events duplicated restored composer = %q, want %q", got, want)
	}
	if len(m.injectedQueue) != 0 {
		t.Fatalf("late queue events recreated interrupted state: injected=%+v", m.injectedQueue)
	}
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
	value := "high"
	cmd := m.chatSettingsMutationCommand(serverapi.ChatSettingsMutationOperation{
		Kind:  serverapi.ChatSettingsMutationThinking,
		Value: &value,
	})
	msgs := collectCmdMessages(t, cmd)

	var done chatSettingsDoneMsg
	for _, msg := range msgs {
		if typed, ok := msg.(chatSettingsDoneMsg); ok {
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
	value := "high"
	thinkingCmd := m.chatSettingsMutationCommand(serverapi.ChatSettingsMutationOperation{
		Kind:  serverapi.ChatSettingsMutationThinking,
		Value: &value,
	})
	sessionMsgs := collectCmdMessages(t, sessionCmd)
	thinkingMsgs := collectCmdMessages(t, thinkingCmd)

	var sessionDone runtimeControlDoneMsg
	for _, msg := range sessionMsgs {
		if typed, ok := msg.(runtimeControlDoneMsg); ok {
			sessionDone = typed
		}
	}
	var thinkingDone chatSettingsDoneMsg
	for _, msg := range thinkingMsgs {
		if typed, ok := msg.(chatSettingsDoneMsg); ok {
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
func TestSubmitErrorShowsTransientStatusWithoutPersisting(t *testing.T) {
	disableTransientStatusClearForTest(t)

	client := &runtimeControlFakeClient{}
	m := newProjectedStaticUIModel()
	m.engine = client
	m.setRuntimeActivityBusyForTest(true)
	m.activeSubmit = activeSubmitState{token: 1, text: "prompt"}

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
