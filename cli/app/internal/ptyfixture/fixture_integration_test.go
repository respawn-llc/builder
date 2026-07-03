package ptyfixture

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"core/internal/testharness/pty"
)

func TestFixtureCommandRunsScriptedRuntimeThroughPTY(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	bin := filepath.Join(t.TempDir(), "kent-pty-fixture")
	if err := pty.BuildPackage(ctx, "core/cli/app/internal/ptyfixture/cmd/kent-pty-fixture", bin); err != nil {
		t.Fatalf("build fixture: %v", err)
	}
	workspace := t.TempDir()
	persistenceRoot := t.TempDir()
	scriptPath := filepath.Join(t.TempDir(), "script.json")
	observationsPath := filepath.Join(t.TempDir(), "observations.json")
	script := []byte(`{"prompt":"hello fixture","stream_deltas":["hello "],"final":"hello fixture done"}`)
	if err := os.WriteFile(scriptPath, script, 0o644); err != nil {
		t.Fatalf("write script: %v", err)
	}

	capture, err := pty.RunCommand(ctx, pty.CommandSpec{
		Path: bin,
		Args: []string{
			"--workspace", workspace,
			"--persistence-root", persistenceRoot,
			"--script", scriptPath,
			"--observations", observationsPath,
		},
		Dimensions: pty.MustDimensions(24, 80),
		PhaseInputs: []pty.PhaseInputEvent{
			{Phase: pty.PhaseScenarioStart, Bytes: []byte("hello fixture\r")},
			{Phase: pty.PhaseScenarioComplete, Bytes: []byte{0x03, 0x03}},
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
	if !slices.Contains(phases, pty.PhaseScenarioStart) || !slices.Contains(phases, pty.PhaseScenarioComplete) {
		t.Fatalf("phases = %+v, want scenario start and complete", phases)
	}

	var obs fixtureObservation
	data, err := os.ReadFile(observationsPath)
	if err != nil {
		t.Fatalf("read observations: %v", err)
	}
	if err := json.Unmarshal(data, &obs); err != nil {
		t.Fatalf("decode observations: %v", err)
	}
	if !slices.Contains(obs.FactoryPurposes, "main") {
		t.Fatalf("factory purposes = %+v, want main", obs.FactoryPurposes)
	}
	if obs.ModelRequestCount != 1 || obs.RemainingScriptSteps != 0 || !obs.FinalResponseConsumed {
		t.Fatalf("unexpected observations: %+v", obs)
	}
	if obs.DefaultProviderFallbacks != 0 {
		t.Fatalf("default provider fallbacks = %d, want 0", obs.DefaultProviderFallbacks)
	}
}

type fixtureObservation struct {
	FactoryPurposes          []string `json:"factory_purposes"`
	ModelRequestCount        int      `json:"model_request_count"`
	RemainingScriptSteps     int      `json:"remaining_script_steps"`
	FinalResponseConsumed    bool     `json:"final_response_consumed"`
	DefaultProviderFallbacks int      `json:"default_provider_fallbacks"`
}
