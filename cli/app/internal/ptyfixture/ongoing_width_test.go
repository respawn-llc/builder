package ptyfixture

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"core/internal/testharness/pty"
	"github.com/charmbracelet/x/ansi"
)

// TestOngoingRenderedWidthFollowsTerminalWidth reproduces the width-cap regression
// (KENT-196 issue 3): in a wide terminal the ongoing surface must render content
// at the real terminal width, not a hardcoded smaller value. Runs the real fixture
// binary at 150 columns with a long seeded user message and asserts the emitted
// content width reaches well past any legacy 80-column cap.
func TestOngoingRenderedWidthFollowsTerminalWidth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	bin := filepath.Join(t.TempDir(), "kent-pty-fixture")
	if err := pty.BuildPackage(ctx, "core/cli/app/internal/ptyfixture/cmd/kent-pty-fixture", bin); err != nil {
		t.Fatalf("build fixture: %v", err)
	}

	longText := strings.Repeat("x", 140)
	script := map[string]any{
		"seed_transcript": []map[string]any{
			{"kind": "message", "role": "user", "text": longText},
		},
		"final": "wide complete",
	}
	root := t.TempDir()
	scriptPath := filepath.Join(root, "script.json")
	observationsPath := filepath.Join(root, "observations.json")
	data, err := json.Marshal(script)
	if err != nil {
		t.Fatalf("marshal script: %v", err)
	}
	if err := os.WriteFile(scriptPath, data, 0o644); err != nil {
		t.Fatalf("write script: %v", err)
	}

	capture, err := pty.RunCommand(ctx, pty.CommandSpec{
		Path: bin,
		Args: []string{
			"--workspace", filepath.Join(root, "workspace"),
			"--persistence-root", filepath.Join(root, "persistence"),
			"--script", scriptPath,
			"--observations", observationsPath,
		},
		Dimensions: pty.MustDimensions(24, 150),
		PhaseInputs: []pty.PhaseInputEvent{
			{Phase: pty.PhaseScenarioStart, Bytes: []byte("width_follows_terminal\r")},
			{Phase: pty.PhaseScenarioStart, After: 4 * time.Second, Bytes: []byte{0x03, 0x03}},
		},
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("run fixture: %v raw=%q", err, truncateRawForWidth(capture.Raw))
	}

	analysis, err := pty.Analyze(capture)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	window, err := scenarioOperationWindow(analysis)
	if err != nil {
		t.Fatalf("operation window: %v", err)
	}

	maxWidth := 0
	widestSample := ""
	for boundary := analysis.Dimensions.Rows - 1; boundary > 0; boundary-- {
		appends := pty.CoalesceAppendRows(pty.ClassifyAppends(analysis, window, boundary))
		for _, op := range appends {
			if op.Operation.Write == nil {
				continue
			}
			if w := visualWidth(op.Operation.Write.Text); w > maxWidth {
				maxWidth = w
				widestSample = truncateRawForWidth([]byte(op.Operation.Write.Text))
			}
		}
	}

	t.Logf("max rendered append width at 150 cols = %d sample=%q", maxWidth, widestSample)
	// A full-width divider rule at 150 cols is 150 cells; the seeded 140-char user
	// message renders at 142 cells. Either proves the surface follows the 150-col
	// terminal. The legacy fallback (effectiveWidth=120) caps both at 120.
	if maxWidth < 140 {
		t.Fatalf("ongoing content capped at width %d in a 150-col terminal; want content to follow terminal width (>=140, divider=150)", maxWidth)
	}
}

func visualWidth(s string) int {
	return utf8.RuneCountInString(ansi.Strip(s))
}

func truncateRawForWidth(b []byte) string {
	const limit = 200
	if len(b) <= limit {
		return string(b)
	}
	return string(b[:limit]) + "..."
}
