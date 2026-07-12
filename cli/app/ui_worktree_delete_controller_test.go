package app

import (
	"errors"
	"testing"
	"time"

	"core/cli/app/internal/worktreeui"

	tea "github.com/charmbracelet/bubbletea"
)

func worktreeDeleteControllerTarget(t *testing.T) worktreeui.Item {
	t.Helper()
	return mustProjectWorktreeItem(t, testRegisteredWorktreeListEntry(
		"wt-feature", "feature", "/wt/feature", "feature", false, false, true, true,
	))
}

func newWorktreeDeleteControllerTestModel(t *testing.T, client *worktreeCommandTestClient) *uiModel {
	t.Helper()
	model := newWorktreeControllerTestModel(t, client, uiWorktreeOverlayPhaseDeleteConfirm)
	model.worktrees.deleteConfirm = uiWorktreeDeleteDialogState{target: worktreeDeleteControllerTarget(t)}
	model.worktrees.deleteConfirm.clampSelection()
	model.setInputMode(uiInputModeWorktree)
	return model
}

func applyWorktreeDeleteControllerKey(model *uiModel, key tea.KeyMsg) (*uiModel, tea.Cmd) {
	next, cmd := uiInputController{model: model}.handleWorktreeDeleteDialogKey(key)
	return next.(*uiModel), cmd
}

func withDeterministicSpinnerClock(t *testing.T) {
	t.Helper()
	oldNow := uiAnimationNow
	oldInterval := spinnerTickInterval
	anchor := time.Unix(1_700_010_000, 0)
	uiAnimationNow = func() time.Time { return anchor }
	spinnerTickInterval = time.Millisecond
	t.Cleanup(func() {
		uiAnimationNow = oldNow
		spinnerTickInterval = oldInterval
	})
}

func TestWorktreeDeleteControllerEscapeAndCancelCloseDialog(t *testing.T) {
	model := newWorktreeDeleteControllerTestModel(t, nil)
	updated, cmd := applyWorktreeDeleteControllerKey(model, tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Fatal("escape should not return command")
	}
	if updated.worktrees.phase != uiWorktreeOverlayPhaseList {
		t.Fatalf("phase after escape = %q, want list", updated.worktrees.phase)
	}

	model = newWorktreeDeleteControllerTestModel(t, nil)
	model.worktrees.deleteConfirm.selectedAction = uiWorktreeDeleteActionCancel
	updated, cmd = applyWorktreeDeleteControllerKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("cancel should not return command")
	}
	if updated.worktrees.phase != uiWorktreeOverlayPhaseList {
		t.Fatalf("phase after cancel = %q, want list", updated.worktrees.phase)
	}
}

func TestWorktreeDeleteControllerCyclesActions(t *testing.T) {
	model := newWorktreeDeleteControllerTestModel(t, nil)
	model.worktrees.deleteConfirm.selectedAction = uiWorktreeDeleteActionCancel

	updated, _ := applyWorktreeDeleteControllerKey(model, tea.KeyMsg{Type: tea.KeyTab})
	if updated.worktrees.deleteConfirm.selectedAction != uiWorktreeDeleteActionDelete {
		t.Fatalf("action after tab = %v, want delete", updated.worktrees.deleteConfirm.selectedAction)
	}
	updated, _ = applyWorktreeDeleteControllerKey(updated, tea.KeyMsg{Type: tea.KeyRight})
	if updated.worktrees.deleteConfirm.selectedAction != uiWorktreeDeleteActionDeleteBranch {
		t.Fatalf("action after right = %v, want delete branch", updated.worktrees.deleteConfirm.selectedAction)
	}
	updated, _ = applyWorktreeDeleteControllerKey(updated, tea.KeyMsg{Type: tea.KeyShiftTab})
	if updated.worktrees.deleteConfirm.selectedAction != uiWorktreeDeleteActionDelete {
		t.Fatalf("action after shift+tab = %v, want delete", updated.worktrees.deleteConfirm.selectedAction)
	}
}

func TestWorktreeDeleteControllerSubmitSchedulesSpinnerTick(t *testing.T) {
	withDeterministicSpinnerClock(t)

	tests := []struct {
		name         string
		action       uiWorktreeDeleteAction
		deleteBranch bool
	}{
		{name: "delete worktree", action: uiWorktreeDeleteActionDelete, deleteBranch: false},
		{name: "delete worktree and branch", action: uiWorktreeDeleteActionDeleteBranch, deleteBranch: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &worktreeCommandTestClient{listResp: testMainWorktreeListResponse()}
			model := newWorktreeDeleteControllerTestModel(t, client)
			model.worktrees.deleteConfirm.selectedAction = tt.action

			updated, cmd := applyWorktreeDeleteControllerKey(model, tea.KeyMsg{Type: tea.KeyEnter})
			if cmd == nil {
				t.Fatal("expected delete command")
			}
			if !updated.worktrees.deleteConfirm.submitting {
				t.Fatal("expected delete submitting state")
			}
			if updated.spinnerTickToken == 0 {
				t.Fatal("expected delete submit to start spinner ticking")
			}
			if updated.spinnerTickDue.IsZero() {
				t.Fatal("expected delete submit to record spinner tick deadline")
			}

			msgs := collectCmdMessages(t, cmd)
			sawDeleteDone := false
			sawSpinnerTick := false
			for _, msg := range msgs {
				switch typed := msg.(type) {
				case worktreeDeleteDoneMsg:
					sawDeleteDone = true
				case spinnerTickMsg:
					if typed.token != updated.spinnerTickToken {
						t.Fatalf("spinner tick token = %d, want %d", typed.token, updated.spinnerTickToken)
					}
					sawSpinnerTick = true
				}
			}
			if !sawDeleteDone {
				t.Fatalf("expected returned command to emit delete completion, got %+v", msgs)
			}
			if !sawSpinnerTick {
				t.Fatalf("expected returned command to emit spinner tick, got %+v", msgs)
			}
			if len(client.deleteRequests) != 1 {
				t.Fatalf("delete requests = %d, want 1", len(client.deleteRequests))
			}
			if got := client.deleteRequests[0]; got.WorktreeID != "wt-feature" || got.DeleteBranch != tt.deleteBranch {
				t.Fatalf("delete request = %+v, want deleteBranch=%t for wt-feature", got, tt.deleteBranch)
			}
		})
	}
}

func TestWorktreeDeleteControllerPendingSpinnerAdvancesFromReturnedTick(t *testing.T) {
	oldNow := uiAnimationNow
	oldInterval := spinnerTickInterval
	spinnerTickInterval = 20 * time.Millisecond
	uiAnimationNow = func() time.Time { return time.Now().Add(-3 * spinnerTickInterval) }
	t.Cleanup(func() {
		uiAnimationNow = oldNow
		spinnerTickInterval = oldInterval
	})

	client := &worktreeCommandTestClient{listResp: testMainWorktreeListResponse()}
	model := newWorktreeDeleteControllerTestModel(t, client)
	model.worktrees.deleteConfirm.selectedAction = uiWorktreeDeleteActionDelete

	updated, cmd := applyWorktreeDeleteControllerKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected delete command")
	}
	initialFrame := updated.spinnerFrame
	var tick spinnerTickMsg
	foundTick := false
	for _, msg := range collectCmdMessages(t, cmd) {
		if typed, ok := msg.(spinnerTickMsg); ok {
			tick = typed
			foundTick = true
		}
	}
	if !foundTick {
		t.Fatal("expected returned delete command to emit spinner tick")
	}

	next, _ := updated.Update(tick)
	advanced := next.(*uiModel)
	if !advanced.worktrees.deleteConfirm.submitting {
		t.Fatal("expected delete to remain submitting after spinner tick")
	}
	if advanced.spinnerFrame == initialFrame {
		t.Fatalf("expected spinner frame to advance from %d after returned tick", initialFrame)
	}
}

func TestWorktreeDeleteCompletionStopsSpinnerAfterOverlayError(t *testing.T) {
	withDeterministicSpinnerClock(t)

	client := &worktreeCommandTestClient{
		listResp:  testMainWorktreeListResponse(),
		deleteErr: errors.New("delete failed"),
	}
	model := newWorktreeDeleteControllerTestModel(t, client)
	model.worktrees.deleteConfirm.selectedAction = uiWorktreeDeleteActionDelete

	updated, cmd := applyWorktreeDeleteControllerKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	if updated.spinnerTickToken == 0 {
		t.Fatal("expected delete submit to start spinner ticking")
	}
	done, ok := findWorktreeDeleteDoneMsg(collectCmdMessages(t, cmd))
	if !ok {
		t.Fatal("expected delete completion from command")
	}

	next, _ := updated.Update(done)
	completed := next.(*uiModel)
	if completed.worktrees.deleteConfirm.submitting {
		t.Fatal("expected delete completion to clear submitting state")
	}
	if completed.spinnerTickToken != 0 {
		t.Fatalf("expected delete error completion to stop spinner ticking, got token %d", completed.spinnerTickToken)
	}
}

func TestWorktreeDeleteCompletionPreservesSpinnerForFollowUpListLoading(t *testing.T) {
	withDeterministicSpinnerClock(t)

	client := &worktreeCommandTestClient{listResp: testMainWorktreeListResponse()}
	model := newWorktreeDeleteControllerTestModel(t, client)
	model.worktrees.deleteConfirm.selectedAction = uiWorktreeDeleteActionDelete

	updated, cmd := applyWorktreeDeleteControllerKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	if updated.spinnerTickToken == 0 {
		t.Fatal("expected delete submit to start spinner ticking")
	}
	done, ok := findWorktreeDeleteDoneMsg(collectCmdMessages(t, cmd))
	if !ok {
		t.Fatal("expected delete completion from command")
	}

	next, _ := updated.Update(done)
	completed := next.(*uiModel)
	if completed.worktrees.deleteConfirm.submitting {
		t.Fatal("expected delete completion to clear submitting state")
	}
	if !completed.worktrees.loading {
		t.Fatal("expected delete success in overlay to start follow-up list loading")
	}
	if completed.spinnerTickToken == 0 {
		t.Fatal("expected spinner ticking to continue for follow-up list loading")
	}
}

func TestWorktreeDeleteControllerSubmitsDelete(t *testing.T) {
	withDeterministicSpinnerClock(t)

	client := &worktreeCommandTestClient{listResp: testMainWorktreeListResponse()}
	model := newWorktreeDeleteControllerTestModel(t, client)
	model.worktrees.deleteConfirm.selectedAction = uiWorktreeDeleteActionDelete

	updated, cmd := applyWorktreeDeleteControllerKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected delete command")
	}
	if !updated.worktrees.deleteConfirm.submitting {
		t.Fatal("expected delete submitting state")
	}
	msgs := collectCmdMessages(t, cmd)
	if !hasWorktreeDeleteDoneMsg(msgs) {
		t.Fatalf("expected worktreeDeleteDoneMsg, got %+v", msgs)
	}
	if len(client.deleteRequests) != 1 {
		t.Fatalf("delete requests = %d, want 1", len(client.deleteRequests))
	}
	if got := client.deleteRequests[0]; got.WorktreeID != "wt-feature" || got.DeleteBranch {
		t.Fatalf("delete request = %+v, want worktree-only delete", got)
	}
	deadline, ok := client.deleteCtx.Deadline()
	if !ok {
		t.Fatal("expected bounded delete context")
	}
	if remaining := time.Until(deadline); remaining <= 20*time.Second {
		t.Fatalf("delete context remaining = %v, want worktree mutation timeout", remaining)
	}
}

func TestWorktreeDeleteControllerSubmitsDeleteBranch(t *testing.T) {
	withDeterministicSpinnerClock(t)

	client := &worktreeCommandTestClient{listResp: testMainWorktreeListResponse()}
	model := newWorktreeDeleteControllerTestModel(t, client)
	model.worktrees.deleteConfirm.selectedAction = uiWorktreeDeleteActionDeleteBranch

	_, cmd := applyWorktreeDeleteControllerKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected delete-branch command")
	}
	msgs := collectCmdMessages(t, cmd)
	if !hasWorktreeDeleteDoneMsg(msgs) {
		t.Fatalf("expected worktreeDeleteDoneMsg, got %+v", msgs)
	}
	if len(client.deleteRequests) != 1 {
		t.Fatalf("delete requests = %d, want 1", len(client.deleteRequests))
	}
	if got := client.deleteRequests[0]; got.WorktreeID != "wt-feature" || !got.DeleteBranch {
		t.Fatalf("delete request = %+v, want delete branch", got)
	}
}

func TestWorktreeDeleteControllerUpdateRoutesToDeleteDialog(t *testing.T) {
	withDeterministicSpinnerClock(t)

	client := &worktreeCommandTestClient{listResp: testMainWorktreeListResponse()}
	model := newWorktreeDeleteControllerTestModel(t, client)
	model.worktrees.deleteConfirm.selectedAction = uiWorktreeDeleteActionDelete

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected delete command through uiModel.Update")
	}
	if !updated.worktrees.deleteConfirm.submitting {
		t.Fatal("expected submitting state through uiModel.Update")
	}
	msgs := collectCmdMessages(t, cmd)
	if !hasWorktreeDeleteDoneMsg(msgs) {
		t.Fatalf("expected worktreeDeleteDoneMsg, got %+v", msgs)
	}
	if len(client.deleteRequests) != 1 || client.deleteRequests[0].WorktreeID != "wt-feature" {
		t.Fatalf("delete requests = %+v, want wt-feature delete", client.deleteRequests)
	}
}

func hasWorktreeDeleteDoneMsg(msgs []tea.Msg) bool {
	_, ok := findWorktreeDeleteDoneMsg(msgs)
	return ok
}

func findWorktreeDeleteDoneMsg(msgs []tea.Msg) (worktreeDeleteDoneMsg, bool) {
	for _, msg := range msgs {
		if typed, ok := msg.(worktreeDeleteDoneMsg); ok {
			return typed, true
		}
	}
	return worktreeDeleteDoneMsg{}, false
}
