package sessiontest_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
)

func TestSnapshotFromDirReturnsDurableSessionState(t *testing.T) {
	root := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-1", "sessions")
	if err := os.MkdirAll(containerDir, 0o755); err != nil {
		t.Fatalf("mkdir container dir: %v", err)
	}
	store, err := session.Create(containerDir, "workspace-x", "/tmp/work")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := store.SetName("incident triage"); err != nil {
		t.Fatalf("set name: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	startedAt := time.Now().UTC().Add(-time.Minute)
	if _, err := store.AppendRunStarted(session.RunRecord{RunID: "run-1", StepID: "step-1", StartedAt: startedAt}); err != nil {
		t.Fatalf("append run start: %v", err)
	}

	snapshot, err := sessiontest.SnapshotFromDir(store.Dir())
	if err != nil {
		t.Fatalf("snapshot from dir: %v", err)
	}
	if snapshot.Meta.SessionID != store.Meta().SessionID || snapshot.Meta.Name != "incident triage" {
		t.Fatalf("unexpected snapshot meta: %+v", snapshot.Meta)
	}
	if snapshot.ConversationFreshness != session.ConversationFreshnessEstablished {
		t.Fatalf("unexpected conversation freshness: %v", snapshot.ConversationFreshness)
	}
	if len(snapshot.Events) != 2 || len(snapshot.Runs) != 1 {
		t.Fatalf("unexpected snapshot counts: events=%d runs=%d", len(snapshot.Events), len(snapshot.Runs))
	}
	if snapshot.Runs[0].RunID != "run-1" || snapshot.Runs[0].Status != session.RunStatusRunning {
		t.Fatalf("unexpected snapshot run: %+v", snapshot.Runs[0])
	}
}

func TestSnapshotFromDirRejectsSymlinkedEventsFile(t *testing.T) {
	root := t.TempDir()
	targetDir := filepath.Join(root, "target-session")
	targetStore, err := session.Create(targetDir, "workspace-target", "/tmp/work-target")
	if err != nil {
		t.Fatalf("create target store: %v", err)
	}
	if _, _, err := targetStore.AppendEvent("target-step", "message", llm.Message{Role: llm.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("append target event: %v", err)
	}

	sessionDir := filepath.Join(root, "session")
	store, err := session.Create(sessionDir, "workspace-x", "/tmp/work")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := store.SetName("session"); err != nil {
		t.Fatalf("persist session meta: %v", err)
	}
	if err := os.Remove(filepath.Join(store.Dir(), "events.jsonl")); err != nil {
		t.Fatalf("remove events file: %v", err)
	}
	if err := os.Symlink(filepath.Join(targetStore.Dir(), "events.jsonl"), filepath.Join(store.Dir(), "events.jsonl")); err != nil {
		t.Fatalf("symlink events file: %v", err)
	}

	if _, err := sessiontest.SnapshotFromDir(store.Dir()); err == nil || !errors.Is(err, session.ErrSessionFileSymlink) {
		t.Fatalf("expected snapshot to reject symlinked events file, got %v", err)
	}
}
