package worktreeui

import (
	"testing"

	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
)

func TestOrderedFieldsIncludesBaseRefOnlyForNewBranch(t *testing.T) {
	got := OrderedFields(worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_NEW_BRANCH)
	if len(got) != 3 || got[0] != FieldBranchTarget || got[1] != FieldBaseRef || got[2] != FieldActions {
		t.Fatalf("new branch fields = %+v", got)
	}
	got = OrderedFields(worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_EXISTING_BRANCH)
	if len(got) != 2 || got[0] != FieldBranchTarget || got[1] != FieldActions {
		t.Fatalf("existing branch fields = %+v", got)
	}
}

func TestMoveFieldSkipsDisabledBaseRef(t *testing.T) {
	got := MoveField(FieldBranchTarget, worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_EXISTING_BRANCH, 1)
	if got != FieldActions {
		t.Fatalf("field = %v, want FieldActions", got)
	}
}

func TestMoveActionClampsToKnownActions(t *testing.T) {
	if got := MoveCreateFormAction(CreateFormActionCreate, -10); got != CreateFormActionCreate {
		t.Fatalf("move left = %v, want CreateFormActionCreate", got)
	}
	if got := MoveCreateFormAction(CreateFormActionCreate, 10); got != CreateFormActionCancel {
		t.Fatalf("move right = %v, want CreateFormActionCancel", got)
	}
}
