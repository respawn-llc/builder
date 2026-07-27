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

func TestRemoteCompactionFailsNonOverflowProvider400WithoutReplacementOrFallback(t *testing.T) {
	t.Parallel()
	store, client, engine := mustNewRemoteCompactionFailureTestEngine(t, &llm.ProviderAPIError{
		ProviderID: "openai",
		StatusCode: 400,
		Code:       llm.UnifiedErrorCodeUnknown,
	})

	assertRemoteCompactionFailureWithoutReplacementOrFallback(
		t,
		store,
		client,
		engine.CompactContext(context.Background(), ""),
	)
}

func TestRemoteCompactionRetries413OverflowByCollapsingToolOutput(t *testing.T) {
	t.Parallel()
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
	if err := engine.steer("input", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}
	if err := engine.steer("tool", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
			ID:    "call-1",
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{"command":"pwd"}`),
		}}}},
	)); err != nil {
		t.Fatalf("persist tool call: %v", err)
	}
	if err := engine.steer("tool", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
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

func TestRemoteCompactionFails404WithoutReplacementOrFallback(t *testing.T) {
	t.Parallel()
	store, client, engine := mustNewRemoteCompactionFailureTestEngine(t, &llm.ProviderAPIError{
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
	assertRemoteCompactionFailureWithoutReplacementOrFallback(t, store, client, err)
}

func mustNewRemoteCompactionFailureTestEngine(
	t *testing.T,
	compactionErr error,
) (*session.Store, *fakeCompactionClient, *Engine) {
	t.Helper()
	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{compactionErrors: []error{compactionErr}}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model:          "gpt-5",
		CompactionMode: "native",
	})
	if err := engine.steer("input", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}
	return store, client, engine
}

func assertRemoteCompactionFailureWithoutReplacementOrFallback(
	t *testing.T,
	store *session.Store,
	client *fakeCompactionClient,
	err error,
) {
	t.Helper()
	if err == nil {
		t.Fatal("compact context succeeded after provider failure")
	}
	if len(client.compactionCalls) != 1 || len(client.calls) != 0 {
		t.Fatalf(
			"remote/local compaction calls = %d/%d, want one/zero",
			len(client.compactionCalls),
			len(client.calls),
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
