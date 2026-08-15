package shell

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/shared/runtimeids"
)

type processReadResult struct {
	command string
	err     error
}

func TestManagerPublishedProcessReadModels(t *testing.T) {
	manager := newBackgroundTestManager(t)
	correlation, err := runtimeids.NewExecutionCorrelation(runtimeids.NewExecutionScopeID(), 7)
	if err != nil {
		t.Fatalf("new execution correlation: %v", err)
	}
	started, err := manager.Start(context.Background(), ExecRequest{
		Command: []string{"sh", "-c", "sleep 30"}, DisplayCommand: "published process",
		ExecutionCorrelation: &correlation, Workdir: t.TempDir(), YieldTime: 25 * time.Millisecond,
	})
	if err != nil || !started.Backgrounded {
		t.Fatalf("start process = %+v, %v", started, err)
	}

	t.Run("catalog during manager mutation", func(t *testing.T) {
		manager.mu.Lock()
		result := make(chan []Snapshot, 1)
		go func() { result <- manager.List() }()
		select {
		case got := <-result:
			manager.mu.Unlock()
			if len(got) != 1 || got[0].ID != started.SessionID {
				t.Fatalf("published catalog = %+v", got)
			}
		case <-time.After(time.Second):
			manager.mu.Unlock()
			t.Fatal("process catalog waited for Manager mutation")
		}
	})

	manager.mu.Lock()
	entry := manager.entries[started.SessionID]
	manager.mu.Unlock()
	want, err := manager.Snapshot(started.SessionID)
	if err != nil {
		t.Fatalf("initial snapshot: %v", err)
	}
	for _, test := range []struct {
		name string
		read func() (string, error)
	}{
		{name: "list during entry mutation", read: func() (string, error) { return manager.List()[0].Command, nil }},
		{name: "detail during entry mutation", read: func() (string, error) {
			snapshot, err := manager.Snapshot(started.SessionID)
			return snapshot.Command, err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry.mu.Lock()
			entry.command = "unpublished mutation"
			result := make(chan processReadResult, 1)
			go func() {
				command, err := test.read()
				result <- processReadResult{command, err}
			}()
			select {
			case got := <-result:
				entry.mu.Unlock()
				if got.err != nil || got.command != want.Command {
					t.Fatalf("published command = %q, %v; want %q", got.command, got.err, want.Command)
				}
			case <-time.After(time.Second):
				entry.mu.Unlock()
				t.Fatal("process read waited for entry mutation")
			}
		})
	}

	t.Run("clone isolation", func(t *testing.T) {
		first := manager.List()
		*first[0].ExecutionCorrelation = runtimeids.ExecutionCorrelation{}
		second := manager.List()
		if second[0].ExecutionCorrelation == nil || second[0].ExecutionCorrelation.Validate() != nil {
			t.Fatalf("returned process facts aliased publication: %+v", second[0])
		}
	})

	t.Run("inline output bounded poll", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "process.log")
		if err := os.WriteFile(logPath, nil, 0o600); err != nil {
			t.Fatalf("create log: %v", err)
		}
		entry := &processEntry{id: "1000", logPath: logPath, running: true, notify: make(chan struct{}, 1), done: make(chan struct{})}
		entry.publishSnapshotLocked()
		polling := &Manager{entries: map[string]*processEntry{entry.id: entry}}
		polling.publishCatalogLocked()
		go func() {
			time.Sleep(logWriterFlushDelay)
			_ = os.WriteFile(logPath, []byte("flushed output"), 0o600)
		}()
		output, _, err := polling.InlineOutput(entry.id, 1_024)
		if err != nil || output != "flushed output" {
			t.Fatalf("inline output = %q, %v", output, err)
		}
	})
}
