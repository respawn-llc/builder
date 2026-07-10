package blackbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"core/internal/testharness/pty"
	"core/internal/testharness/pty/analyzer"

	"github.com/google/uuid"
)

// publishFailureArtifacts uses a per-run staging directory so callers never
// observe a partially written failure bundle. The run root is already unique,
// so this publisher has no cross-run deletion authority.
func publishFailureArtifacts(root string, capture analyzer.Capture, analysis *analyzer.Analysis, runErr error, cleanup *IncompleteCleanup) (string, error) {
	if root == "" {
		return "", fmt.Errorf("artifact root is required")
	}
	if analysis == nil {
		replayed, err := analyzer.Analyze(capture)
		if err != nil {
			return "", fmt.Errorf("analyze artifact capture: %w", err)
		}
		analysis = &replayed
	}
	artifactRoot := filepath.Join(root, "artifacts")
	runsRoot := filepath.Join(artifactRoot, "runs")
	if err := os.MkdirAll(runsRoot, 0o700); err != nil {
		return "", fmt.Errorf("create artifact runs root: %w", err)
	}
	id := uuid.NewString()
	staging := filepath.Join(runsRoot, id+".staging")
	final := filepath.Join(runsRoot, id)
	if err := os.Mkdir(staging, 0o700); err != nil {
		return "", fmt.Errorf("create artifact staging: %w", err)
	}
	if err := pty.WriteArtifacts(staging, capture, *analysis, runErr); err != nil {
		return "", err
	}
	metadata := struct {
		RunError string             `json:"run_error"`
		Cleanup  *IncompleteCleanup `json:"cleanup,omitempty"`
	}{
		RunError: runErr.Error(),
		Cleanup:  cleanup,
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return "", fmt.Errorf("marshal artifact metadata: %w", err)
	}
	if err := os.WriteFile(filepath.Join(staging, "result.json"), encoded, 0o600); err != nil {
		return "", fmt.Errorf("write artifact metadata: %w", err)
	}
	if err := os.Rename(staging, final); err != nil {
		return "", fmt.Errorf("publish artifact bundle: %w", err)
	}
	latestStaging := filepath.Join(artifactRoot, "latest.json.staging")
	if err := os.WriteFile(latestStaging, []byte(`{"run":"runs/`+id+`"}`), 0o600); err != nil {
		return "", fmt.Errorf("write latest artifact pointer: %w", err)
	}
	if err := os.Rename(latestStaging, filepath.Join(artifactRoot, "latest.json")); err != nil {
		return "", fmt.Errorf("publish latest artifact pointer: %w", err)
	}
	return final, nil
}
