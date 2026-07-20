package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"core/server/auth"
	"core/server/llm"
	"core/server/metadata"
	"core/server/session"
	"core/shared/config"
	"core/shared/sessioncontract"
)

func TestCaptureSessionRequestRestoresLegacyHistoryWithoutMutatingSource(t *testing.T) {
	fixture := newLegacyCaptureFixture(t)
	captured, err := captureSessionRequest(
		context.Background(),
		fixture.persistenceRoot,
		fixture.sessionID,
		"openai-compatible",
		false,
	)
	if err != nil {
		t.Fatalf("capture legacy session request: %v", err)
	}
	messages := llm.MessagesFromItems(captured.Request.Items)
	found := false
	for _, message := range messages {
		if message.Role == llm.RoleUser &&
			message.Content != nil &&
			*message.Content == fixture.content {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("captured request omitted legacy user history: %#v", messages)
	}
	assertLegacyCaptureFixtureUnchangedAndClean(t, fixture)
}

func TestCaptureSessionRequestCancellationCleansLegacyArtifacts(t *testing.T) {
	fixture := newLegacyCaptureFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := captureSessionRequest(
		ctx,
		fixture.persistenceRoot,
		fixture.sessionID,
		"openai-compatible",
		false,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("capture error = %v, want context canceled", err)
	}
	assertLegacyCaptureFixtureUnchangedAndClean(t, fixture)
}

func TestCaptureSessionRequestConstructionFailureCleansLegacyArtifacts(t *testing.T) {
	fixture := newLegacyCaptureFixture(t)

	_, err := captureSessionRequest(
		context.Background(),
		fixture.persistenceRoot,
		fixture.sessionID,
		"anthropic",
		false,
	)
	if err == nil {
		t.Fatal("unsupported diagnostic provider unexpectedly succeeded")
	}
	assertLegacyCaptureFixtureUnchangedAndClean(t, fixture)
}

type legacyCaptureFixture struct {
	persistenceRoot string
	sessionID       string
	eventsPath      string
	sourceBytes     []byte
	sourceSize      int64
	sourceModTime   time.Time
	tempRoot        string
	content         string
}

func newLegacyCaptureFixture(t *testing.T) legacyCaptureFixture {
	t.Helper()
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)
	persistenceRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	ctx := context.Background()
	md, err := metadata.Open(persistenceRoot)
	if err != nil {
		t.Fatalf("open metadata: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := md.Close(); closeErr != nil {
			t.Errorf("close metadata: %v", closeErr)
		}
	})
	binding, err := md.RegisterWorkspaceBinding(ctx, workspaceRoot)
	if err != nil {
		t.Fatalf("register workspace: %v", err)
	}
	sessionDir := filepath.Join(
		persistenceRoot,
		"projects",
		binding.ProjectID,
		"sessions",
	)
	store, err := session.Create(
		sessionDir,
		filepath.Base(sessionDir),
		workspaceRoot,
		sessioncontract.SessionCategoryMain,
		md.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("ensure durable session: %v", err)
	}
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize durable event log: %v", err)
	}
	content := "legacy diagnostic history"
	if _, _, err := eventLog.AppendRecord(nil, session.MessageRecord{
		Role:    session.MessageRoleUser,
		Content: &content,
	}); err != nil {
		t.Fatalf("append source history: %v", err)
	}
	legacy := []byte(
		`{"seq":1,"timestamp":"2026-07-20T00:00:00Z","kind":"message","payload":{"role":"user","content":"legacy diagnostic history"}}` + "\n",
	)
	eventsPath := filepath.Join(store.Dir(), "events.jsonl")
	if err := os.WriteFile(eventsPath, legacy, 0o644); err != nil {
		t.Fatalf("replace source with legacy fixture: %v", err)
	}
	info, err := os.Stat(eventsPath)
	if err != nil {
		t.Fatalf("stat legacy source: %v", err)
	}
	sourceBytes, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read legacy source: %v", err)
	}
	return legacyCaptureFixture{
		persistenceRoot: persistenceRoot,
		sessionID:       store.Metadata().SessionID,
		eventsPath:      eventsPath,
		sourceBytes:     sourceBytes,
		sourceSize:      info.Size(),
		sourceModTime:   info.ModTime(),
		tempRoot:        tempRoot,
		content:         content,
	}
}

func assertLegacyCaptureFixtureUnchangedAndClean(
	t *testing.T,
	fixture legacyCaptureFixture,
) {
	t.Helper()
	info, err := os.Stat(fixture.eventsPath)
	if err != nil {
		t.Fatalf("stat legacy source after capture: %v", err)
	}
	sourceBytes, err := os.ReadFile(fixture.eventsPath)
	if err != nil {
		t.Fatalf("read legacy source after capture: %v", err)
	}
	if !reflect.DeepEqual(sourceBytes, fixture.sourceBytes) ||
		info.Size() != fixture.sourceSize ||
		!info.ModTime().Equal(fixture.sourceModTime) {
		t.Fatal("capture mutated the legacy source event log")
	}
	entries, err := os.ReadDir(fixture.tempRoot)
	if err != nil {
		t.Fatalf("read diagnostic temp root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("diagnostic capture leaked temporary artifacts: %+v", entries)
	}
}

func TestWriteOutputUsesPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(path, []byte("existing"), 0o644); err != nil {
		t.Fatalf("seed output: %v", err)
	}
	if _, err := writeOutput(path, capturedRequest{SessionID: "session"}); err != nil {
		t.Fatalf("writeOutput: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat output: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("output permissions = %o, want 600", got)
	}
}

func TestResolveInspectionProviderCapabilitiesUsesRuntimeBaseURLResolution(t *testing.T) {
	caps, forced, err := resolveInspectionProviderCapabilities(
		auth.EmptyState(),
		config.Settings{
			Model:            "gpt-5.5",
			ProviderOverride: "openai",
			OpenAIBaseURL:    "https://example.invalid/v1",
		},
		nil,
		"",
	)
	if err != nil {
		t.Fatalf("resolveInspectionProviderCapabilities: %v", err)
	}
	if forced {
		t.Fatal("runtime provider resolution unexpectedly forced a contract")
	}
	if got, want := caps.ProviderID, "openai-compatible"; got != want {
		t.Fatalf("provider id = %q, want %q", got, want)
	}
}

func TestResolveInspectionProviderCapabilitiesAcceptsProviderContractOverrides(t *testing.T) {
	lockedVerbosity := true
	locked := &session.LockedContract{ProviderContract: session.LockedProviderCapabilities{
		ProviderID:                "locked",
		SupportsResponsesAPI:      true,
		SupportsProviderVerbosity: &lockedVerbosity,
	}}
	for _, providerID := range []string{"openai", "openai-compatible", "chatgpt-codex"} {
		t.Run(providerID, func(t *testing.T) {
			caps, forced, err := resolveInspectionProviderCapabilities(auth.EmptyState(), config.Settings{OpenAIBaseURL: "https://example.invalid/v1"}, locked, providerID)
			if err != nil {
				t.Fatalf("resolveInspectionProviderCapabilities: %v", err)
			}
			if !forced {
				t.Fatal("provider override did not force its capability contract")
			}
			if got := caps.ProviderID; got != providerID {
				t.Fatalf("provider id = %q, want %q", got, providerID)
			}
		})
	}
}

func TestResolveInspectionProviderCapabilitiesPrefersLockedContractOverChangedConfiguration(t *testing.T) {
	lockedVerbosity := true
	caps, forced, err := resolveInspectionProviderCapabilities(
		auth.EmptyState(),
		config.Settings{ProviderCapabilities: config.ProviderCapabilitiesOverride{
			ProviderID:                "configured",
			SupportsResponsesAPI:      true,
			SupportsProviderVerbosity: false,
		}},
		&session.LockedContract{ProviderContract: session.LockedProviderCapabilities{
			ProviderID:                "locked",
			SupportsResponsesAPI:      true,
			SupportsProviderVerbosity: &lockedVerbosity,
		}},
		"",
	)
	if err != nil {
		t.Fatalf("resolveInspectionProviderCapabilities: %v", err)
	}
	if forced {
		t.Fatal("locked contract unexpectedly forced an inspector override")
	}
	if got, want := caps.ProviderID, "locked"; got != want {
		t.Fatalf("provider id = %q, want %q", got, want)
	}
	if !caps.SupportsProviderVerbosity {
		t.Fatalf("locked provider verbosity support = false, want true")
	}
}

func TestResolveInspectionProviderCapabilitiesUsesLockedContract(t *testing.T) {
	caps, forced, err := resolveInspectionProviderCapabilities(
		auth.EmptyState(),
		config.Settings{OpenAIBaseURL: "https://api.openai.com/v1"},
		&session.LockedContract{ProviderContract: session.LockedProviderCapabilities{ProviderID: "openai-compatible", SupportsResponsesAPI: true}},
		"",
	)
	if err != nil {
		t.Fatalf("resolveInspectionProviderCapabilities: %v", err)
	}
	if forced {
		t.Fatal("locked contract unexpectedly forced an inspector override")
	}
	if got, want := caps.ProviderID, "openai-compatible"; got != want {
		t.Fatalf("provider id = %q, want %q", got, want)
	}
}

func TestResumedInspectionWirePayloadUsesLockedVerbosityAcrossConfigChanges(t *testing.T) {
	lockedVerbosity := true
	caps, _, err := resolveInspectionProviderCapabilities(
		auth.EmptyState(),
		config.Settings{ProviderCapabilities: config.ProviderCapabilitiesOverride{
			ProviderID:                "configured",
			SupportsResponsesAPI:      true,
			SupportsProviderVerbosity: false,
		}},
		&session.LockedContract{ProviderContract: session.LockedProviderCapabilities{
			ProviderID:                "locked",
			SupportsResponsesAPI:      true,
			SupportsProviderVerbosity: &lockedVerbosity,
		}},
		"",
	)
	if err != nil {
		t.Fatalf("resolve inspection provider capabilities: %v", err)
	}
	wire, err := llm.MarshalOpenAIWirePayload(
		llm.OpenAIRequest{Model: "operator-alias", ToolChoiceMode: llm.ToolChoiceModeAutomatic},
		false,
		"high",
		llm.OpenAIAuthMode{},
		caps,
	)
	if err != nil {
		t.Fatalf("marshal wire payload: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(wire, &payload); err != nil {
		t.Fatalf("decode wire payload: %v", err)
	}
	text, ok := payload["text"].(map[string]any)
	if !ok {
		t.Fatalf("expected text config in wire payload, got %#v", payload)
	}
	if got := text["verbosity"]; got != "high" {
		t.Fatalf("text.verbosity = %#v, want high", got)
	}
}

func TestValidateOpenAIResponsesInspectionProviderRejectsUnsupportedProvider(t *testing.T) {
	err := validateOpenAIResponsesInspectionProvider(llm.ProviderCapabilities{ProviderID: "anthropic"})
	if err == nil {
		t.Fatal("expected unsupported provider error")
	}
}

func TestInspectionHeadlessModeIncludesPersistedHeadlessSessions(t *testing.T) {
	if !inspectionHeadlessMode(true, false) {
		t.Fatal("persisted headless session was not inspected as headless")
	}
	if !inspectionHeadlessMode(false, true) {
		t.Fatal("workflow session was not inspected as headless")
	}
	if inspectionHeadlessMode(false, false) {
		t.Fatal("interactive session was inspected as headless")
	}
}
