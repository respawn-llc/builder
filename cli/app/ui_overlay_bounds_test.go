package app

import (
	"strings"
	"testing"

	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/worktreecontract"
)

func TestBoundedOverlayFramesFitTerminal(t *testing.T) {
	const width, height = 32, 12

	status := sizedTestUIModel(newProjectedStaticUIModel(), width, height)
	status.status.open = true
	status.status.snapshot = uiStatusSnapshot{SessionName: "session", Workdir: "/workspace"}
	for range 12 {
		status.status.snapshot.AgentsPaths = append(status.status.snapshot.AgentsPaths, "/workspace/long/path/to/AGENTS.md")
	}
	status.status.pendingSections = map[uiStatusSection]bool{uiStatusSectionEnvironment: true}
	status.status.scroll = 1 << 30
	_ = status.activateSurface(uiSurfaceStatus)

	list := sizedTestUIModel(newWorktreeListFixture(t, nil).model, width, height)
	list.worktrees.selection = list.worktreeRowCount() - 1
	_ = list.activateSurface(uiSurfaceWorktree)

	create := sizedTestUIModel(newWorktreeCreateControllerTestModel(t, nil), width, height)
	create.worktrees.create.submitting = true
	create.worktrees.create.setupEvent = &worktreepb.SetupEvent{
		SetupOperationId: worktreecontract.NewSetupOperationID().String(),
		Phase: &worktreepb.SetupEvent_Started{
			Started: &worktreepb.SetupStarted{
				SourceWorkspaceRoot: "/workspace/with/a/long/source/path",
				WorktreeRoot:        "/workspace/with/a/long/worktree/path",
				ScriptPath:          "/workspace/with/a/long/scripts/setup-worktree.sh",
			},
		},
	}
	_ = create.activateSurface(uiSurfaceWorktree)

	deleteConfirm := sizedTestUIModel(newWorktreeDeleteControllerTestModel(t, nil), width, height)
	deleteConfirm.worktrees.deleteConfirm.forceFolderRemoval = true
	deleteConfirm.worktrees.deleteConfirm.errorText = strings.Repeat("force removal warning ", 8)
	_ = deleteConfirm.activateSurface(uiSurfaceWorktree)

	for name, model := range map[string]*uiModel{
		"status": status, "worktree-list": list, "worktree-create": create, "worktree-delete": deleteConfirm,
	} {
		t.Run(name, func(t *testing.T) {
			view := model.View()
			if got := len(strings.Split(strings.TrimSuffix(view, ansiHideCursor), "\n")); got != height {
				t.Fatalf("rendered line count = %d, want %d", got, height)
			}
			assertRenderedLinesFitWidth(t, view, width)
		})
	}
}
