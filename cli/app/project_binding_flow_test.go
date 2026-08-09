package app

import (
	"fmt"
	"testing"
	"time"

	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func TestProjectBindingPickerMouseWheelScrolls(t *testing.T) {
	projects := make([]clientui.ProjectSummary, 0, 12)
	for i := range 12 {
		projects = append(projects, clientui.ProjectSummary{
			ProjectID:   fmt.Sprintf("project-%02d", i),
			DisplayName: fmt.Sprintf("Project %02d", i),
			RootPath:    fmt.Sprintf("/tmp/project-%02d", i),
			UpdatedAt:   time.Now().Add(-time.Duration(i) * time.Minute),
		})
	}
	model := newProjectBindingPickerModel(projects, "dark", projectPickerOptions{
		HeaderMarkdown: serverProjectPickerHeaderMarkdown,
		HeaderFallback: serverProjectPickerHeaderFallback,
		NoticeText:     serverProjectPickerNoticeText,
		GroupLabel:     serverProjectExistingLabel,
	})
	model.height = 8

	updated, _ := model.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	got := updated.(*projectBindingPickerModel)
	if got.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 after mouse wheel down", got.cursor)
	}
	updated, _ = got.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	if got = updated.(*projectBindingPickerModel); got.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 after mouse wheel up", got.cursor)
	}
}

func TestProjectWorkspacePickerMouseWheelScrolls(t *testing.T) {
	workspaces := make([]clientui.ProjectWorkspaceSummary, 0, 12)
	for i := range 12 {
		workspaces = append(workspaces, clientui.ProjectWorkspaceSummary{
			WorkspaceID: fmt.Sprintf("workspace-%02d", i),
			DisplayName: fmt.Sprintf("Workspace %02d", i),
			RootPath:    fmt.Sprintf("/tmp/workspace-%02d", i),
			UpdatedAt:   time.Now().Add(-time.Duration(i) * time.Minute),
		})
	}
	model := newProjectWorkspacePickerModel(workspaces, "dark")
	model.height = 8

	updated, _ := model.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	got := updated.(*projectWorkspacePickerModel)
	if got.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 after mouse wheel down", got.cursor)
	}
	updated, _ = got.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	if got = updated.(*projectWorkspacePickerModel); got.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 after mouse wheel up", got.cursor)
	}
}
