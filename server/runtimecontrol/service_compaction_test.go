package runtimecontrol

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/runtime"
	"core/server/sessionruntime"
	"core/server/tools"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
	"core/shared/toolspec"
)

type boundaryCompactionRuntimeControlClient struct {
	mu              sync.Mutex
	generateCalls   int
	compactionCalls int
	activeStarted   chan struct{}
	releaseActive   chan struct{}
	compactionStart chan struct{}
	nextStepStarted chan struct{}
}

type sequentialCompactionRuntimeControlClient struct {
	runtimeControlFakeClient
	compactionErrors []error
}

func (c *sequentialCompactionRuntimeControlClient) Compact(ctx context.Context, req llm.CompactionRequest) (llm.CompactionResponse, error) {
	c.mu.Lock()
	c.compactionCalls++
	if len(c.compactionErrors) != 0 {
		err := c.compactionErrors[0]
		c.compactionErrors = c.compactionErrors[1:]
		c.mu.Unlock()
		return llm.CompactionResponse{}, err
	}
	if len(c.compactionResponses) == 0 {
		c.mu.Unlock()
		return llm.CompactionResponse{}, nil
	}
	resp := c.compactionResponses[0]
	c.compactionResponses = c.compactionResponses[1:]
	c.mu.Unlock()
	return resp, nil
}

func newBoundaryCompactionRuntimeControlClient() *boundaryCompactionRuntimeControlClient {
	return &boundaryCompactionRuntimeControlClient{
		activeStarted:   make(chan struct{}),
		releaseActive:   make(chan struct{}),
		compactionStart: make(chan struct{}),
		nextStepStarted: make(chan struct{}),
	}
}

func (c *boundaryCompactionRuntimeControlClient) Generate(ctx context.Context, _ llm.Request) (llm.Response, error) {
	c.mu.Lock()
	c.generateCalls++
	call := c.generateCalls
	c.mu.Unlock()
	switch call {
	case 1:
		return llm.Response{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("seeded"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		}, nil
	case 2:
		close(c.activeStarted)
		select {
		case <-c.releaseActive:
		case <-ctx.Done():
			return llm.Response{}, context.Cause(ctx)
		}
		return llm.Response{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("working"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
			ToolCalls: []llm.ToolCall{{
				ID:    "call-before-compaction",
				Name:  string(toolspec.ToolExecCommand),
				Input: json.RawMessage(`{"command":"true"}`),
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		}, nil
	case 3:
		close(c.nextStepStarted)
		return llm.Response{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		}, nil
	default:
		return llm.Response{}, errors.New("unexpected model request")
	}
}

func (c *boundaryCompactionRuntimeControlClient) Compact(context.Context, llm.CompactionRequest) (llm.CompactionResponse, error) {
	c.mu.Lock()
	c.compactionCalls++
	c.mu.Unlock()
	close(c.compactionStart)
	trimmed := 1
	return llm.CompactionResponse{
		OutputItems: []llm.ResponseItem{
			{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleUser), MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("summary")},
			{Type: llm.ResponseItemTypeCompaction, EncryptedContent: textutil.Value("checkpoint")},
		},
		Usage:             llm.Usage{WindowTokens: 200000},
		TrimmedItemsCount: &trimmed,
	}, nil
}

func (c *boundaryCompactionRuntimeControlClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return runtimeControlOpenAICapabilities, nil
}

func TestServiceCompactContextDuringAgentStepRunsBeforeNextStep(t *testing.T) {
	client := newBoundaryCompactionRuntimeControlClient()
	registry := tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeShellHandler{}})
	store, engine, service := newRuntimeControlTestService(t, client, registry, runtime.Config{
		Model:                        "gpt-5",
		ProviderCapabilitiesOverride: &runtimeControlOpenAICapabilities,
	})
	if _, err := engine.SubmitUserMessage(context.Background(), "seed compaction eligibility"); err != nil {
		t.Fatalf("seed runtime transcript: %v", err)
	}

	turnDone := make(chan error, 1)
	go func() {
		_, err := service.SubmitUserTurn(context.Background(), runtimeControlUserTurnRequest(store, "active-before-compact", "run a tool"))
		turnDone <- err
	}()
	select {
	case <-client.activeStarted:
	case err := <-turnDone:
		t.Fatalf("Agent Turn ended before active step: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active Agent Step")
	}

	compactDone := make(chan error, 1)
	go func() {
		compactDone <- service.CompactContext(context.Background(), serverapi.RuntimeCompactContextRequest{
			ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
			SessionID:       store.Meta().SessionID,
			Args:            "preserve the tool result",
		})
	}()
	select {
	case err := <-compactDone:
		if errors.Is(err, serverapi.ErrSessionRunStarting) {
			t.Fatalf("CompactContext returned active-run recreation error: %v", err)
		}
		t.Fatalf("CompactContext completed before the active step boundary: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(client.releaseActive)
	select {
	case <-client.compactionStart:
	case <-client.nextStepStarted:
		t.Fatal("next Agent Step started before reserved compaction")
	case err := <-compactDone:
		t.Fatalf("CompactContext ended before provider compaction: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for compaction at the Agent Step boundary")
	}
	if err := <-compactDone; err != nil {
		t.Fatalf("CompactContext: %v", err)
	}
	select {
	case <-client.nextStepStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("next Agent Step did not start after compaction")
	}
	if err := <-turnDone; err != nil {
		t.Fatalf("SubmitUserTurn: %v", err)
	}
}

func TestServiceCompactContextWithoutReadyRuntimeIsNotAcceptedAndUnavailable(t *testing.T) {
	service := NewService(sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{}))
	err := service.CompactContext(context.Background(), serverapi.RuntimeCompactContextRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
		SessionID:       "018fdd67-89ab-4cde-8123-456789abcdef",
	})
	if !errors.Is(err, serverapi.ErrRuntimeCommandNotAccepted) {
		t.Fatalf("CompactContext error = %v, want runtime command not accepted", err)
	}
	if !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		t.Fatalf("CompactContext error = %v, want runtime unavailable cause", err)
	}
}

func TestServiceCompactContextRunsOnIdleReadyRuntime(t *testing.T) {
	store, _, client, service := newRuntimeControlCompactionFixture(t)
	err := service.CompactContext(context.Background(), serverapi.RuntimeCompactContextRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
		SessionID:       store.Meta().SessionID,
		Args:            "compact while idle",
	})
	if err != nil {
		t.Fatalf("CompactContext: %v", err)
	}
	if client.compactionCalls != 1 {
		t.Fatalf("compaction calls = %d, want 1", client.compactionCalls)
	}
	if got := countEventsByKind(t, store, "history_replaced"); got != 1 {
		t.Fatalf("history replacement events = %d, want 1", got)
	}
}

func TestServiceCompactContextRepeatedRequestsUseSeparateReservationsAfterPreCommitFailure(t *testing.T) {
	providerErr := &llm.APIStatusError{StatusCode: 400, Body: "typed compaction policy failure"}
	trimmed := 1
	client := &sequentialCompactionRuntimeControlClient{
		runtimeControlFakeClient: runtimeControlFakeClient{
			responses: []llm.Response{{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("seeded"), Phase: textutil.Value(llm.MessagePhaseFinal)},
				Usage:     llm.Usage{WindowTokens: 200000},
			}},
			compactionResponses: []llm.CompactionResponse{{
				OutputItems: []llm.ResponseItem{
					{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleUser), MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("summary")},
					{Type: llm.ResponseItemTypeCompaction, EncryptedContent: textutil.Value("checkpoint")},
				},
				Usage:             llm.Usage{WindowTokens: 200000},
				TrimmedItemsCount: &trimmed,
			}},
		},
		compactionErrors: []error{providerErr},
	}
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{
		Model:                        "gpt-5",
		ProviderCapabilitiesOverride: &runtimeControlOpenAICapabilities,
	})
	if _, err := engine.SubmitUserMessage(context.Background(), "seed compaction eligibility"); err != nil {
		t.Fatalf("seed runtime transcript: %v", err)
	}

	firstErr := service.CompactContext(context.Background(), serverapi.RuntimeCompactContextRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
		SessionID:       store.Meta().SessionID,
		Args:            "first request",
	})
	if !errors.Is(firstErr, serverapi.ErrRuntimeCommandNotAccepted) || !errors.Is(firstErr, providerErr) {
		t.Fatalf("first CompactContext error = %v, want not accepted typed provider cause", firstErr)
	}
	if err := service.CompactContext(context.Background(), serverapi.RuntimeCompactContextRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
		SessionID:       store.Meta().SessionID,
		Args:            "second request",
	}); err != nil {
		t.Fatalf("second CompactContext: %v", err)
	}
	if client.compactionCalls != 2 {
		t.Fatalf("compaction calls = %d, want 2 separate requests", client.compactionCalls)
	}
	if got := countEventsByKind(t, store, "history_replaced"); got != 1 {
		t.Fatalf("history replacement events = %d, want only the accepted request", got)
	}
}
