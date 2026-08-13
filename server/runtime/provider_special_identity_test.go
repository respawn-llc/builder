package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/shared/sessioncontract"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestStandaloneCompactionUsesExclusiveRunIdentityAndFreshRebuildState(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &standaloneIdentityCompactionClient{}
	engine := mustNewTestEngine(t, store, client, newTestToolRegistry(t), Config{
		Model:               "gpt-5",
		CompactionMode:      "native",
		ContextWindowTokens: 2_500,
	})
	client.engine = engine
	if err := engine.steer("input", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}
	if err := engine.steer("tool-call", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
			ID:    "call-1",
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{"command":"pwd"}`),
		}}}},
	)); err != nil {
		t.Fatalf("persist compaction tool call: %v", err)
	}
	if err := engine.steer("tool-output", steerMessagesWithPersistenceIntent(
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
		t.Fatalf("persist compaction tool output: %v", err)
	}

	if err := engine.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("standalone compaction: %v", err)
	}
	if len(client.calls) != 2 {
		t.Fatalf("compaction calls = %d, want changed-payload rebuild", len(client.calls))
	}
	if len(client.activeRuns) != 2 || client.activeRuns[0] == nil || client.activeRuns[1] == nil {
		t.Fatalf("active compaction Runs = %+v, want one live Run observed by both calls", client.activeRuns)
	}
	if client.activeRuns[0].ActiveKind != ActiveKindCompaction ||
		client.activeRuns[1].RunID != client.activeRuns[0].RunID {
		t.Fatalf("active compaction Runs = %+v, want one exclusive compaction Run", client.activeRuns)
	}
	assertCodexIdentity(t, client.calls[0].CodexDispatch, codexIdentityExpectation{
		SessionID:   store.Meta().SessionID,
		TurnID:      client.activeRuns[0].RunID,
		WindowID:    store.Meta().SessionID + ":0",
		RequestKind: llm.CodexRequestKindCompaction,
	})
	assertCodexIdentity(t, client.calls[1].CodexDispatch, codexIdentityExpectation{
		SessionID:   store.Meta().SessionID,
		TurnID:      client.activeRuns[0].RunID,
		WindowID:    store.Meta().SessionID + ":0",
		RequestKind: llm.CodexRequestKindCompaction,
	})
	if client.calls[0].CodexDispatch == client.calls[1].CodexDispatch {
		t.Fatal("changed standalone compaction payload reused retry-local state")
	}
}

func TestReviewerRequestsUseSupervisorIdentityAndIsolateRetryState(t *testing.T) {
	withGenerateRetryDelays(t, []time.Duration{0})
	store := mustCreateTestSession(t)
	reviewerClient := &fakeClient{
		errors: []error{errors.New("temporary reviewer failure"), nil, nil},
		responses: []llm.Response{
			{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(`{"suggestions":[]}`)}},
			{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(`{"suggestions":[]}`)}},
		},
	}
	engine := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), Config{
		Model:    "gpt-5",
		Reviewer: ReviewerConfig{Model: "gpt-5"},
	})
	engine.compactionRuntimeState().SetCount(3)

	err := withActiveTestRun(t, engine, ActiveKindUserTurn, func(ctx context.Context, stepID string) error {
		if _, runErr := engine.runReviewerSuggestions(ctx, stepID, reviewerClient); runErr != nil {
			return runErr
		}
		_, runErr := engine.runReviewerSuggestions(ctx, stepID, reviewerClient)
		return runErr
	})
	if err != nil {
		t.Fatalf("Reviewer requests: %v", err)
	}
	if len(reviewerClient.calls) != 3 {
		t.Fatalf("Reviewer provider calls = %d, want retry plus later request", len(reviewerClient.calls))
	}
	activeTurnID := codexIdentity(t, reviewerClient.calls[0].CodexDispatch).TurnID
	wantSessionID := store.Meta().SessionID + "/supervisor"
	for index, call := range reviewerClient.calls {
		assertCodexIdentity(t, call.CodexDispatch, codexIdentityExpectation{
			SessionID:   wantSessionID,
			TurnID:      activeTurnID,
			WindowID:    wantSessionID + ":3",
			RequestKind: llm.CodexRequestKindTurn,
		})
		if call.SessionID != wantSessionID {
			t.Fatalf("Reviewer call %d SessionID = %q, want %q", index+1, call.SessionID, wantSessionID)
		}
	}
	if reviewerClient.calls[0].CodexDispatch != reviewerClient.calls[1].CodexDispatch {
		t.Fatal("unchanged Reviewer retry did not reuse retry-local state")
	}
	if reviewerClient.calls[1].CodexDispatch == reviewerClient.calls[2].CodexDispatch {
		t.Fatal("later Reviewer request reused preceding retry-local state")
	}
}

func TestSubagentUsesOwnSessionRunAndWindowWithoutLineageMetadata(t *testing.T) {
	root := t.TempDir()
	parent := mustCreateTestSessionAt(t, root)
	subagent, err := session.NewLazy(
		root,
		"ws",
		root,
		sessioncontract.SessionCategorySubagent,
		runtimeTestSessionPersistence.Options()...,
	)
	if err != nil {
		t.Fatalf("create subagent Session: %v", err)
	}
	if err := session.InitializeCreationContext(
		subagent,
		parent,
		session.SessionCreationSourceParentAgent,
		session.ChildContextOptions{},
	); err != nil {
		t.Fatalf("initialize subagent ancestry: %v", err)
	}
	if err := subagent.EnsureDurable(); err != nil {
		t.Fatalf("persist subagent Session: %v", err)
	}
	initializeTestEventLog(t, subagent)
	engine := mustNewTestEngine(t, subagent, &fakeClient{}, newTestToolRegistry(t), Config{Model: "gpt-5"})
	engine.compactionRuntimeState().SetCount(2)

	var request llm.Request
	err = withActiveTestRun(t, engine, ActiveKindUserTurn, func(ctx context.Context, stepID string) error {
		var buildErr error
		request, buildErr = engine.buildActiveTurnDispatchRequest(ctx, stepID, nil, true)
		return buildErr
	})
	if err != nil {
		t.Fatalf("build subagent dispatch: %v", err)
	}
	identity := codexIdentity(t, request.CodexDispatch)
	assertCodexIdentity(t, request.CodexDispatch, codexIdentityExpectation{
		SessionID:   subagent.Meta().SessionID,
		TurnID:      identity.TurnID,
		WindowID:    subagent.Meta().SessionID + ":2",
		RequestKind: llm.CodexRequestKindTurn,
	})
	if request.SessionID != subagent.Meta().SessionID {
		t.Fatalf("subagent request SessionID = %q, want own Session %q", request.SessionID, subagent.Meta().SessionID)
	}
	raw, err := request.CodexDispatch.TurnMetadataJSON()
	if err != nil {
		t.Fatalf("subagent metadata: %v", err)
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var fields codexIdentityFields
	if err := decoder.Decode(&fields); err != nil {
		t.Fatalf("decode subagent metadata: %v", err)
	}
	if fields.SessionID != subagent.Meta().SessionID ||
		fields.ThreadID != subagent.Meta().SessionID ||
		fields.TurnID != identity.TurnID ||
		fields.WindowID != subagent.Meta().SessionID+":2" ||
		fields.RequestKind != llm.CodexRequestKindTurn {
		t.Fatalf("strict subagent metadata = %+v, want only own identity facts", fields)
	}
}

type standaloneIdentityCompactionClient struct {
	engine     *Engine
	calls      []llm.CompactionRequest
	activeRuns []*RunSnapshot
}

func (*standaloneIdentityCompactionClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}

func (c *standaloneIdentityCompactionClient) Compact(_ context.Context, request llm.CompactionRequest) (llm.CompactionResponse, error) {
	c.calls = append(c.calls, request)
	c.activeRuns = append(c.activeRuns, c.engine.ActiveRun())
	if len(c.calls) == 1 {
		return llm.CompactionResponse{}, &llm.ProviderAPIError{
			ProviderID: "openai",
			StatusCode: 413,
			Code:       llm.UnifiedErrorCodeContextLengthOverflow,
		}
	}
	return remoteCompactionReplacement(1_000, 100, 2_500), nil
}

func (*standaloneIdentityCompactionClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{
		ProviderID:               "openai",
		SupportsResponsesAPI:     true,
		SupportsResponsesCompact: true,
		IsOpenAIFirstParty:       true,
	}, nil
}

type codexIdentityExpectation struct {
	SessionID   string
	TurnID      string
	WindowID    string
	RequestKind llm.CodexRequestKind
}

type codexIdentityFields struct {
	SessionID   string               `json:"session_id"`
	ThreadID    string               `json:"thread_id"`
	TurnID      string               `json:"turn_id"`
	WindowID    string               `json:"window_id"`
	RequestKind llm.CodexRequestKind `json:"request_kind"`
}

func codexIdentity(t *testing.T, dispatch *llm.CodexDispatchContext) codexIdentityFields {
	t.Helper()
	if dispatch == nil {
		t.Fatal("Codex dispatch context is absent")
	}
	raw, err := dispatch.TurnMetadataJSON()
	if err != nil {
		t.Fatalf("turn metadata: %v", err)
	}
	var fields codexIdentityFields
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatalf("decode turn metadata: %v", err)
	}
	return fields
}

func assertCodexIdentity(t *testing.T, dispatch *llm.CodexDispatchContext, want codexIdentityExpectation) {
	t.Helper()
	got := codexIdentity(t, dispatch)
	if got.SessionID != want.SessionID || got.ThreadID != want.SessionID ||
		got.TurnID != want.TurnID || got.WindowID != want.WindowID ||
		got.RequestKind != want.RequestKind {
		t.Fatalf("Codex identity = %+v, want %+v with matching thread", got, want)
	}
}
