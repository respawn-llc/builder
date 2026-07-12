package worktree

import (
	"testing"

	"core/server/metadata"
	"core/shared/clientui"
)

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
