//go:build darwin || linux

package session

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestForkReplayUnderHardMemoryLimit(t *testing.T) {
	if os.Getenv("KENT_FORK_MEMORY_LIMIT_CHILD") == "1" {
		_, err := applyMigrationHardMemoryLimit()
		if err != nil {
			t.Fatalf("apply fork memory limit: %v", err)
		}
		runForkMemoryFixture(t)
		if err := assertMigrationResidentMemoryWithinLimit(); err != nil {
			t.Fatalf("fork resident memory limit: %v", err)
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(
		ctx,
		os.Args[0],
		"-test.run=^TestForkReplayUnderHardMemoryLimit$",
		"-test.count=1",
	)
	command.Env = append(os.Environ(), "KENT_FORK_MEMORY_LIMIT_CHILD=1")
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("memory-limited fork subprocess timed out: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("memory-limited fork subprocess: %v\n%s", err, output)
	}
}

func runForkMemoryFixture(t *testing.T) {
	t.Helper()
	parent := newSessionTestStore(t)
	parentLog := materializedForkEventLog(t, parent)
	const recordCount = 32
	content := strings.Repeat("x", forkReplayFlushByteBudget/2)
	for index := 0; index < recordCount; index++ {
		if _, _, err := parentLog.AppendRecord(
			forkStringPointer("step"),
			LocalEntryRecord{
				Visibility: EntryVisibilityHidden,
				Role:       "test",
				Text:       content,
			},
		); err != nil {
			t.Fatalf("append memory fixture %d: %v", index, err)
		}
	}

	child, err := CloneSession(parentLog, "clone", testSessionCategory)
	if err != nil {
		t.Fatalf("clone memory fixture: %v", err)
	}
	childLog := materializedForkEventLog(t, child)
	if got := mustMaterializedRevision(childLog); got != recordCount {
		t.Fatalf("cloned revision = %d, want %d", got, recordCount)
	}
	assertNoForkTemporaryArtifacts(t, parent.Dir())
	assertNoForkTemporaryArtifacts(t, child.Dir())
}
