package ptyfixture

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"core/internal/testharness/pty"
	"core/internal/testharness/pty/appfixture"
	"github.com/charmbracelet/x/ansi"
)

func TestFixtureCommandRunsScriptedRuntimeThroughPTY(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	bin := buildPTYFixtureBinary(t, ctx)
	workspace := t.TempDir()
	persistenceRoot := t.TempDir()
	scriptPath := filepath.Join(t.TempDir(), "script.json")
	observationsPath := filepath.Join(t.TempDir(), "observations.json")
	longText := strings.Repeat("x", 140)
	script := map[string]any{
		"seed_transcript": []map[string]any{
			{"kind": "message", "role": "user", "text": longText},
		},
		"prompt":        "hello fixture",
		"stream_deltas": []string{"hello "},
		"final":         "hello fixture done",
	}
	encodedScript, err := json.Marshal(script)
	if err != nil {
		t.Fatalf("marshal script: %v", err)
	}
	if err := os.WriteFile(scriptPath, encodedScript, 0o644); err != nil {
		t.Fatalf("write script: %v", err)
	}

	capture, err := pty.RunCommand(ctx, pty.CommandSpec{
		Path: bin,
		Args: []string{appfixture.ProcessTestRunArgument},
		Env: []string{
			ptyFixtureProcessEnv(
				t,
				filepath.Dir(scriptPath),
				workspace,
				persistenceRoot,
				scriptPath,
				observationsPath,
			),
		},
		Dimensions: pty.MustDimensions(24, 150),
		PhaseInputs: []pty.PhaseInputEvent{
			{Phase: pty.PhaseScenarioStart, Bytes: []byte("hello fixture\r")},
			{Phase: pty.PhaseScenarioFinalApplied, Bytes: []byte{0x03, 0x03}},
		},
		Timeout: 20 * time.Second,
	})
	if err != nil {
		t.Fatalf("run fixture: %v raw=%q", err, string(capture.Raw))
	}
	analysis, err := pty.Analyze(capture)
	if err != nil {
		t.Fatalf("analyze fixture capture: %v", err)
	}
	phases := make([]pty.PhaseKind, 0, len(analysis.PhaseEvents))
	for _, event := range analysis.PhaseEvents {
		phases = append(phases, event.Phase)
	}
	completeIndex := slices.Index(phases, pty.PhaseScenarioComplete)
	appliedIndex := slices.Index(phases, pty.PhaseScenarioFinalApplied)
	if !slices.Contains(phases, pty.PhaseScenarioStart) ||
		completeIndex < 0 ||
		appliedIndex < 0 ||
		completeIndex >= appliedIndex {
		t.Fatalf("phases = %+v, want scenario start and scenario final applied after scenario complete", phases)
	}

	window, err := scenarioOperationWindow(analysis)
	if err != nil {
		t.Fatalf("resolve scenario operation window: %v", err)
	}
	maxWidth := 0
	for boundary := analysis.Dimensions.Rows - 1; boundary > 0; boundary-- {
		appends := pty.CoalesceAppendRows(pty.ClassifyAppends(analysis, window, boundary))
		for _, append := range appends {
			if append.Operation.Write == nil {
				continue
			}
			maxWidth = max(maxWidth, utf8.RuneCountInString(ansi.Strip(append.Operation.Write.Text())))
		}
	}
	if maxWidth < len(longText) {
		t.Fatalf("ongoing content capped at width %d in a 150-col terminal; want >=%d", maxWidth, len(longText))
	}
}
