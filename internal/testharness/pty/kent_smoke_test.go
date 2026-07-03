package pty_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/pty"
	"core/internal/testharness/pty/appfixture"
)

func TestProductionKentBinaryPTYSmoke(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	bin := filepath.Join(t.TempDir(), "kent")
	if err := pty.BuildPackage(ctx, "core/cli/kent", bin); err != nil {
		t.Fatalf("build production kent: %v", err)
	}
	workspace := t.TempDir()
	persistenceRoot := t.TempDir()
	if err := appfixture.PrepareConfigAndBinding(ctx, persistenceRoot, workspace); err != nil {
		t.Fatalf("prepare isolated config: %v", err)
	}

	capture, err := pty.RunCommand(ctx, pty.CommandSpec{
		Path:       bin,
		Args:       []string{"--force-interactive", "--persistence-root", persistenceRoot},
		Dir:        workspace,
		Dimensions: pty.MustDimensions(24, 80),
		ParseableInputs: []pty.ParseableInputEvent{
			{Bytes: []byte{0x03, 0x03}},
		},
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("run production kent smoke: %v raw=%q", err, string(capture.Raw))
	}
	if len(capture.Raw) == 0 {
		t.Fatal("expected production kent to emit terminal bytes")
	}
	if _, err := pty.Analyze(capture); err != nil {
		t.Fatalf("analyze production kent smoke: %v", err)
	}
	if capture.ProcessExit == nil {
		t.Fatal("expected process exit state")
	}
}
