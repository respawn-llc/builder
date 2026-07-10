package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"core/server/session"
	"core/server/tools"
)

func TestPrepareInspectionRequestKeepsMetaContextEphemeral(t *testing.T) {
	persisted := mustCreateTestSession(t)
	if err := persisted.SetName("inspection"); err != nil {
		t.Fatalf("persist session metadata: %v", err)
	}
	eventsPath := filepath.Join(persisted.Dir(), "events.jsonl")
	before, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read event log before inspection: %v", err)
	}

	inspectionStore, err := session.Open(
		persisted.Dir(),
		session.WithFilelessMetadataPersistence(),
		session.WithFilelessEventPersistence(),
	)
	if err != nil {
		t.Fatalf("open inspection session: %v", err)
	}
	engine := mustNewTestEngine(
		t,
		inspectionStore,
		&fakeClient{},
		tools.NewRegistry(),
		Config{
			Model:           "gpt-5",
			GlobalConfigDir: t.TempDir(),
		},
	)
	request, err := PrepareInspectionRequest(context.Background(), engine, true)
	if err != nil {
		t.Fatalf("PrepareInspectionRequest: %v", err)
	}
	if len(request.Items) == 0 {
		t.Fatal("inspection request is missing prepared meta context")
	}

	after, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read event log after inspection: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("inspection request appended meta context to the durable event log")
	}
}
