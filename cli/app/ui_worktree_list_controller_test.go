package app

import (
	"testing"

	"core/cli/app/internal/worktreeui"

	tea "github.com/charmbracelet/bubbletea"
)

func newWorktreeListControllerTestModel(t *testing.T, client *worktreeCommandTestClient) *uiModel {
	t.Helper()
	model := newWorktreeControllerTestModel(t, client, uiWorktreeOverlayPhaseList)
	model.worktrees.entries = []worktreeui.Item{
		mustProjectWorktreeItem(t, testRegisteredWorktreeListEntry("wt-feature", "feature", "/wt/feature", "feature", false, false, true, true)),
		mustProjectWorktreeItem(t, testRegisteredWorktreeListEntry("wt-current", "current", "/wt/current", "current", false, true, true, true)),
	}
	model.setInputMode(uiInputModeWorktree)
	return model
}

func applyWorktreeListControllerKey(model *uiModel, key tea.KeyMsg) (*uiModel, tea.Cmd) {
	next, cmd := uiInputController{model: model}.handleWorktreeOverlayKey(key)
	return next.(*uiModel), cmd
}

func TestWorktreeListControllerNavigatesRows(t *testing.T) {
	model := newWorktreeListControllerTestModel(t, nil)
	updated, _ := applyWorktreeListControllerKey(model, tea.KeyMsg{Type: tea.KeyDown})
	if updated.worktrees.selection != 1 {
		t.Fatalf("selection after down = %d, want 1", updated.worktrees.selection)
	}
	updated, _ = applyWorktreeListControllerKey(updated, tea.KeyMsg{Type: tea.KeyEnd})
	if updated.worktrees.selection != updated.worktreeRowCount()-1 {
		t.Fatalf("selection after end = %d, want last", updated.worktrees.selection)
	}
	updated, _ = applyWorktreeListControllerKey(updated, tea.KeyMsg{Type: tea.KeyHome})
	if updated.worktrees.selection != 0 {
		t.Fatalf("selection after home = %d, want create row", updated.worktrees.selection)
	}
}

func TestWorktreeListControllerEnterCreateRowOpensCreateDialogThroughUpdate(t *testing.T) {
	model := newWorktreeListControllerTestModel(t, nil)
	model.worktrees.selection = 0
	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if updated.worktrees.phase != uiWorktreeOverlayPhaseCreate {
		t.Fatalf("phase after enter create row = %q, want create", updated.worktrees.phase)
	}
}

func TestWorktreeListControllerEnterWorktreeSubmitsSwitch(t *testing.T) {
	client := &worktreeCommandTestClient{listResp: testMainWorktreeListResponse()}
	model := newWorktreeListControllerTestModel(t, client)
	model.worktrees.selection = 1
	updated, cmd := applyWorktreeListControllerKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected switch command")
	}
	if !updated.worktrees.switchPending {
		t.Fatal("expected switch pending state")
	}
	result := cmd()
	if _, ok := result.(worktreeSwitchDoneMsg); !ok {
		t.Fatalf("command message type = %T, want worktreeSwitchDoneMsg", result)
	}
	if len(client.switchRequests) != 1 || client.switchRequests[0].WorktreeID != "wt-feature" {
		t.Fatalf("switch requests = %+v, want wt-feature", client.switchRequests)
	}
}

func TestWorktreeListControllerDeleteKeysSetIntent(t *testing.T) {
	model := newWorktreeListControllerTestModel(t, nil)
	model.worktrees.selection = 1
	updated, cmd := applyWorktreeListControllerKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil {
		t.Fatal("expected refresh command for delete intent")
	}
	if !updated.worktrees.intent.OpenDelete || updated.worktrees.intent.ConfirmDeleteTarget != "feature" || !updated.worktrees.intent.PreferDeleteBranch {
		t.Fatalf("intent = %+v, want delete+branch for selector feature", updated.worktrees.intent)
	}
}
