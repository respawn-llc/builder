package app

import (
	"strings"
	"testing"

	"core/cli/app/internal/worktreeui"
	"core/shared/serverapi"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestWorktreeOverlayScreenReview(t *testing.T) {
	list := testMainWorktreeListResponse()
	list.Worktrees = append(list.Worktrees,
		testRegisteredWorktreeListEntry("wt-feature-a", "feature-a", "/repo/.worktrees/feature-a", "feature/a", false, false, true, true),
		testRegisteredWorktreeListEntry("wt-feature-b", "feature-b", "/repo/.worktrees/feature-b", "feature/b", false, false, true, true),
	)
	items := make([]worktreeui.Item, 0, len(list.Worktrees))
	for _, entry := range list.Worktrees {
		items = append(items, mustProjectWorktreeItem(t, entry))
	}

	scenarios := []struct {
		name   string
		width  int
		height int
		setup  func(*uiModel)
	}{
		{
			name: "narrow-scrolled-list", width: 32, height: 12,
			setup: func(model *uiModel) {
				model.worktrees.target = list.Target
				model.worktrees.entries = items
				model.worktrees.selection = len(items)
			},
		},
		{
			name: "loading", width: 48, height: 10,
			setup: func(model *uiModel) {
				model.worktrees.listPending = true
				model.worktrees.target = list.Target
			},
		},
		{
			name: "list-error", width: 40, height: 10,
			setup: func(model *uiModel) {
				model.worktrees.errorText = "The worktree list could not be refreshed because the server connection closed."
			},
		},
		{
			name: "create-setup", width: 44, height: 16,
			setup: func(model *uiModel) {
				model.worktrees.phase = uiWorktreeOverlayPhaseCreate
				model.worktrees.create = newWorktreeCreateDialog("")
				model.worktrees.create.branchTarget.SetText("feature/screen-review")
				model.worktrees.create.resolution.Kind = serverapi.WorktreeCreateTargetResolutionKindNewBranch
				model.worktrees.create.submitting = true
				model.worktrees.create.setupEvent = &serverapi.WorktreeSetupEvent{
					SetupOperationID:    serverapi.NewWorktreeSetupOperationID(),
					SourceWorkspaceRoot: "/repo",
					WorktreeRoot:        "/repo/.worktrees/feature-screen-review",
					ScriptPath:          "/repo/scripts/setup-worktree.sh",
					Phase:               serverapi.WorktreeSetupPhaseStarted,
				}
			},
		},
		{
			name: "delete-force-confirmation", width: 42, height: 14,
			setup: func(model *uiModel) {
				model.worktrees.phase = uiWorktreeOverlayPhaseDeleteConfirm
				model.worktrees.deleteConfirm = uiWorktreeDeleteDialogState{
					target:             worktreeDeleteControllerTarget(t),
					selectedAction:     uiWorktreeDeleteActionDelete,
					forceFolderRemoval: true,
					errorText:          "The worktree has uncommitted changes. Confirm again to force folder removal.",
				}
			},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			model := newWorktreeTestModel(t, &worktreeCommandTestClient{})
			model.worktrees.open = true
			scenario.setup(model)
			lines := model.layout().renderWorktreeOverlay(scenario.width, scenario.height, uiThemeStyles("dark"))
			if len(lines) != scenario.height {
				t.Fatalf("rendered line count = %d, want %d", len(lines), scenario.height)
			}
			for index, line := range lines {
				if width := lipgloss.Width(line); width > scenario.width {
					t.Fatalf("line %d width = %d, max %d", index, width, scenario.width)
				}
			}
			plain := make([]string, len(lines))
			for index, line := range lines {
				plain[index] = strings.TrimRight(ansi.Strip(line), " ")
			}
			t.Log("\n" + strings.Join(plain, "\n"))
		})
	}
}
