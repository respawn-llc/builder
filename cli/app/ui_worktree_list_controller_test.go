package app

import (
	"testing"

	"core/cli/app/internal/worktreeui"
	"core/shared/serverapi"

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
	if len(client.enterRequests) != 1 || client.enterRequests[0].Selector != "feature" {
		t.Fatalf("enter requests = %+v, want selector feature", client.enterRequests)
	}
}

func TestWorktreeListControllerDeleteKeysSetIntent(t *testing.T) {
	model := newWorktreeListControllerTestModel(t, nil)
	model.worktrees.selection = 1
	updated, cmd := applyWorktreeListControllerKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil {
		t.Fatal("expected refresh command for delete intent")
	}
	target := updated.worktrees.intent.DeleteTarget
	if !updated.worktrees.intent.OpenDelete ||
		target.kind != uiWorktreeDeleteIntentTargetIdentity ||
		target.identity != worktreeui.SelectionIdentityForItem(updated.worktrees.entries[0]) ||
		!updated.worktrees.intent.PreferDeleteBranch {
		t.Fatalf("intent = %+v, want delete+branch for selected worktree identity", updated.worktrees.intent)
	}
}

func TestWorktreeListDeleteIntentSurvivesSelectorChangeDuringRefresh(t *testing.T) {
	model := newWorktreeListControllerTestModel(t, nil)
	model.worktrees.selection = 1
	updated, _ := applyWorktreeListControllerKey(
		model,
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}},
	)
	response := serverapi.WorktreeListResponse{
		Worktrees: []serverapi.WorktreeListEntry{
			testRegisteredWorktreeListEntry(
				"wt-other",
				"other",
				"/wt/other",
				"feature",
				false,
				false,
				true,
				true,
			),
			testRegisteredWorktreeListEntry(
				"wt-feature",
				"renamed",
				"/wt/feature",
				"feature-renamed",
				false,
				false,
				true,
				true,
			),
		},
	}

	next, _ := updated.Update(worktreeListDoneMsg{
		token: updated.worktrees.refreshToken,
		resp:  response,
	})
	refreshed := next.(*uiModel)
	if refreshed.worktrees.phase != uiWorktreeOverlayPhaseDeleteConfirm {
		t.Fatalf("phase = %q, want delete confirmation", refreshed.worktrees.phase)
	}
	target := refreshed.worktrees.deleteConfirm.target
	if worktreeui.WorktreeID(target) != "wt-feature" {
		t.Fatalf("delete target worktree id = %q, want wt-feature", worktreeui.WorktreeID(target))
	}
	if target.Entry.Projection.Selector != "feature-renamed" {
		t.Fatalf("delete target selector = %q, want feature-renamed", target.Entry.Projection.Selector)
	}
}

func TestWorktreeListRefreshPreservesSelectionAndDeleteTargetByKentID(t *testing.T) {
	model := newWorktreeListControllerTestModel(t, nil)
	model.worktrees.selection = 1
	model.openDeleteWorktreeDialog(model.worktrees.entries[0], false)

	model.applyWorktreeListResponse(serverapi.WorktreeListResponse{
		Worktrees: []serverapi.WorktreeListEntry{
			testRegisteredWorktreeListEntry(
				"wt-other",
				"other",
				"/wt/other",
				"feature",
				false,
				false,
				true,
				true,
			),
			testRegisteredWorktreeListEntry(
				"wt-feature",
				"renamed",
				"/wt/feature",
				"feature-renamed",
				false,
				false,
				true,
				true,
			),
		},
	})

	if model.worktrees.selection != 2 {
		t.Fatalf("selection = %d, want row 2", model.worktrees.selection)
	}
	target := model.worktrees.deleteConfirm.target
	if worktreeui.WorktreeID(target) != "wt-feature" {
		t.Fatalf("delete target worktree id = %q, want wt-feature", worktreeui.WorktreeID(target))
	}
	if target.Entry.Projection.Selector != "feature-renamed" {
		t.Fatalf("delete target selector = %q, want feature-renamed", target.Entry.Projection.Selector)
	}
}
