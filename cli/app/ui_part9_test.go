package app

import (
	"context"
	"core/cli/tui"
	"core/server/llm"
	"core/shared/clientui"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestReviewerProgressKeepsInputEditable(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setRuntimeActivityBusyForTest(true)
	m.activity = uiActivityRunning
	m.input = "keep this draft"

	m.applyTranscriptReviewerState(clientui.TranscriptReviewerState{
		StepID: ongoingTestStepID(),
		State:  clientui.ReviewerStateRunning,
	})
	started := m
	if !started.isReviewerBlocking() {
		t.Fatal("expected reviewer state to be marked running")
	}
	lines := started.layout().renderInputLines(80, uiThemeStyles("dark"))
	plain := stripANSIAndTrimRight(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "keep this draft") {
		t.Fatalf("expected original draft visible while reviewer runs, got %q", plain)
	}

	next, _ := started.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	locked := next.(*uiModel)
	if locked.input != "keep this draftx" {
		t.Fatalf("expected key input accepted while reviewer runs, got %q", locked.input)
	}

	locked.applyTranscriptReviewerState(clientui.TranscriptReviewerState{
		StepID: ongoingTestStepID(),
		State:  clientui.ReviewerStateCompleted,
	})
	completed := locked
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
	m.setRuntimeActivityBusyForTest(true)
	m.activity = uiActivityRunning
	m.input = "steer after review"

	m.applyTranscriptReviewerState(clientui.TranscriptReviewerState{
		StepID: ongoingTestStepID(),
		State:  clientui.ReviewerStateRunning,
	})
	started := m
	if !started.isReviewerRunning() {
		t.Fatal("expected reviewer to be running")
	}

	next, _ := started.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if len(updated.queued) != 0 {
		t.Fatalf("did not expect post-turn queue for reviewer steering, got %+v", updated.queued)
	}
	if len(updated.pendingInjected) != 1 || updated.pendingInjected[0].Text != "steer after review" {
		t.Fatalf("expected reviewer steering injected for earliest flush, got %+v", updated.pendingInjected)
	}
	if updated.input != "" {
		t.Fatalf("expected input cleared immediately after queueing reviewer steering, got %q", updated.input)
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

func (statusLineFakeClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("not implemented")
}

func TestHelpDismissesOnRegisteredKeyAndAppliesAction(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.terminalGeometry = terminalGeometryKnown(80, 24)
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

func TestHelpToggleClearsRollbackEscArming(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.terminalGeometry = terminalGeometryKnown(80, 24)
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
	if updated.rollback.isSelecting() {
		t.Fatal("did not expect esc after help toggle to open rollback selection")
	}
	if updated.lastEscAt.IsZero() {
		t.Fatal("expected esc after help toggle to start a fresh rollback arming window")
	}
}

func TestHelpToggleKeyHidesVisibleHelp(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.terminalGeometry = terminalGeometryKnown(80, 24)
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
	m.terminalGeometry = terminalGeometryKnown(80, 24)
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
	m.terminalGeometry = terminalGeometryKnown(80, 24)
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
