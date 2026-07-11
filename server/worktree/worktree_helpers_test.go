package worktree

import (
	"testing"

	"core/server/metadata"
	"core/shared/clientui"
)

func TestWorktreeHelpersRejectPresentTargetWithEmptyWorktreeID(t *testing.T) {
	item := syncedWorktree{
		record: metadata.WorktreeRecord{ID: "worktree-1"},
		git:    GitWorktree{},
	}
	target := clientui.SessionExecutionTarget{Worktree: &clientui.SessionExecutionWorktreeTarget{}}

	if _, err := worktreeViewFromSynced(item, target); err == nil {
		t.Fatal("expected worktree view to reject present worktree target without id")
	}
	if _, err := currentSyncedWorktree([]syncedWorktree{item}, target); err == nil {
		t.Fatal("expected current worktree lookup to reject present worktree target without id")
	}
}

func TestWorktreeViewFromSyncedUsesNilWorktreeAsMainWorkspaceTarget(t *testing.T) {
	main := syncedWorktree{
		record: metadata.WorktreeRecord{ID: "worktree-main"},
		git:    GitWorktree{IsMain: true},
	}
	linked := syncedWorktree{
		record: metadata.WorktreeRecord{ID: "worktree-linked"},
		git:    GitWorktree{IsMain: false},
	}

	mainView, err := worktreeViewFromSynced(main, clientui.SessionExecutionTarget{})
	if err != nil {
		t.Fatalf("main worktree view: %v", err)
	}
	linkedView, err := worktreeViewFromSynced(linked, clientui.SessionExecutionTarget{})
	if err != nil {
		t.Fatalf("linked worktree view: %v", err)
	}
	if !mainView.IsCurrent {
		t.Fatal("expected main worktree current when target has nil worktree")
	}
	if linkedView.IsCurrent {
		t.Fatal("expected linked worktree not current when target has nil worktree")
	}
}

func TestWorktreeReminderTransitionRejectsPresentPreviousTargetWithEmptyWorktreeID(t *testing.T) {
	previous := &syncedWorktree{
		record: metadata.WorktreeRecord{CanonicalRoot: "/repo/worktree"},
		git:    GitWorktree{IsMain: false},
	}
	next := syncedWorktree{
		record: metadata.WorktreeRecord{CanonicalRoot: "/repo"},
		git:    GitWorktree{IsMain: true},
	}
	previousTarget := clientui.SessionExecutionTarget{
		WorkspaceRoot: "/repo",
		Worktree:      &clientui.SessionExecutionWorktreeTarget{},
	}
	nextTarget := clientui.SessionExecutionTarget{
		WorkspaceRoot:    "/repo",
		EffectiveWorkdir: "/repo",
	}

	if _, _, err := worktreeReminderStateForTransition(previous, previousTarget, next, nextTarget); err == nil {
		t.Fatal("expected transition reminder to reject present previous worktree target without id")
	}
}
