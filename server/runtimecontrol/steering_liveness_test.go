package runtimecontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"core/server/llm"
	"core/server/runtime"
	"core/server/tools"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/textutil"
	"core/shared/toolspec"
)

type steeringExchange struct {
	request llm.Request
	reply   chan llm.Response
}

type steeringExchangeClient chan steeringExchange

func (c steeringExchangeClient) Generate(ctx context.Context, request llm.Request, _ llm.StreamCallbacks) (llm.Response, error) {
	exchange := steeringExchange{request: request, reply: make(chan llm.Response, 1)}
	select {
	case c <- exchange:
	case <-ctx.Done():
		return llm.Response{}, context.Cause(ctx)
	}
	select {
	case response := <-exchange.reply:
		return response, nil
	case <-ctx.Done():
		return llm.Response{}, context.Cause(ctx)
	}
}

func (c steeringExchangeClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{}, nil
}

func nextSteeringExchange(t *testing.T, client steeringExchangeClient) steeringExchange {
	t.Helper()
	select {
	case exchange := <-client:
		return exchange
	case <-time.After(3 * time.Second):
		t.Fatal("model loop wedged before its next request")
		return steeringExchange{}
	}
}

func TestServiceRepeatedConcurrentSteersDrainWithoutInterrupt(t *testing.T) {
	client := make(steeringExchangeClient)
	registry := newTestToolRegistry(t, tools.HandlerRegistration{
		ID: toolspec.ToolExecCommand, Handler: fakeShellHandler{},
	})
	store, engine, service := newRuntimeControlTestService(t, client, registry, runtime.Config{})
	_, err := service.SubmitUserTurn(t.Context(), runtimeControlUserTurnRequest(store, "start", "start"))
	if err != nil {
		t.Fatal(err)
	}
	// Release a held provider request even if a liveness assertion fails.
	t.Cleanup(func() {
		if err := engine.Interrupt(); err != nil {
			t.Errorf("interrupt held test request: %v", err)
		}
	})
	exchange := nextSteeringExchange(t, client)
	policy := engine.LiveChatContextSnapshot().Policy
	nearThreshold := int(policy.AutomaticThresholdTokens) - config.DefaultPreSubmitRunwayTokens/2 - exchange.request.MaxTokens
	wantMessages := []string{"start"}
	for round := range 12 {
		done := make(chan error, 3)
		for index := range 3 {
			text := fmt.Sprintf("round %d instruction %d", round, index)
			go func() {
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				_, err := service.SubmitUserTurn(ctx, runtimeControlUserTurnRequest(store, text, text))
				done <- err
			}()
		}
		for range 3 {
			if err := <-done; err != nil {
				t.Fatalf("round %d steering wedged: %v", round, err)
			}
		}
		pending, err := service.ListPendingWork(t.Context(), serverapi.RuntimeListPendingWorkRequest{
			SessionID: store.Meta().SessionID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(pending.PendingWork.Items) != 3 {
			t.Fatalf("round %d has %d pending messages, want 3", round, len(pending.PendingWork.Items))
		}
		for _, item := range pending.PendingWork.Items {
			wantMessages = append(wantMessages, item.CanonicalInput)
		}
		inputTokens := 1000
		if round >= 2 {
			inputTokens = nearThreshold
		}
		exchange.reply <- llm.Response{
			Assistant: llm.Message{
				Role: llm.RoleAssistant, Content: textutil.Value("working"),
				Phase: textutil.Value(llm.MessagePhaseCommentary),
			},
			ToolCalls: []llm.ToolCall{{
				ID: fmt.Sprintf("call-%d", round), Name: string(toolspec.ToolExecCommand),
				Input: json.RawMessage(`{"cmd":":"}`),
			}},
			Usage: llm.Usage{InputTokens: inputTokens, WindowTokens: int(policy.ContextWindowTokens)},
		}
		exchange = nextSteeringExchange(t, client)
		var gotMessages []string
		for _, message := range llm.MessagesFromItems(exchange.request.Items) {
			if message.Role == llm.RoleUser && message.Content != nil {
				gotMessages = append(gotMessages, *message.Content)
			}
		}
		if !slices.Equal(gotMessages, wantMessages) {
			t.Fatalf("round %d provider context = %q, want all separate FIFO messages %q", round, gotMessages, wantMessages)
		}
		if engine.HasQueuedUserWork() {
			t.Fatalf("round %d left messages stuck after the next provider request", round)
		}
	}
	exchange.reply <- llm.Response{
		Assistant: llm.Message{
			Role: llm.RoleAssistant, Content: textutil.Value("done"),
			Phase: textutil.Value(llm.MessagePhaseFinal),
		},
		Usage: llm.Usage{WindowTokens: 200000},
	}
	waitForRuntimeControlIdle(t, engine)
}
