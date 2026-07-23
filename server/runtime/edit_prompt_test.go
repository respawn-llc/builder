package runtime

import (
	"testing"

	"core/shared/toolspec"
)

func TestEditingToolNameMatchesActiveEditTool(t *testing.T) {
	tests := []struct {
		name    string
		enabled []toolspec.ID
		want    string
	}{
		{name: "patch", enabled: []toolspec.ID{toolspec.ToolPatch}, want: "patch"},
		{name: "edit", enabled: []toolspec.ID{toolspec.ToolEdit}, want: "edit"},
		{name: "neither", enabled: []toolspec.ID{toolspec.ToolExecCommand}, want: "shell"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := editingToolName(tt.enabled)
			if got != tt.want {
				t.Fatalf("editingToolName = %q, want %q", got, tt.want)
			}
		})
	}
}
