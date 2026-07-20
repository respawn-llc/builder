package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/config"
)

func TestPrepareInspectionRequestKeepsMetaContextEphemeral(t *testing.T) {
	persisted := mustCreateTestSession(t)
	if err := persisted.SetName("inspection"); err != nil {
		t.Fatalf("persist session metadata: %v", err)
	}
	_ = mustMaterializeTestEventLog(t, persisted)
	eventsPath := filepath.Join(persisted.Dir(), "events.jsonl")
	before, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read event log before inspection: %v", err)
	}

	inspectionStore, err := session.Open(
		persisted.Dir(),
		append(runtimeTestSessionPersistence.Options(), session.WithFilelessEventPersistence())...,
	)
	if err != nil {
		t.Fatalf("open inspection session: %v", err)
	}
	eventLog, lease, err := inspectionStore.MaterializeFilelessEventLog(context.Background())
	if err != nil {
		t.Fatalf("materialize fileless event log: %v", err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	engine, err := New(
		inspectionStore,
		eventLog,
		&fakeClient{},
		tools.NewRegistry(),
		Config{
			Model:           "gpt-5",
			GlobalConfigDir: t.TempDir(),
		},
	)
	if err != nil {
		t.Fatalf("new inspection engine: %v", err)
	}
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

func TestPrepareInspectionRequestWithoutToolsUsesAutomaticChoice(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewWorkflowTestEngine(
		t,
		store,
		&fakeClient{},
		testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeTool),
		Config{GlobalConfigDir: t.TempDir()},
	)

	request, err := PrepareInspectionRequest(context.Background(), engine, false)
	if err != nil {
		t.Fatalf("PrepareInspectionRequest: %v", err)
	}
	if len(request.Tools) != 0 || request.EnableNativeWebSearch {
		t.Fatalf("inspection advertised tools: local=%+v native_web_search=%t", request.Tools, request.EnableNativeWebSearch)
	}
	if request.ToolChoiceMode != llm.ToolChoiceModeAutomatic {
		t.Fatalf("tool choice mode = %q, want automatic", request.ToolChoiceMode)
	}
}
