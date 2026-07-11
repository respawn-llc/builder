package app

import (
	"testing"

	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func TestZeroValueUIModelUsesPromotedFeatureDefaultsSafely(t *testing.T) {
	m := &uiModel{}

	if got := m.inputMode(); got != uiInputModeMain {
		t.Fatalf("zero-value input mode = %q, want %q", got, uiInputModeMain)
	}
	if result := m.reduceFeatureMessage(tea.WindowSizeMsg{Width: 80, Height: 24}); !result.handled {
		t.Fatal("expected zero-value model to route window messages through feature reducers")
	}
	if m.termWidth != 80 || m.termHeight != 24 || !m.windowSizeKnown {
		t.Fatalf("expected promoted window fields updated, got width=%d height=%d known=%t", m.termWidth, m.termHeight, m.windowSizeKnown)
	}
}

func TestUIUpdateRoutesWorktreeMessagesThroughReducer(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.worktrees.open = true
	m.worktrees.loading = true
	m.worktrees.refreshToken = 7

	next, _ := m.Update(worktreeListDoneMsg{token: 7})
	updated := next.(*uiModel)

	if updated.worktrees.loading {
		t.Fatal("expected worktree list completion to be handled by worktree reducer")
	}
}

func TestUIUpdateRoutesProcessRefreshThroughReducer(t *testing.T) {
	m := newProjectedStaticUIModel(WithUIProcessClient(fixedUIProcessClient{
		entries: []clientui.BackgroundProcess{{ID: "proc-1", Command: "sleep 1"}},
	}))
	m.processList.open = true

	next, cmd := m.Update(processListRefreshTickMsg{})
	updated := next.(*uiModel)

	if len(updated.processList.entries) != 0 {
		t.Fatalf("expected process refresh tick to defer entries until command completion, got %#v", updated.processList.entries)
	}
	msgs := collectCmdMessages(t, cmd)
	var refresh processListRefreshDoneMsg
	foundRefresh := false
	for _, msg := range msgs {
		if typed, ok := msg.(processListRefreshDoneMsg); ok {
			refresh = typed
			foundRefresh = true
			break
		}
	}
	if !foundRefresh {
		t.Fatalf("expected process refresh command to produce completion, got %+v", msgs)
	}
	next, _ = updated.Update(refresh)
	updated = next.(*uiModel)

	if len(updated.processList.entries) != 1 || updated.processList.entries[0].ID != "proc-1" {
		t.Fatalf("expected process refresh reducer to update entries, got %#v", updated.processList.entries)
	}
}

func TestProcessRefreshSingleFlightSchedulesOneDirtyFollowUp(t *testing.T) {
	m := newProjectedStaticUIModel(WithUIProcessClient(fixedUIProcessClient{
		entries: []clientui.BackgroundProcess{{ID: "proc-1", Command: "sleep 1"}},
	}))
	m.processList.open = true

	first := m.requestProcessListRefresh()
	if first == nil {
		t.Fatal("expected first refresh command")
	}
	second := m.requestProcessListRefresh()
	if second != nil {
		t.Fatal("expected in-flight refresh to coalesce instead of starting another command")
	}
	if !m.processList.refreshDirty {
		t.Fatal("expected in-flight refresh to mark dirty follow-up")
	}

	next, followUp := m.Update(processListRefreshDoneMsg{
		token:   m.processList.refreshToken,
		entries: []clientui.BackgroundProcess{{ID: "proc-1", Command: "sleep 1"}},
	})
	updated := next.(*uiModel)
	if !updated.processList.refreshInFlight {
		t.Fatal("expected dirty follow-up refresh to start after first completion")
	}
	if updated.processList.refreshDirty {
		t.Fatal("expected dirty flag cleared after scheduling follow-up")
	}
	if followUp == nil {
		t.Fatal("expected follow-up refresh command")
	}
}

func TestUIUpdateRoutesClipboardPasteMessagesThroughReducer(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.mainInputDraftToken = 3

	next, cmd := m.Update(clipboardPasteDoneMsg{
		Target:         uiClipboardPasteTargetMain,
		MainDraftToken: 3,
		Content:        retainedClipboardImage("/tmp/image.png"),
	})
	updated := next.(*uiModel)

	if updated.input != "/tmp/image.png" {
		t.Fatalf("expected clipboard reducer to insert pasted image path, got %q", updated.input)
	}
	if cmd != nil {
		t.Fatalf("did not expect command after successful clipboard image paste, got %T", cmd())
	}

	next, cmd = updated.Update(clipboardTextCopyDoneMsg{})
	updated = next.(*uiModel)
	if updated.transientStatus != "Copied final answer to clipboard" || updated.transientStatusKind != uiStatusNoticeSuccess {
		t.Fatalf("expected clipboard copy reducer success status, got %q kind=%d", updated.transientStatus, updated.transientStatusKind)
	}
	if cmd == nil {
		t.Fatal("expected clipboard text copy reducer to schedule status clear")
	}
}
