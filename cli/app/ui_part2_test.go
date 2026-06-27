package app

import (
	"core/cli/tui"
	"core/server/llm"
	"core/server/runtime"
	"core/shared/clientui"
	"errors"
	"fmt"
	goruntime "runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRollbackSelectionUsesAbsoluteTranscriptEntryIndexWhenPaged(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 14
	m.windowSizeKnown = true
	m.transcriptEntries = []tui.TranscriptEntry{
		{Role: "user", Text: "u-100"},
		{Role: "assistant", Text: "a-100"},
		{Role: "user", Text: "u-101"},
	}
	m.transcriptBaseOffset = 200
	m.transcriptTotalEntries = 203
	m.forwardToView(tui.SetConversationMsg{BaseOffset: m.transcriptBaseOffset, TotalEntries: m.transcriptTotalEntries, Entries: m.transcriptEntries})
	seedTestRollbackTargets(m)
	m.layout().syncViewport()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated = next.(*uiModel)

	if !testRollbackSelecting(updated) {
		t.Fatal("expected rollback selection mode after double esc")
	}
	selectedLine := lineContaining(updated.View(), "u-101")
	if selectedLine == "" {
		t.Fatalf("expected selected paged rollback message visible, got %q", stripANSIAndTrimRight(updated.View()))
	}
	if !strings.Contains(selectedLine, themeSelectionBackgroundEscape(updated.theme)) {
		t.Fatalf("expected paged rollback selection highlight, got %q", selectedLine)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	if !testRollbackEditing(updated) {
		t.Fatal("expected rollback editing mode after enter")
	}
	if updated.input != "u-101" {
		t.Fatalf("expected selected paged message loaded into input, got %q", updated.input)
	}

	updated.input = "edited paged user message"
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	if updated.nextForkRollbackTargetID != rollbackTargetIDForTestSelection(202) {
		t.Fatalf("expected rollback target id, got %q", updated.nextForkRollbackTargetID)
	}
}

func TestRollbackRefreshClearsPendingPageSelectionOutsideSelectionMode(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.transcriptEntries = []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "older"},
		{Role: tui.TranscriptRoleAssistant, Text: "answer"},
		{Role: tui.TranscriptRoleUser, Text: "newer"},
	}
	seedTestRollbackTargets(m)
	m.rollback.phase = uiRollbackPhaseEditing
	m.rollback.selection = 1
	m.rollback.pendingSelectionAnchor = 2
	m.rollback.pendingSelectionDelta = -1

	m.refreshRollbackCandidates()

	if m.rollback.pendingSelectionAnchor != -1 || m.rollback.pendingSelectionDelta != 0 {
		t.Fatalf("pending page selection not cleared: anchor=%d delta=%d", m.rollback.pendingSelectionAnchor, m.rollback.pendingSelectionDelta)
	}
	if m.rollback.selection != 1 {
		t.Fatalf("selection changed outside selection mode: got %d want 1", m.rollback.selection)
	}
}

func TestRollbackSelectionRecentersTranscript(t *testing.T) {
	entries := make([]UITranscriptEntry, 0, 80)
	for i := 0; i < 40; i++ {
		entries = append(entries, UITranscriptEntry{Role: "user", Text: fmt.Sprintf("u-%d", i)})
		entries = append(entries, UITranscriptEntry{Role: "assistant", Text: fmt.Sprintf("a-%d", i)})
	}
	m := newProjectedStaticUIModel(WithUIInitialTranscript(entries))
	m.termWidth = 100
	m.termHeight = 8
	m.layout().syncViewport()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated = next.(*uiModel)
	if updated.view.Mode() != tui.ModeDetail {
		t.Fatalf("expected rollback selection in detail overlay, got mode %q", updated.view.Mode())
	}

	for i := 0; i < 8; i++ {
		next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyUp})
		updated = next.(*uiModel)
	}

	selected := testRollbackCandidates(updated)[testRollbackSelection(updated)].Text
	lines := strings.Split(stripANSIAndTrimRight(updated.view.View()), "\n")
	selectedLine := -1
	for idx, line := range lines {
		if strings.Contains(line, selected) {
			selectedLine = idx
			break
		}
	}
	if selectedLine < 0 {
		t.Fatalf("expected selected rollback message %q visible in viewport", selected)
	}
	mid := len(lines) / 2
	if diff := absInt(selectedLine - mid); diff > 2 {
		t.Fatalf("expected selected rollback message near viewport middle, line=%d mid=%d", selectedLine, mid)
	}
}

func TestRollbackSelectionEdgeArrowRecentersWhenNoPageAvailable(t *testing.T) {
	entries := make([]UITranscriptEntry, 0, 80)
	for i := 0; i < 40; i++ {
		entries = append(entries, UITranscriptEntry{Role: "user", Text: fmt.Sprintf("u-%d", i)})
		entries = append(entries, UITranscriptEntry{Role: "assistant", Text: fmt.Sprintf("a-%d", i)})
	}
	m := newProjectedStaticUIModel(WithUIInitialTranscript(entries))
	m.termWidth = 100
	m.termHeight = 8
	m.layout().syncViewport()

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if !testRollbackSelecting(m) {
		t.Fatal("expected rollback selection mode")
	}
	for testRollbackSelection(m) > 0 {
		m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyUp})
	}
	selected := testRollbackCandidates(m)[testRollbackSelection(m)].Text
	for i := 0; i < 6; i++ {
		m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	}
	if got := m.view.DetailScroll(); got == 0 {
		t.Fatalf("expected page down to move detail scroll away from focused edge, got %d", got)
	}
	if strings.Contains(stripANSIAndTrimRight(m.view.View()), selected) {
		t.Fatalf("expected page down to move selected rollback point out of view, got %q", stripANSIAndTrimRight(m.view.View()))
	}

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.view.DetailScroll(); got != 0 {
		t.Fatalf("expected edge up fallback to restore focused clamped detail scroll, got %d", got)
	}
	lines := strings.Split(stripANSIAndTrimRight(m.view.View()), "\n")
	selectedLine := -1
	for idx, line := range lines {
		if strings.Contains(line, selected) {
			selectedLine = idx
			break
		}
	}
	if selectedLine < 0 {
		t.Fatalf("expected edge up to refocus selected rollback point %q, got %q", selected, stripANSIAndTrimRight(m.view.View()))
	}
}

func TestRollbackSelectionCancelRestoresPriorOngoingScroll(t *testing.T) {
	entries := make([]UITranscriptEntry, 0, 120)
	for i := 0; i < 60; i++ {
		entries = append(entries, UITranscriptEntry{Role: "user", Text: fmt.Sprintf("u-%d", i)})
		entries = append(entries, UITranscriptEntry{Role: "assistant", Text: fmt.Sprintf("a-%d", i)})
	}
	m := newProjectedStaticUIModel(WithUIInitialTranscript(entries))
	m.termWidth = 100
	m.termHeight = 10
	m.layout().syncViewport()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	updated := next.(*uiModel)
	initialScroll := updated.view.OngoingScroll()
	if initialScroll <= 0 {
		t.Fatalf("expected non-zero ongoing scroll after page up, got %d", initialScroll)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated = next.(*uiModel)
	if !testRollbackSelecting(updated) {
		t.Fatal("expected rollback mode after double esc")
	}
	if updated.view.Mode() != tui.ModeDetail {
		t.Fatalf("expected rollback selection in detail overlay, got mode %q", updated.view.Mode())
	}

	for i := 0; i < 6; i++ {
		next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyUp})
		updated = next.(*uiModel)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated = next.(*uiModel)
	if testRollbackSelecting(updated) {
		t.Fatal("expected rollback mode to be canceled")
	}
	if updated.view.Mode() != tui.ModeOngoing {
		t.Fatalf("expected return to ongoing mode, got %q", updated.view.Mode())
	}
}

func TestRollbackTransitionsUseDetailOverlayInNativeMode(t *testing.T) {
	m := newProjectedStaticUIModel(
		WithUIInitialTranscript([]UITranscriptEntry{{Role: "user", Text: "u1"}, {Role: "assistant", Text: "a1"}, {Role: "user", Text: "u2"}}),
	)
	m.termWidth = 100
	m.termHeight = 10
	m.layout().syncViewport()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := next.(*uiModel)
	next, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated = next.(*uiModel)
	if !testRollbackSelecting(updated) {
		t.Fatal("expected rollback mode after double esc")
	}
	if updated.view.Mode() != tui.ModeDetail {
		t.Fatalf("expected rollback selection in detail overlay, got mode %q", updated.view.Mode())
	}
	if !testRollbackSelectionSurfaceActive(updated) {
		t.Fatal("expected rollback selection surface in native mode")
	}
	if cmd == nil {
		t.Fatal("expected native rollback entry to emit detail overlay transition command")
	}

	selected := testRollbackCandidates(updated)[testRollbackSelection(updated)].Text
	lines := strings.Split(stripANSIAndTrimRight(updated.View()), "\n")
	selectedLine := -1
	for idx, line := range lines {
		if strings.Contains(line, selected) {
			selectedLine = idx
			break
		}
	}
	if selectedLine < 0 {
		t.Fatalf("expected selected rollback message %q visible in detail overlay", selected)
	}
	mid := len(lines) / 2
	if diff := absInt(selectedLine - mid); diff > 2 {
		t.Fatalf("expected selected rollback message near overlay center, line=%d mid=%d", selectedLine, mid)
	}

	next, cmd = updated.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated = next.(*uiModel)
	if testRollbackSelecting(updated) {
		t.Fatal("expected rollback mode canceled")
	}
	if updated.view.Mode() != tui.ModeOngoing {
		t.Fatalf("expected cancel to return to ongoing mode, got %q", updated.view.Mode())
	}
	if testRollbackSelectionSurfaceActive(updated) {
		t.Fatal("expected rollback selection surface cleared after cancel")
	}
	if cmd == nil {
		t.Fatal("expected native rollback cancel to emit detail overlay exit command")
	}
}

func TestRollbackEditCancelChainRestoresPriorOngoingScroll(t *testing.T) {
	entries := make([]UITranscriptEntry, 0, 120)
	for i := 0; i < 60; i++ {
		entries = append(entries, UITranscriptEntry{Role: "user", Text: fmt.Sprintf("u-%d", i)})
		entries = append(entries, UITranscriptEntry{Role: "assistant", Text: fmt.Sprintf("a-%d", i)})
	}
	m := newProjectedStaticUIModel(WithUIInitialTranscript(entries))
	m.termWidth = 100
	m.termHeight = 10
	m.layout().syncViewport()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	updated := next.(*uiModel)
	initialScroll := updated.view.OngoingScroll()
	if initialScroll <= 0 {
		t.Fatalf("expected non-zero ongoing scroll after page up, got %d", initialScroll)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated = next.(*uiModel)
	if !testRollbackSelecting(updated) {
		t.Fatal("expected rollback mode after double esc")
	}
	if updated.view.Mode() != tui.ModeDetail {
		t.Fatalf("expected rollback selection in detail overlay, got mode %q", updated.view.Mode())
	}
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	if !testRollbackEditing(updated) {
		t.Fatal("expected rollback editing mode after enter")
	}

	updated.input = ""
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated = next.(*uiModel)
	if !testRollbackSelecting(updated) {
		t.Fatal("expected rollback selection mode after esc on empty edit input")
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated = next.(*uiModel)
	if testRollbackSelecting(updated) {
		t.Fatal("expected rollback mode canceled")
	}
	if updated.view.Mode() != tui.ModeOngoing {
		t.Fatalf("expected return to ongoing mode, got %q", updated.view.Mode())
	}

	beforeAppend := updated.view.OngoingScroll()
	updated.forwardToView(tui.AppendTranscriptMsg{Role: "assistant", Text: "new tail"})
	afterAppend := updated.view.OngoingScroll()
	if afterAppend < beforeAppend {
		t.Fatalf("expected append not to move ongoing scroll away from tail, got %d from %d", afterAppend, beforeAppend)
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func TestApprovalAskTabAllowsWithCommentary(t *testing.T) {
	_, eng := newAppRuntimeEngine(t, statusLineFakeClient{}, runtime.Config{ContextWindowTokens: 400_000})
	m := newProjectedEngineUIModel(eng)
	m.setBusy(true)
	reply := make(chan askReply, 1)
	event := askEvent{req: clientui.PendingPromptEvent{Question: "Approve?", Approval: true, ApprovalOptions: []clientui.ApprovalOption{{Decision: clientui.ApprovalDecisionAllowOnce, Label: "Allow once"}, {Decision: clientui.ApprovalDecisionAllowSession, Label: "Allow for this session"}, {Decision: clientui.ApprovalDecisionDeny, Label: "Deny"}}}, reply: reply}

	next, _ := m.Update(askEventMsg{event: event})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated = next.(*uiModel)
	if !testAskFreeform(updated) {
		t.Fatal("expected tab to switch approval prompt to commentary freeform")
	}
	lines := updated.layout().renderInputLines(120, uiThemeStyles("dark"))
	plain := stripANSIAndTrimRight(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "Commentary for Allow once:") {
		t.Fatalf("expected commentary prompt for selected approval option, got %q", plain)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ok but please keep it minimal")})
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
	if resp.response.Approval.Decision != clientui.ApprovalDecisionAllowOnce || resp.response.Approval.Commentary != "ok but please keep it minimal" {
		t.Fatalf("unexpected approval allow-with-commentary answer: %+v", resp.response.Approval)
	}
	if len(updated.pendingInjected) != 1 || updated.pendingInjected[0].Text != "ok but please keep it minimal" {
		t.Fatalf("expected queued user commentary injection, got %+v", updated.pendingInjected)
	}
	if testActiveAsk(updated) != nil {
		t.Fatal("expected ask to resolve after approval commentary submit")
	}
}

func TestApprovalAskAnswersWhenCommentaryQueueFails(t *testing.T) {
	client := &runtimeControlFakeClient{queueUserMessageErr: errors.New("queue create failed")}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.setBusy(true)
	reply := make(chan askReply, 1)
	event := askEvent{req: clientui.PendingPromptEvent{Question: "Approve?", Approval: true, ApprovalOptions: []clientui.ApprovalOption{{Decision: clientui.ApprovalDecisionAllowOnce, Label: "Allow once"}, {Decision: clientui.ApprovalDecisionDeny, Label: "Deny"}}}, reply: reply}

	next, _ := m.Update(askEventMsg{event: event})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("please be careful")})
	updated = next.(*uiModel)
	next, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected approval commentary queue command")
	}
	select {
	case <-reply:
		t.Fatal("did not expect approval answer before failed queue completion")
	default:
	}
	for _, msg := range collectCmdMessages(t, cmd) {
		next, cmd = updated.Update(msg)
		updated = next.(*uiModel)
	}

	resp := <-reply
	if resp.response.Approval == nil {
		t.Fatal("expected approval answer after failed commentary queue")
	}
	if resp.response.Approval.Decision != clientui.ApprovalDecisionAllowOnce || resp.response.Approval.Commentary != "please be careful" {
		t.Fatalf("unexpected approval response after failed queue: %+v", resp.response.Approval)
	}
	if updated.input != "please be careful" {
		t.Fatalf("expected failed commentary restored into input, got %q", updated.input)
	}
	if len(updated.pendingInjected) != 0 || len(updated.injectedQueue) != 0 {
		t.Fatalf("expected failed commentary queue removed, pending=%+v queue=%+v", updated.pendingInjected, updated.injectedQueue)
	}
	if testActiveAsk(updated) != nil {
		t.Fatal("expected ask to resolve after failed approval commentary queue")
	}
}

func TestApprovalAskIgnoresRepeatSubmitWhileCommentaryQueuePending(t *testing.T) {
	client := &runtimeControlFakeClient{queueUserMessageID: "server-commentary-1"}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.setBusy(true)
	reply := make(chan askReply, 1)
	event := askEvent{req: clientui.PendingPromptEvent{Question: "Approve?", Approval: true, ApprovalOptions: []clientui.ApprovalOption{{Decision: clientui.ApprovalDecisionAllowOnce, Label: "Allow once"}, {Decision: clientui.ApprovalDecisionDeny, Label: "Deny"}}}, reply: reply}

	next, _ := m.Update(askEventMsg{event: event})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("queue once")})
	updated = next.(*uiModel)
	next, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected first approval commentary queue command")
	}
	if !updated.ask.answerPending {
		t.Fatal("expected ask answer pending while commentary queues")
	}
	next, secondCmd := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	if secondCmd != nil {
		t.Fatal("did not expect repeat submit command while commentary queues")
	}
	if len(updated.injectedQueue) != 1 || len(updated.pendingInjected) != 1 {
		t.Fatalf("expected one queued commentary item, pending=%+v queue=%+v", updated.pendingInjected, updated.injectedQueue)
	}
	select {
	case <-reply:
		t.Fatal("did not expect approval answer before first queue completes")
	default:
	}

	for _, msg := range collectCmdMessages(t, cmd) {
		next, cmd = updated.Update(msg)
		updated = next.(*uiModel)
	}
	resp := <-reply
	if resp.response.Approval == nil || resp.response.Approval.Commentary != "queue once" {
		t.Fatalf("unexpected approval response after queued commentary: %+v", resp.response.Approval)
	}
}

func TestApprovalAskAnswersWhenQueuedCommentarySubmitsBeforeCreateAck(t *testing.T) {
	client := &runtimeControlFakeClient{queueUserMessageID: "server-commentary-1"}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.setBusy(true)
	reply := make(chan askReply, 2)
	event := askEvent{req: clientui.PendingPromptEvent{Question: "Approve?", Approval: true, ApprovalOptions: []clientui.ApprovalOption{{Decision: clientui.ApprovalDecisionAllowOnce, Label: "Allow once"}, {Decision: clientui.ApprovalDecisionDeny, Label: "Deny"}}}, reply: reply}

	next, _ := m.Update(askEventMsg{event: event})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("queue then ack")})
	updated = next.(*uiModel)
	next, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	if cmd == nil || len(updated.pendingInjected) != 1 {
		t.Fatalf("expected queued commentary create command and pending item, cmd=%v pending=%+v", cmd, updated.pendingInjected)
	}
	clientRequestID := updated.pendingInjected[0].ClientRequestID

	next, _ = updated.Update(runtimeEventMsg{event: clientui.Event{
		Kind: clientui.EventQueuedUserMessageStatus,
		QueuedUserMessageStatus: &clientui.QueuedUserMessageStatusEvent{
			QueueItemID:     "server-commentary-1",
			ClientRequestID: clientRequestID,
			Status:          clientui.QueuedUserMessageSubmitted,
		},
	}})
	updated = next.(*uiModel)
	resp := <-reply
	if resp.response.Approval == nil || resp.response.Approval.Commentary != "queue then ack" {
		t.Fatalf("unexpected approval response after early submitted status: %+v", resp.response.Approval)
	}
	if len(updated.pendingInjected) != 0 || len(updated.injectedQueue) != 0 {
		t.Fatalf("expected early status to consume queued commentary, pending=%+v queue=%+v", updated.pendingInjected, updated.injectedQueue)
	}

	for _, msg := range collectCmdMessages(t, cmd) {
		next, _ = updated.Update(msg)
		updated = next.(*uiModel)
	}
	select {
	case extra := <-reply:
		t.Fatalf("unexpected duplicate approval response after late create ack: %+v", extra.response.Approval)
	default:
	}
	if len(updated.pendingInjected) != 0 || len(updated.injectedQueue) != 0 {
		t.Fatalf("late create ack re-added queued commentary, pending=%+v queue=%+v", updated.pendingInjected, updated.injectedQueue)
	}
}

func TestAskEventsQueueUntilCurrentQuestionAnswered(t *testing.T) {
	m := newProjectedStaticUIModel()
	reply1 := make(chan askReply, 1)
	reply2 := make(chan askReply, 1)

	ask1 := askEvent{req: clientui.PendingPromptEvent{Question: "First", Suggestions: []string{"one"}}, reply: reply1}
	ask2 := askEvent{req: clientui.PendingPromptEvent{Question: "Second", Suggestions: []string{"two"}}, reply: reply2}

	next, _ := m.Update(askEventMsg{event: ask1})
	updated := next.(*uiModel)
	next, _ = updated.Update(askEventMsg{event: ask2})
	updated = next.(*uiModel)

	if testActiveAsk(updated) == nil || testActiveAsk(updated).req.Question != "First" {
		t.Fatalf("expected first ask to remain active, got %#v", testActiveAsk(updated))
	}
	if len(testAskQueue(updated)) != 1 {
		t.Fatalf("expected one queued ask, got %d", len(testAskQueue(updated)))
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)

	first := <-reply1
	if first.response.SelectedOptionNumber != 1 || first.response.Answer != "" || first.response.FreeformAnswer != "" {
		t.Fatalf("unexpected first answer: %+v", first.response)
	}
	if testActiveAsk(updated) == nil || testActiveAsk(updated).req.Question != "Second" {
		t.Fatalf("expected second ask to become active, got %#v", testActiveAsk(updated))
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)

	second := <-reply2
	if second.response.SelectedOptionNumber != 1 || second.response.Answer != "" || second.response.FreeformAnswer != "" {
		t.Fatalf("unexpected second answer: %+v", second.response)
	}
	if testActiveAsk(updated) != nil {
		t.Fatal("expected no active ask after queue is drained")
	}
}

func TestAskResolutionEventDismissesCurrentAndPromotesQueuedAsk(t *testing.T) {
	m := newProjectedStaticUIModel()
	reply1 := make(chan askReply, 1)
	reply2 := make(chan askReply, 1)

	first := askEvent{req: clientui.PendingPromptEvent{PromptID: "ask-1", Question: "First", Suggestions: []string{"one"}}, reply: reply1}
	second := askEvent{req: clientui.PendingPromptEvent{PromptID: "ask-2", Question: "Second", Suggestions: []string{"two"}}, reply: reply2}

	next, _ := m.Update(askEventMsg{event: first})
	updated := next.(*uiModel)
	next, _ = updated.Update(askEventMsg{event: second})
	updated = next.(*uiModel)

	next, _ = updated.Update(askEventMsg{event: askEvent{resolvedPromptID: "ask-1"}})
	updated = next.(*uiModel)

	if testActiveAsk(updated) == nil || testActiveAsk(updated).req.PromptID != "ask-2" {
		t.Fatalf("expected queued ask to become active after resolution, got %#v", testActiveAsk(updated))
	}
	if len(testAskQueue(updated)) != 0 {
		t.Fatalf("expected queue to drain after promoting next ask, got %d", len(testAskQueue(updated)))
	}
	select {
	case <-reply1:
		t.Fatal("did not expect resolved ask to receive a reply")
	default:
	}
}

func TestAskResolutionEventRestoresRunningActivityWhenRuntimeIsBusy(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	first := askEvent{req: clientui.PendingPromptEvent{PromptID: "ask-1", Question: "First", Suggestions: []string{"one"}}, reply: make(chan askReply, 1)}

	next, _ := m.Update(askEventMsg{event: first})
	updated := next.(*uiModel)
	next, _ = updated.Update(askEventMsg{event: askEvent{resolvedPromptID: "ask-1"}})
	updated = next.(*uiModel)

	if updated.activity != uiActivityRunning {
		t.Fatalf("activity = %v, want %v", updated.activity, uiActivityRunning)
	}
}

func TestTabIdleAppendsUserOnce(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "echo hi"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated := next.(*uiModel)

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	updated = next.(*uiModel)

	view := stripANSIAndTrimRight(updated.View())
	if count := strings.Count(view, "echo hi"); count != 1 {
		t.Fatalf("expected one user transcript entry, got %d", count)
	}
}

func TestSubmitErrorShowsStatusOnlyWithoutRuntimeClient(t *testing.T) {
	m := newProjectedStaticUIModel()
	longErr := "openai status 400: " + strings.Repeat("X", 320)

	next, _ := m.Update(submitDoneMsg{err: errors.New(longErr)})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	updated = next.(*uiModel)

	if !strings.Contains(updated.transientStatus, "openai status 400:") {
		t.Fatalf("expected status text, got: %q", updated.transientStatus)
	}
	if len(updated.transcriptEntries) != 0 {
		t.Fatalf("submit error without runtime must not create transcript entries: %+v", updated.transcriptEntries)
	}
}

func TestSubmitErrorShowsAPIStatusOnlyWithoutRuntimeClient(t *testing.T) {
	m := newProjectedStaticUIModel()
	body := strings.Repeat("AUTH_ERR_", 64)
	root := &llm.APIStatusError{StatusCode: 429, Body: body}
	wrapped := fmt.Errorf("model generation failed after retries: %w", root)

	next, _ := m.Update(submitDoneMsg{err: wrapped})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	updated = next.(*uiModel)

	if !strings.Contains(updated.transientStatus, "openai status 429") {
		t.Fatalf("expected status line, got: %q", updated.transientStatus)
	}
	if len(updated.transcriptEntries) != 0 {
		t.Fatalf("submit error without runtime must not create transcript entries: %+v", updated.transcriptEntries)
	}
}

func TestMainInputAcceptsSpaceKey(t *testing.T) {
	m := newProjectedStaticUIModel()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeySpace})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("world")})
	updated = next.(*uiModel)

	if updated.input != "hello world" {
		t.Fatalf("expected input with space, got %q", updated.input)
	}
}

func TestMainInputCtrlJInsertsNewline(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "line 1"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	updated := next.(*uiModel)

	if updated.isBusy() {
		t.Fatal("did not expect submit on ctrl+j")
	}
	if updated.input != "line 1\n" {
		t.Fatalf("expected ctrl+j to insert newline, got %q", updated.input)
	}
}

func TestMainInputCtrlBackspaceDeletesCurrentLine(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "111\n22\n333"
	m.inputCursor = 5 // second line

	next, _ := m.Update(tea.KeyMsg{Type: keyTypeCtrlBackspaceCSI})
	updated := next.(*uiModel)

	if updated.input != "111\n333" {
		t.Fatalf("expected ctrl+backspace to remove current line, got %q", updated.input)
	}
	if updated.inputCursor != 4 {
		t.Fatalf("expected cursor at start of remaining line, got %d", updated.inputCursor)
	}
}

func TestMainInputCmdBackspaceDeletesCurrentLine(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "aaa\nbbb\nccc"
	m.inputCursor = 9 // third line

	next, _ := m.Update(tea.KeyMsg{Type: keyTypeSuperBackspaceCSI})
	updated := next.(*uiModel)

	if updated.input != "aaa\nbbb" {
		t.Fatalf("expected cmd+backspace to remove current line, got %q", updated.input)
	}
	if updated.inputCursor != 7 {
		t.Fatalf("expected cursor at end of remaining text, got %d", updated.inputCursor)
	}
}

func TestMainInputCtrlUDeletesCurrentLine(t *testing.T) {
	if goruntime.GOOS != "darwin" {
		t.Skip("ctrl+u alias for cmd+backspace is darwin-only")
	}
	m := newProjectedStaticUIModel()
	m.input = "top\ncurrent\nbottom"
	m.inputCursor = 8 // inside "current"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	updated := next.(*uiModel)

	if updated.input != "top\nbottom" {
		t.Fatalf("expected ctrl+u alias to remove current line, got %q", updated.input)
	}
	if updated.inputCursor != 4 {
		t.Fatalf("expected cursor at start of joined line after delete, got %d", updated.inputCursor)
	}
}

func TestRegisterSteeredQueuedUserMessageTracksDiscardablePendingItem(t *testing.T) {
	m := &uiModel{}
	m.registerSteeredQueuedUserMessage(clientui.QueuedUserMessage{ID: "srv-1", Text: "queued while busy", ClientRequestID: "req-1"})
	if len(m.pendingInjected) != 1 || m.pendingInjected[0].ID != "srv-1" || m.pendingInjected[0].Text != "queued while busy" {
		t.Fatalf("pendingInjected = %+v, want one srv-1 item", m.pendingInjected)
	}
	if idx := m.injectedQueueIndexByAnyID("srv-1"); idx < 0 || m.injectedQueue[idx].State != injectedRuntimeQueueEnqueued {
		t.Fatalf("injectedQueue = %+v, want enqueued srv-1", m.injectedQueue)
	}
	m.registerSteeredQueuedUserMessage(clientui.QueuedUserMessage{ID: "srv-1", Text: "queued while busy"})
	if len(m.pendingInjected) != 1 || len(m.injectedQueue) != 1 {
		t.Fatalf("re-register duplicated item, pending=%+v queue=%+v", m.pendingInjected, m.injectedQueue)
	}
}
