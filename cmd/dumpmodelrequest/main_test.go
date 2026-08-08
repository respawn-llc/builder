package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"core/server/llm"
	"core/server/metadata"
	"core/server/session"
	"core/shared/sessioncontract"
)

func TestCaptureSessionRequestLeavesSourceSessionUntouched(t *testing.T) {
	fixture := newCaptureSessionFixture(t, false)
	assertCapturedSessionHistory(t, fixture)
}

func TestCaptureSessionRequestRejectsLegacyHistoryWithoutMutatingSource(t *testing.T) {
	fixture := newCaptureSessionFixture(t, true)
	_, err := captureSessionRequest(
		context.Background(),
		fixture.persistenceRoot,
		fixture.sessionID,
		"openai-compatible",
		false,
	)
	if !errors.Is(err, session.ErrDiagnosticLegacyEventLogUnsupported) {
		t.Fatalf("capture legacy Session request error = %v, want %v", err, session.ErrDiagnosticLegacyEventLogUnsupported)
	}
	assertSourceSessionUntouched(t, fixture)
}

func TestOfflineWorkflowRequestRequiresLiveAuthority(t *testing.T) {
	if err := offlineWorkflowRequestError("workflow-session"); !errors.Is(err, errWorkflowRequestRequiresLiveAuthority) {
		t.Fatalf("offline workflow request error = %v, want live-authority requirement", err)
	}
}

type captureSessionFixture struct {
	persistenceRoot string
	sessionID       string
	eventsPath      string
	content         string
	beforeEvents    []byte
	beforeInfo      os.FileInfo
	beforeMeta      *session.Meta
	metadataStore   *metadata.Store
}

func newCaptureSessionFixture(t *testing.T, legacy bool) captureSessionFixture {
	t.Helper()
	persistenceRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	metadataStore, err := metadata.Open(persistenceRoot)
	if err != nil {
		t.Fatalf("open metadata: %v", err)
	}
	t.Cleanup(func() {
		if err := metadataStore.Close(); err != nil {
			t.Errorf("close metadata: %v", err)
		}
	})
	binding, err := metadataStore.RegisterWorkspaceBinding(t.Context(), workspaceRoot)
	if err != nil {
		t.Fatalf("register workspace: %v", err)
	}
	sessionDir := filepath.Join(persistenceRoot, "projects", binding.ProjectID, "sessions")
	store, err := session.Create(
		sessionDir,
		filepath.Base(sessionDir),
		workspaceRoot,
		sessioncontract.SessionCategoryMain,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("create Session: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("ensure durable Session: %v", err)
	}
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	content := "diagnostic history"
	if _, receipt, err := eventLog.AppendRecord(nil, session.MessageRecord{
		Role:    session.MessageRoleUser,
		Content: &content,
	}); err != nil || !receipt.Committed {
		t.Fatalf("append source history = %+v, %v; want committed", receipt, err)
	}
	eventsPath := filepath.Join(store.Dir(), "events.jsonl")
	if legacy {
		legacyEvents := []byte(
			`{"seq":1,"timestamp":"2026-07-20T00:00:00Z","kind":"message","payload":{"role":"user","content":"diagnostic history"}}` + "\n",
		)
		if err := os.WriteFile(eventsPath, legacyEvents, 0o644); err != nil {
			t.Fatalf("write legacy source event log: %v", err)
		}
	}
	beforeEvents, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read source events: %v", err)
	}
	beforeInfo, err := os.Stat(eventsPath)
	if err != nil {
		t.Fatalf("stat source events: %v", err)
	}
	beforeRecord, err := metadataStore.ResolvePersistedSession(t.Context(), store.Meta().SessionID)
	if err != nil {
		t.Fatalf("resolve source Session: %v", err)
	}
	return captureSessionFixture{
		persistenceRoot: persistenceRoot,
		sessionID:       store.Meta().SessionID,
		eventsPath:      eventsPath,
		content:         content,
		beforeEvents:    beforeEvents,
		beforeInfo:      beforeInfo,
		beforeMeta:      beforeRecord.Meta,
		metadataStore:   metadataStore,
	}
}

func assertCapturedSessionHistory(t *testing.T, fixture captureSessionFixture) {
	t.Helper()
	captured, err := captureSessionRequest(
		context.Background(),
		fixture.persistenceRoot,
		fixture.sessionID,
		"openai-compatible",
		false,
	)
	if err != nil {
		t.Fatalf("capture Session request: %v", err)
	}
	found := false
	for _, message := range llm.MessagesFromItems(captured.Request.Items) {
		if message.Role == llm.RoleUser && message.Content != nil && *message.Content == fixture.content {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("captured request omitted persisted user history")
	}

	assertSourceSessionUntouched(t, fixture)
}

func assertSourceSessionUntouched(t *testing.T, fixture captureSessionFixture) {
	t.Helper()
	afterEvents, err := os.ReadFile(fixture.eventsPath)
	if err != nil {
		t.Fatalf("read source events after capture: %v", err)
	}
	afterInfo, err := os.Stat(fixture.eventsPath)
	if err != nil {
		t.Fatalf("stat source events after capture: %v", err)
	}
	if string(afterEvents) != string(fixture.beforeEvents) ||
		afterInfo.Size() != fixture.beforeInfo.Size() ||
		!afterInfo.ModTime().Equal(fixture.beforeInfo.ModTime()) {
		t.Fatal("request capture mutated the source event log")
	}
	afterRecord, err := fixture.metadataStore.ResolvePersistedSession(t.Context(), fixture.sessionID)
	if err != nil {
		t.Fatalf("resolve source Session after capture: %v", err)
	}
	if !reflect.DeepEqual(afterRecord.Meta, fixture.beforeMeta) {
		t.Fatalf("request capture mutated source metadata: before=%+v after=%+v", fixture.beforeMeta, afterRecord.Meta)
	}
}
