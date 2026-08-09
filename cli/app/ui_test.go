package app

import (
	"core/cli/tui"
	"core/cli/tui/transcriptrender"
	tuitest "core/internal/testharness/pty"
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
	testSetMainInput(m, "echo hi")

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated := next.(*uiModel)

	if !updated.isBusy() {
		t.Fatal("expected busy after tab queued submission")
	}
	if testMainInput(updated) != "" {
		t.Fatalf("expected input cleared, got %q", testMainInput(updated))
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
	testSetMainInput(m, "follow up")

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
	testSetMainInput(m, "echo hi")

	next, _ := m.Update(customKeyMsg{Kind: customKeyCtrlEnter})
	updated := next.(*uiModel)

	if !updated.isBusy() {
		t.Fatal("expected busy after ctrl+enter custom key")
	}
	if testMainInput(updated) != "" {
		t.Fatalf("expected input cleared after ctrl+enter custom key, got %q", testMainInput(updated))
	}
}

func TestCustomKeyCtrlEnterQueuesPostTurnWhenBusy(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setRuntimeActivityBusyForTest(true)
	testSetMainInput(m, "echo hi")

	next, _ := m.Update(customKeyMsg{Kind: customKeyCtrlEnter})
	updated := next.(*uiModel)

	if len(updated.queued) != 1 {
		t.Fatalf("expected one queued post-turn message, got %d", len(updated.queued))
	}
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("did not expect injected steering messages, got %d", len(updated.pendingInjected))
	}
}

func TestCustomKeyShiftEnterInsertsNewline(t *testing.T) {
	m := newProjectedStaticUIModel()
	testSetMainInput(m, "hello")

	next, _ := m.Update(customKeyMsg{Kind: customKeyShiftEnter})
	updated := next.(*uiModel)

	if updated.isBusy() {
		t.Fatal("did not expect busy after shift+enter CSI sequence")
	}
	if testMainInput(updated) != "hello\n" {
		t.Fatalf("expected newline insertion from shift+enter CSI sequence, got %q", testMainInput(updated))
	}
}

func TestCustomKeyShiftEnterThenEnterDoesNotSubmitTrailingNewline(t *testing.T) {
	m := newProjectedStaticUIModel()
	testSetMainInput(m, "hello")

	next, _ := m.Update(customKeyMsg{Kind: customKeyShiftEnter})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)

	if !updated.isBusy() {
		t.Fatal("expected submission started")
	}
}

func TestCustomKeyCtrlBackspaceDeletesCurrentLine(t *testing.T) {
	m := newProjectedStaticUIModel()
	testSetMainInputAtRuneCursor(m, "one\ntwo\nthree", 5) // inside "two"

	next, _ := m.Update(customKeyMsg{Kind: customKeyCtrlBackspace})
	updated := next.(*uiModel)

	if testMainInput(updated) != "one\nthree" {
		t.Fatalf("expected ctrl+backspace CSI to remove current line, got %q", testMainInput(updated))
	}
	if testMainInputRuneCursor(updated) != 4 {
		t.Fatalf("expected cursor at start of joined line after delete, got %d", testMainInputRuneCursor(updated))
	}
}

func TestMainComposerMutatesTokenOnlyForEditsAndKeepsByteCursor(t *testing.T) {
	m := newProjectedStaticUIModel()
	initialToken := m.mainInputDraftToken

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	if got := m.mainInputDraftToken; got != initialToken {
		t.Fatalf("empty backspace token = %d, want unchanged %d", got, initialToken)
	}

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("界")})
	if got, want := testMainInput(m), "界"; got != want {
		t.Fatalf("input = %q, want %q", got, want)
	}
	if got, want := m.mainEditor.Cursor(), len("界"); got != want {
		t.Fatalf("byte cursor after unicode insert = %d, want %d", got, want)
	}
	if m.mainInputDraftToken == initialToken {
		t.Fatal("unicode insert did not advance main draft token")
	}
	afterInsertToken := m.mainInputDraftToken

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyLeft})
	if got, want := m.mainEditor.Cursor(), 0; got != want {
		t.Fatalf("byte cursor after left = %d, want %d", got, want)
	}
	if got := m.mainInputDraftToken; got != afterInsertToken {
		t.Fatalf("cursor movement token = %d, want unchanged %d", got, afterInsertToken)
	}

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyDelete})
	if got, want := testMainInput(m), ""; got != want {
		t.Fatalf("input after delete = %q, want %q", got, want)
	}
	if m.mainInputDraftToken == afterInsertToken {
		t.Fatal("delete did not advance main draft token")
	}
	afterDeleteToken := m.mainInputDraftToken

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyDelete})
	if got := m.mainInputDraftToken; got != afterDeleteToken {
		t.Fatalf("empty delete token = %d, want unchanged %d", got, afterDeleteToken)
	}
}

func TestSharedEditorsRemoveSplitMouseSGRSequencesAroundUnicode(t *testing.T) {
	const prefix = "界"
	const suffix = "尾"
	const first = "\x1b[<64;"
	const second = "63;24M" + suffix

	t.Run("main", func(t *testing.T) {
		m := newProjectedStaticUIModel()
		m.insertInputRunes([]rune(prefix + first))
		m.insertInputRunes([]rune(second))
		if got, want := m.mainEditor.Text(), prefix+suffix; got != want {
			t.Fatalf("main editor = %q, want %q", got, want)
		}
	})

	t.Run("ask", func(t *testing.T) {
		m := newProjectedStaticUIModel()
		m.insertAskInputRunes([]rune(prefix + first))
		m.insertAskInputRunes([]rune(second))
		if got, want := m.ask.editor.Text(), prefix+suffix; got != want {
			t.Fatalf("ask editor = %q, want %q", got, want)
		}
	})

	t.Run("single line", func(t *testing.T) {
		editor := newSingleLineEditor("")
		updateSingleLineEditorWithAppKeys(&editor, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(prefix + first)})
		updateSingleLineEditorWithAppKeys(&editor, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(second)})
		if got, want := editor.Text(), prefix+suffix; got != want {
			t.Fatalf("single-line editor = %q, want %q", got, want)
		}
	})
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
	m, control := newProjectedPromptTestUIModel(t)
	event := testQuestionAskEvent("ask-1", "Pick one", "a", "b")

	updated := updateUIModel(t, m, askEventMsg{event: event})
	if testAskFreeform(updated) {
		t.Fatal("expected picker mode first")
	}

	next, _ := updated.Update(tea.KeyMsg{Type: tea.KeyTab})
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
	updated, request := submitAskPromptKey(t, updated, control, tea.KeyMsg{Type: tea.KeyEnter})
	entry := requireQuestionAnswerEntry(t, request)
	if entry.QuestionAnswer.Freeform == nil || *entry.QuestionAnswer.Freeform != "custom" {
		t.Fatalf("unexpected freeform answer: %+v", entry.QuestionAnswer)
	}
	if entry.QuestionAnswer.SelectedOptionNumber == nil || *entry.QuestionAnswer.SelectedOptionNumber != 1 {
		t.Fatalf("expected selected option 1 preserved when switching to freeform, got %+v", request)
	}
	if testActiveAsk(updated) != nil {
		t.Fatal("successful batch did not immediately remove the ask")
	}
}

func TestAskQuestionRecommendedOptionIsSelectedAndSubmitted(t *testing.T) {
	m, control := newProjectedPromptTestUIModel(t)
	event := testQuestionAskEvent("ask-1", "Pick one", "first", "second")
	recommended := 2
	event.prompt.RecommendedOptionIndex = &recommended

	updated := updateUIModel(t, m, askEventMsg{event: event})
	selectedRecommended := false
	for _, line := range updated.askController().renderPriorityPromptLines() {
		if line.Kind == askPromptLineKindOption && line.Selected && line.Recommended {
			selectedRecommended = true
		}
	}
	if !selectedRecommended {
		t.Fatal("recommended option was not selected")
	}

	updated, request := submitAskPromptKey(t, updated, control, tea.KeyMsg{Type: tea.KeyEnter})
	entry := requireQuestionAnswerEntry(t, request)
	if request.SessionID != event.prompt.SessionID || request.StepID != event.prompt.StepID ||
		entry.PromptID != event.prompt.PromptID || entry.QuestionAnswer.SelectedOptionNumber == nil ||
		*entry.QuestionAnswer.SelectedOptionNumber != recommended {
		t.Fatalf("selected option = %+v, want %d", entry.QuestionAnswer.SelectedOptionNumber, recommended)
	}
	if testActiveAsk(updated) != nil {
		t.Fatal("successful batch did not immediately remove the ask")
	}
}

func TestQueuedAskQuestionReceivesItsRecommendationWhenPromoted(t *testing.T) {
	m, control := newProjectedPromptTestUIModel(t)
	updated := updateUIModel(t, m, askEventMsg{event: testQuestionAskEvent(
		"ask-a",
		"First question",
		"first",
		"second",
	)})
	queued := testQuestionAskEvent("ask-b", "Second question", "first", "second")
	recommended := 2
	queued.prompt.RecommendedOptionIndex = &recommended
	updated = updateUIModel(t, updated, askEventMsg{event: queued})
	if updated.ask.current == nil || updated.ask.current.prompt.PromptID != "ask-a" || updated.ask.cursor != 0 {
		t.Fatalf("queued question changed active selection: current=%+v cursor=%d", updated.ask.current, updated.ask.cursor)
	}

	updated = updateUIModel(t, updated, askEventMsg{event: askEvent{resolvedPromptID: "ask-a"}})
	selectedRecommended := false
	for _, line := range updated.askController().renderPriorityPromptLines() {
		if line.Kind == askPromptLineKindOption && line.Selected && line.Recommended {
			selectedRecommended = true
		}
	}
	if !selectedRecommended {
		t.Fatal("promoted question did not select its recommendation")
	}

	updated, request := submitAskPromptKey(t, updated, control, tea.KeyMsg{Type: tea.KeyEnter})
	entry := requireQuestionAnswerEntry(t, request)
	if entry.QuestionAnswer.SelectedOptionNumber == nil || *entry.QuestionAnswer.SelectedOptionNumber != recommended {
		t.Fatalf("selected option = %+v, want %d", entry.QuestionAnswer.SelectedOptionNumber, recommended)
	}
	if testActiveAsk(updated) != nil {
		t.Fatal("successful batch did not immediately remove the ask")
	}
}

func TestAskQuestionPickerSubmitPreservesPendingFreeformDraft(t *testing.T) {
	m, control := newProjectedPromptTestUIModel(t)
	event := testQuestionAskEvent("ask-1", "Pick one", "a", "b")

	updated := updateUIModel(t, m, askEventMsg{event: event})
	next, _ := updated.Update(tea.KeyMsg{Type: tea.KeyTab})
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
	promptLines := updated.askController().renderPriorityPromptLines()
	hasDisabledDraftPreview := false
	hasHintLine := false
	for _, line := range promptLines {
		if line.Kind == askPromptLineKindInput && line.Disabled && line.InputEditor.Text() == "custom" {
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
	updated, request := submitAskPromptKey(t, updated, control, tea.KeyMsg{Type: tea.KeyEnter})
	entry := requireQuestionAnswerEntry(t, request)
	if entry.QuestionAnswer.SelectedOptionNumber == nil || *entry.QuestionAnswer.SelectedOptionNumber != 2 {
		t.Fatalf("expected selected option number 2, got %+v", request)
	}
	if entry.QuestionAnswer.Freeform == nil || *entry.QuestionAnswer.Freeform != "custom" {
		t.Fatalf("expected pending freeform draft submitted with picker answer, got %+v", request)
	}
	if testActiveAsk(updated) != nil {
		t.Fatal("successful batch did not immediately remove the ask")
	}
}

func TestAskQuestionTabRoundTripRestoresPendingFreeformDraftAndCursor(t *testing.T) {
	m, control := newProjectedPromptTestUIModel(t)
	event := testQuestionAskEvent("ask-1", "Pick one", "a", "b")

	updated := updateUIModel(t, m, askEventMsg{event: event})
	next, _ := updated.Update(tea.KeyMsg{Type: tea.KeyTab})
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
	updated, request := submitAskPromptKey(t, updated, control, tea.KeyMsg{Type: tea.KeyEnter})
	entry := requireQuestionAnswerEntry(t, request)
	if entry.QuestionAnswer.SelectedOptionNumber == nil || *entry.QuestionAnswer.SelectedOptionNumber != 2 {
		t.Fatalf("expected selected option number 2 after round-trip, got %+v", request)
	}
	if entry.QuestionAnswer.Freeform == nil || *entry.QuestionAnswer.Freeform != "custoXm" {
		t.Fatalf("expected restored draft to remain editable, got %+v", request)
	}
	if testActiveAsk(updated) != nil {
		t.Fatal("successful batch did not immediately remove the ask")
	}
}

func TestAskQuestionFreeformSelectionEnterDropsIntoFreeformWhenEmpty(t *testing.T) {
	m := newProjectedStaticUIModel()
	event := testQuestionAskEvent("ask-1", "Pick one", "a", "b")

	updated := updateUIModel(t, m, askEventMsg{event: event})
	next, _ := updated.Update(tea.KeyMsg{Type: tea.KeyDown})
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
}

func TestAskQuestionFreeformSelectionEmptySubmitRequiresCommentary(t *testing.T) {
	m, control := newProjectedPromptTestUIModel(t)
	event := testQuestionAskEvent("ask-1", "Pick one", "a", "b")

	updated := updateUIModel(t, m, askEventMsg{event: event})
	next, _ := updated.Update(tea.KeyMsg{Type: tea.KeyDown})
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
	case request := <-control.batchRequests:
		t.Fatalf("did not expect request on validation error, got %+v", request)
	default:
	}
}

func TestAskQuestionFreeformSelectionSubmitsFreeformOnly(t *testing.T) {
	m, control := newProjectedPromptTestUIModel(t)
	event := testQuestionAskEvent("ask-1", "Pick one", "a", "b")

	updated := updateUIModel(t, m, askEventMsg{event: event})
	next, _ := updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("custom")})
	updated = next.(*uiModel)
	updated, request := submitAskPromptKey(t, updated, control, tea.KeyMsg{Type: tea.KeyEnter})
	entry := requireQuestionAnswerEntry(t, request)
	if entry.QuestionAnswer.SelectedOptionNumber != nil {
		t.Fatalf("expected freeform selection to submit without selected option number, got %+v", request)
	}
	if entry.QuestionAnswer.Freeform == nil || *entry.QuestionAnswer.Freeform != "custom" {
		t.Fatalf("unexpected freeform selection response: %+v", request)
	}
	if testActiveAsk(updated) != nil {
		t.Fatal("successful batch did not immediately remove the ask")
	}
}

func TestAskFreeformUsesMainEditingStack(t *testing.T) {
	m, control := newProjectedPromptTestUIModel(t)
	event := testQuestionAskEvent("ask-1", "Type answer")

	updated := updateUIModel(t, m, askEventMsg{event: event})
	if !testAskFreeform(updated) {
		t.Fatal("expected freeform ask input")
	}

	next, _ := updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello world")})
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
	updated, request := submitAskPromptKey(t, updated, control, tea.KeyMsg{Type: tea.KeyEnter})
	entry := requireQuestionAnswerEntry(t, request)
	if entry.QuestionAnswer.Freeform == nil || *entry.QuestionAnswer.Freeform != ">hello _worl" {
		t.Fatalf("unexpected inline edit result: %+v", entry.QuestionAnswer)
	}
	if testActiveAsk(updated) != nil {
		t.Fatal("successful batch did not immediately remove the ask")
	}
}

func TestAskFreeformCtrlUEditingMatchesMainInput(t *testing.T) {
	m := newProjectedStaticUIModel()
	event := testQuestionAskEvent("ask-1", "Type answer")

	updated := updateUIModel(t, m, askEventMsg{event: event})
	testSetAskInputAtRuneCursor(updated, "top\ncurrent\nbottom", len([]rune("top\ncur")))

	next, _ := updated.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	updated = next.(*uiModel)

	if goruntime.GOOS == "darwin" {
		if testAskInput(updated) != "top\nbottom" {
			t.Fatalf("expected ctrl+u to delete current ask line on darwin, got %q", testAskInput(updated))
		}
		if testAskInputRuneCursor(updated) != len([]rune("top\n")) {
			t.Fatalf("expected cursor at joined ask line on darwin, got %d", testAskInputRuneCursor(updated))
		}
		return
	}
	if testAskInput(updated) != "top\nrent\nbottom" {
		t.Fatalf("expected ctrl+u to kill to ask line start, got %q", testAskInput(updated))
	}
	if testAskInputRuneCursor(updated) != len([]rune("top\n")) {
		t.Fatalf("expected cursor at ask line start, got %d", testAskInputRuneCursor(updated))
	}
}

func TestApprovalAskUsesSingleDenyOptionAndTabCommentary(t *testing.T) {
	m, control := newProjectedPromptTestUIModel(t)
	m.setRuntimeActivityBusyForTest(true)
	event := testApprovalAskEvent(
		"approval-1",
		"Approve?",
		clientui.ApprovalDecisionAllowOnce,
		clientui.ApprovalDecisionAllowSession,
		clientui.ApprovalDecisionDeny,
	)

	updated := updateUIModel(t, m, askEventMsg{event: event})
	promptLines := updated.askController().renderPriorityPromptLines()
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
		next, _ := updated.Update(tea.KeyMsg{Type: tea.KeyDown})
		updated = next.(*uiModel)
	}
	next, _ := updated.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated = next.(*uiModel)
	if !testAskFreeform(updated) {
		t.Fatal("expected tab on deny selection to switch to commentary input")
	}
	promptLines = updated.askController().renderPriorityPromptLines()
	if len(promptLines) != 2 || promptLines[0].Kind != askPromptLineKindHint || promptLines[1].Kind != askPromptLineKindInput {
		t.Fatalf("expected commentary prompt to collapse to hint+input, got %+v", promptLines)
	}
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("blocked by policy")})
	updated = next.(*uiModel)
	next, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("deny commentary did not create a direct approval delivery command")
	}
	updated = runPromptDeliveryCommand(t, updated, cmd)
	request := requirePromptAnswerBatchRequest(t, control)
	entry := requireApprovalAnswerEntry(t, request)
	if entry.ApprovalAnswer.Decision != clientui.ApprovalDecisionDeny ||
		entry.ApprovalAnswer.Commentary == nil ||
		*entry.ApprovalAnswer.Commentary != "blocked by policy" {
		t.Fatalf("unexpected approval request: %+v", request)
	}
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("deny commentary created a duplicate queued user message: %+v", updated.pendingInjected)
	}
	if testActiveAsk(updated) != nil {
		t.Fatal("successful batch did not immediately remove the Approval")
	}
}

func TestApprovalAskDefaultsToAllowOnceOrFirstDecision(t *testing.T) {
	tests := []struct {
		name      string
		decisions []clientui.ApprovalDecision
		want      clientui.ApprovalDecision
	}{
		{
			name: "allow once is selected despite order",
			decisions: []clientui.ApprovalDecision{
				clientui.ApprovalDecisionDeny,
				clientui.ApprovalDecisionAllowSession,
				clientui.ApprovalDecisionAllowOnce,
			},
			want: clientui.ApprovalDecisionAllowOnce,
		},
		{
			name: "first decision is selected without allow once",
			decisions: []clientui.ApprovalDecision{
				clientui.ApprovalDecisionAllowSession,
				clientui.ApprovalDecisionDeny,
			},
			want: clientui.ApprovalDecisionAllowSession,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m, control := newProjectedPromptTestUIModel(t)
			updated := updateUIModel(t, m, askEventMsg{event: testApprovalAskEvent(
				"approval-1",
				"Approve?",
				test.decisions...,
			)})

			next, command := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
			updated = runPromptDeliveryCommand(t, next.(*uiModel), command)
			request := requirePromptAnswerBatchRequest(t, control)
			entry := requireApprovalAnswerEntry(t, request)
			if entry.ApprovalAnswer.Decision != test.want || entry.ApprovalAnswer.Commentary != nil {
				t.Fatalf("approval answer = %+v, want %q without commentary", entry.ApprovalAnswer, test.want)
			}
		})
	}
}

func TestDetailModeHidesInputBox(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.terminalGeometry = terminalGeometryKnown(80, 16)
	testSetMainInput(m, "draft input should be hidden")
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

func TestTabInsideDetailReturnsToOngoingMode(t *testing.T) {
	m := newProjectedStaticUIModel()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	updated := next.(*uiModel)
	if updated.view.Mode() != tui.ModeDetail {
		t.Fatalf("mode=%q want detail", updated.view.Mode())
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated = next.(*uiModel)
	if updated.view.Mode() != tui.ModeOngoing {
		t.Fatalf("mode=%q want ongoing after Tab", updated.view.Mode())
	}
}

func TestDetailModeStatusLineShowsSelectedExpandAction(t *testing.T) {
	page := clientui.TranscriptPage{
		SessionID: detailTestSessionID,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestAssistantRow("line one\nline two\nline three\nline four"),
		},
	}
	m := newProjectedStaticUIModel(
		WithUISessionID(detailTestSessionID),
		WithUIModelName("gpt-5"),
	)
	m.statusConfig.SessionViews = &countingSessionViewClient{page: page}
	m.terminalGeometry = terminalGeometryKnown(100, 16)
	m.layout().syncViewport()

	cmd := m.transitionTranscriptModeWithOptions(transcriptModeTransitionOptions{
		target:            tui.ModeDetail,
		suppressAltScreen: true,
		preserveSurface:   true,
	})
	updated := m
	for _, msg := range collectCmdMessages(t, cmd) {
		if load, ok := msg.(detailTranscriptLoadMsg); ok {
			updated = updateUIModel(t, updated, load)
		}
	}
	if updated.view.Mode() != tui.ModeDetail {
		t.Fatalf("mode=%q want detail", updated.view.Mode())
	}
	if got := updated.view.DetailSelectionAction(); got != tui.DetailSelectionActionExpand {
		t.Fatalf("detail selection action = %v, want expand", got)
	}

	next, _ := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	if got := updated.view.DetailSelectionAction(); got != tui.DetailSelectionActionCollapse {
		t.Fatalf("detail selection action = %v, want collapse", got)
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
	m.terminalGeometry = terminalGeometryKnown(64, 12)
	m.layout().syncViewport()
	event := testQuestionAskEvent("ask-1", question)
	testSetActiveAsk(m, &event)
	testSetAskInput(m, "typed")

	wrapped, cursorLine := testAskPaneContent(m, 64)
	if cursorLine == nil || *cursorLine >= len(wrapped) {
		t.Fatalf("expected cursor line in wrapped prompt, got %v of %d", cursorLine, len(wrapped))
	}
	if wrapped[*cursorLine].prompt.Kind != askPromptLineKindInput {
		t.Fatalf("expected cursor to land on input after markdown-expanded question, got line %+v", wrapped[*cursorLine])
	}

	visible, visibleCursor := testVisibleAskPaneContent(m, 64)
	if visibleCursor == nil || *visibleCursor >= len(visible) {
		t.Fatalf("expected cursor line in visible prompt, got %v of %d", visibleCursor, len(visible))
	}
	if visible[*visibleCursor].prompt.Kind != askPromptLineKindInput {
		t.Fatalf("expected visible cursor to land on input after markdown-expanded question, got line %+v", visible[*visibleCursor])
	}
}

func TestAskQuestionMarkdownLinksWrapIntoIndependentBoundedRows(t *testing.T) {
	const target = "https://github.com/org/repo/pull/456"
	for _, presentation := range []struct {
		name           string
		links          transcriptrender.MarkdownLinkPresentation
		wantLinkedText string
	}{
		{
			name:           "supported terminal",
			links:          transcriptrender.MarkdownLinkLabelOnly,
			wantLinkedText: "PR #456",
		},
		{
			name:           "fallback terminal",
			links:          transcriptrender.MarkdownLinkLabelAndDestination,
			wantLinkedText: "PR #456" + target,
		},
	} {
		t.Run(presentation.name, func(t *testing.T) {
			m := newProjectedStaticUIModel(
				WithUIMarkdownLinkPresentation(presentation.links),
			)
			m.terminalGeometry = terminalGeometryKnown(12, 12)
			m.layout().syncViewport()
			event := testQuestionAskEvent(
				"ask-1",
				"[PR #456](https://github.com/org/repo/pull/456)",
			)
			testSetActiveAsk(m, &event)
			testSetAskInput(m, "answer")

			wrapped, cursorLine := testAskPaneContent(m, 12)
			if cursorLine == nil || wrapped[*cursorLine].prompt.Kind != askPromptLineKindInput {
				t.Fatalf("cursor=%v should remain on answer input: %+v", cursorLine, wrapped)
			}

			var linked strings.Builder
			for _, line := range wrapped {
				if width := lipgloss.Width(line.text); width > 12 {
					t.Fatalf("line width=%d want <=12: %+v", width, line)
				}
				trace := tuitest.TraceTerminalHyperlinks(t, line.text)
				if line.prompt.Kind == askPromptLineKindQuestion {
					linked.WriteString(trace.LinkedText(target))
					continue
				}
				if len(trace.Events) != 0 {
					t.Fatalf("non-question row inherited hyperlink metadata: %+v", line)
				}
			}
			if got := linked.String(); got != presentation.wantLinkedText {
				t.Fatalf("linked visible content = %q, want %q", got, presentation.wantLinkedText)
			}
		})
	}
}

func TestAskQuestionPlainPRReferenceDoesNotCreateHyperlink(t *testing.T) {
	m := newProjectedStaticUIModel()
	event := testQuestionAskEvent("ask-1", "PR #456")
	testSetActiveAsk(m, &event)

	wrapped, _ := testAskPaneContent(m, 12)
	for _, line := range wrapped {
		if line.prompt.Kind != askPromptLineKindQuestion {
			continue
		}
		if trace := tuitest.TraceTerminalHyperlinks(t, line.text); len(trace.Events) != 0 {
			t.Fatalf("plain PR reference emitted hyperlink events: %+v", trace.Events)
		}
	}
}

func TestAskQuestionViewportPrioritizesAnswerOptionsOverQuestionLines(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.terminalGeometry = terminalGeometryKnown(48, 9)
	m.layout().syncViewport()
	event := testQuestionAskEvent(
		"ask-1",
		strings.Repeat("Long **Markdown question** content. ", 8),
		"First",
		"Second",
	)
	testSetActiveAsk(m, &event)

	visible, _ := testVisibleAskPaneContent(m, 48)
	questionLines := 0
	optionLines := 0
	hintLines := 0
	for _, line := range visible {
		switch line.prompt.Kind {
		case askPromptLineKindQuestion:
			questionLines++
		case askPromptLineKindOption:
			optionLines++
		case askPromptLineKindHint:
			hintLines++
		}
	}
	if optionLines != 3 {
		t.Fatalf("visible option lines = %d, want all two suggestions plus freeform: %+v", optionLines, visible)
	}
	if hintLines != 1 {
		t.Fatalf("visible hint lines = %d, want 1: %+v", hintLines, visible)
	}
	if questionLines != 1 {
		t.Fatalf("visible question lines = %d, want remaining one-line capacity: %+v", questionLines, visible)
	}
}
