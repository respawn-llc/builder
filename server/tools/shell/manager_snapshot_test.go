package shell

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/shared/runtimeids"
)

const processSnapshotReadTimeout = time.Second

func TestManagerListReturnsPublishedCatalogWhileManagerMutationIsPaused(t *testing.T) {
	manager, processID := startSnapshotTestProcess(t)
	want := manager.List()
	if len(want) != 1 || want[0].ID != processID {
		t.Fatalf("initial process catalog = %+v, want process %s", want, processID)
	}

	manager.mu.Lock()
	result := make(chan []Snapshot, 1)
	go func() {
		result <- manager.List()
	}()

	got := receiveProcessSnapshotsWhileLocked(t, result, manager.mu.Unlock)
	if len(got) != 1 || got[0].ID != processID {
		t.Fatalf("process catalog during mutation = %+v, want prior published process %s", got, processID)
	}
}

func TestManagerListReturnsPublishedProcessSnapshotWhileEntryMutationIsPaused(t *testing.T) {
	manager, processID := startSnapshotTestProcess(t)
	entry := processEntryForSnapshotTest(t, manager, processID)
	want, err := manager.Snapshot(processID)
	if err != nil {
		t.Fatalf("initial process snapshot: %v", err)
	}

	entry.mu.Lock()
	entry.command = "mutating command"
	result := make(chan []Snapshot, 1)
	go func() {
		result <- manager.List()
	}()

	got := receiveProcessSnapshotsWhileLocked(t, result, entry.mu.Unlock)
	if len(got) != 1 || got[0].Command != want.Command {
		t.Fatalf("process catalog during entry mutation = %+v, want prior command %q", got, want.Command)
	}
}

func TestManagerSnapshotReturnsPublishedProcessWhileEntryMutationIsPaused(t *testing.T) {
	manager, processID := startSnapshotTestProcess(t)
	entry := processEntryForSnapshotTest(t, manager, processID)
	want, err := manager.Snapshot(processID)
	if err != nil {
		t.Fatalf("initial process snapshot: %v", err)
	}

	entry.mu.Lock()
	entry.command = "mutating command"
	type snapshotResult struct {
		snapshot Snapshot
		err      error
	}
	result := make(chan snapshotResult, 1)
	go func() {
		snapshot, err := manager.Snapshot(processID)
		result <- snapshotResult{snapshot: snapshot, err: err}
	}()

	var got snapshotResult
	select {
	case got = <-result:
		entry.mu.Unlock()
	case <-time.After(processSnapshotReadTimeout):
		entry.mu.Unlock()
		<-result
		t.Fatal("process detail read waited for a paused process mutation")
	}
	if got.err != nil {
		t.Fatalf("process detail during mutation: %v", got.err)
	}
	if got.snapshot.Command != want.Command {
		t.Fatalf("process detail command during mutation = %q, want prior command %q", got.snapshot.Command, want.Command)
	}
}

func TestManagerSnapshotsDoNotAliasPublishedProcessFacts(t *testing.T) {
	manager := newBackgroundTestManager(t)
	correlation, err := runtimeids.NewExecutionCorrelation(
		runtimeids.NewExecutionScopeID(),
		runtimeids.ResourceGeneration(7),
	)
	if err != nil {
		t.Fatalf("new execution correlation: %v", err)
	}
	result, err := manager.Start(context.Background(), ExecRequest{
		Command:              []string{"sh", "-c", "sleep 30"},
		DisplayCommand:       "snapshot-alias-process",
		ExecutionCorrelation: &correlation,
		Workdir:              t.TempDir(),
		YieldTime:            25 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if !result.Backgrounded {
		t.Fatalf("process did not move to background: %+v", result)
	}

	first := manager.List()
	if len(first) != 1 || first[0].ExecutionCorrelation == nil {
		t.Fatalf("first process catalog = %+v, want correlated process", first)
	}
	*first[0].ExecutionCorrelation = runtimeids.ExecutionCorrelation{}
	first[0] = Snapshot{}

	second := manager.List()
	if len(second) != 1 || second[0].ID != result.SessionID {
		t.Fatalf("mutating returned catalog changed published catalog: %+v", second)
	}
	if second[0].ExecutionCorrelation == nil || second[0].ExecutionCorrelation.Validate() != nil {
		t.Fatalf("mutating returned correlation changed published process facts: %+v", second[0].ExecutionCorrelation)
	}
}

func TestManagerInlineOutputRetainsBoundedFlushPoll(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "process.log")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatalf("create process log: %v", err)
	}
	entry := &processEntry{
		id:      "1000",
		logPath: logPath,
		running: true,
		notify:  make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
	manager := &Manager{entries: map[string]*processEntry{entry.id: entry}}
	entry.mu.Lock()
	entry.publishSnapshotLocked()
	entry.mu.Unlock()
	manager.mu.Lock()
	manager.publishCatalogLocked()
	manager.mu.Unlock()
	writeResult := make(chan error, 1)
	go func() {
		time.Sleep(logWriterFlushDelay)
		writeResult <- os.WriteFile(logPath, []byte("flushed output"), 0o600)
	}()

	output, gotPath, err := manager.InlineOutput(entry.id, 1_024)
	if err != nil {
		t.Fatalf("inline output: %v", err)
	}
	if err := <-writeResult; err != nil {
		t.Fatalf("write delayed process output: %v", err)
	}
	if output != "flushed output" || gotPath != logPath {
		t.Fatalf("inline output = %q at %q, want delayed output at %q", output, gotPath, logPath)
	}
}

func startSnapshotTestProcess(t *testing.T) (*Manager, string) {
	t.Helper()
	manager := newBackgroundTestManager(t)
	result, err := manager.Start(context.Background(), ExecRequest{
		Command:        []string{"sh", "-c", "sleep 30"},
		DisplayCommand: "published-snapshot-process",
		Workdir:        t.TempDir(),
		YieldTime:      25 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if !result.Backgrounded {
		t.Fatalf("process did not move to background: %+v", result)
	}
	return manager, result.SessionID
}

func processEntryForSnapshotTest(t *testing.T, manager *Manager, processID string) *processEntry {
	t.Helper()
	manager.mu.Lock()
	entry := manager.entries[processID]
	manager.mu.Unlock()
	if entry == nil {
		t.Fatalf("process entry %s is unavailable", processID)
	}
	return entry
}

func receiveProcessSnapshotsWhileLocked(
	t *testing.T,
	result <-chan []Snapshot,
	unlock func(),
) []Snapshot {
	t.Helper()
	select {
	case snapshots := <-result:
		unlock()
		return snapshots
	case <-time.After(processSnapshotReadTimeout):
		unlock()
		<-result
		t.Fatal("process list read waited for a paused process mutation")
		return nil
	}
}
