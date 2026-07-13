package worktree

import (
	"testing"

	"core/server/metadata"
	"core/shared/clientui"
)

func TestGitMetadataRoundTripPreservesBranchIdentity(t *testing.T) {
	source := GitWorktree{
		HeadOID:    "deadbeef",
		BranchRef:  "refs/heads/feature/round-trip",
		BranchName: "feature/round-trip",
	}
	encoded, err := marshalGitMetadata(source)
	if err != nil {
		t.Fatalf("marshalGitMetadata: %v", err)
	}
	decoded, err := worktreeGitMetadataFromRecord(metadata.WorktreeRecord{GitMetadataJSON: encoded})
	if err != nil {
		t.Fatalf("worktreeGitMetadataFromRecord: %v", err)
	}
	if decoded.HeadOID != source.HeadOID || decoded.BranchRef != source.BranchRef || decoded.BranchName != source.BranchName {
		t.Fatalf("decoded metadata = %+v, want branch identity from %+v", decoded, source)
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
