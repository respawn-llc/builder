package app

import (
	"context"
	goruntime "runtime"
	"strings"
	"testing"

	"core/cli/tui"
	"core/server/llm"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

type stubClipboardImagePaster struct {
	path  string
	err   error
	calls int
}

type stubClipboardTextCopier struct {
	text  string
	err   error
	calls int
}

func (s *stubClipboardImagePaster) PasteImage(context.Context) (string, error) {
	s.calls++
	return s.path, s.err
}

func (s *stubClipboardTextCopier) CopyText(_ context.Context, text string) error {
	s.calls++
	s.text = text
	return s.err
}

func TestIsClipboardImagePasteKeyRecognizesConfiguredBindings(t *testing.T) {
	if !isClipboardImagePasteKey(tea.KeyMsg{Type: tea.KeyCtrlV}) {
		t.Fatal("expected ctrl+v to trigger clipboard image paste")
	}
	if !isClipboardImagePasteKey(tea.KeyMsg{Type: tea.KeyCtrlD}) {
		t.Fatal("expected ctrl+d to trigger clipboard image paste")
	}
	if isClipboardImagePasteKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}}) {
		t.Fatal("did not expect plain runes to trigger clipboard image paste")
	}
	if isClipboardImagePasteKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello"), Paste: true}) {
		t.Fatal("did not expect bracketed paste to trigger clipboard image paste")
	}
}

func TestBracketedTextPasteStillInsertsText(t *testing.T) {
	m := newProjectedStaticUIModel()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello"), Paste: true})
	updated := next.(*uiModel)
	if updated.input != "hello" {
		t.Fatalf("expected bracketed paste to insert text, got %q", updated.input)
	}
}

func TestCopySlashCommandCopiesLatestAssistantFinalAnswer(t *testing.T) {
	copier := &stubClipboardTextCopier{}
	m := newProjectedStaticUIModel(
		WithUIClipboardTextCopier(copier),
	)
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "assistant", Text: "line one\nline two", Phase: llm.MessagePhaseFinal}}
	m.input = "/copy"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected clipboard copy command")
	}
	if updated.input != "" {
		t.Fatalf("expected input cleared after /copy, got %q", updated.input)
	}

	next, followCmd := updated.Update(cmd())
	updated = next.(*uiModel)
	if copier.calls != 1 {
		t.Fatalf("expected one clipboard copy, got %d", copier.calls)
	}
	if copier.text != "line one\nline two" {
		t.Fatalf("copied text = %q, want %q", copier.text, "line one\nline two")
	}
	if updated.transientStatus != "Copied final answer to clipboard" {
		t.Fatalf("unexpected transient status %q", updated.transientStatus)
	}
	if updated.transientStatusKind != uiStatusNoticeSuccess {
		t.Fatalf("expected success status kind, got %d", updated.transientStatusKind)
	}
	if followCmd == nil {
		t.Fatal("expected transient-status clear command after successful copy")
	}
}

func TestCopySlashCommandCopiesLatestAssistantFinalAnswerAfterReviewerFeedback(t *testing.T) {
	copier := &stubClipboardTextCopier{}
	m := newProjectedStaticUIModel(WithUIClipboardTextCopier(copier))
	m.transcriptEntries = []tui.TranscriptEntry{
		{Role: "assistant", Text: "copy after review", Phase: llm.MessagePhaseFinal},
		{Role: "developer_feedback", Text: "reviewer suggestions", MessageType: llm.MessageTypeReviewerFeedback},
	}
	m.input = "/copy"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected clipboard copy command")
	}
	next, _ = updated.Update(cmd())
	if copier.calls != 1 {
		t.Fatalf("expected one clipboard copy, got %d", copier.calls)
	}
	if copier.text != "copy after review" {
		t.Fatalf("copied text = %q, want %q", copier.text, "copy after review")
	}
}

func TestCopySlashCommandDoesNotUseVisibleProjectionWhenRuntimeStatusIsStale(t *testing.T) {
	copier := &stubClipboardTextCopier{}
	client := &runtimeControlFakeClient{sessionView: clientui.RuntimeSessionView{SessionID: "session-1"}}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents(), WithUIClipboardTextCopier(copier))
	next, _ := m.Update(runtimeEventMsg{event: clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		CommittedEntryStart:        0,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  "visible final",
			Phase: string(llm.MessagePhaseFinal),
		}},
	}})
	updated := next.(*uiModel)
	if got := localLastCommittedAssistantFinalAnswer(updated.transcriptEntries); got != "visible final" {
		t.Fatalf("test setup expected visible final answer fallback, got %q entries=%+v", got, updated.transcriptEntries)
	}
	if strings.TrimSpace(updated.runtimeStatus().LastCommittedAssistantFinalAnswer) != "" {
		t.Fatalf("test setup expected stale empty runtime status, got %q", updated.runtimeStatus().LastCommittedAssistantFinalAnswer)
	}
	updated.input = "/copy"

	next, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected transient-status clear command")
	}
	if copier.calls != 0 {
		t.Fatalf("did not expect clipboard copy from visible projection, got %d", copier.calls)
	}
	if updated.transientStatus != "No final answer available to copy" {
		t.Fatalf("expected no-answer status, got %q", updated.transientStatus)
	}
	if client.refreshMainViewCalls != 0 {
		t.Fatalf("copy command refreshed runtime status %d times, want 0", client.refreshMainViewCalls)
	}
}

func TestCopySlashCommandWithoutFinalAnswerShowsError(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "/copy"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected transient-status clear command for empty /copy")
	}
	if updated.transientStatus != "No final answer available to copy" {
		t.Fatalf("unexpected transient status %q", updated.transientStatus)
	}
	if updated.transientStatusKind != uiStatusNoticeError {
		t.Fatalf("expected error status kind, got %d", updated.transientStatusKind)
	}
}

func TestCtrlVClipboardImagePasteInsertsIntoMainInput(t *testing.T) {
	paster := &stubClipboardImagePaster{path: "/tmp/kent-clipboard-main.png"}
	m := newProjectedStaticUIModel(WithUIClipboardImagePaster(paster))
	m.input = "see "
	m.inputCursor = len([]rune(m.input))

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected clipboard paste command")
	}

	next, cmd = updated.Update(cmd())
	updated = next.(*uiModel)
	if got := updated.input; got != "see /tmp/kent-clipboard-main.png" {
		t.Fatalf("expected pasted image path in prompt, got %q", got)
	}
	if updated.transientStatus != "" {
		t.Fatalf("did not expect transient status on successful paste, got %q", updated.transientStatus)
	}
	if cmd != nil {
		t.Fatalf("did not expect follow-up command after successful paste, got %T", cmd())
	}
	if paster.calls != 1 {
		t.Fatalf("expected one clipboard image lookup, got %d", paster.calls)
	}
}

func TestClipboardImagePasteEmptyPathDoesNotInsertDot(t *testing.T) {
	paster := &stubClipboardImagePaster{path: ""}
	m := newProjectedStaticUIModel(WithUIClipboardImagePaster(paster))
	m.input = "draft"
	m.inputCursor = len([]rune(m.input))

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected clipboard paste command")
	}

	next, followCmd := updated.Update(cmd())
	updated = next.(*uiModel)
	if got := updated.input; got != "draft" {
		t.Fatalf("expected empty clipboard path not to modify input, got %q", got)
	}
	if followCmd != nil {
		t.Fatalf("did not expect follow-up command after empty clipboard path, got %T", followCmd())
	}
}

func TestClipboardImagePasteInsertsIntoRollbackEditInput(t *testing.T) {
	paster := &stubClipboardImagePaster{path: "/tmp/kent-clipboard-rollback.png"}
	m := newProjectedStaticUIModel(WithUIClipboardImagePaster(paster))
	testSetRollbackEditing(m, 0, 1)
	m.input = "rollback: "
	m.inputCursor = len([]rune(m.input))

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected clipboard paste command")
	}

	next, followCmd := updated.Update(cmd())
	updated = next.(*uiModel)
	if got := updated.input; got != "rollback: /tmp/kent-clipboard-rollback.png" {
		t.Fatalf("expected pasted image path in rollback edit input, got %q", got)
	}
	if followCmd != nil {
		t.Fatalf("did not expect follow-up command after successful rollback paste, got %T", followCmd())
	}
	if updated.transientStatus != "" {
		t.Fatalf("did not expect transient status on successful rollback paste, got %q", updated.transientStatus)
	}
}

func TestClipboardImagePasteSkipsStaleMainDraft(t *testing.T) {
	paster := &stubClipboardImagePaster{path: "/tmp/kent-clipboard-main.png"}
	m := newProjectedStaticUIModel(WithUIClipboardImagePaster(paster))
	m.input = "first prompt"
	m.inputCursor = len([]rune(m.input))

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected clipboard paste command")
	}

	updated.clearInput()
	updated.insertInputRunes([]rune("second prompt"))
	next, followCmd := updated.Update(cmd())
	updated = next.(*uiModel)
	if got := updated.input; got != "second prompt" {
		t.Fatalf("expected stale clipboard completion not to modify next draft, got %q", got)
	}
	if followCmd != nil {
		t.Fatalf("did not expect follow-up command after skipped stale main paste, got %T", followCmd())
	}
	if updated.transientStatus != "" {
		t.Fatalf("did not expect transient status when skipping stale main paste, got %q", updated.transientStatus)
	}
}

func TestClipboardImagePasteSkipsPromptHistoryDraftSwitch(t *testing.T) {
	paster := &stubClipboardImagePaster{path: "/tmp/kent-clipboard-main.png"}
	m := newProjectedStaticUIModel(WithUIClipboardImagePaster(paster))
	m.promptHistory = []string{"older prompt"}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected clipboard paste command")
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated = next.(*uiModel)
	if got := updated.input; got != "older prompt" {
		t.Fatalf("expected prompt history selection to load older draft, got %q", got)
	}
	next, followCmd := updated.Update(cmd())
	updated = next.(*uiModel)
	if got := updated.input; got != "older prompt" {
		t.Fatalf("expected stale clipboard completion not to modify history draft, got %q", got)
	}
	if followCmd != nil {
		t.Fatalf("did not expect follow-up command after skipped history draft paste, got %T", followCmd())
	}
}

func TestClipboardImagePasteSkipsSlashAutocompleteReplacement(t *testing.T) {
	paster := &stubClipboardImagePaster{path: "/tmp/kent-clipboard-main.png"}
	m := newProjectedStaticUIModel(WithUIClipboardImagePaster(paster))
	m.input = "/ne"
	m.refreshSlashCommandFilterFromInputWithAuth(true)

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected clipboard paste command")
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated = next.(*uiModel)
	if got := updated.input; got != "/new " {
		t.Fatalf("expected slash autocomplete to replace draft, got %q", got)
	}
	next, followCmd := updated.Update(cmd())
	updated = next.(*uiModel)
	if got := updated.input; got != "/new " {
		t.Fatalf("expected stale clipboard completion not to modify autocompleted slash draft, got %q", got)
	}
	if followCmd != nil {
		t.Fatalf("did not expect follow-up command after skipped slash autocomplete paste, got %T", followCmd())
	}
	if updated.transientStatus != "" {
		t.Fatalf("did not expect transient status when skipping stale slash autocomplete paste, got %q", updated.transientStatus)
	}
}

func TestClipboardImagePasteSkipsSlashPickerNavigationReplacement(t *testing.T) {
	paster := &stubClipboardImagePaster{path: "/tmp/kent-clipboard-main.png"}
	m := newProjectedStaticUIModel(WithUIClipboardImagePaster(paster))
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	updated := next.(*uiModel)

	next, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected clipboard paste command")
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)
	if got := updated.input; got != "/new" {
		t.Fatalf("expected slash picker navigation to replace draft, got %q", got)
	}
	next, followCmd := updated.Update(cmd())
	updated = next.(*uiModel)
	if got := updated.input; got != "/new" {
		t.Fatalf("expected stale clipboard completion not to modify slash picker draft, got %q", got)
	}
	if followCmd != nil {
		t.Fatalf("did not expect follow-up command after skipped slash picker paste, got %T", followCmd())
	}
	if updated.transientStatus != "" {
		t.Fatalf("did not expect transient status when skipping stale slash picker paste, got %q", updated.transientStatus)
	}
}

func TestCtrlDClipboardImagePasteInsertsIntoAskFreeform(t *testing.T) {
	paster := &stubClipboardImagePaster{path: "/tmp/kent-clipboard-ask.png"}
	m := newProjectedStaticUIModel(WithUIClipboardImagePaster(paster))
	testSetActiveAsk(m, &askEvent{req: clientui.PendingPromptEvent{Question: "Add context?"}})
	m.ask.freeform = true
	testSetAskInput(m, "image: ")
	testSetAskInputCursor(m, len([]rune(testAskInput(m))))

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected clipboard paste command")
	}

	next, cmd = updated.Update(cmd())
	updated = next.(*uiModel)
	if got := testAskInput(updated); got != "image: /tmp/kent-clipboard-ask.png" {
		t.Fatalf("expected pasted image path in ask input, got %q", got)
	}
	if updated.transientStatus != "" {
		t.Fatalf("did not expect transient status on successful ask paste, got %q", updated.transientStatus)
	}
	if cmd != nil {
		t.Fatalf("did not expect follow-up command after successful ask paste, got %T", cmd())
	}
}

func TestClipboardImagePasteSkipsDismissedAsk(t *testing.T) {
	paster := &stubClipboardImagePaster{path: "/tmp/kent-clipboard-ask.png"}
	m := newProjectedStaticUIModel(WithUIClipboardImagePaster(paster))
	testSetActiveAsk(m, &askEvent{req: clientui.PendingPromptEvent{Question: "Add context?"}})
	m.ask.freeform = true
	testSetAskInput(m, "image: ")
	testSetAskInputCursor(m, len([]rune(testAskInput(m))))

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected clipboard paste command")
	}

	testSetActiveAsk(updated, nil)
	updated.ask.freeform = false
	next, followCmd := updated.Update(cmd())
	updated = next.(*uiModel)
	if got := testAskInput(updated); got != "image: " {
		t.Fatalf("expected dismissed ask input to remain unchanged, got %q", got)
	}
	if followCmd != nil {
		t.Fatalf("did not expect follow-up command after skipped ask paste, got %T", followCmd())
	}
	if updated.transientStatus != "" {
		t.Fatalf("did not expect transient status when skipping stale ask paste, got %q", updated.transientStatus)
	}
}

func TestClipboardImagePasteSkipsReplacedAsk(t *testing.T) {
	paster := &stubClipboardImagePaster{path: "/tmp/kent-clipboard-ask.png"}
	m := newProjectedStaticUIModel(WithUIClipboardImagePaster(paster))
	controller := uiAskController{model: m}
	controller.setActiveAsk(askEvent{req: clientui.PendingPromptEvent{Question: "Ask A?"}})
	m.ask.freeform = true
	testSetAskInput(m, "A: ")
	testSetAskInputCursor(m, len([]rune(testAskInput(m))))

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected clipboard paste command")
	}

	controller = uiAskController{model: updated}
	controller.setActiveAsk(askEvent{req: clientui.PendingPromptEvent{Question: "Ask B?"}})
	updated.ask.freeform = true
	testSetAskInput(updated, "B: ")
	testSetAskInputCursor(updated, len([]rune(testAskInput(updated))))

	next, followCmd := updated.Update(cmd())
	updated = next.(*uiModel)
	if got := testAskInput(updated); got != "B: " {
		t.Fatalf("expected stale ask paste not to modify replacement ask input, got %q", got)
	}
	if followCmd != nil {
		t.Fatalf("did not expect follow-up command after skipped replacement ask paste, got %T", followCmd())
	}
	if updated.transientStatus != "" {
		t.Fatalf("did not expect transient status when skipping replacement ask paste, got %q", updated.transientStatus)
	}
}

func TestClipboardImagePasteNoImageShowsTransientStatusAndPreservesInput(t *testing.T) {
	paster := &stubClipboardImagePaster{err: &uiClipboardPasteError{Kind: uiClipboardPasteErrorNoImage, Message: "Clipboard does not contain an image"}}
	m := newProjectedStaticUIModel(WithUIClipboardImagePaster(paster))
	m.input = "draft"
	m.inputCursor = len([]rune(m.input))

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected clipboard paste command")
	}

	next, clearCmd := updated.Update(cmd())
	updated = next.(*uiModel)
	if got := updated.input; got != "draft" {
		t.Fatalf("expected input to remain unchanged, got %q", got)
	}
	if got := updated.transientStatus; got != "Clipboard does not contain an image" {
		t.Fatalf("expected non-image transient status, got %q", got)
	}
	if clearCmd == nil {
		t.Fatal("expected transient status clear command")
	}
	if _, ok := clearCmd().(clearTransientStatusMsg); !ok {
		t.Fatalf("expected clearTransientStatusMsg, got %T", clearCmd())
	}
}

func TestClipboardImagePasteMissingToolShowsErrorStatusAndPreservesInput(t *testing.T) {
	paster := &stubClipboardImagePaster{err: &uiClipboardPasteError{Kind: uiClipboardPasteErrorMissingTool, Message: "Clipboard image paste on macOS requires `osascript`"}}
	m := newProjectedStaticUIModel(WithUIClipboardImagePaster(paster))
	m.input = "draft"
	m.inputCursor = len([]rune(m.input))

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected clipboard paste command")
	}

	next, _ = updated.Update(cmd())
	updated = next.(*uiModel)
	if got := updated.input; got != "draft" {
		t.Fatalf("expected input to remain unchanged, got %q", got)
	}
	if got := updated.transientStatus; got != "Clipboard image paste on macOS requires `osascript`" {
		t.Fatalf("expected missing-tool transient status, got %q", got)
	}
	if updated.transientStatusKind != uiStatusNoticeError {
		t.Fatalf("expected missing-tool status to be an error, got %d", updated.transientStatusKind)
	}
}

func TestHelpSectionsIncludeClipboardImagePasteEntry(t *testing.T) {
	sections := helpSectionsForGOOS(goruntime.GOOS)
	for _, section := range sections {
		for _, entry := range section.Entries {
			if entry.Description != "paste a clipboard screenshot as a file path" {
				continue
			}
			if len(entry.Bindings) != 1 || entry.Bindings[0] != "Ctrl + V/D" {
				t.Fatalf("unexpected clipboard paste bindings: %#v", entry.Bindings)
			}
			return
		}
	}
	t.Fatal("expected help entry for clipboard image paste")
}
