package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestRemoteCompactionRetries413OverflowByCollapsingToolOutput(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{
		compactionErrors: []error{
			&llm.ProviderAPIError{
				ProviderID: "openai",
				StatusCode: 413,
				Code:       llm.UnifiedErrorCodeContextLengthOverflow,
			},
			nil,
		},
		compactionResponses: []llm.CompactionResponse{
			remoteCompactionReplacement(1_000, 100, 2_500),
		},
	}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model:               "gpt-5",
		CompactionMode:      "native",
		ContextWindowTokens: 2_500,
	})
	restoreStep := setTestActiveStep(engine, "input")
	if err := engine.steer("input", steerMessagesWithPersistenceIntent(steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}
	if err := engine.steer("input", steerMessagesWithPersistenceIntent(steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
			ID:    "call-1",
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{"command":"pwd"}`),
		}}}},
	)); err != nil {
		t.Fatalf("persist tool call: %v", err)
	}
	if err := engine.steer("input", steerMessagesWithPersistenceIntent(steeringMessageEventNone,
		true,
		[]llm.Message{{
			Role:       llm.RoleTool,
			ToolCallID: textutil.Value("call-1"),
			Name:       textutil.Value(string(toolspec.ToolExecCommand)),
			Content:    textutil.Value(`{"output":"` + strings.Repeat("x", 4_000) + `"}`),
		}},
	)); err != nil {
		t.Fatalf("persist tool output: %v", err)
	}
	restoreStep()

	if err := engine.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact context after overflow: %v", err)
	}
	if len(client.compactionCalls) != 2 || len(client.calls) != 0 {
		t.Fatalf(
			"remote/local compaction calls = %d/%d, want two/zero",
			len(client.compactionCalls),
			len(client.calls),
		)
	}
	for _, item := range client.compactionCalls[1].InputItems {
		if item.Type == llm.ResponseItemTypeFunctionCallOutput &&
			item.CallID != nil &&
			*item.CallID == "call-1" &&
			isCollapsedCompactionOverflowShellOutput(item.Output) {
			return
		}
	}
	t.Fatal("overflow retry omitted collapsed typed tool output")
}

func TestRemoteCompactionFailureAndCheckpointFallback(t *testing.T) {
	t.Run("non-overflow provider 400 fails without replacement or fallback", func(t *testing.T) {
		store, client, engine := newRemoteCompactionFixture(t, &llm.ProviderAPIError{
			ProviderID: "openai",
			StatusCode: 400,
			Code:       llm.UnifiedErrorCodeUnknown,
		})
		assertRemoteCompactionFailureWithoutReplacementOrFallback(
			t,
			store,
			client,
			engine.CompactContext(context.Background(), ""),
			1,
		)
	})

	t.Run("404 fails without replacement or fallback", func(t *testing.T) {
		store, client, engine := newRemoteCompactionFixture(t, &llm.ProviderAPIError{
			ProviderID: "openai",
			StatusCode: 404,
			Code:       llm.UnifiedErrorCodeUnknown,
		})
		err := engine.CompactContext(context.Background(), "")
		var providerErr *llm.ProviderAPIError
		if !errors.As(err, &providerErr) {
			t.Fatalf("compaction error type = %T, want ProviderAPIError", err)
		}
		if providerErr.StatusCode != 404 {
			t.Fatalf("provider error status = %d, want 404", providerErr.StatusCode)
		}
		assertRemoteCompactionFailureWithoutReplacementOrFallback(t, store, client, err, 1)
	})

	t.Run("missing checkpoint falls back to local", func(t *testing.T) {
		_, client, engine := newRemoteCompactionFixture(t, nil)
		if err := engine.CompactContext(context.Background(), ""); err != nil {
			t.Fatalf("compact context: %v", err)
		}
		summaries := 0
		for _, item := range engine.transcriptRuntimeState().SnapshotItems() {
			if item.Type == llm.ResponseItemTypeMessage &&
				item.MessageType != nil &&
				*item.MessageType == llm.MessageTypeCompactionSummary {
				summaries++
			}
		}
		if len(client.compactionCalls) != 1 || len(client.calls) != 1 || summaries != 1 {
			t.Fatalf(
				"remote/local/summary calls = %d/%d/%d, want one/one/one",
				len(client.compactionCalls),
				len(client.calls),
				summaries,
			)
		}
	})
}

func newRemoteCompactionFixture(
	t *testing.T,
	compactionError error,
) (*session.Store, *fakeCompactionClient, *Engine) {
	t.Helper()

	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("summary")},
		}},
		compactionErrors: []error{compactionError},
		compactionResponses: []llm.CompactionResponse{{
			OutputItems: []llm.ResponseItem{{
				Type:    llm.ResponseItemTypeMessage,
				Role:    textutil.Value(llm.RoleUser),
				Content: textutil.Value("summary"),
			}},
			Usage: llm.Usage{InputTokens: 1_000, OutputTokens: 100, WindowTokens: 200_000},
		}},
	}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model:          "gpt-5",
		CompactionMode: "native",
	})
	restoreStep := setTestActiveStep(engine, "input")
	if err := engine.steer("input", steerMessagesWithPersistenceIntent(steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}
	restoreStep()
	return store, client, engine
}

func assertRemoteCompactionFailureWithoutReplacementOrFallback(
	t *testing.T,
	store *session.Store,
	client *fakeCompactionClient,
	err error,
	wantRemoteCalls int,
) {
	t.Helper()
	if err == nil {
		t.Fatal("compact context succeeded after provider failure")
	}
	if len(client.compactionCalls) != wantRemoteCalls || len(client.calls) != 0 {
		t.Fatalf(
			"remote/local compaction calls = %d/%d, want %d/zero",
			len(client.compactionCalls),
			len(client.calls),
			wantRemoteCalls,
		)
	}
	recent, readErr := mustMaterializeTestEventLog(t, store).ReadRecentRecords(4)
	if readErr != nil {
		t.Fatalf("read bounded compaction records: %v", readErr)
	}
	for _, record := range recent.Records {
		if _, ok := mustSessionEventPayload(record).(session.HistoryReplacementRecord); ok {
			t.Fatalf("provider failure committed history replacement: %+v", record)
		}
	}
}
