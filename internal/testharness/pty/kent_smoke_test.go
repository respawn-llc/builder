//go:build !windows

package pty_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/pty"
	"core/internal/testharness/pty/blackbox"
)

func TestProductionKentBinaryPTYSmoke(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	bin := filepath.Join(t.TempDir(), "kent")
	if err := pty.BuildPackage(ctx, "core/cli/kent", bin); err != nil {
		t.Fatalf("build production kent: %v", err)
	}
	environment, err := blackbox.NewIsolatedEnvironment(bin, nil)
	if err != nil {
		t.Fatalf("start isolated configured server: %v", err)
	}
	defer func() { _ = environment.Server.ForceKill() }()
	if err := environment.WaitReady(); err != nil {
		t.Fatalf("wait for isolated server readiness: %v", err)
	}
	if err := environment.BindProject(); err != nil {
		t.Fatalf("bind isolated server workspace: %v", err)
	}
	clientEnv, err := environment.ClientEnvironment()
	if err != nil {
		t.Fatalf("build isolated client environment: %v", err)
	}

	capture, err := pty.RunCommand(ctx, pty.CommandSpec{
		Path:       bin,
		Args:       []string{"--force-interactive", "--persistence-root", environment.Root},
		Dir:        environment.Workspace,
		Env:        clientEnv,
		Dimensions: pty.MustDimensions(24, 80),
		Inputs: []pty.InputEvent{{
			After: 2 * time.Second,
			Bytes: []byte{0x03},
		}},
		Timeout: 15 * time.Second,
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
	if capture.ProcessExit.Code != 0 && !capture.ProcessExit.Signaled {
		t.Fatalf("process exit = %#v, want zero exit or interrupt signal", capture.ProcessExit)
	}
}
