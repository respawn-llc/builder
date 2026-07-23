package app

import (
	"strings"
	"testing"

	"core/shared/clientui"

	"github.com/charmbracelet/lipgloss"
)

func TestProcessListEntryRenderTruncatesToWidth(t *testing.T) {
	width := 32
	lines := renderProcessListEntry(clientui.BackgroundProcess{
		ID:           "proc-long",
		State:        "running",
		Running:      true,
		Command:      strings.Repeat("very-long-command ", 8),
		RecentOutput: strings.Repeat("very-long-output ", 8),
	}, true, width, "dark", 0, uiStyles{})

	if len(lines) != processListEntryLines {
		t.Fatalf("rendered line count = %d, want %d", len(lines), processListEntryLines)
	}
	for index, line := range lines {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("line %d width = %d, want <= %d: %q", index, got, width, line)
		}
	}
}

func TestProcessListEntryRenderEmptyOutputFallback(t *testing.T) {
	lines := renderProcessListEntry(clientui.BackgroundProcess{
		ID:      "proc-empty",
		State:   "completed",
		Command: "echo ok",
	}, false, 80, "dark", 0, uiStyles{})

	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "<no output yet>") {
		t.Fatalf("rendered entry missing empty-output fallback: %q", joined)
	}
}

func TestProcessListOutputPreviewUsesLastNonEmptyLine(t *testing.T) {
	got := processListOutputPreview("first\n\n  last value  \n")
	if got != "last value" {
		t.Fatalf("preview = %q, want last non-empty line", got)
	}
	if got := processListOutputPreview("\n \r\n\t"); got != "" {
		t.Fatalf("blank preview = %q, want empty", got)
	}
}

func TestCompactProcessCommandPreviewMarksMultilineTruncation(t *testing.T) {
	got := compactProcessCommandPreview("echo first\necho second")
	if strings.Contains(got, "\n") {
		t.Fatalf("preview contains newline: %q", got)
	}
	if !strings.HasSuffix(got, " …") {
		t.Fatalf("preview = %q, want truncation marker", got)
	}
}

func TestProcessListStartRowEdges(t *testing.T) {
	tests := []struct {
		name          string
		selection     int
		entryCount    int
		contentHeight int
		want          int
	}{
		{name: "negative selection", selection: -1, entryCount: 3, contentHeight: 8, want: 0},
		{name: "empty entries", selection: 0, entryCount: 0, contentHeight: 8, want: 0},
		{name: "zero height", selection: 2, entryCount: 5, contentHeight: 0, want: 0},
		{name: "keeps selected visible", selection: 3, entryCount: 5, contentHeight: processListEntryLines * 2, want: processListEntryLines * 2},
		{name: "clamps past end", selection: 9, entryCount: 5, contentHeight: processListEntryLines, want: processListEntryLines * 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := processListStartRow(tt.selection, tt.entryCount, tt.contentHeight)
			if got != tt.want {
				t.Fatalf("start row = %d, want %d", got, tt.want)
			}
		})
	}
}
