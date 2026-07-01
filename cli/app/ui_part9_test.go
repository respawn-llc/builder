package app

import (
	"context"
	"core/cli/tui"
	"core/server/llm"
	"core/server/runtime"
	"core/server/tools"
	"core/shared/clientui"
	"encoding/json"
	"errors"
	goruntime "runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
)

func TestReviewerProgressKeepsInputEditable(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.input = "keep this draft"

	next, _ := m.Update(projectedRuntimeEventMsg(runtime.Event{Kind: runtime.EventReviewerStarted}))
	started := next.(*uiModel)
	if !started.isReviewerBlocking() {
		t.Fatal("expected reviewer state to be marked running")
	}
	lines := started.layout().renderInputLines(80, uiThemeStyles("dark"))
	plain := stripANSIAndTrimRight(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "keep this draft") {
		t.Fatalf("expected original draft visible while reviewer runs, got %q", plain)
	}

	next, _ = started.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	locked := next.(*uiModel)
	if locked.input != "keep this draftx" {
		t.Fatalf("expected key input accepted while reviewer runs, got %q", locked.input)
	}

	next, _ = locked.Update(projectedRuntimeEventMsg(runtime.Event{Kind: runtime.EventReviewerCompleted}))
	completed := next.(*uiModel)
	if completed.isReviewerBlocking() {
		t.Fatal("expected reviewer state cleared after completion")
	}
	lines = completed.layout().renderInputLines(80, uiThemeStyles("dark"))
	plain = stripANSIAndTrimRight(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "keep this draftx") {
		t.Fatalf("expected edited draft retained after reviewer completion, got %q", plain)
	}
}

func TestBusyEnterDuringReviewerUsesSteeringInjection(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.input = "steer after review"

	next, _ := m.Update(projectedRuntimeEventMsg(runtime.Event{Kind: runtime.EventReviewerStarted}))
	started := next.(*uiModel)
	if !started.isReviewerRunning() {
		t.Fatal("expected reviewer to be running")
	}
	if started.isInputSubmitLocked() {
		t.Fatal("did not expect input lock while reviewer is running")
	}

	next, _ = started.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if len(updated.queued) != 0 {
		t.Fatalf("did not expect post-turn queue for reviewer steering, got %+v", updated.queued)
	}
	if len(updated.pendingInjected) != 1 || updated.pendingInjected[0].Text != "steer after review" {
		t.Fatalf("expected reviewer steering injected for earliest flush, got %+v", updated.pendingInjected)
	}
	if updated.isInputSubmitLocked() {
		t.Fatal("did not expect submit lock while waiting for reviewer steering flush")
	}
	if updated.input != "" {
		t.Fatalf("expected input cleared immediately after queueing reviewer steering, got %q", updated.input)
	}
}

func TestMouseSGRReportRunesDoNotPolluteInput(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "draft"
	m.inputCursor = len([]rune(m.input))

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<64;74;25M")})
	updated := next.(*uiModel)
	if updated.input != "draft" {
		t.Fatalf("expected mouse sgr sequence ignored, got %q", updated.input)
	}

	longBurst := "[<64;81;40M[<64;81;40M[<64;80;40M[<64;80;40M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M[<65;80;39M"
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(longBurst)})
	updated = next.(*uiModel)
	if updated.input != "draft" {
		t.Fatalf("expected long mouse sgr burst ignored, got %q", updated.input)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	updated = next.(*uiModel)
	if updated.input != "draftx" {
		t.Fatalf("expected normal runes to still insert, got %q", updated.input)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<64;63;24M")})
	updated = next.(*uiModel)
	if updated.input != "draftx" {
		t.Fatalf("expected up-scroll mouse sgr ignored, got %q", updated.input)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<65;69;20M")})
	updated = next.(*uiModel)
	if updated.input != "draftx" {
		t.Fatalf("expected down-scroll mouse sgr ignored, got %q", updated.input)
	}
}

func TestMouseSGRSplitEscAndRunesDoNotArmRollback(t *testing.T) {
	m := newProjectedStaticUIModel()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := next.(*uiModel)
	if updated.lastEscAt.IsZero() {
		t.Fatal("expected esc to arm rollback window before potential sgr continuation")
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<64;63;24M")})
	updated = next.(*uiModel)
	if !updated.lastEscAt.IsZero() {
		t.Fatal("expected split mouse sgr continuation to clear rollback esc arming")
	}
	if updated.input != "" {
		t.Fatalf("expected split sgr payload ignored, got %q", updated.input)
	}
}

type statusLineFakeClient struct{}

type statusLineFastClient struct{}

type statusLineAzureClient struct{}

type busyToggleFakeClient struct {
	mu        sync.Mutex
	responses []llm.Response
	calls     int
	delay     time.Duration
}

type requestCaptureFakeClient struct {
	mu        sync.Mutex
	responses []llm.Response
	requests  []llm.Request
}

func (f *busyToggleFakeClient) Generate(ctx context.Context, _ llm.Request) (llm.Response, error) {
	if f.delay > 0 {
		select {
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		case <-time.After(f.delay):
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if len(f.responses) == 0 {
		return llm.Response{}, errors.New("no fake response configured")
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp, nil
}

func (f *busyToggleFakeClient) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *requestCaptureFakeClient) Generate(_ context.Context, req llm.Request) (llm.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	if len(f.responses) == 0 {
		return llm.Response{}, errors.New("no fake response configured")
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp, nil
}

func (f *requestCaptureFakeClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{
		ProviderID:                    "openai",
		SupportsResponsesAPI:          true,
		SupportsResponsesCompact:      true,
		SupportsReasoningEncrypted:    true,
		SupportsServerSideContextEdit: true,
		IsOpenAIFirstParty:            true,
	}, nil
}

func (f *requestCaptureFakeClient) Requests() []llm.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]llm.Request, len(f.requests))
	copy(out, f.requests)
	return out
}

type busyTogglePatchTool struct {
	delay time.Duration
}

func (t busyTogglePatchTool) Call(ctx context.Context, c tools.Call) (tools.Result, error) {
	if t.delay > 0 {
		select {
		case <-ctx.Done():
			return tools.Result{}, ctx.Err()
		case <-time.After(t.delay):
		}
	}
	return tools.Result{CallID: c.ID, Name: c.Name, Output: json.RawMessage(`{"ok":true}`)}, nil
}

func (statusLineFakeClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("not implemented")
}

func (statusLineFastClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("not implemented")
}

func (statusLineFastClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{ProviderID: "openai", SupportsResponsesAPI: true, IsOpenAIFirstParty: true}, nil
}

func (statusLineAzureClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("not implemented")
}

func (statusLineAzureClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{ProviderID: "azure-openai", SupportsResponsesAPI: true, IsOpenAIFirstParty: false}, nil
}

func TestHelpDismissesOnRegisteredKeyAndAppliesAction(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 24
	m.windowSizeKnown = true
	m.layout().syncViewport()

	next, _ := m.Update(customKeyMsg{Kind: customKeyHelp})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	updated = next.(*uiModel)

	if updated.helpVisible {
		t.Fatal("expected help dismissed by registered key")
	}
	if updated.input != "x" {
		t.Fatalf("expected keypress to keep its normal behavior, got %q", updated.input)
	}
}

func TestHelpDismissesOnAnyKeypress(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 24
	m.windowSizeKnown = true
	testSetActiveAsk(m, &askEvent{req: clientui.PendingPromptEvent{Question: "Proceed?", Suggestions: []string{"Yes", "No"}}})
	m.layout().syncViewport()

	next, _ := m.Update(customKeyMsg{Kind: customKeyHelp})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	updated = next.(*uiModel)

	if updated.helpVisible {
		t.Fatal("expected any keypress to dismiss help")
	}
	if testAskFreeform(updated) {
		t.Fatal("did not expect plain rune key to alter ask prompt state")
	}
}

func TestQuestionMarkTogglesHelpWhenInputIsEmpty(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 24
	m.windowSizeKnown = true
	m.layout().syncViewport()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	updated := next.(*uiModel)

	if !updated.helpVisible {
		t.Fatal("expected ? to open help from an empty prompt")
	}
}

func TestQuestionMarkInsertsLiteralWhenInputIsNotEmpty(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 24
	m.windowSizeKnown = true
	m.input = "draft"
	m.inputCursor = len([]rune(m.input))
	m.layout().syncViewport()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	updated := next.(*uiModel)

	if updated.helpVisible {
		t.Fatal("did not expect ? to open help while a draft is present")
	}
	if updated.input != "draft?" {
		t.Fatalf("expected ? to be inserted into the draft, got %q", updated.input)
	}
}

func TestAltQuestionMarkTogglesHelp(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 24
	m.windowSizeKnown = true
	m.layout().syncViewport()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}, Alt: true})
	updated := next.(*uiModel)

	if !updated.helpVisible {
		t.Fatal("expected alt+? to open help")
	}
}

func TestF1TogglesHelp(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 24
	m.windowSizeKnown = true
	m.layout().syncViewport()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyF1})
	updated := next.(*uiModel)

	if !updated.helpVisible {
		t.Fatal("expected f1 to open help")
	}
}

func TestHelpSectionsUseCompactBindingsWithoutStandaloneTranscriptSection(t *testing.T) {
	sections := helpSectionsForGOOS(goruntime.GOOS)

	for _, section := range sections {
		if section.Title == "Transcript" {
			t.Fatal("did not expect standalone transcript help section")
		}
		for _, entry := range section.Entries {
			if slices.Equal(entry.Bindings, []string{"PgUp", "PgDn"}) {
				t.Fatalf("did not expect split transcript page binding: %#v", entry.Bindings)
			}
		}
	}

	assertHelpEntryBindings(t, sections, "toggle keyboard help", []string{shortcutLabelsForGOOS(goruntime.GOOS).helpToggleBinding()})
	assertHelpEntryBindings(t, sections, "paste a clipboard screenshot as a file path", []string{"Ctrl + V/D"})
	assertHelpEntryBindings(t, sections, "delete the current input line", deleteCurrentLineBindingsForGOOS(goruntime.GOOS))
	assertHelpEntryBindings(t, sections, "move the cursor by word", []string{"Alt/Ctrl + ←/→"})
}

func TestHelpSectionsUsePlatformSpecificSuperKeyLabels(t *testing.T) {
	tests := []struct {
		goos       string
		helpToggle []string
		deleteLine []string
	}{
		{
			goos:       "darwin",
			helpToggle: []string{"F1 / ? (empty) / Alt/⌘ + /"},
			deleteLine: []string{"Ctrl/⌘ + Backspace", "Ctrl + U"},
		},
		{
			goos:       "linux",
			helpToggle: []string{"F1 / ? (empty) / Alt/Super + /"},
			deleteLine: []string{"Ctrl/Super + Backspace"},
		},
		{
			goos:       "windows",
			helpToggle: []string{"F1 / ? (empty) / Alt/Win + /"},
			deleteLine: []string{"Ctrl/Win + Backspace"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.goos, func(t *testing.T) {
			sections := helpSectionsForGOOS(tc.goos)
			assertHelpEntryBindings(t, sections, "toggle keyboard help", tc.helpToggle)
			assertHelpEntryBindings(t, sections, "delete the current input line", tc.deleteLine)
		})
	}
}

func TestHelpPaneRendersPlatformSuperKeyAtNarrowWidth(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.helpVisible = true
	width := 60
	lines := m.layout().renderHelpPane(width, 18, uiThemeStyles("dark"))
	plain := stripANSIPreserve(strings.Join(lines, "\n"))
	expected := "Alt/" + shortcutLabelsForGOOS(goruntime.GOOS).super
	if !strings.Contains(plain, expected) {
		t.Fatalf("expected rendered help pane to contain %q binding, got %q", expected, plain)
	}
	if goruntime.GOOS == "darwin" && (strings.Contains(plain, "Cmd") || strings.Contains(plain, "CMD")) {
		t.Fatalf("did not expect macOS command text in rendered help pane, got %q", plain)
	}
	for _, line := range strings.Split(stripANSIPreserve(strings.Join(lines, "\n")), "\n") {
		if got := runewidth.StringWidth(line); got > width {
			t.Fatalf("help pane line width = %d, want <= %d for %q", got, width, line)
		}
	}
}

func assertHelpEntryBindings(t *testing.T, sections []uiHelpSection, description string, want []string) {
	t.Helper()
	for _, section := range sections {
		for _, entry := range section.Entries {
			if entry.Description != description {
				continue
			}
			if !slices.Equal(entry.Bindings, want) {
				t.Fatalf("bindings for %q = %#v, want %#v", description, entry.Bindings, want)
			}
			return
		}
	}
	t.Fatalf("missing help entry %q", description)
}

func TestAltSlashTogglesHelp(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 24
	m.windowSizeKnown = true
	m.layout().syncViewport()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}, Alt: true})
	updated := next.(*uiModel)

	if !updated.helpVisible {
		t.Fatal("expected alt+/ to open help")
	}
}

func TestHelpToggleClearsRollbackEscArming(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 24
	m.windowSizeKnown = true
	m.layout().syncViewport()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := next.(*uiModel)
	if updated.lastEscAt.IsZero() {
		t.Fatal("expected first esc to arm rollback window")
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}, Alt: true})
	updated = next.(*uiModel)
	if !updated.helpVisible {
		t.Fatal("expected alt+/ to open help")
	}
	if !updated.lastEscAt.IsZero() {
		t.Fatal("expected help toggle to clear rollback esc arming")
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated = next.(*uiModel)
	if updated.helpVisible {
		t.Fatal("expected esc to dismiss help")
	}
	if testRollbackSelecting(updated) {
		t.Fatal("did not expect esc after help toggle to open rollback selection")
	}
	if updated.lastEscAt.IsZero() {
		t.Fatal("expected esc after help toggle to start a fresh rollback arming window")
	}
}

func TestCmdSlashCSIUTogglesHelp(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 24
	m.windowSizeKnown = true
	m.layout().syncViewport()

	next, _ := m.Update(adaptCustomKeyMsg(testBubbleTeaUnknownCSISequence("\x1b[47;10u")))
	updated := next.(*uiModel)

	if !updated.helpVisible {
		t.Fatal("expected cmd+/ CSI-u sequence to open help")
	}
}

func TestHelpToggleKeyHidesVisibleHelp(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 24
	m.windowSizeKnown = true
	m.layout().syncViewport()

	next, _ := m.Update(customKeyMsg{Kind: customKeyHelp})
	updated := next.(*uiModel)
	next, _ = updated.Update(customKeyMsg{Kind: customKeyHelp})
	updated = next.(*uiModel)

	if updated.helpVisible {
		t.Fatal("expected help toggle key to hide visible help")
	}
}

func TestHelpToggleIgnoredInDetailMode(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 24
	m.windowSizeKnown = true
	m.forwardToView(tui.ToggleModeMsg{})
	m.layout().syncViewport()

	next, _ := m.Update(customKeyMsg{Kind: customKeyHelp})
	updated := next.(*uiModel)

	if updated.helpVisible {
		t.Fatal("did not expect help to open in detail mode")
	}
}

func TestTranscriptToggleClosesVisibleHelp(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 24
	m.windowSizeKnown = true
	m.layout().syncViewport()

	next, _ := m.Update(customKeyMsg{Kind: customKeyHelp})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	updated = next.(*uiModel)

	if updated.helpVisible {
		t.Fatal("expected transcript toggle to hide help")
	}
	if updated.view.Mode() != tui.ModeDetail {
		t.Fatalf("expected detail mode after transcript toggle, got %q", updated.view.Mode())
	}
}

func TestHelpRollbackSelectionDismissesAndMovesSelection(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 24
	m.windowSizeKnown = true
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "user", Text: "one"}, {Role: "assistant", Text: "a"}, {Role: "user", Text: "two"}}
	seedTestRollbackTargets(m)
	if !m.startRollbackSelectionMode() {
		t.Fatal("expected rollback selection mode to start")
	}
	m.layout().syncViewport()

	next, _ := m.Update(customKeyMsg{Kind: customKeyHelp})
	updated := next.(*uiModel)
	updated.rollback.selection = 0
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)

	if updated.helpVisible {
		t.Fatal("expected rollback selection key to dismiss help")
	}
	if testRollbackSelection(updated) != 1 {
		t.Fatalf("expected rollback selection to move, got %d", testRollbackSelection(updated))
	}
}

func TestHelpRollbackEditDismissesAndReturnsToSelection(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 24
	m.windowSizeKnown = true
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "user", Text: "one"}, {Role: "assistant", Text: "a"}, {Role: "user", Text: "two"}}
	seedTestRollbackTargets(m)
	if !m.startRollbackSelectionMode() {
		t.Fatal("expected rollback selection mode to start")
	}
	if _, ok := m.beginRollbackEditing(); !ok {
		t.Fatal("expected rollback editing mode to start")
	}
	m.input = ""
	m.layout().syncViewport()

	next, _ := m.Update(customKeyMsg{Kind: customKeyHelp})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated = next.(*uiModel)

	if updated.helpVisible {
		t.Fatal("expected rollback edit key to dismiss help")
	}
	if !testRollbackSelecting(updated) || testRollbackEditing(updated) {
		t.Fatalf("expected esc to return to rollback selection, rollbackMode=%t rollbackEditing=%t", testRollbackSelecting(updated), testRollbackEditing(updated))
	}
}
