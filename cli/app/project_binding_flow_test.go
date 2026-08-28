package app

import (
	"fmt"
	"testing"
	"time"

	"core/cli/app/internal/projectbinding"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func TestProjectBindingPickerKeyboardScrolls(t *testing.T) {
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
	}, projectbinding.ProjectPickerSnapshot{})
	model.height = 8

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	got := updated.(*projectBindingPickerModel)
	if got.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 after Down", got.cursor)
	}
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyUp})
	if got = updated.(*projectBindingPickerModel); got.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 after Up", got.cursor)
	}
}
