package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
)

func TestManagerRetainsMostRecentlyAccessedCompletedShellsWithoutEvictingRunningShells(t *testing.T) {
	const completedRetentionLimit = 1_000

	manager := newShellTestManager(t, time.Millisecond)
	workdir := t.TempDir()
	completedIDs := make([]string, 0, completedRetentionLimit)
	for nextIndex := 0; nextIndex < completedRetentionLimit; {
		batchSize := 32
		if remaining := completedRetentionLimit - nextIndex; remaining < batchSize {
			batchSize = remaining
		}
		completedIDs = append(
			completedIDs,
			startAndCompleteRetainedShellBatch(t, manager, workdir, nextIndex, batchSize)...,
		)
		nextIndex += batchSize
	}

	victimID := completedIDs[0]
	for _, id := range completedIDs[1:] {
		if _, err := manager.Snapshot(id); err != nil {
			t.Fatalf("refresh completed shell %s: %v", id, err)
		}
	}
	if got := len(manager.List()); got != completedRetentionLimit {
		t.Fatalf("retained process count = %d, want %d", got, completedRetentionLimit)
	}

	running := startRetainedShell(
		t,
		manager,
		workdir,
		retainedShellReleaseName(completedRetentionLimit),
		completedRetentionLimit,
	)
	_ = manager.List()
	startAndCompleteRetainedShell(t, manager, workdir, completedRetentionLimit+1)

	if _, err := manager.Snapshot(victimID); !errors.Is(err, ErrResultUnavailable) {
		t.Fatalf("least-recently-accessed completed shell error = %v, want %v", err, ErrResultUnavailable)
	}
	if _, err := manager.Snapshot(completedIDs[1]); err != nil {
		t.Fatalf("recently accessed completed shell was evicted: %v", err)
	}
	runningSnapshot, err := manager.Snapshot(running.SessionID)
	if err != nil {
		t.Fatalf("running shell was evicted: %v", err)
	}
	if !runningSnapshot.Running {
		t.Fatalf("protected shell is not running: %+v", runningSnapshot)
	}
	if got := len(manager.List()); got != completedRetentionLimit+1 {
		t.Fatalf("retained process count with running shell = %d, want %d", got, completedRetentionLimit+1)
	}
}

func startAndCompleteRetainedShell(t *testing.T, manager *Manager, workdir string, index int) string {
	t.Helper()
	return startAndCompleteRetainedShellBatch(t, manager, workdir, index, 1)[0]
}

func startAndCompleteRetainedShellBatch(
	t *testing.T,
	manager *Manager,
	workdir string,
	firstIndex int,
	count int,
) []string {
	t.Helper()
	releaseName := retainedShellReleaseName(firstIndex)
	results := make([]ExecResult, 0, count)
	for offset := range count {
		results = append(results, startRetainedShell(t, manager, workdir, releaseName, firstIndex+offset))
	}
	releasePath := filepath.Join(workdir, releaseName)
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatalf("release shell batch at %d: %v", firstIndex, err)
	}
	ids := make([]string, 0, len(results))
	for _, result := range results {
		testsetup.RequireUntil(t, time.Now().Add(2*time.Second), time.Millisecond, func() bool {
			snapshot, err := manager.Snapshot(result.SessionID)
			return err == nil && !snapshot.Running
		}, "timed out waiting for retained shell %s to complete", result.SessionID)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := manager.WriteStdin(ctx, WriteRequest{
			SessionID:      result.SessionID,
			YieldTime:      time.Millisecond,
			MaxOutputChars: 1_000,
		})
		cancel()
		if err != nil {
			t.Fatalf("wait for retained shell %s terminal delivery: %v", result.SessionID, err)
		}
		ids = append(ids, result.SessionID)
	}
	return ids
}

func startRetainedShell(
	t *testing.T,
	manager *Manager,
	workdir string,
	releaseName string,
	index int,
) ExecResult {
	t.Helper()
	result, err := manager.Start(context.Background(), ExecRequest{
		Command:        []string{"/bin/sh", "-c", fmt.Sprintf("while [ ! -f %s ]; do sleep 0.01; done", releaseName)},
		DisplayCommand: "wait for release",
		Workdir:        workdir,
		YieldTime:      time.Millisecond,
		MaxOutputChars: 1_000,
	})
	if err != nil {
		t.Fatalf("start retained shell %d: %v", index, err)
	}
	if !result.Backgrounded || !result.Running {
		t.Fatalf("shell %d did not move to background: %+v", index, result)
	}
	return result
}

func retainedShellReleaseName(index int) string {
	return fmt.Sprintf("release-%04d", index)
}
