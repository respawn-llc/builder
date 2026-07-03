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

	tea "github.com/charmbracelet/bubbletea"
)

type runtimeControlFakeClient struct {
	status                 clientui.RuntimeStatus
	sessionView            clientui.RuntimeSessionView
	mainView               clientui.RuntimeMainView
	cachedMainView         clientui.RuntimeMainView
	hasCachedMainView      bool
	setSessionNameArg      string
	setThinkingLevelArg    string
	setFastModeArg         bool
	setFastModeCalls       int
	setAutoCompactCalls    int
	goal                   *clientui.RuntimeGoal
	showGoalCalls          int
	setGoalArg             string
	pauseGoalCalls         int
	resumeGoalCalls        int
	clearGoalCalls         int
	appendedRole           string
	appendedText           string
	submitText             string
	submitOperationRef     clientui.RuntimeOperationRef
	preSubmitOperationRef  clientui.RuntimeOperationRef
	submitResult           string
	hasQueuedUserWork      bool
	hasQueuedUserWorkCalls int
	submitQueuedResult     string
	submitQueuedCalls      int
	submitQueuedOperation  clientui.RuntimeOperationRef
	interruptCalls         int
	interruptPendingRefs   []clientui.RuntimeOperationRef
	interruptTargetRef     *clientui.RuntimeOperationRef
	queuedText             string
	queuedClientRequestID  string
	queueUserMessageCalls  int
	queueUserMessageErr    error
	queueUserMessageID     string
	discardQueuedID        string
	discardQueuedCalls     int
	discardQueuedResult    bool
	recordedPromptHistory  string
	refreshMainViewCalls   int
	err                    error
	appendErr              error
	submitErr              error
	hasQueuedUserWorkErr   error
	interruptErr           error
	collaborative          bool
}

type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return false }

func (f *runtimeControlFakeClient) MainView() clientui.RuntimeMainView {
	if f.mainView.Session.SessionID != "" || f.mainView.Status.ThinkingLevel != "" || f.mainView.Activity.State != "" || f.mainView.Status.WorkflowSession != nil || f.mainView.Status.WorkflowActive {
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
	f.appendedRole = role
	f.appendedText = text
	if f.appendErr != nil {
		return f.appendErr
	}
	return f.err
}
func (f *runtimeControlFakeClient) submitUserMessage(_ context.Context, text string) (clientui.UserTurnSubmission, error) {
	f.submitText = text
	result := clientui.UserTurnSubmission{Message: f.submitResult}
	if f.submitErr != nil {
		return result, f.submitErr
	}
	return result, f.err
}
func (f *runtimeControlFakeClient) SubmitRuntimeInput(ctx context.Context, req clientui.RuntimeSubmitRequest) (clientui.UserTurnSubmission, error) {
	f.submitOperationRef = req.OperationRef
	f.preSubmitOperationRef = req.PreSubmitCompactionOperationRef
	return f.submitUserMessage(ctx, req.Text)
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
func (f *runtimeControlFakeClient) HasQueuedUserWork() (bool, error) {
	f.hasQueuedUserWorkCalls++
	if f.hasQueuedUserWorkErr != nil {
		return f.hasQueuedUserWork, f.hasQueuedUserWorkErr
	}
	return f.hasQueuedUserWork, f.err
}
func (f *runtimeControlFakeClient) submitQueuedUserMessages(context.Context) (string, error) {
	f.submitQueuedCalls++
	return f.submitQueuedResult, f.err
}
func (f *runtimeControlFakeClient) SubmitRuntimeQueued(ctx context.Context, req clientui.RuntimeSubmitQueuedRequest) (string, error) {
	f.submitQueuedOperation = req.OperationRef
	return f.submitQueuedUserMessages(ctx)
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
func (f *runtimeControlFakeClient) QueueRuntimeUserMessage(req clientui.RuntimeQueueUserMessageRequest) (clientui.QueuedUserMessage, error) {
	if err := req.Validate(); err != nil {
		return clientui.QueuedUserMessage{}, err
	}
	f.queueUserMessageCalls++
	f.queuedText = req.Text
	f.queuedClientRequestID = strings.TrimSpace(req.OperationRef.ClientRequestID)
	if f.queueUserMessageErr != nil {
		return clientui.QueuedUserMessage{}, f.queueUserMessageErr
	}
	id := strings.TrimSpace(f.queueUserMessageID)
	if id == "" {
		id = "queue-1"
	}
	return clientui.QueuedUserMessage{ID: id, Text: req.Text, ClientRequestID: f.queuedClientRequestID}, nil
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

func TestRuntimeControlCompletionsAreScopedPerOperation(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
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

func TestRuntimeControlTextMutationsCoalesceAfterApplyingInFlightCompletion(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil

	firstCmd := m.runtimeControlCommand(runtimeControlSetThinkingLevel, "high", false, "")
	if firstCmd == nil {
		t.Fatal("expected first thinking-level command")
	}
	secondCmd := m.runtimeControlCommand(runtimeControlSetThinkingLevel, "low", false, "")
	if secondCmd != nil {
		t.Fatal("did not expect second thinking-level command while first is in flight")
	}
	firstMsgs := collectCmdMessages(t, firstCmd)
	if client.setThinkingLevelArg != "high" {
		t.Fatalf("first thinking-level RPC = %q, want high", client.setThinkingLevelArg)
	}

	var firstDone runtimeControlDoneMsg
	for _, msg := range firstMsgs {
		if typed, ok := msg.(runtimeControlDoneMsg); ok {
			firstDone = typed
		}
	}
	next, followUpCmd := m.Update(firstDone)
	updated := next.(*uiModel)
	if updated.thinkingLevel != "high" {
		t.Fatalf("expected first thinking-level completion to update UI before follow-up, got %q", updated.thinkingLevel)
	}
	if followUpCmd == nil {
		t.Fatal("expected follow-up command for coalesced thinking-level target")
	}
	followUpMsgs := collectCmdMessages(t, followUpCmd)
	if client.setThinkingLevelArg != "low" {
		t.Fatalf("follow-up thinking-level RPC = %q, want low", client.setThinkingLevelArg)
	}

	var followUpDone runtimeControlDoneMsg
	for _, msg := range followUpMsgs {
		if typed, ok := msg.(runtimeControlDoneMsg); ok {
			followUpDone = typed
		}
	}
	next, _ = updated.Update(followUpDone)
	updated = next.(*uiModel)
	if updated.thinkingLevel != "low" {
		t.Fatalf("thinking level = %q, want low", updated.thinkingLevel)
	}
}

func TestRuntimeControlRapidFastToggleUsesPendingTargetAfterApplyingOlderCompletion(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.fastModeAvailable = true
	m.fastModeEnabled = false

	_, firstCmd := m.inputController().handleFastModeCommand("")
	if firstCmd == nil {
		t.Fatal("expected first fast toggle command")
	}
	_, secondCmd := m.inputController().handleFastModeCommand("")
	if secondCmd != nil {
		t.Fatal("did not expect second fast toggle command while first is in flight")
	}

	firstMsgs := collectCmdMessages(t, firstCmd)
	if client.setFastModeCalls != 1 || client.setFastModeArg != true {
		t.Fatalf("first fast target calls=%d arg=%t, want one true", client.setFastModeCalls, client.setFastModeArg)
	}

	var firstDone runtimeControlDoneMsg
	for _, msg := range firstMsgs {
		if typed, ok := msg.(runtimeControlDoneMsg); ok {
			firstDone = typed
		}
	}
	next, followUpCmd := m.Update(firstDone)
	updated := next.(*uiModel)
	if !updated.fastModeEnabled {
		t.Fatal("expected first fast toggle completion to apply before follow-up")
	}
	if followUpCmd == nil {
		t.Fatal("expected follow-up command for coalesced fast target")
	}
	followUpMsgs := collectCmdMessages(t, followUpCmd)
	if client.setFastModeCalls != 2 || client.setFastModeArg != false {
		t.Fatalf("follow-up fast target calls=%d arg=%t, want second false", client.setFastModeCalls, client.setFastModeArg)
	}
	var followUpDone runtimeControlDoneMsg
	for _, msg := range followUpMsgs {
		if typed, ok := msg.(runtimeControlDoneMsg); ok {
			followUpDone = typed
		}
	}
	next, _ = updated.Update(followUpDone)
	updated = next.(*uiModel)
	if updated.fastModeEnabled {
		t.Fatal("expected rapid double-toggle to end disabled")
	}
}

func TestRuntimeControlStaleSessionCompletionClearsPendingToggle(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
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

func TestRuntimeControlHelpersFallbackWithoutRuntimeClient(t *testing.T) {
	m := newProjectedStaticUIModel()

	if err := m.setRuntimeSessionName("name"); err != nil {
		t.Fatalf("set runtime session name without client: %v", err)
	}
	if err := m.setRuntimeThinkingLevel("high"); err != nil {
		t.Fatalf("set runtime thinking level without client: %v", err)
	}
	if changed, err := m.setRuntimeFastModeEnabled(true); changed || err != nil {
		t.Fatalf("set runtime fast mode without client = (%t, %v), want (false, nil)", changed, err)
	}
	if changed, mode, err := m.setRuntimeReviewerEnabled(true); changed || mode != "" || err != nil {
		t.Fatalf("set runtime reviewer without client = (%t, %q, %v)", changed, mode, err)
	}
	if changed, enabled, err := m.setRuntimeAutoCompactionEnabled(true); changed || enabled || err != nil {
		t.Fatalf("set runtime autocompaction without client = (%t, %t, %v), want (false, false, nil)", changed, enabled, err)
	}
	if goal, err := m.showRuntimeGoal(); goal != nil || err != nil {
		t.Fatalf("show runtime goal without client = (%+v, %v), want (nil, nil)", goal, err)
	}
	if goal, err := m.setRuntimeGoal("goal"); goal != nil || err != nil {
		t.Fatalf("set runtime goal without client = (%+v, %v), want (nil, nil)", goal, err)
	}
	if goal, err := m.pauseRuntimeGoal(); goal != nil || err != nil {
		t.Fatalf("pause runtime goal without client = (%+v, %v), want (nil, nil)", goal, err)
	}
	if goal, err := m.resumeRuntimeGoal(); goal != nil || err != nil {
		t.Fatalf("resume runtime goal without client = (%+v, %v), want (nil, nil)", goal, err)
	}
	if goal, err := m.clearRuntimeGoal(); goal != nil || err != nil {
		t.Fatalf("clear runtime goal without client = (%+v, %v), want (nil, nil)", goal, err)
	}
	if submission, err := m.submitRuntimeUserMessage(context.Background(), "prompt"); submission.Message != "" || err != nil {
		t.Fatalf("submit runtime user message without client = (%q, %v), want (empty, nil)", submission.Message, err)
	}
	if err := m.submitRuntimeUserShellCommand(context.Background(), "echo hi"); err != nil {
		t.Fatalf("submit runtime shell command without client: %v", err)
	}
	if err := m.compactRuntimeContext(context.Background(), "--force"); err != nil {
		t.Fatalf("compact runtime context without client: %v", err)
	}
	queuedWork, err := m.hasQueuedRuntimeUserWork()
	if err != nil {
		t.Fatalf("has queued runtime user work without client: %v", err)
	}
	if queuedWork {
		t.Fatal("did not expect queued runtime user work without client")
	}
	if message, err := m.submitQueuedRuntimeUserMessages(context.Background()); message != "" || err != nil {
		t.Fatalf("submit queued runtime user messages without client = (%q, %v), want (empty, nil)", message, err)
	}
	if err := m.interruptRuntime(); err != nil {
		t.Fatalf("interrupt runtime without client: %v", err)
	}
	queued, err := m.queueRuntimeUserMessage("queued text")
	if err != nil || queued.ID == "" || queued.Text != "queued text" {
		t.Fatalf("queue runtime user message without client = (%+v, %v), want generated item", queued, err)
	}
	if discarded := m.discardQueuedRuntimeUserMessage(queued.ID); discarded {
		t.Fatal("did not expect queued runtime user message discarded without client")
	}
	if err := m.recordRuntimePromptHistory("prompt history"); err != nil {
		t.Fatalf("record runtime prompt history without client: %v", err)
	}
}
func TestSubmitErrorShowsTransientStatusWithoutPersisting(t *testing.T) {
	originalClear := scheduleTransientStatusClear
	scheduleTransientStatusClear = func(time.Duration, uint64) tea.Cmd { return nil }
	defer func() { scheduleTransientStatusClear = originalClear }()

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
	if committed := committedTranscriptEntriesForApp(updated.transcriptEntries); len(committed) != 0 {
		t.Fatalf("client must not advance committed transcript on submit error: %+v", committed)
	}
	if updated.transientStatus == "" || updated.transientStatusKind != uiStatusNoticeError {
		t.Fatalf("expected a transient error status for a submit failure, got status=%q kind=%v", updated.transientStatus, updated.transientStatusKind)
	}
}

func TestRuntimeControlMarksDisconnectOnTransportError(t *testing.T) {
	client := &runtimeControlFakeClient{submitErr: io.EOF}
	m := newProjectedTestUIModel(client, nil, nil)

	if _, err := m.submitRuntimeUserMessage(context.Background(), "prompt"); !errors.Is(err, io.EOF) {
		t.Fatalf("submit runtime user message err = %v, want EOF", err)
	}
	if !m.runtimeDisconnectStatusVisible() {
		t.Fatal("expected runtime disconnect notice after transport error")
	}
}

func TestRuntimeControlClearsDisconnectOnReachableServerError(t *testing.T) {
	client := &runtimeControlFakeClient{submitErr: &llm.APIStatusError{StatusCode: 429, Body: "rate limit"}}
	m := newProjectedTestUIModel(client, nil, nil)
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
	m := newProjectedTestUIModel(client, nil, nil)

	if _, err := m.submitRuntimeUserMessage(context.Background(), "prompt"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("submit runtime user message err = %v, want deadline exceeded", err)
	}
	if m.runtimeDisconnectStatusVisible() {
		t.Fatal("did not expect timeout to mark disconnect")
	}
}

func TestRuntimeControlTimeoutDoesNotClearExistingDisconnect(t *testing.T) {
	client := &runtimeControlFakeClient{submitErr: context.DeadlineExceeded}
	m := newProjectedTestUIModel(client, nil, nil)
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
	m := newProjectedTestUIModel(client, nil, nil)

	if _, err := m.submitRuntimeUserMessage(context.Background(), "prompt"); err == nil {
		t.Fatal("expected submit runtime user message error")
	}
	if m.runtimeDisconnectStatusVisible() {
		t.Fatal("did not expect URL timeout to mark disconnect")
	}
}

func TestRuntimeControlOpTimeoutDoesNotMarkDisconnect(t *testing.T) {
	client := &runtimeControlFakeClient{submitErr: &net.OpError{Op: "read", Net: "tcp", Err: timeoutNetError{}}}
	m := newProjectedTestUIModel(client, nil, nil)

	if _, err := m.submitRuntimeUserMessage(context.Background(), "prompt"); err == nil {
		t.Fatal("expected submit runtime user message error")
	}
	if m.runtimeDisconnectStatusVisible() {
		t.Fatal("did not expect op timeout to mark disconnect")
	}
}
