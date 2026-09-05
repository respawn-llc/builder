package worktree

import (
	"testing"

	"core/server/metadata"
	"core/shared/clientui"
)

func TestGitMetadataRoundTripPreservesBranchIdentity(t *testing.T) {
	source := GitWorktree{
		HeadOID: "deadbeef",
		Branch:  mustLocalBranch(t, "feature/round-trip"),
	}
	encoded, err := marshalGitMetadata(source)
	if err != nil {
		t.Fatalf("marshalGitMetadata: %v", err)
	}
	decoded, err := worktreeGitMetadataFromRecord(metadata.WorktreeRecord{GitMetadataJSON: encoded})
	if err != nil {
		t.Fatalf("worktreeGitMetadataFromRecord: %v", err)
	}
	if decoded.HeadOID != source.HeadOID ||
		decoded.Branch == nil ||
		decoded.Branch.Ref() != source.Branch.Ref() ||
		decoded.Branch.Name() != source.Branch.Name() ||
		decoded.RecordedBranch == nil ||
		decoded.RecordedBranch.Ref() != source.Branch.Ref() {
		t.Fatalf("decoded metadata = %+v, want branch identity from %+v", decoded, source)
	}
}

func TestGitMetadataRoundTripSeparatesDetachedHeadFromRecordedBranch(t *testing.T) {
	source := GitWorktree{
		HeadOID:        "deadbeef",
		Detached:       true,
		RecordedBranch: mustLocalBranch(t, "feature/recorded"),
	}
	encoded, err := marshalGitMetadata(source)
	if err != nil {
		t.Fatalf("marshalGitMetadata: %v", err)
	}
	decoded, err := worktreeGitMetadataFromRecord(metadata.WorktreeRecord{GitMetadataJSON: encoded})
	if err != nil {
		t.Fatalf("worktreeGitMetadataFromRecord: %v", err)
	}
	if !decoded.Detached ||
		decoded.Branch != nil ||
		decoded.RecordedBranch == nil ||
		decoded.RecordedBranch.Name() != source.RecordedBranch.Name() {
		t.Fatalf("decoded metadata = %+v, want detached live state with recorded branch %q", decoded, source.RecordedBranch.Name())
	}
}

func TestGitMetadataDecodesLegacySingleBranchField(t *testing.T) {
	for name, metadataJSON := range map[string]string{
		"ref_only":  `{"branch_ref":"refs/heads/feature/legacy"}`,
		"name_only": `{"branch_name":"feature/legacy"}`,
	} {
		t.Run(name, func(t *testing.T) {
			decoded, err := worktreeGitMetadataFromRecord(metadata.WorktreeRecord{GitMetadataJSON: metadataJSON})
			if err != nil {
				t.Fatalf("worktreeGitMetadataFromRecord: %v", err)
			}
			if decoded.Branch == nil ||
				decoded.Branch.Ref() != "refs/heads/feature/legacy" ||
				decoded.Branch.Name() != "feature/legacy" ||
				decoded.RecordedBranch == nil ||
				decoded.RecordedBranch.Name() != "feature/legacy" {
				t.Fatalf("decoded legacy metadata = %+v", decoded)
			}
		})
	}
}

func TestWorktreeReminderTransitionRejectsPresentPreviousTargetWithEmptyWorktreeID(t *testing.T) {
	previous := &syncedWorktree{
		record: metadata.WorktreeRecord{CanonicalRoot: "/repo/worktree"},
		git:    GitWorktree{IsMainWorktree: false},
	}
	next := syncedWorktree{
		record: metadata.WorktreeRecord{CanonicalRoot: "/repo"},
		git:    GitWorktree{IsMainWorktree: true},
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
