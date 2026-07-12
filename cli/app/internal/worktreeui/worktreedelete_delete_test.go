package worktreeui

import (
	"reflect"
	"testing"
)

func TestDeleteActionsExposeBranchDeletionForBranchBackedRows(t *testing.T) {
	target := testWorktreeItem(t, "wt-1", "feature-a", "/repo-feature", "feature/a", false, false)
	got := DeleteActions(target)
	want := []DeleteAction{DeleteActionCancel, DeleteActionDelete, DeleteActionDeleteBranch}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("actions = %+v, want %+v", got, want)
	}
	if got := ClampDeleteAction(target, DeleteActionCancel, true); got != DeleteActionDeleteBranch {
		t.Fatalf("preferred action = %v", got)
	}
}

func TestPreviewLinesDescribeBranchRootAndWorktreeWithoutDirtyProbe(t *testing.T) {
	target := testWorktreeItem(t, "wt-1", "feature-a", "/repo-feature", "feature/a", false, false)
	lines := PreviewLines(target, DeleteActionDeleteBranch)
	texts := make([]string, 0, len(lines))
	for _, line := range lines {
		texts = append(texts, line.Text)
	}
	want := []string{
		"Will delete:",
		"• Local branch feature/a",
		"• Workspace folder at /repo-feature",
		"• Git worktree feature-a",
	}
	if !reflect.DeepEqual(texts, want) {
		t.Fatalf("preview lines = %+v, want %+v", texts, want)
	}
}
