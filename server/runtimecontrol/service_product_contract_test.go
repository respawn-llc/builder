package runtimecontrol_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"core/internal/testharness/runtimecontrolfixture"
	"core/internal/testharness/toolfixture"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
	"core/shared/textutil"
	"core/shared/transcript"
)

var productRuntimeCapabilities = llm.ProviderCapabilities{
	ProviderID:               "openai",
	SupportsResponsesAPI:     true,
	SupportsResponsesCompact: true,
	IsOpenAIFirstParty:       true,
}

type productRuntimeClient struct {
	mu                  sync.Mutex
	responses           []llm.Response
	generateErr         error
	generateCalls       int
	compactionResponses []llm.CompactionResponse
	compactionCalls     int
}

func (c *productRuntimeClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generateCalls++
	if c.generateErr != nil {
		return llm.Response{}, c.generateErr
	}
	if len(c.responses) == 0 {
		return llm.Response{}, nil
	}
	response := c.responses[0]
	c.responses = c.responses[1:]
	return response, nil
}

func (c *productRuntimeClient) Compact(context.Context, llm.CompactionRequest) (llm.CompactionResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.compactionCalls++
	if len(c.compactionResponses) == 0 {
		return llm.CompactionResponse{}, nil
	}
	response := c.compactionResponses[0]
	c.compactionResponses = c.compactionResponses[1:]
	return response, nil
}

func (c *productRuntimeClient) calls() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generateCalls, c.compactionCalls
}

func productRuntimeConfig() runtime.Config {
	return runtime.Config{Model: "gpt-5", ProviderCapabilitiesOverride: &productRuntimeCapabilities}
}

func TestServiceSubmitUserTurnReplaysAcceptedError(t *testing.T) {
	acceptedErr := &llm.APIStatusError{StatusCode: 400, Body: "model failed after input acceptance"}
	client := &productRuntimeClient{generateErr: acceptedErr}
	fixture := runtimecontrolfixture.New(t, runtimecontrolfixture.Options{Client: client, Runtime: productRuntimeConfig()})
	request := serverapi.RuntimeSubmitUserTurnRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
		SessionID:       fixture.Store.Meta().SessionID,
		Input:           runtimeinput.Text("accepted once"),
	}

	for attempt := 0; attempt < 2; attempt++ {
		if _, err := fixture.Service.SubmitUserTurn(t.Context(), request); !errors.Is(err, acceptedErr) || errors.Is(err, serverapi.ErrRuntimeCommandNotAccepted) {
			t.Fatalf("SubmitUserTurn attempt %d error = %v, want accepted model error", attempt+1, err)
		}
	}
	generateCalls, _ := client.calls()
	if generateCalls != 1 || countUserMessages(t, fixture.Store, "accepted once") != 1 {
		t.Fatalf("generate calls/user messages = %d/%d, want 1/1", generateCalls, countUserMessages(t, fixture.Store, "accepted once"))
	}
}

func TestServiceSubmitUserShellCommandReplaysCommittedError(t *testing.T) {
	registry := toolfixture.NewRegistry(t)
	fixture := runtimecontrolfixture.New(t, runtimecontrolfixture.Options{
		Registry: registry, Runtime: productRuntimeConfig(),
	})
	request := serverapi.RuntimeSubmitUserShellCommandRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
		SessionID:       fixture.Store.Meta().SessionID,
		Command:         "false",
	}

	for attempt := 0; attempt < 2; attempt++ {
		if err := fixture.Service.SubmitUserShellCommand(t.Context(), request); err == nil || errors.Is(err, serverapi.ErrRuntimeCommandNotAccepted) {
			t.Fatalf("SubmitUserShellCommand attempt %d error = %v, want accepted execution error", attempt+1, err)
		}
	}
	if countUserShellCalls(t, fixture.Store, "false") != 1 {
		t.Fatalf("accepted shell command records = %d, want 1", countUserShellCalls(t, fixture.Store, "false"))
	}
}

func TestServiceCompactionReplaysCommittedError(t *testing.T) {
	trimmed := 1
	client := &productRuntimeClient{
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("seeded"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{InputTokens: 330000, WindowTokens: 372000},
		}},
		compactionResponses: []llm.CompactionResponse{{
			OutputItems: []llm.ResponseItem{
				{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleUser), MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("summary")},
				{Type: llm.ResponseItemTypeCompaction, EncryptedContent: textutil.Value("checkpoint")},
			},
			Usage:             llm.Usage{WindowTokens: 200000},
			TrimmedItemsCount: &trimmed,
		}},
	}
	persistence := sessiontest.NewPersistence()
	gate := sessiontest.NewPersistenceGate(persistence)
	fixture := runtimecontrolfixture.New(t, runtimecontrolfixture.Options{
		Client: client, Runtime: productRuntimeConfig(), Persistence: persistence,
		StoreOptions: []session.StoreOption{session.WithPersistenceObserver(gate)},
	})
	if _, err := fixture.Engine.SubmitUserMessage(t.Context(), "seed"); err != nil {
		t.Fatalf("seed runtime transcript: %v", err)
	}
	request := serverapi.RuntimeCompactContextRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
		SessionID:       fixture.Store.Meta().SessionID,
		Args:            "compact now",
	}
	committedErr := errors.New("history replacement observer failed")
	gate.FailNext(committedErr)

	for attempt := 0; attempt < 2; attempt++ {
		if err := fixture.Service.CompactContext(t.Context(), request); !errors.Is(err, committedErr) || errors.Is(err, serverapi.ErrRuntimeCommandNotAccepted) {
			t.Fatalf("CompactContext attempt %d error = %v, want committed error", attempt+1, err)
		}
	}
	_, compactionCalls := client.calls()
	if compactionCalls != 1 {
		t.Fatalf("compaction calls = %d, want 1", compactionCalls)
	}
}

func TestServiceAppendCommittedEntryReplaysVisibility(t *testing.T) {
	fixture := runtimecontrolfixture.New(t, runtimecontrolfixture.Options{Runtime: productRuntimeConfig()})
	request := serverapi.RuntimeAppendCommittedEntryRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
		SessionID:       fixture.Store.Meta().SessionID,
		Role:            "warning",
		Text:            "visible warning",
		Visibility:      string(transcript.EntryVisibilityOngoing),
	}
	if err := fixture.Service.AppendCommittedEntry(t.Context(), request); err != nil {
		t.Fatalf("AppendCommittedEntry first: %v", err)
	}
	if err := fixture.Service.AppendCommittedEntry(t.Context(), request); err != nil {
		t.Fatalf("AppendCommittedEntry replay: %v", err)
	}
	entries := localEntries(t, fixture.Store)
	if len(entries) != 1 || entries[0].Visibility != session.EntryVisibilityOngoing {
		t.Fatalf("local entries = %+v, want one ongoing entry", entries)
	}
}

func TestServiceDiscardQueuedUserMessageDedupesSuccessfulRetry(t *testing.T) {
	fixture := runtimecontrolfixture.New(t, runtimecontrolfixture.Options{Runtime: productRuntimeConfig()})
	first, err := fixture.Engine.QueueUserMessage("same")
	if err != nil {
		t.Fatal(err)
	}
	other, err := fixture.Engine.QueueUserMessage("other")
	if err != nil {
		t.Fatal(err)
	}
	target, err := fixture.Engine.QueueUserMessage("same")
	if err != nil {
		t.Fatal(err)
	}
	request := serverapi.RuntimeDiscardQueuedUserMessageRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
		SessionID:       fixture.Store.Meta().SessionID,
		QueueItemID:     target.ID,
	}
	for attempt := 0; attempt < 2; attempt++ {
		response, err := fixture.Service.DiscardQueuedUserMessage(t.Context(), request)
		if err != nil || !response.Discarded {
			t.Fatalf("DiscardQueuedUserMessage attempt %d = %+v, %v", attempt+1, response, err)
		}
	}
	if !fixture.Engine.DiscardQueuedUserMessage(first.ID) || !fixture.Engine.DiscardQueuedUserMessage(other.ID) || fixture.Engine.DiscardQueuedUserMessage(target.ID) {
		t.Fatal("discard replay changed the wrong queued messages")
	}
}

type productPromptHistoryStore struct {
	mu      sync.Mutex
	entries []metadata.PromptHistoryEntry
}

func (s *productPromptHistoryStore) RecordPromptHistoryEntry(_ context.Context, entry metadata.PromptHistoryEntry) (metadata.PromptHistoryRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
	return metadata.PromptHistoryRecord{Sequence: int64(len(s.entries)), SessionID: entry.SessionID, SourceID: entry.SourceID, Text: entry.Text}, true, nil
}

func (s *productPromptHistoryStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

func TestServiceRecordPromptHistoryDedupesSuccessfulRetry(t *testing.T) {
	fixture := runtimecontrolfixture.New(t, runtimecontrolfixture.Options{Runtime: productRuntimeConfig()})
	history := &productPromptHistoryStore{}
	fixture.Service.WithPromptHistoryStore(history)
	request := serverapi.RuntimeRecordPromptHistoryRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
		SessionID:       fixture.Store.Meta().SessionID,
		Text:            "/resume",
	}
	if err := fixture.Service.RecordPromptHistory(t.Context(), request); err != nil {
		t.Fatalf("RecordPromptHistory first: %v", err)
	}
	if err := fixture.Service.RecordPromptHistory(t.Context(), request); err != nil {
		t.Fatalf("RecordPromptHistory replay: %v", err)
	}
	if history.count() != 1 {
		t.Fatalf("prompt history writes = %d, want 1", history.count())
	}
}

type productActivityResolver struct {
	mu    sync.Mutex
	calls int
}

func (r *productActivityResolver) RuntimeReadModelSnapshot(context.Context, string) (runtimeactivity.ResponseSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return runtimeactivity.ResponseSnapshot{
		Activity: clientui.RuntimeActivity{State: clientui.RuntimeActivityRunning},
	}, nil
}

func (r *productActivityResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func TestServiceInterruptRejectsStaleReadModelLiveness(t *testing.T) {
	fixture := runtimecontrolfixture.New(t, runtimecontrolfixture.Options{Runtime: productRuntimeConfig()})
	resolver := &productActivityResolver{}
	fixture.Service.WithRuntimeActivityResolver(resolver)
	request := serverapi.RuntimeInterruptRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
		SessionID:       fixture.Store.Meta().SessionID,
	}
	for attempt := 0; attempt < 2; attempt++ {
		response, err := fixture.Service.Interrupt(t.Context(), request)
		if !errors.Is(err, serverapi.ErrRuntimeCommandNotAccepted) || response.Activity.ActiveForControl() {
			t.Fatalf("Interrupt attempt %d = %+v, %v", attempt+1, response, err)
		}
	}
	if resolver.callCount() != 0 {
		t.Fatalf("read-model snapshot calls = %d, want 0", resolver.callCount())
	}
}

func countUserMessages(t *testing.T, store *session.Store, content string) int {
	t.Helper()
	records, err := sessiontest.CollectRecords(store)
	if err != nil {
		t.Fatalf("collect Session records: %v", err)
	}
	count := 0
	for _, record := range records {
		payload, err := record.Payload()
		if err != nil {
			t.Fatalf("decode Session record: %v", err)
		}
		message, ok := payload.(session.MessageRecord)
		if ok && message.Role == session.MessageRoleUser && message.Content != nil && *message.Content == content {
			count++
		}
	}
	return count
}

func localEntries(t *testing.T, store *session.Store) []session.LocalEntryRecord {
	t.Helper()
	records, err := sessiontest.CollectRecords(store)
	if err != nil {
		t.Fatalf("collect Session records: %v", err)
	}
	entries := make([]session.LocalEntryRecord, 0)
	for _, record := range records {
		if kind, err := record.Kind(); err != nil || kind != session.EventKindLocalEntry {
			continue
		}
		payload, err := record.Payload()
		if err != nil {
			t.Fatalf("decode local entry: %v", err)
		}
		entry, ok := payload.(session.LocalEntryRecord)
		if !ok {
			t.Fatalf("local entry payload = %T", payload)
		}
		entries = append(entries, entry)
	}
	return entries
}

func countUserShellCalls(t *testing.T, store *session.Store, command string) int {
	t.Helper()
	records, err := sessiontest.CollectRecords(store)
	if err != nil {
		t.Fatalf("collect Session records: %v", err)
	}
	count := 0
	for _, record := range records {
		payload, err := record.Payload()
		if err != nil {
			t.Fatalf("decode Session record: %v", err)
		}
		message, ok := payload.(session.MessageRecord)
		if !ok {
			continue
		}
		for _, call := range message.ToolCalls {
			var input struct {
				Command       string `json:"cmd"`
				UserInitiated bool   `json:"user_initiated"`
			}
			if err := json.Unmarshal(call.Input, &input); err == nil && input.UserInitiated && input.Command == command {
				count++
			}
		}
	}
	return count
}
