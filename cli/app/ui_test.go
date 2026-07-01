package app

import (
	"core/cli/tui"
	"core/server/runtime"
	"core/shared/clientui"
	goruntime "runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestTabQueuesAndStartsSubmission(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "echo hi"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated := next.(*uiModel)

	if !updated.isBusy() {
		t.Fatal("expected busy after tab queued submission")
	}
	if updated.input != "" {
		t.Fatalf("expected input cleared, got %q", updated.input)
	}
}

func TestEmptyEnterFlushesOnlyNextQueuedItem(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.queued = queuedInputsForTest("/name queued title", "follow up")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)

	if cmd == nil {
		t.Fatal("expected command from queued /name flush")
	}
	if updated.sessionName != "queued title" {
		t.Fatalf("expected only first queued item to execute, got session name %q", updated.sessionName)
	}
	if updated.isBusy() {
		t.Fatal("did not expect follow-up prompt submission from empty-enter flush")
	}
	if len(updated.queued) != 1 || updated.queued[0].Text != "follow up" {
		t.Fatalf("expected follow-up prompt to remain queued, got %+v", updated.queued)
	}
}

func TestIdleTabWithExistingQueueFlushesOnlyNextQueuedItem(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.queued = queuedInputsForTest("/name queued title")
	m.input = "follow up"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated := next.(*uiModel)

	if cmd == nil {
		t.Fatal("expected command from queued /name flush")
	}
	if updated.sessionName != "queued title" {
		t.Fatalf("expected queued /name to execute first, got %q", updated.sessionName)
	}
	if updated.isBusy() {
		t.Fatal("did not expect appended prompt to auto-submit while idle tab is flushing one queued item")
	}
	if len(updated.queued) != 1 || updated.queued[0].Text != "follow up" {
		t.Fatalf("expected appended prompt to remain queued, got %+v", updated.queued)
	}
}

func TestCustomKeyCtrlEnterQueuesAndStartsSubmission(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "echo hi"

	next, _ := m.Update(customKeyMsg{Kind: customKeyCtrlEnter})
	updated := next.(*uiModel)

	if !updated.isBusy() {
		t.Fatal("expected busy after ctrl+enter custom key")
	}
	if updated.input != "" {
		t.Fatalf("expected input cleared after ctrl+enter custom key, got %q", updated.input)
	}
}

func TestCustomKeyCtrlEnterXtermVariantQueuesAndStartsSubmission(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "echo hi"

	next, _ := m.Update(customKeyMsg{Kind: customKeyCtrlEnter})
	updated := next.(*uiModel)

	if !updated.isBusy() {
		t.Fatal("expected busy after xterm ctrl+enter sequence")
	}
	if updated.input != "" {
		t.Fatalf("expected input cleared after xterm ctrl+enter sequence, got %q", updated.input)
	}
}

func TestCustomKeyCtrlEnterQueuesPostTurnWhenBusy(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.input = "echo hi"

	next, _ := m.Update(customKeyMsg{Kind: customKeyCtrlEnter})
	updated := next.(*uiModel)

	if len(updated.queued) != 1 {
		t.Fatalf("expected one queued post-turn message, got %d", len(updated.queued))
	}
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("did not expect injected steering messages, got %d", len(updated.pendingInjected))
	}
	if updated.isInputSubmitLocked() {
		t.Fatal("did not expect submit lock for ctrl+enter queue")
	}
}

func TestCustomKeyShiftEnterInsertsNewline(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "hello"

	next, _ := m.Update(customKeyMsg{Kind: customKeyShiftEnter})
	updated := next.(*uiModel)

	if updated.isBusy() {
		t.Fatal("did not expect busy after shift+enter CSI sequence")
	}
	if updated.input != "hello\n" {
		t.Fatalf("expected newline insertion from shift+enter CSI sequence, got %q", updated.input)
	}
}

func TestCustomKeyShiftEnterThenEnterDoesNotSubmitTrailingNewline(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "hello"

	next, _ := m.Update(customKeyMsg{Kind: customKeyShiftEnter})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)

	if !updated.isBusy() {
		t.Fatal("expected submission started")
	}
	snapshot := stripANSIAndTrimRight(updated.view.CommittedOngoingProjection().Render(tui.TranscriptDivider))
	if strings.Contains(snapshot, "❯ hello\n\n") {
		t.Fatalf("expected submitted user message without trailing blank line, got %q", snapshot)
	}
}

func TestCustomKeyCtrlBackspaceDeletesCurrentLine(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "one\ntwo\nthree"
	m.inputCursor = 5 // inside "two"

	next, _ := m.Update(customKeyMsg{Kind: customKeyCtrlBackspace})
	updated := next.(*uiModel)

	if updated.input != "one\nthree" {
		t.Fatalf("expected ctrl+backspace CSI to remove current line, got %q", updated.input)
	}
	if updated.inputCursor != 4 {
		t.Fatalf("expected cursor at start of joined line after delete, got %d", updated.inputCursor)
	}
}

func TestCustomKeyCtrlBackspaceWithSubtypeDeletesCurrentLine(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "one\ntwo\nthree"
	m.inputCursor = 5 // inside "two"

	next, _ := m.Update(customKeyMsg{Kind: customKeyCtrlBackspace})
	updated := next.(*uiModel)

	if updated.input != "one\nthree" {
		t.Fatalf("expected ctrl+backspace CSI with subtype to remove current line, got %q", updated.input)
	}
	if updated.inputCursor != 4 {
		t.Fatalf("expected cursor at start of joined line after delete, got %d", updated.inputCursor)
	}
}

func TestParseUserShellCommand(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantCmd string
		wantOK  bool
	}{
		{name: "basic", input: "$ pwd", wantCmd: "pwd", wantOK: true},
		{name: "leading spaces", input: "   $   echo hi", wantCmd: "echo hi", wantOK: true},
		{name: "empty", input: "$", wantCmd: "", wantOK: false},
		{name: "not shell prefix", input: "echo $HOME", wantCmd: "", wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotCmd, gotOK := parseUserShellCommand(tc.input)
			if gotOK != tc.wantOK {
				t.Fatalf("ok = %v, want %v", gotOK, tc.wantOK)
			}
			if gotCmd != tc.wantCmd {
				t.Fatalf("command = %q, want %q", gotCmd, tc.wantCmd)
			}
		})
	}
}

func TestAskQuestionTabFreeformFlow(t *testing.T) {
	m := newProjectedStaticUIModel()
	reply := make(chan askReply, 1)
	event := askEvent{req: clientui.PendingPromptEvent{Question: "Pick one", Suggestions: []string{"a", "b"}}, reply: reply}

	next, _ := m.Update(askEventMsg{event: event})
	updated := next.(*uiModel)
	if testAskFreeform(updated) {
		t.Fatal("expected picker mode first")
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated = next.(*uiModel)
	if !testAskFreeform(updated) {
		t.Fatal("expected tab to open freeform commentary")
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<64;55;24M[<64;56;26M[<65;56;26M")})
	updated = next.(*uiModel)
	if testAskInput(updated) != "" {
		t.Fatalf("expected mouse sgr sequence ignored in ask freeform input, got %q", testAskInput(updated))
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("custom")})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)

	resp := <-reply
	if resp.response.Answer != "custom" {
		t.Fatalf("unexpected answer: %q", resp.response.Answer)
	}
	if resp.response.FreeformAnswer != "custom" {
		t.Fatalf("unexpected freeform answer: %q", resp.response.FreeformAnswer)
	}
	if resp.response.SelectedOptionNumber != 1 {
		t.Fatalf("expected selected option 1 preserved when switching to freeform, got %+v", resp.response)
	}
	if testActiveAsk(updated) != nil {
		t.Fatal("ask should be resolved")
	}
}

func TestAskQuestionPickerSubmitPreservesPendingFreeformDraft(t *testing.T) {
	m := newProjectedStaticUIModel()
	reply := make(chan askReply, 1)
	event := askEvent{req: clientui.PendingPromptEvent{Question: "Pick one", Suggestions: []string{"a", "b"}}, reply: reply}

	next, _ := m.Update(askEventMsg{event: event})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("custom")})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated = next.(*uiModel)

	if testAskFreeform(updated) {
		t.Fatal("expected tab to return to picker mode")
	}
	if testAskInput(updated) != "custom" {
		t.Fatalf("expected pending freeform draft preserved, got %q", testAskInput(updated))
	}
	promptLines := updated.askController().renderPromptLines()
	hasDisabledDraftPreview := false
	hasHintLine := false
	for _, line := range promptLines {
		if line.Kind == askPromptLineKindInput && line.Disabled && line.InputText == "custom" {
			hasDisabledDraftPreview = true
		}
		if line.Kind == askPromptLineKindHint {
			hasHintLine = true
		}
	}
	if !hasDisabledDraftPreview {
		t.Fatalf("expected disabled draft preview in picker, got %+v", promptLines)
	}
	if hasHintLine {
		t.Fatalf("expected draft preview to replace picker hint, got %+v", promptLines)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)

	resp := <-reply
	if resp.response.SelectedOptionNumber != 2 {
		t.Fatalf("expected selected option number 2, got %+v", resp.response)
	}
	if resp.response.FreeformAnswer != "custom" {
		t.Fatalf("expected pending freeform draft submitted with picker answer, got %+v", resp.response)
	}
	if testActiveAsk(updated) != nil {
		t.Fatal("ask should be resolved")
	}
}

func TestAskQuestionTabRoundTripRestoresPendingFreeformDraftAndCursor(t *testing.T) {
	m := newProjectedStaticUIModel()
	reply := make(chan askReply, 1)
	event := askEvent{req: clientui.PendingPromptEvent{Question: "Pick one", Suggestions: []string{"a", "b"}}, reply: reply}

	next, _ := m.Update(askEventMsg{event: event})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("custom")})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyLeft})
	updated = next.(*uiModel)
	wantCursor := testAskInputCursor(updated)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated = next.(*uiModel)

	if !testAskFreeform(updated) {
		t.Fatal("expected tab to restore freeform editing")
	}
	if testAskCursor(updated) != 1 {
		t.Fatalf("expected changed picker selection preserved, got %d", testAskCursor(updated))
	}
	if testAskInput(updated) != "custom" {
		t.Fatalf("expected pending freeform draft restored, got %q", testAskInput(updated))
	}
	if testAskInputCursor(updated) != wantCursor {
		t.Fatalf("expected freeform cursor restored, got %d want %d", testAskInputCursor(updated), wantCursor)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)

	resp := <-reply
	if resp.response.SelectedOptionNumber != 2 {
		t.Fatalf("expected selected option number 2 after round-trip, got %+v", resp.response)
	}
	if resp.response.FreeformAnswer != "custoXm" {
		t.Fatalf("expected restored draft to remain editable, got %+v", resp.response)
	}
	if testActiveAsk(updated) != nil {
		t.Fatal("ask should be resolved")
	}
}

func TestAskQuestionPickerSubmitReturnsSelectedOptionNumber(t *testing.T) {
	m := newProjectedStaticUIModel()
	reply := make(chan askReply, 1)
	event := askEvent{req: clientui.PendingPromptEvent{Question: "Pick one", Suggestions: []string{"a", "b"}}, reply: reply}

	next, _ := m.Update(askEventMsg{event: event})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)

	resp := <-reply
	if resp.response.SelectedOptionNumber != 2 {
		t.Fatalf("expected selected option number 2, got %+v", resp.response)
	}
	if resp.response.Answer != "" || resp.response.FreeformAnswer != "" {
		t.Fatalf("expected structured picker response without raw answer text, got %+v", resp.response)
	}
	if testActiveAsk(updated) != nil {
		t.Fatal("ask should be resolved")
	}
}

func TestAskQuestionFreeformSelectionEnterDropsIntoFreeformWhenEmpty(t *testing.T) {
	m := newProjectedStaticUIModel()
	reply := make(chan askReply, 1)
	event := askEvent{req: clientui.PendingPromptEvent{Question: "Pick one", Suggestions: []string{"a", "b"}}, reply: reply}

	next, _ := m.Update(askEventMsg{event: event})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)
	next, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)

	if cmd != nil {
		t.Fatal("did not expect validation error when opening freeform from Freeform answer")
	}
	if !testAskFreeform(updated) {
		t.Fatal("expected Freeform answer to switch into freeform mode")
	}
	if updated.transientStatus != "" {
		t.Fatalf("did not expect transient status while opening freeform, got %q", updated.transientStatus)
	}
	if testActiveAsk(updated) == nil {
		t.Fatal("expected ask to remain active after switching to freeform")
	}
	select {
	case resp := <-reply:
		t.Fatalf("did not expect reply while opening freeform, got %+v", resp)
	default:
	}
}

func TestAskQuestionFreeformSelectionEmptySubmitRequiresCommentary(t *testing.T) {
	m := newProjectedStaticUIModel()
	reply := make(chan askReply, 1)
	event := askEvent{req: clientui.PendingPromptEvent{Question: "Pick one", Suggestions: []string{"a", "b"}}, reply: reply}

	next, _ := m.Update(askEventMsg{event: event})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	next, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)

	if cmd == nil {
		t.Fatal("expected transient error status cmd")
	}
	if strings.TrimSpace(updated.transientStatus) == "" {
		t.Fatal("expected non-empty transient validation status")
	}
	if updated.transientStatusKind != uiStatusNoticeError {
		t.Fatalf("expected error notice kind, got %d", updated.transientStatusKind)
	}
	if testActiveAsk(updated) == nil {
		t.Fatal("expected ask to remain active after validation error")
	}
	select {
	case resp := <-reply:
		t.Fatalf("did not expect reply on validation error, got %+v", resp)
	default:
	}
}

func TestAskQuestionFreeformSelectionSubmitsFreeformOnly(t *testing.T) {
	m := newProjectedStaticUIModel()
	reply := make(chan askReply, 1)
	event := askEvent{req: clientui.PendingPromptEvent{Question: "Pick one", Suggestions: []string{"a", "b"}}, reply: reply}

	next, _ := m.Update(askEventMsg{event: event})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("custom")})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)

	resp := <-reply
	if resp.response.SelectedOptionNumber != 0 {
		t.Fatalf("expected freeform selection to submit without selected option number, got %+v", resp.response)
	}
	if resp.response.Answer != "custom" || resp.response.FreeformAnswer != "custom" {
		t.Fatalf("unexpected freeform selection response: %+v", resp.response)
	}
	if testActiveAsk(updated) != nil {
		t.Fatal("ask should be resolved")
	}
}

func TestAskFreeformUsesMainEditingStack(t *testing.T) {
	m := newProjectedStaticUIModel()
	reply := make(chan askReply, 1)
	event := askEvent{req: clientui.PendingPromptEvent{Question: "Type answer"}, reply: reply}

	next, _ := m.Update(askEventMsg{event: event})
	updated := next.(*uiModel)
	if !testAskFreeform(updated) {
		t.Fatal("expected freeform ask input")
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello world")})
	updated = next.(*uiModel)
	for range 5 {
		next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyLeft})
		updated = next.(*uiModel)
	}
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
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)

	resp := <-reply
	if resp.response.Answer != ">hello _worl" {
		t.Fatalf("unexpected inline edit result: %q", resp.response.Answer)
	}
	if testActiveAsk(updated) != nil {
		t.Fatal("ask should be resolved")
	}
}

func TestAskFreeformCtrlUEditingMatchesMainInput(t *testing.T) {
	m := newProjectedStaticUIModel()
	reply := make(chan askReply, 1)
	event := askEvent{req: clientui.PendingPromptEvent{Question: "Type answer"}, reply: reply}

	next, _ := m.Update(askEventMsg{event: event})
	updated := next.(*uiModel)
	updated.ask.input = "top\ncurrent\nbottom"
	updated.ask.inputCursor = len([]rune("top\ncur"))

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	updated = next.(*uiModel)

	if goruntime.GOOS == "darwin" {
		if updated.ask.input != "top\nbottom" {
			t.Fatalf("expected ctrl+u to delete current ask line on darwin, got %q", updated.ask.input)
		}
		if updated.ask.inputCursor != len([]rune("top\n")) {
			t.Fatalf("expected cursor at joined ask line on darwin, got %d", updated.ask.inputCursor)
		}
		return
	}
	if updated.ask.input != "top\nrent\nbottom" {
		t.Fatalf("expected ctrl+u to kill to ask line start, got %q", updated.ask.input)
	}
	if updated.ask.inputCursor != len([]rune("top\n")) {
		t.Fatalf("expected cursor at ask line start, got %d", updated.ask.inputCursor)
	}
}

func TestApprovalAskUsesSingleDenyOptionAndTabCommentary(t *testing.T) {
	_, eng := newAppRuntimeEngine(t, statusLineFakeClient{}, runtime.Config{ContextWindowTokens: 400_000})
	m := newProjectedEngineUIModel(eng)
	m.setBusy(true)
	reply := make(chan askReply, 1)
	event := askEvent{req: clientui.PendingPromptEvent{Question: "Approve?", Approval: true, ApprovalOptions: []clientui.ApprovalOption{{Decision: clientui.ApprovalDecisionAllowOnce, Label: "Allow once"}, {Decision: clientui.ApprovalDecisionAllowSession, Label: "Allow for this session"}, {Decision: clientui.ApprovalDecisionDeny, Label: "Deny"}}}, reply: reply}

	next, _ := m.Update(askEventMsg{event: event})
	updated := next.(*uiModel)
	promptLines := updated.askController().renderPromptLines()
	optionLines := 0
	hintLines := 0
	for _, line := range promptLines {
		if line.Kind == askPromptLineKindOption {
			optionLines++
		}
		if line.Kind == askPromptLineKindHint {
			hintLines++
		}
	}
	if optionLines != 3 {
		t.Fatalf("expected exactly 3 approval options, got %+v", promptLines)
	}
	if hintLines != 1 {
		t.Fatalf("expected one approval picker hint line, got %+v", promptLines)
	}

	for i := 0; i < 2; i++ {
		next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
		updated = next.(*uiModel)
	}
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated = next.(*uiModel)
	if !testAskFreeform(updated) {
		t.Fatal("expected tab on deny selection to switch to commentary input")
	}
	promptLines = updated.askController().renderPromptLines()
	if len(promptLines) != 2 || promptLines[0].Kind != askPromptLineKindHint || promptLines[1].Kind != askPromptLineKindInput {
		t.Fatalf("expected commentary prompt to collapse to hint+input, got %+v", promptLines)
	}
	select {
	case <-reply:
		t.Fatal("did not expect answer submission before commentary")
	default:
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("blocked by policy")})
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
	if resp.response.Approval.Decision != clientui.ApprovalDecisionDeny || resp.response.Approval.Commentary != "blocked by policy" {
		t.Fatalf("unexpected approval response: %+v", resp.response.Approval)
	}
	if len(updated.pendingInjected) != 1 || updated.pendingInjected[0].Text != "blocked by policy" {
		t.Fatalf("expected deny commentary injected into regular user-said flow, got %+v", updated.pendingInjected)
	}
	if testActiveAsk(updated) != nil {
		t.Fatal("expected ask to resolve after commentary submit")
	}
}

func TestDetailModeHidesInputBox(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 16
	m.input = "draft input should be hidden"
	m.layout().syncViewport()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	updated := next.(*uiModel)
	if updated.view.Mode() != tui.ModeDetail {
		t.Fatalf("mode=%q want detail", updated.view.Mode())
	}

	view := ansi.Strip(updated.View())
	if strings.Contains(view, "draft input should be hidden") {
		t.Fatalf("expected detail mode to hide input text, got %q", view)
	}
	if strings.Contains(view, "› ") {
		t.Fatalf("expected detail mode to hide input prompt, got %q", view)
	}
}

func TestDetailModeStatusLineOmitsModeLabel(t *testing.T) {
	m := newProjectedStaticUIModel(
		WithUIModelName("gpt-5"),
	)
	m.termWidth = 80
	m.termHeight = 16
	m.windowSizeKnown = true
	m.status.snapshot.Git = uiStatusGitInfo{Visible: true, Branch: "detail-mode-v2"}
	m.layout().syncViewport()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	updated := next.(*uiModel)
	if updated.view.Mode() != tui.ModeDetail {
		t.Fatalf("mode=%q want detail", updated.view.Mode())
	}

	lines := strings.Split(ansi.Strip(updated.View()), "\n")
	statusLine := lines[len(lines)-1]
	if want := statusStateCircleGlyph + statusLineSpinnerSeparator + "gpt-5 · detail-mode-v2"; !strings.HasPrefix(statusLine, want) {
		t.Fatalf("detail status line prefix = %q, want prefix %q", statusLine, want)
	}
	if strings.Contains(statusLine, statusStateCircleGlyph+statusLineSpinnerSeparator+"ongoing"+statusLineSeparator) ||
		strings.Contains(statusLine, statusStateCircleGlyph+statusLineSpinnerSeparator+"detail"+statusLineSeparator) ||
		strings.Contains(statusLine, statusLineSeparator+"ongoing"+statusLineSeparator) ||
		strings.Contains(statusLine, statusLineSeparator+"detail"+statusLineSeparator) {
		t.Fatalf("did not expect transcript mode label in detail status line, got %q", statusLine)
	}
}

func TestAskQuestionLargeMarkdownPromptPreservesLogicalLines(t *testing.T) {
	question := strings.Join([]string{
		"    val preserved = true",
		"Please review this plan before I continue:",
		"",
		"```kotlin",
		"fun main() {",
		"    println(\"hi\")",
		"}",
		"```",
		"",
		"- Keep the four leading spaces in the code block.",
		"- Do not collapse blank lines.",
	}, "\n")
	m := newProjectedStaticUIModel()
	m.termWidth = 72
	m.termHeight = 24
	m.windowSizeKnown = true
	m.layout().syncViewport()
	testSetActiveAsk(m, &askEvent{req: clientui.PendingPromptEvent{Question: question}, reply: make(chan askReply, 1)})

	wrapped, _ := m.layout().wrappedAskPromptLines(64)
	gotLines := make([]string, 0, len(wrapped))
	for _, line := range wrapped {
		if strings.Contains(line.Text, "\n") {
			t.Fatalf("ask prompt line contains embedded newline: %+v", line)
		}
		gotLines = append(gotLines, strings.TrimRight(ansi.Strip(line.Text), " "))
	}
	got := strings.Join(gotLines, "\n")
	if strings.Contains(got, "```") || strings.Contains(got, "- Keep") || strings.Contains(got, "**") {
		t.Fatalf("expected inline ask question markdown rendered, got %q", got)
	}
	for _, want := range []string{
		"val preserved = true",
		"Please review this plan before I continue:",
		"fun main() {",
		"    println(\"hi\")",
		"}",
		"• Keep the four leading spaces in the code block.",
		"• Do not collapse blank lines.",
		"›",
		"Enter to submit",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("ask prompt markdown missing %q in %q", want, got)
		}
	}
}

func TestAskQuestionPickerQuestionLinesWrapInsteadOfEllipsizing(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 40
	m.termHeight = 12
	m.windowSizeKnown = true
	m.layout().syncViewport()
	testSetActiveAsk(m, &askEvent{req: clientui.PendingPromptEvent{
		Question: "This question is intentionally far too long to fit in the live ask input area on one line.",
		Suggestions: []string{
			"Proceed",
		},
	}, reply: make(chan askReply, 1)})

	wrapped, _ := m.layout().wrappedAskPromptLines(32)
	if len(wrapped) == 0 {
		t.Fatal("expected ask prompt lines")
	}
	questionLines := 0
	plain := make([]string, 0, len(wrapped))
	for _, line := range wrapped {
		if line.Line.Kind == askPromptLineKindQuestion {
			questionLines++
			plain = append(plain, ansi.Strip(line.Text))
			if strings.HasSuffix(ansi.Strip(line.Text), "…") {
				t.Fatalf("expected picker question line to wrap, not ellipsize: %+v", line)
			}
		}
	}
	if questionLines < 2 {
		t.Fatalf("expected long picker question to wrap across multiple lines, got %d in %q", questionLines, strings.Join(plain, "\n"))
	}
}

func TestAskQuestionFreeformPromptQuestionLinesWrapInsteadOfEllipsizing(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 40
	m.termHeight = 12
	m.windowSizeKnown = true
	m.layout().syncViewport()
	testSetActiveAsk(m, &askEvent{req: clientui.PendingPromptEvent{
		Question: "This question is intentionally far too long to fit in the live ask input area on one line.",
	}, reply: make(chan askReply, 1)})

	wrapped, _ := m.layout().wrappedAskPromptLines(32)
	questionLines := 0
	plain := make([]string, 0, len(wrapped))
	for _, line := range wrapped {
		if line.Line.Kind == askPromptLineKindQuestion {
			questionLines++
			plain = append(plain, ansi.Strip(line.Text))
			if strings.HasSuffix(ansi.Strip(line.Text), "…") {
				t.Fatalf("expected freeform question line to wrap, not ellipsize: %+v", line)
			}
		}
	}
	if questionLines < 2 {
		t.Fatalf("expected long freeform question to wrap across multiple lines, got %d in %q", questionLines, strings.Join(plain, "\n"))
	}
}

func TestAskQuestionMarkdownPromptCursorTracksInputAfterExpandedQuestion(t *testing.T) {
	question := strings.Join([]string{
		"Review **this plan** before answer:",
		"",
		"- First item",
		"- Second item",
	}, "\n")
	m := newProjectedStaticUIModel()
	m.termWidth = 72
	m.termHeight = 12
	m.windowSizeKnown = true
	m.layout().syncViewport()
	testSetActiveAsk(m, &askEvent{req: clientui.PendingPromptEvent{Question: question}, reply: make(chan askReply, 1)})
	m.ask.input = "typed"
	m.ask.inputCursor = len([]rune(m.ask.input))

	wrapped, cursorLine := m.layout().wrappedAskPromptLines(64)
	if cursorLine < 0 || cursorLine >= len(wrapped) {
		t.Fatalf("expected cursor line in wrapped prompt, got %d of %d", cursorLine, len(wrapped))
	}
	if wrapped[cursorLine].Line.Kind != askPromptLineKindInput {
		t.Fatalf("expected cursor to land on input after markdown-expanded question, got line %+v", wrapped[cursorLine])
	}

	visible, visibleCursor := m.layout().visibleAskPromptLinesWithCursor(64)
	if visibleCursor < 0 || visibleCursor >= len(visible) {
		t.Fatalf("expected cursor line in visible prompt, got %d of %d", visibleCursor, len(visible))
	}
	if visible[visibleCursor].Line.Kind != askPromptLineKindInput {
		t.Fatalf("expected visible cursor to land on input after markdown-expanded question, got line %+v", visible[visibleCursor])
	}
}

func TestDoubleEscEntersRollbackSelectionAndEnterStartsEditing(t *testing.T) {
	m := newProjectedStaticUIModel(WithUIInitialTranscript([]UITranscriptEntry{
		{Role: "user", Text: "u1"},
		{Role: "assistant", Text: "a1"},
		{Role: "user", Text: "u2"},
	}))

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated = next.(*uiModel)

	if !testRollbackSelecting(updated) {
		t.Fatal("expected rollback selection mode after double esc")
	}
	if testRollbackSelection(updated) != 1 {
		t.Fatalf("expected last user message selected by default, got %d", testRollbackSelection(updated))
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	if !testRollbackEditing(updated) {
		t.Fatal("expected rollback editing mode after enter")
	}
	if testRollbackSelecting(updated) {
		t.Fatal("did not expect rollback selection mode while editing")
	}
	if updated.input != "u2" {
		t.Fatalf("expected selected message loaded into input, got %q", updated.input)
	}
	rawDetail := updated.view.DetailProjection(true, true).Render(tui.TranscriptDivider)
	selectedLine := lineContaining(rawDetail, "u2")
	if selectedLine == "" {
		t.Fatalf("expected rollback edit state to retain selected detail entry, got %q", stripANSIPreserve(rawDetail))
	}
	if !strings.Contains(selectedLine, themeSelectionBackgroundEscape(updated.theme)) {
		t.Fatalf("expected rollback edit state to keep selection background, got %q", selectedLine)
	}
}

func TestBareEscapeRuneDoubleEscEntersRollbackSelection(t *testing.T) {
	m := newProjectedStaticUIModel(WithUIInitialTranscript([]UITranscriptEntry{
		{Role: "user", Text: "u1"},
		{Role: "assistant", Text: "a1"},
	}))

	escapeRune := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'\x1b'}}
	next, _ := m.Update(escapeRune)
	updated := next.(*uiModel)
	next, _ = updated.Update(escapeRune)
	updated = next.(*uiModel)

	if !testRollbackSelecting(updated) {
		t.Fatal("expected rollback selection mode after double bare escape rune")
	}
	if updated.input != "" {
		t.Fatalf("expected bare escape rune not to enter prompt text, got %q", updated.input)
	}
}

func TestRollbackSelectionHighlightsSelectedMessageFullWidth(t *testing.T) {
	m := newProjectedStaticUIModel(WithUIInitialTranscript([]UITranscriptEntry{
		{Role: "user", Text: "first user"},
		{Role: "assistant", Text: "first answer"},
		{Role: "user", Text: "selected user"},
		{Role: "assistant", Text: "latest answer"},
	}))
	m.termWidth = 80
	m.termHeight = 16
	m.windowSizeKnown = true
	m.layout().syncViewport()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated = next.(*uiModel)

	if !testRollbackSelecting(updated) {
		t.Fatal("expected rollback selection mode after double esc")
	}
	if updated.view.Mode() != tui.ModeDetail {
		t.Fatalf("expected rollback selection in detail overlay, got mode %q", updated.view.Mode())
	}

	raw := updated.View()
	selectedLine := lineContaining(raw, "selected user")
	if selectedLine == "" {
		t.Fatalf("expected rollback selection view to contain selected message, got %q", stripANSIPreserve(raw))
	}
	if got := lipgloss.Width(selectedLine); got != updated.termWidth {
		t.Fatalf("expected selected rollback line to span viewport width %d, got %d in %q", updated.termWidth, got, selectedLine)
	}
	if !strings.Contains(selectedLine, themeSelectionBackgroundEscape(updated.theme)) {
		t.Fatalf("expected rollback selection line to use selection background, got %q", selectedLine)
	}
}

func TestRollbackEditingEscReturnsToSelection(t *testing.T) {
	m := newProjectedStaticUIModel(WithUIInitialTranscript([]UITranscriptEntry{
		{Role: "user", Text: "u1"},
		{Role: "assistant", Text: "a1"},
		{Role: "user", Text: "u2"},
	}))
	testSetRollbackEditing(m, 1, 2)
	m.input = "edited"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := next.(*uiModel)
	if !testRollbackSelecting(updated) {
		t.Fatal("expected rollback selection mode after esc")
	}
	if testRollbackSelection(updated) != 1 {
		t.Fatalf("expected rollback selection preserved, got %d", testRollbackSelection(updated))
	}
}

func TestRollbackEditingSubmitQuitsIntoForkTransition(t *testing.T) {
	m := newProjectedStaticUIModel()
	testSetRollbackEditing(m, 0, 3)
	m.input = "edited user message"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if updated.exitAction != UIActionForkRollback {
		t.Fatalf("expected fork rollback action, got %q", updated.exitAction)
	}
	if updated.nextForkRollbackTargetID != rollbackTargetIDForTestSelection(3) {
		t.Fatalf("expected rollback target id, got %q", updated.nextForkRollbackTargetID)
	}
	if updated.nextSessionInitialPrompt != "edited user message" {
		t.Fatalf("expected startup prompt to match edited input, got %q", updated.nextSessionInitialPrompt)
	}
	if updated.input != "" {
		t.Fatalf("expected rollback edit buffer cleared before quit, got %q", updated.input)
	}
}
