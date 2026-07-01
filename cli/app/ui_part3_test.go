package app

import (
	"bytes"
	"context"
	"core/server/llm"
	"core/server/runtime"
	"core/shared/clientui"
	"errors"
	goruntime "runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDebugKeysTransientStatusShowsNormalizationSource(t *testing.T) {
	t.Setenv("KENT_DEBUG_KEYS", "1")
	m := newProjectedStaticUIModel()

	next, _ := m.Update(customKeyMsg{Kind: customKeyCtrlBackspace})
	updated := next.(*uiModel)

	status := strings.TrimSpace(updated.transientStatus)
	if status == "" {
		t.Fatal("expected debug key status to be set")
	}
	if !strings.Contains(status, "src=custom_key") {
		t.Fatalf("expected custom key source in debug status, got %q", status)
	}
	if !strings.Contains(status, "type=-1026") {
		t.Fatalf("expected normalized ctrl+backspace key type in debug status, got %q", status)
	}
}

func TestShowErrorStatusSetsErrorNoticeKind(t *testing.T) {
	m := newProjectedStaticUIModel()
	cmd := m.sendTransientStatusWithNoticeID("boom", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
	if cmd == nil {
		t.Fatal("expected clear command")
	}
	if m.transientStatus != "boom" {
		t.Fatalf("unexpected transient status %q", m.transientStatus)
	}
	if m.transientStatusKind != uiStatusNoticeError {
		t.Fatalf("expected error notice kind, got %d", m.transientStatusKind)
	}
}

func TestTransientStatusQueuePromotesNextNoticeAfterClear(t *testing.T) {
	m := newProjectedStaticUIModel()
	first := m.sendTransientStatusWithNoticeID("first", uiStatusNoticeSuccess, transientStatusDuration, uiStatusNoticeQueue, "")
	second := m.sendTransientStatusWithNoticeID("second", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeQueue, "")

	if first == nil {
		t.Fatal("expected first notice clear command")
	}
	if second != nil {
		t.Fatalf("expected queued notice to wait for active clear, got %T", second())
	}
	if m.transientStatus != "first" {
		t.Fatalf("active notice = %q, want first", m.transientStatus)
	}

	next, cmd := m.Update(clearTransientStatusMsg{token: m.transientStatusToken})
	updated := next.(*uiModel)
	if updated.transientStatus != "second" {
		t.Fatalf("promoted notice = %q, want second", updated.transientStatus)
	}
	if updated.transientStatusKind != uiStatusNoticeError {
		t.Fatalf("promoted notice kind = %d, want error", updated.transientStatusKind)
	}
	if cmd == nil {
		t.Fatal("expected promoted notice clear command")
	}
}

func TestTransientStatusQueueKeepsSameTextAndKindWithDifferentNoticeID(t *testing.T) {
	m := newProjectedStaticUIModel()
	first := m.sendTransientStatusWithNoticeID("same", uiStatusNoticeSuccess, transientStatusDuration, uiStatusNoticeQueue, "notice-1")
	second := m.sendTransientStatusWithNoticeID("same", uiStatusNoticeSuccess, transientStatusDuration, uiStatusNoticeQueue, "notice-2")

	if first == nil {
		t.Fatal("expected first notice clear command")
	}
	if second != nil {
		t.Fatalf("expected queued notice to wait for active clear, got %T", second())
	}
	if got := len(m.transientStatusQueue); got != 1 {
		t.Fatalf("queued notice count = %d, want 1", got)
	}
	if got := m.transientStatusQueue[0].NoticeID; got != "notice-2" {
		t.Fatalf("queued notice id = %q, want notice-2", got)
	}
}

func TestTransientStatusQueueDedupesSameTextKindAndNoticeID(t *testing.T) {
	m := newProjectedStaticUIModel()
	first := m.sendTransientStatusWithNoticeID("same", uiStatusNoticeSuccess, transientStatusDuration, uiStatusNoticeQueue, "notice-1")
	second := m.sendTransientStatusWithNoticeID("same", uiStatusNoticeSuccess, transientStatusDuration, uiStatusNoticeQueue, "notice-1")

	if first == nil {
		t.Fatal("expected first notice clear command")
	}
	if second != nil {
		t.Fatalf("expected duplicate active notice suppressed, got %T", second())
	}
	if got := len(m.transientStatusQueue); got != 0 {
		t.Fatalf("queued notice count = %d, want 0", got)
	}
}

func TestTransientStatusReplaceUpdatesActiveNoticeImmediately(t *testing.T) {
	m := newProjectedStaticUIModel()
	first := m.sendTransientStatusWithNoticeID("first", uiStatusNoticeSuccess, transientStatusDuration, uiStatusNoticeReplace, "")
	second := m.sendTransientStatusWithNoticeID("second", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")

	if first == nil || second == nil {
		t.Fatal("expected replacement notices to schedule clear commands")
	}
	if m.transientStatus != "second" {
		t.Fatalf("active notice = %q, want second", m.transientStatus)
	}
	if m.transientStatusKind != uiStatusNoticeError {
		t.Fatalf("active notice kind = %d, want error", m.transientStatusKind)
	}
}

func TestStartupUpdateNoticeShowsAvailableVersionOnce(t *testing.T) {
	client := &runtimeControlFakeClient{
		status: clientui.RuntimeStatus{
			Update: clientui.UpdateStatus{Checked: true, Available: true, LatestVersion: "1.2.3"},
		},
		sessionView: clientui.RuntimeSessionView{SessionID: "session-1"},
	}
	m := newProjectedTestUIModel(client, nil, nil, WithUIStartupUpdateNotice(true))

	msg := m.startupUpdateNoticeCmd(client.status.Update)()
	next, cmd := m.Update(msg)
	updated := next.(*uiModel)
	if updated.transientStatus != "update available: 1.2.3" {
		t.Fatalf("startup update notice = %q", updated.transientStatus)
	}
	if updated.transientStatusKind != uiStatusNoticeUpdateAvailable {
		t.Fatalf("startup update notice kind = %d, want update available", updated.transientStatusKind)
	}
	if cmd == nil {
		t.Fatal("expected update notice clear command")
	}

	next, _ = updated.Update(startupUpdateNoticeMsg{version: "1.2.4"})
	updated = next.(*uiModel)
	if updated.transientStatus != "update available: 1.2.3" {
		t.Fatalf("expected duplicate startup update notice suppressed, got %q", updated.transientStatus)
	}
}

func TestStartupUpdateNoticeMarksShownOnlyAfterDisplay(t *testing.T) {
	m := newProjectedStaticUIModel()
	initialClear := m.sendTransientStatusWithNoticeID("busy", uiStatusNoticeNeutral, transientStatusDuration, uiStatusNoticeReplace, "")
	if initialClear == nil {
		t.Fatal("expected initial notice clear command")
	}

	next, cmd := m.Update(startupUpdateNoticeMsg{version: "1.2.3"})
	updated := next.(*uiModel)
	if cmd != nil {
		t.Fatalf("expected queued update notice to wait for active clear, got %T", cmd())
	}
	if updated.startupUpdateShown {
		t.Fatal("did not expect startup update notice marked shown while queued")
	}
	if updated.transientStatus != "busy" {
		t.Fatalf("active notice = %q, want busy", updated.transientStatus)
	}

	next, cmd = updated.Update(clearTransientStatusMsg{token: updated.transientStatusToken})
	updated = next.(*uiModel)
	if updated.transientStatus != "update available: 1.2.3" {
		t.Fatalf("promoted startup update notice = %q", updated.transientStatus)
	}
	if !updated.startupUpdateShown {
		t.Fatal("expected startup update notice marked shown after promotion")
	}
	if cmd == nil {
		t.Fatal("expected promoted update notice clear command")
	}
}

func TestMainInputSupportsInlineCursorEditing(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "hello world"

	next := tea.Model(m)
	for range 5 {
		next, _ = next.(*uiModel).Update(tea.KeyMsg{Type: tea.KeyLeft})
	}
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("_")})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyHome})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(">")})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnd})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	updated = next.(*uiModel)

	if updated.input != ">hello _worl" {
		t.Fatalf("unexpected inline edit result: %q", updated.input)
	}
}

func TestMainInputSupportsWordNavigation(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "alpha beta gamma"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlLeft})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")})
	updated = next.(*uiModel)

	if updated.input != "alpha beta Xgamma" {
		t.Fatalf("expected ctrl+left insertion near word boundary, got %q", updated.input)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyLeft, Alt: true})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Y")})
	updated = next.(*uiModel)

	if updated.input != "alpha beta YXgamma" {
		t.Fatalf("expected alt+left insertion near previous word boundary, got %q", updated.input)
	}
}

func TestMainInputEditingUsesGraphemeBoundaries(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "a👍e\u0301b"
	m.inputCursor = len([]rune("a👍e\u0301"))

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	updated := next.(*uiModel)
	if updated.inputCursor != len([]rune("a👍")) {
		t.Fatalf("expected left to cross combining grapheme, got cursor %d", updated.inputCursor)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	updated = next.(*uiModel)
	if updated.input != "ae\u0301b" {
		t.Fatalf("expected backspace to delete emoji grapheme, got %q", updated.input)
	}
	if updated.inputCursor != len([]rune("a")) {
		t.Fatalf("expected cursor after emoji delete at rune 1, got %d", updated.inputCursor)
	}
}

func TestMainInputDeleteKillAndYankShortcuts(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "alpha beta gamma"
	m.inputCursor = len([]rune("alpha beta"))

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlW})
	updated := next.(*uiModel)
	if updated.input != "alpha  gamma" {
		t.Fatalf("expected ctrl+w to delete previous word, got %q", updated.input)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	updated = next.(*uiModel)
	if updated.input != "alpha beta gamma" {
		t.Fatalf("expected ctrl+y to yank killed word, got %q", updated.input)
	}

	updated.inputCursor = len([]rune("alpha "))
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDelete})
	updated = next.(*uiModel)
	if updated.input != "alpha eta gamma" {
		t.Fatalf("expected delete to remove next grapheme, got %q", updated.input)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	updated = next.(*uiModel)
	if updated.input != "alpha " {
		t.Fatalf("expected ctrl+k to kill to line end, got %q", updated.input)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	updated = next.(*uiModel)
	if updated.input != "alpha eta gamma" {
		t.Fatalf("expected ctrl+y to yank killed suffix, got %q", updated.input)
	}

	if goruntime.GOOS != "darwin" {
		next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
		updated = next.(*uiModel)
		if updated.input != "" {
			t.Fatalf("expected ctrl+u to kill to line start on non-darwin paths, got %q", updated.input)
		}
	}
}

func TestMainInputUpDownSingleLineMoveToStartAndEnd(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "abcd"
	m.inputCursor = 2

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated := next.(*uiModel)
	if updated.inputCursor != 0 {
		t.Fatalf("expected up to move cursor to start on single line, got %d", updated.inputCursor)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)
	if updated.inputCursor != len([]rune(updated.input)) {
		t.Fatalf("expected down to move cursor to end on single line, got %d", updated.inputCursor)
	}
}

func TestMainInputUpDownMultilineMoveAcrossLines(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "1111\n22\n3333"
	m.inputCursor = -1 // end of the input

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated := next.(*uiModel)
	if updated.inputCursor != 7 {
		t.Fatalf("expected first up to land on previous line end, got %d", updated.inputCursor)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated = next.(*uiModel)
	if updated.inputCursor != 2 {
		t.Fatalf("expected second up to keep column on first line, got %d", updated.inputCursor)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)
	if updated.inputCursor != 7 {
		t.Fatalf("expected down to return to second line at same column, got %d", updated.inputCursor)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)
	if updated.inputCursor != 10 {
		t.Fatalf("expected second down to land on third line at same column, got %d", updated.inputCursor)
	}
}

func TestMainInputUpDownMovesAcrossWrappedVisualLines(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 5
	m.input = "abcd efgh ijkl"
	m.inputCursor = -1

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated := next.(*uiModel)
	if updated.inputCursor != len([]rune("abcd efgh")) {
		t.Fatalf("expected up to land on previous wrapped row, got %d", updated.inputCursor)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated = next.(*uiModel)
	if updated.inputCursor != len([]rune("abcd")) {
		t.Fatalf("expected second up to land on first wrapped row, got %d", updated.inputCursor)
	}
}

func TestPromptHistoryUpDownBrowseSubmittedPrompts(t *testing.T) {
	m := newProjectedStaticUIModel(
		WithUIPromptHistory([]string{"first prompt", "second line\nthird line", "/resume"}),
	)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated := next.(*uiModel)
	if updated.input != "/resume" {
		t.Fatalf("expected newest prompt selected first, got %q", updated.input)
	}
	if clampCursor(updated.inputCursor, len([]rune(updated.input))) != len([]rune(updated.input)) {
		t.Fatalf("expected history recall to place cursor at end, got %d", clampCursor(updated.inputCursor, len([]rune(updated.input))))
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated = next.(*uiModel)
	if updated.input != "second line\nthird line" {
		t.Fatalf("expected previous prompt selected, got %q", updated.input)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)
	if updated.input != "/resume" {
		t.Fatalf("expected down to move toward newer prompt, got %q", updated.input)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)
	if updated.input != "" {
		t.Fatalf("expected down past newest to restore draft, got %q", updated.input)
	}
	if updated.inputCursor != -1 {
		t.Fatalf("expected restored empty draft to track tail cursor, got %d", updated.inputCursor)
	}
}

func TestPromptHistoryUpCanEnterFromNewDraftAndRestoreItAfterReuse(t *testing.T) {
	m := newProjectedStaticUIModel(
		WithUIPromptHistory([]string{"hello"}),
	)
	m.input = "world"
	m.inputCursor = -1

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated := next.(*uiModel)
	if updated.input != "world" {
		t.Fatalf("expected first up from draft tail to stay on draft, got %q", updated.input)
	}
	if updated.inputCursor != 0 {
		t.Fatalf("expected first up from draft tail to move cursor to start, got %d", updated.inputCursor)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated = next.(*uiModel)
	if updated.input != "hello" {
		t.Fatalf("expected second up from draft start to recall history, got %q", updated.input)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyHome})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Hi!!")})
	updated = next.(*uiModel)
	if updated.input != "Hi!!hello" {
		t.Fatalf("expected edited recalled prompt, got %q", updated.input)
	}

	next, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	if updated.input != "world" {
		t.Fatalf("expected parked draft restored after submitting recalled prompt, got %q", updated.input)
	}
	if cmd == nil {
		t.Fatal("expected submission command")
	}
}

func TestPromptHistoryUpFromMultilineDraftTailMovesWithinDraftBeforeRecall(t *testing.T) {
	m := newProjectedStaticUIModel(
		WithUIPromptHistory([]string{"hello"}),
	)
	m.input = "one\ntwo"
	m.inputCursor = -1

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated := next.(*uiModel)
	if updated.input != "one\ntwo" {
		t.Fatalf("expected first up from multiline draft tail to stay on draft, got %q", updated.input)
	}
	if updated.inputCursor != 3 {
		t.Fatalf("expected first up from multiline draft tail to move to previous line, got %d", updated.inputCursor)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated = next.(*uiModel)
	if updated.input != "one\ntwo" {
		t.Fatalf("expected second up within multiline draft to stay on draft, got %q", updated.input)
	}
	if updated.inputCursor != 0 {
		t.Fatalf("expected second up within multiline draft to reach buffer start, got %d", updated.inputCursor)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated = next.(*uiModel)
	if updated.input != "hello" {
		t.Fatalf("expected third up from multiline draft start to recall history, got %q", updated.input)
	}
}

func TestPromptHistoryUsesBoundaryNavigationForMultilineSelection(t *testing.T) {
	m := newProjectedStaticUIModel(
		WithUIPromptHistory([]string{"one\ntwo\nthree", "older"}),
	)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated := next.(*uiModel)
	if updated.input != "older" {
		t.Fatalf("expected newest prompt selected, got %q", updated.input)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated = next.(*uiModel)
	if updated.input != "one\ntwo\nthree" {
		t.Fatalf("expected multiline prompt selected, got %q", updated.input)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyHome})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)
	if updated.input != "older" {
		t.Fatalf("expected down at buffer start to browse newer history, got %q", updated.input)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyHome})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRight})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)
	if updated.input != "one\ntwo\nthree" {
		t.Fatalf("expected down after sideways edit intent to stay in selected prompt, got %q", updated.input)
	}
	if updated.inputCursor != 5 {
		t.Fatalf("expected down to move within multiline prompt after leaving history mode, got %d", updated.inputCursor)
	}
}

func TestPromptHistoryBellWritesRawTerminalBell(t *testing.T) {
	var out bytes.Buffer
	previous := writeTerminalSequence
	writeTerminalSequence = func(sequence string) {
		_, _ = out.WriteString(sequence)
	}
	t.Cleanup(func() {
		writeTerminalSequence = previous
	})

	m := newProjectedStaticUIModel(
		WithUIPromptHistory([]string{"only prompt"}),
	)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated := next.(*uiModel)
	next, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected bell command")
	}
	_ = cmd()

	if got := out.String(); got != terminalBell {
		t.Fatalf("expected raw terminal bell, got %q", got)
	}
	if updated.input != "only prompt" {
		t.Fatalf("expected prompt selection unchanged after bell miss, got %q", updated.input)
	}
}

func TestInterruptedQueuedPromptDoesNotEnterLocalHistoryBeforeFlush(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.input = "queued later"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated := next.(*uiModel)
	if len(updated.promptHistory) != 0 {
		t.Fatalf("expected no local prompt history before queued prompt flushes, got %+v", updated.promptHistory)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	updated = next.(*uiModel)
	updated = applyInterruptedRunStateForTest(t, updated)
	if len(updated.promptHistory) != 0 {
		t.Fatalf("expected interrupted queued prompt not to enter local history, got %+v", updated.promptHistory)
	}
	if updated.input != "queued later" {
		t.Fatalf("expected queued draft restored after interrupt, got %q", updated.input)
	}
}

func TestRuntimeClientSubmitShowsUserMessageInTranscriptWhenFlushedEventArrives(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true
	m.input = "say hi"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if updated.activeSubmit.text != "say hi" {
		t.Fatalf("expected active submit text preserved, got %q", updated.activeSubmit.text)
	}

	cmd := updated.runtimeAdapter().applyProjectedRuntimeEvent(projectRuntimeEvent(runtime.Event{
		Kind:        runtime.EventUserMessageFlushed,
		StepID:      "step-1",
		UserMessage: "say hi",
	})).
		cmd
	if got := len(updated.transcriptEntries); got != 1 {
		t.Fatalf("expected one transcript entry after flushed user message, got %d", got)
	}
	if updated.transcriptEntries[0].Role != "user" || updated.transcriptEntries[0].Text != "say hi" {
		t.Fatalf("unexpected transcript entry: %+v", updated.transcriptEntries[0])
	}
	if updated.transcriptEntries[0].Transient != true {
		t.Fatalf("expected runtime-backed flushed user message to stay transient until hydrate, got %+v", updated.transcriptEntries[0])
	}
	msgs := collectCmdMessages(t, cmd)
	refreshFound := false
	for _, msg := range msgs {
		if _, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			refreshFound = true
			break
		}
	}
	if refreshFound {
		t.Fatalf("did not expect flushed runtime user message to trigger transcript hydration, got %+v", msgs)
	}
}

func TestSubmitDoneKeepsDuplicateQueuedPromptsInOrder(t *testing.T) {
	client := &runtimeAdapterFakeClient{}
	_, eng := newAppRuntimeEngine(t, client, runtime.Config{})

	m := newProjectedEngineUIModel(eng)
	m.input = "continue"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	updated.queued = append(updated.queued, queuedInputsForTest("fix", "continue")...)

	done := submitDoneMsg{token: updated.activeSubmit.token, submittedText: "continue"}
	next, _ = updated.Update(done)
	updated = next.(*uiModel)
	if len(updated.queued) != 2 {
		t.Fatalf("expected two queued prompts to remain, got %+v", updated.queued)
	}
	if updated.queued[0].Text != "fix" || updated.queued[1].Text != "continue" {
		t.Fatalf("expected duplicate queued prompts to preserve order, got %+v", updated.queued)
	}
}

func TestCtrlCWhileSubmitRestoresQueuedDraft(t *testing.T) {
	_, eng := newAppRuntimeEngine(t, &runtimeAdapterFakeClient{}, runtime.Config{})

	m := newProjectedEngineUIModel(eng)
	m.input = "continue"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	updated = next.(*uiModel)
	if !updated.isBusy() || !updated.hasPendingInterrupt() {
		t.Fatal("expected ctrl+c to wait for server interrupted run state")
	}
	updated = applyInterruptedRunStateForTest(t, updated)
	if updated.isBusy() {
		t.Fatal("expected busy=false after ctrl+c during submit")
	}
	if updated.activity != uiActivityInterrupted {
		t.Fatalf("expected interrupted activity, got %v", updated.activity)
	}
	if updated.input != "continue" {
		t.Fatalf("expected draft restored after ctrl+c, got %q", updated.input)
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected queued draft restored into input and cleared, got %+v", updated.queued)
	}
}

func TestCtrlCWhileGoalRunWaitsForInterruptedRunStateBeforeCleanup(t *testing.T) {
	client := &runtimeControlFakeClient{status: clientui.RuntimeStatus{
		Goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusActive},
	}}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.setGoalRun(true)
	m.activity = uiActivityRunning
	m.queued = queuedInputsForTest("queued while goal runs")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	updated := next.(*uiModel)
	if !updated.isBusy() || !updated.hasPendingInterrupt() {
		t.Fatal("expected ctrl+c to request an interrupt while goal run is busy")
	}

	for _, msg := range collectCmdMessages(t, cmd) {
		if typed, ok := msg.(runtimeControlDoneMsg); ok {
			next, _ = updated.Update(typed)
			updated = next.(*uiModel)
		}
	}

	if client.interruptCalls != 1 {
		t.Fatalf("interrupt calls = %d, want 1", client.interruptCalls)
	}
	if !updated.isBusy() || !updated.hasPendingInterrupt() {
		t.Fatalf("interrupt control acknowledgement must wait for run-state cleanup, busy=%t pending=%t", updated.isBusy(), updated.hasPendingInterrupt())
	}
	if updated.input != "" || len(updated.queued) != 1 {
		t.Fatalf("interrupt control acknowledgement restored input before run-state event, input=%q queue=%+v", updated.input, updated.queued)
	}

	updated = applyInterruptedRunStateForTest(t, updated)
	if updated.isBusy() || updated.hasPendingInterrupt() {
		t.Fatalf("expected interrupted run-state to unblock CLI, busy=%t pending=%t", updated.isBusy(), updated.hasPendingInterrupt())
	}
	if updated.activity != uiActivityInterrupted {
		t.Fatalf("activity = %v, want interrupted", updated.activity)
	}
	if updated.input != "queued while goal runs" || len(updated.queued) != 0 {
		t.Fatalf("expected queued goal draft restored into input, input=%q queue=%+v", updated.input, updated.queued)
	}
}

func TestCtrlCInterruptControlErrorKeepsPendingCleanupForLaterRunState(t *testing.T) {
	client := &runtimeControlFakeClient{interruptErr: errors.New("interrupt transport failed")}
	m := newProjectedStaticUIModel()
	m.engine = client
	m.setBusy(true)
	m.queued = queuedInputsForTest("queued after delayed interrupt")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	updated := next.(*uiModel)
	for _, msg := range collectCmdMessages(t, cmd) {
		if typed, ok := msg.(runtimeControlDoneMsg); ok {
			next, _ = updated.Update(typed)
			updated = next.(*uiModel)
		}
	}

	if !updated.hasPendingInterrupt() {
		t.Fatal("interrupt control error must keep pending cleanup for a later interrupted run-state event")
	}
	if !updated.isBusy() {
		t.Fatal("failed interrupt control call must not locally mark the run idle")
	}
	if updated.input != "" || len(updated.queued) != 1 {
		t.Fatalf("failed interrupt control call restored input early: input=%q queue=%+v", updated.input, updated.queued)
	}

	updated = applyInterruptedRunStateForTest(t, updated)
	if updated.isBusy() || updated.hasPendingInterrupt() {
		t.Fatalf("later interrupted run-state must finish cleanup, busy=%t pending=%t", updated.isBusy(), updated.hasPendingInterrupt())
	}
	if updated.input != "queued after delayed interrupt" || len(updated.queued) != 0 {
		t.Fatalf("later interrupted run-state must restore queued input, input=%q queue=%+v", updated.input, updated.queued)
	}
}

func TestActiveSubmitErrorRestoresQueuedSteeringInput(t *testing.T) {
	_, eng := newAppRuntimeEngine(t, &runtimeAdapterFakeClient{}, runtime.Config{})

	m := newProjectedEngineUIModel(eng)
	m.input = "continue"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	updated.input = "later"

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	if updated.isInputSubmitLocked() {
		t.Fatal("did not expect follow-up enter during submit to lock input")
	}
	if updated.input != "" {
		t.Fatalf("expected queued steering input cleared immediately, got %q", updated.input)
	}
	if len(updated.pendingInjected) != 1 || updated.pendingInjected[0].Text != "later" {
		t.Fatalf("expected pending injected follow-up recorded, got %+v", updated.pendingInjected)
	}

	next, _ = updated.Update(submitDoneMsg{token: updated.activeSubmit.token, submittedText: "continue", err: errors.New("submit failed")})
	updated = next.(*uiModel)
	if updated.isInputSubmitLocked() {
		t.Fatal("did not expect submit error to leave input locked")
	}
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("expected pending injected follow-up cleared, got %+v", updated.pendingInjected)
	}
	if updated.input != "later\n\ncontinue" {
		t.Fatalf("expected restored prompt and unlocked follow-up draft, got %q", updated.input)
	}
}

func TestActiveSubmitErrorRestoresQueuedSteeringAndDiscardsEngineQueue(t *testing.T) {
	client := &requestCaptureFakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "ok"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	_, eng := newAppRuntimeEngine(t, client, runtime.Config{})

	m := newProjectedEngineUIModel(eng)
	m.input = "continue"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	updated.input = "later"
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	next, _ = updated.Update(submitDoneMsg{token: updated.activeSubmit.token, submittedText: "continue", err: errors.New("submit failed")})
	updated = next.(*uiModel)

	if updated.input != "later\n\ncontinue" {
		t.Fatalf("expected restored steering and submit text in input, got %q", updated.input)
	}
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("expected UI pending steering cleared after restore, got %+v", updated.pendingInjected)
	}

	msg, err := eng.SubmitUserMessage(context.Background(), "fresh prompt")
	if err != nil {
		t.Fatalf("submit fresh prompt: %v", err)
	}
	if msg.Content != "ok" {
		t.Fatalf("assistant content = %q, want ok", msg.Content)
	}
	requests := client.Requests()
	if len(requests) != 1 {
		t.Fatalf("expected one model request without stale runtime steering, got %d", len(requests))
	}
	for _, message := range llm.MessagesFromItems(requests[0].Items) {
		if message.Role == llm.RoleUser && message.Content == "later" {
			t.Fatalf("did not expect restored steering to remain queued in runtime request: %+v", llm.MessagesFromItems(requests[0].Items))
		}
	}
}

func TestAskFreeformAcceptsSpaceKey(t *testing.T) {
	m := newProjectedStaticUIModel()
	reply := make(chan askReply, 1)
	event := askEvent{req: clientui.PendingPromptEvent{Question: "Type answer"}, reply: reply}

	next, _ := m.Update(askEventMsg{event: event})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeySpace})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("world")})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)

	resp := <-reply
	if resp.response.Answer != "hello world" {
		t.Fatalf("expected freeform answer with space, got %q", resp.response.Answer)
	}
	if testActiveAsk(updated) != nil {
		t.Fatal("ask should be resolved")
	}
}

func TestApprovalAskTabInCommentaryDoesNotReturnToPicker(t *testing.T) {
	m := newProjectedStaticUIModel()
	reply := make(chan askReply, 1)
	event := askEvent{req: clientui.PendingPromptEvent{Question: "Approve?", Approval: true, ApprovalOptions: []clientui.ApprovalOption{{Decision: clientui.ApprovalDecisionAllowOnce, Label: "Allow once"}, {Decision: clientui.ApprovalDecisionAllowSession, Label: "Allow for this session"}, {Decision: clientui.ApprovalDecisionDeny, Label: "Deny"}}}, reply: reply}

	next, _ := m.Update(askEventMsg{event: event})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated = next.(*uiModel)
	if !testAskFreeform(updated) {
		t.Fatal("expected approval tab to enter commentary mode")
	}
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("commentary")})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated = next.(*uiModel)
	if !testAskFreeform(updated) {
		t.Fatal("did not expect approval commentary tab to return to picker")
	}
	if testAskInput(updated) != "commentary" {
		t.Fatalf("expected commentary preserved in approval freeform, got %q", testAskInput(updated))
	}
}

func TestApprovalAskTabCommentaryUsesCurrentSelection(t *testing.T) {
	_, eng := newAppRuntimeEngine(t, statusLineFakeClient{}, runtime.Config{ContextWindowTokens: 400_000})
	m := newProjectedEngineUIModel(eng)
	m.setBusy(true)
	reply := make(chan askReply, 1)
	event := askEvent{req: clientui.PendingPromptEvent{Question: "Approve?", Approval: true, ApprovalOptions: []clientui.ApprovalOption{{Decision: clientui.ApprovalDecisionAllowOnce, Label: "Allow once"}, {Decision: clientui.ApprovalDecisionAllowSession, Label: "Allow for this session"}, {Decision: clientui.ApprovalDecisionDeny, Label: "Deny"}}}, reply: reply}

	next, _ := m.Update(askEventMsg{event: event})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("session only")})
	updated = next.(*uiModel)
	next, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected approval commentary queue command")
	}
	select {
	case <-reply:
		t.Fatal("did not expect approval answer before commentary queue command completes")
	default:
	}
	for _, msg := range collectCmdMessages(t, cmd) {
		next, cmd = updated.Update(msg)
		updated = next.(*uiModel)
	}

	resp := <-reply
	if resp.response.Approval == nil {
		t.Fatal("expected typed approval response")
	}
	if resp.response.Approval.Decision != clientui.ApprovalDecisionAllowSession || resp.response.Approval.Commentary != "session only" {
		t.Fatalf("unexpected approval response: %+v", resp.response.Approval)
	}
	if len(updated.pendingInjected) != 1 || updated.pendingInjected[0].Text != "session only" {
		t.Fatalf("expected selected approval commentary injected, got %+v", updated.pendingInjected)
	}
}

func TestApprovalAskPickerSubmitIgnoresPendingCommentaryDraft(t *testing.T) {
	m := newProjectedStaticUIModel()
	reply := make(chan askReply, 1)
	event := askEvent{req: clientui.PendingPromptEvent{Question: "Approve?", Approval: true, ApprovalOptions: []clientui.ApprovalOption{{Decision: clientui.ApprovalDecisionAllowOnce, Label: "Allow once"}, {Decision: clientui.ApprovalDecisionAllowSession, Label: "Allow for this session"}, {Decision: clientui.ApprovalDecisionDeny, Label: "Deny"}}}, reply: reply}

	next, _ := m.Update(askEventMsg{event: event})
	updated := next.(*uiModel)
	testSetAskInput(updated, "stale commentary")
	testSetAskInputCursor(updated, len([]rune(testAskInput(updated))))
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)

	resp := <-reply
	if resp.response.Approval == nil {
		t.Fatal("expected typed approval response")
	}
	if resp.response.Approval.Decision != clientui.ApprovalDecisionAllowSession {
		t.Fatalf("unexpected approval decision: %+v", resp.response.Approval)
	}
	if resp.response.Approval.Commentary != "" {
		t.Fatalf("did not expect picker submission to include commentary draft, got %+v", resp.response.Approval)
	}
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("did not expect picker submission to inject commentary, got %+v", updated.pendingInjected)
	}
}

func TestBusyInputRemainsEditableUntilSubmitLock(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.input = "seed"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeySpace})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	updated = next.(*uiModel)

	if updated.input != "seedx" {
		t.Fatalf("expected input to remain editable while busy, got %q", updated.input)
	}
	if strings.Contains(updated.View(), "input locked while agent is running") {
		t.Fatalf("did not expect legacy locked hint in view: %q", updated.View())
	}
}

func TestViewRendersOverlayCursorWithoutShiftingText(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 40
	m.termHeight = 16
	m.windowSizeKnown = true
	m.input = "hello world"
	m.layout().syncViewport()

	view := m.View()
	if !strings.Contains(view, ansiHideCursor) {
		t.Fatalf("expected terminal cursor hidden in view: %q", view)
	}
	plain := stripANSIAndTrimRight(view)
	if !strings.Contains(plain, "› hello world") {
		t.Fatalf("expected input text preserved in view, got %q", plain)
	}
}

func TestViewCursorMovementDoesNotDropCharacters(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 40
	m.termHeight = 16
	m.windowSizeKnown = true
	m.input = "hello"
	m.inputCursor = 2
	m.layout().syncViewport()

	plain := stripANSIAndTrimRight(m.View())
	if !strings.Contains(plain, "› hello") {
		t.Fatalf("expected all characters preserved while moving cursor, got %q", plain)
	}
}

func TestViewHidesCursorWhenInputLocked(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 40
	m.termHeight = 16
	m.windowSizeKnown = true
	m.setInputSubmitLocked(true)
	m.input = "hello world"
	m.layout().syncViewport()

	view := m.View()
	if !strings.Contains(view, ansiHideCursor) {
		t.Fatalf("expected terminal cursor hide sequence in view: %q", view)
	}
	plain := stripANSIAndTrimRight(view)
	if !strings.Contains(plain, "⨯ hello world") {
		t.Fatalf("expected locked input text preserved, got %q", plain)
	}
}
