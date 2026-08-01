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
	"core/server/runtimeactivity"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
	"core/shared/toolspec"
)

type inlineCompactionCancellationClient struct {
	firstStarted      chan struct{}
	releaseFirst      chan struct{}
	secondStarted     chan struct{}
	releaseSecond     chan struct{}
	compactionStart   chan struct{}
	compactionRelease chan struct{}
	firstOnce         sync.Once
	secondOnce        sync.Once
	compactionOnce    sync.Once
}

func newInlineCompactionCancellationClient() *inlineCompactionCancellationClient {
	return &inlineCompactionCancellationClient{
		firstStarted:      make(chan struct{}),
		releaseFirst:      make(chan struct{}),
		secondStarted:     make(chan struct{}),
		releaseSecond:     make(chan struct{}),
		compactionStart:   make(chan struct{}),
		compactionRelease: make(chan struct{}),
	}
}

func (c *inlineCompactionCancellationClient) Generate(ctx context.Context, _ llm.Request) (llm.Response, error) {
	select {
	case <-c.firstStarted:
		c.secondOnce.Do(func() { close(c.secondStarted) })
		select {
		case <-c.releaseSecond:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
		return llm.Response{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("continued"),
				Phase:   textutil.Value(llm.MessagePhaseFinal),
			},
			Usage: llm.Usage{WindowTokens: 100},
		}, nil
	default:
		c.firstOnce.Do(func() { close(c.firstStarted) })
		select {
		case <-c.releaseFirst:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
		return llm.Response{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("tool boundary"),
				Phase:   textutil.Value(llm.MessagePhaseCommentary),
			},
			ToolCalls: []llm.ToolCall{{
				ID:    "inline-cancel-tool",
				Name:  string(toolspec.ToolExecCommand),
				Input: json.RawMessage(`{"command":"printf boundary"}`),
			}},
			Usage: llm.Usage{WindowTokens: 100},
		}, nil
	}
}

func (c *inlineCompactionCancellationClient) Compact(ctx context.Context, _ llm.CompactionRequest) (llm.CompactionResponse, error) {
	c.compactionOnce.Do(func() { close(c.compactionStart) })
	select {
	case <-c.compactionRelease:
		return llm.CompactionResponse{}, errors.New("unexpected compaction release")
	case <-ctx.Done():
		return llm.CompactionResponse{}, ctx.Err()
	}
}

func (c *inlineCompactionCancellationClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return runtimeControlOpenAICapabilities, nil
}

func TestServiceInterruptCancelsInlineCompactionAttemptOnly(t *testing.T) {
	client := newInlineCompactionCancellationClient()
	store, _, service := newRuntimeControlTestService(
		t,
		client,
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: fakeShellHandler{},
		}),
		runtime.Config{
			Model:                        "gpt-5",
			ProviderCapabilitiesOverride: &runtimeControlOpenAICapabilities,
		},
	)
	sessionID := store.Meta().SessionID
	parentDone := make(chan error, 1)
	go func() {
		_, err := service.SubmitUserTurn(context.Background(), runtimeControlUserTurnRequest(store, "parent-turn", "start parent turn"))
		parentDone <- err
	}()
	select {
	case <-client.firstStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for parent Agent Turn provider dispatch")
	}
	sessionIDValue, err := runtimeids.ParseSessionID(sessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	if err := service.withLiveExecutionRuntime(context.Background(), sessionIDValue, func(context.Context, *runtime.Engine) error {
		return nil
	}); err != nil {
		t.Fatalf("exact live runtime attachment before compaction: %v", err)
	}
	compactRef := runtimeControlOperationRef(clientui.RuntimeOperationKindCompact)
	compactDone := make(chan error, 1)
	go func() {
		compactDone <- service.CompactContext(context.Background(), serverapi.RuntimeCompactContextRequest{
			ClientRequestID: compactRef.ClientRequestID.String(),
			SessionID:       sessionID,
			Args:            "compact",
			OperationRef:    compactRef,
		})
	}()

	close(client.releaseFirst)
	select {
	case <-client.compactionStart:
	case err := <-compactDone:
		t.Fatalf("inline compaction ended before provider dispatch: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for inline compaction provider dispatch")
	}

	if _, err := service.Interrupt(context.Background(), serverapi.RuntimeInterruptRequest{
		ClientRequestID:    "cancel-inline-compaction",
		SessionID:          sessionID,
		TargetOperationRef: &compactRef,
	}); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	select {
	case err := <-compactDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("inline compaction cancellation error = %v, want context cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("inline compaction attempt was not canceled")
	}

	select {
	case <-client.secondStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("parent Agent Turn did not continue after inline compaction cancellation")
	}
	close(client.releaseSecond)
	select {
	case err := <-parentDone:
		if err != nil {
			t.Fatalf("parent Agent Turn: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("parent Agent Turn did not finish")
	}
}

var _ llm.CompactionClient = (*inlineCompactionCancellationClient)(nil)

type activeCompactionRuntimeActivityResolver struct{}

func (activeCompactionRuntimeActivityResolver) RuntimeReadModelSnapshot(context.Context, string, []clientui.RuntimeOperationRef) (runtimeactivity.ResponseSnapshot, error) {
	return runtimeactivity.ResponseSnapshot{
		Activity: clientui.RuntimeActivity{
			State: clientui.RuntimeActivityRunning,
			ActiveStep: &clientui.RuntimeActiveStep{
				ActiveKind: clientui.RuntimeActivityActiveKindCompaction,
			},
		},
	}, nil
}

func TestServiceInterruptCancelsIdleCompactionWithExclusiveRunOwnership(t *testing.T) {
	trimmed := 1
	client := &blockingCompactionRuntimeControlClient{
		runtimeControlFakeClient: runtimeControlFakeClient{
			responses: []llm.Response{{
				Assistant: llm.Message{
					Role:    llm.RoleAssistant,
					Content: textutil.Value("seeded"),
					Phase:   textutil.Value(llm.MessagePhaseFinal),
				},
				Usage: llm.Usage{WindowTokens: 100},
			}},
			compactionResponses: []llm.CompactionResponse{{
				OutputItems: []llm.ResponseItem{
					{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleUser), MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("summary")},
					{Type: llm.ResponseItemTypeCompaction, EncryptedContent: textutil.Value("checkpoint")},
				},
				Usage:             llm.Usage{WindowTokens: 100},
				TrimmedItemsCount: &trimmed,
			}},
		},
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{
		Model:                        "gpt-5",
		ProviderCapabilitiesOverride: &runtimeControlOpenAICapabilities,
	})
	if _, err := engine.SubmitUserMessage(context.Background(), "seed runtime transcript"); err != nil {
		t.Fatalf("seed runtime transcript: %v", err)
	}
	service.WithRuntimeActivityResolver(activeCompactionRuntimeActivityResolver{})
	ref := runtimeControlOperationRef(clientui.RuntimeOperationKindCompact)
	done := make(chan error, 1)
	go func() {
		done <- service.CompactContext(context.Background(), serverapi.RuntimeCompactContextRequest{
			ClientRequestID: ref.ClientRequestID.String(),
			SessionID:       store.Meta().SessionID,
			Args:            "compact",
			OperationRef:    ref,
		})
	}()
	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for idle compaction provider dispatch")
	}
	if _, err := service.Interrupt(context.Background(), serverapi.RuntimeInterruptRequest{
		ClientRequestID:    "cancel-idle-compaction",
		SessionID:          store.Meta().SessionID,
		TargetOperationRef: &ref,
	}); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("idle compaction cancellation error = %v, want context cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("idle compaction was not canceled")
	}
}
