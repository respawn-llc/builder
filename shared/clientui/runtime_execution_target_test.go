package clientui

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestSessionExecutionTargetWorkspaceRootSerializesNilWorktree(t *testing.T) {
	target := SessionExecutionTarget{
		WorkspaceID:           "workspace-1",
		WorkspaceName:         "Workspace",
		WorkspaceRoot:         "/repo",
		WorkspaceAvailability: "available",
		Worktree:              nil,
		CwdRelpath:            ".",
		EffectiveWorkdir:      "/repo",
	}

	encoded, err := json.Marshal(target)
	if err != nil {
		t.Fatalf("marshal execution target: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("unmarshal encoded execution target: %v", err)
	}
	worktree, ok := fields["Worktree"]
	if !ok {
		t.Fatalf("encoded execution target = %s, want explicit worktree field", encoded)
	}
	if !bytes.Equal(worktree, []byte("null")) {
		t.Fatalf("encoded worktree JSON = %s, want null", worktree)
	}
}

func TestNormalizeSessionExecutionTargetPreservesNilWorktreeAbsence(t *testing.T) {
	target := NormalizeSessionExecutionTarget(SessionExecutionTarget{
		WorkspaceID:           " workspace-1 ",
		WorkspaceName:         " Workspace ",
		WorkspaceRoot:         " /repo ",
		WorkspaceAvailability: " available ",
		Worktree:              nil,
		CwdRelpath:            " . ",
		EffectiveWorkdir:      " /repo ",
	})

	if target.Worktree != nil {
		t.Fatalf("normalized workspace-root target worktree = %+v, want nil", target.Worktree)
	}
	if target.WorkspaceID != "workspace-1" || target.CwdRelpath != "." || target.EffectiveWorkdir != "/repo" {
		t.Fatalf("normalized target = %+v, want trimmed workspace-root fields", target)
	}
}

func TestSessionExecutionTargetsEqualNormalizesPresentWorktree(t *testing.T) {
	left := SessionExecutionTarget{
		WorkspaceID:           "workspace-1",
		WorkspaceName:         "Workspace",
		WorkspaceRoot:         "/repo",
		WorkspaceAvailability: "available",
		Worktree: &SessionExecutionWorktreeTarget{
			ID:           " worktree-1 ",
			Name:         " Task worktree ",
			Root:         " /repo/.kent-worktree ",
			Availability: " missing ",
		},
		CwdRelpath:       " subdir ",
		EffectiveWorkdir: " /repo/.kent-worktree/subdir ",
	}
	right := SessionExecutionTarget{
		WorkspaceID:           "workspace-1",
		WorkspaceName:         "Workspace",
		WorkspaceRoot:         "/repo",
		WorkspaceAvailability: "available",
		Worktree: &SessionExecutionWorktreeTarget{
			ID:           "worktree-1",
			Name:         "Task worktree",
			Root:         "/repo/.kent-worktree",
			Availability: "missing",
		},
		CwdRelpath:       "subdir",
		EffectiveWorkdir: "/repo/.kent-worktree/subdir",
	}

	if !SessionExecutionTargetsEqual(left, right) {
		t.Fatalf("targets should compare equal after normalization:\nleft=%+v\nright=%+v", left, right)
	}
}
