package pty

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

type artifactDiagnostics struct {
	AssertionError string       `json:"assertion_error,omitempty"`
	ProcessExit    *ProcessExit `json:"process_exit,omitempty"`
	ReadLoopDone   bool         `json:"read_loop_done"`
	Dimensions     Dimensions   `json:"dimensions"`
}

func WriteArtifacts(dir string, capture Capture, analysis Analysis, assertionErr error) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create artifact directory %s: %w", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "raw.bin"), capture.Raw, 0o644); err != nil {
		return fmt.Errorf("write raw artifact: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "escaped.txt"), []byte(strconv.QuoteToASCII(string(capture.Raw))), 0o644); err != nil {
		return fmt.Errorf("write escaped artifact: %w", err)
	}
	if err := writeJSON(filepath.Join(dir, "chunks.json"), capture.Chunks); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "operations.json"), analysis.Operations); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "screen.txt"), []byte(analysis.Screen.RenderText()), 0o644); err != nil {
		return fmt.Errorf("write screen artifact: %w", err)
	}
	diagnostics := artifactDiagnostics{
		ProcessExit:  capture.ProcessExit,
		ReadLoopDone: capture.ReadLoopDone,
		Dimensions:   capture.Dimensions,
	}
	if assertionErr != nil {
		diagnostics.AssertionError = assertionErr.Error()
	}
	if err := writeJSON(filepath.Join(dir, "diagnostics.json"), diagnostics); err != nil {
		return err
	}
	return nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal artifact %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write artifact %s: %w", path, err)
	}
	return nil
}
