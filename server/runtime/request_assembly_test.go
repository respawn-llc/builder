package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"core/server/llm"
	"core/shared/textutil"
)

type requestAssemblyProbeClient struct {
	capabilityCalls int
}

func (c *requestAssemblyProbeClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}

func (c *requestAssemblyProbeClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	c.capabilityCalls++
	return llm.ProviderCapabilities{
		ProviderID:           "openai",
		SupportsResponsesAPI: true,
	}, nil
}

func TestDispatchRequestAssemblyRequiresOwningImmutableIdentity(t *testing.T) {
	t.Parallel()
	client := &requestAssemblyProbeClient{}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		newTestToolRegistry(t),
		Config{Model: "gpt-5"},
	)
	client.capabilityCalls = 0

	_, err := engine.buildDispatchRequest(context.Background(), "", nil, true, dispatchRequestIdentity{})
	if !errors.Is(err, llm.ErrInvalidRequest) {
		t.Fatalf("build dispatch request error = %v, want ErrInvalidRequest", err)
	}
	if client.capabilityCalls != 0 {
		t.Fatalf("provider capability calls = %d, want none before identity validation", client.capabilityCalls)
	}
}

func TestDispatchRequestAssemblyUsesOnlyExplicitIdentityFacts(t *testing.T) {
	t.Parallel()
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		newTestToolRegistry(t),
		Config{Model: "gpt-5"},
	)
	engine.compactionRuntimeState().SetCount(99)

	request, err := engine.buildDispatchRequest(context.Background(), "", nil, true, dispatchRequestIdentity{
		SessionID:            "explicit-session",
		RunID:                "explicit-run",
		CompactionGeneration: 4,
		RequestKind:          llm.CodexRequestKindTurn,
	})
	if err != nil {
		t.Fatalf("build dispatch request: %v", err)
	}
	if request.SessionID != "explicit-session" {
		t.Fatalf("SessionID = %q, want explicit-session", request.SessionID)
	}
	if request.CodexDispatch == nil {
		t.Fatal("dispatch request omitted Codex dispatch context")
	}
	metadata, err := request.CodexDispatch.TurnMetadataJSON()
	if err != nil {
		t.Fatalf("turn metadata: %v", err)
	}
	if metadata != `{"session_id":"explicit-session","thread_id":"explicit-session","turn_id":"explicit-run","window_id":"explicit-session:4","request_kind":"turn"}` {
		t.Fatalf("turn metadata = %s, want explicit identity facts", metadata)
	}
}

func TestActiveRunDispatchesKeepImmutableIdentityAndAllocateFreshState(t *testing.T) {
	t.Parallel()
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		newTestToolRegistry(t),
		Config{Model: "gpt-5"},
	)
	engine.compactionRuntimeState().SetCount(7)

	err := engine.stepLifecycle.Run(
		context.Background(),
		exclusiveStepOptions{ActiveKind: ActiveKindUserTurn},
		func(ctx context.Context, stepID string) error {
			first, err := engine.buildActiveTurnDispatchRequest(ctx, stepID, nil, true)
			if err != nil {
				return err
			}
			continuation, err := engine.buildActiveTurnDispatchRequest(ctx, stepID, nil, true)
			if err != nil {
				return err
			}
			if first.CodexDispatch == nil || continuation.CodexDispatch == nil {
				t.Fatal("active Run dispatch omitted Codex context")
			}
			if first.CodexDispatch.SameState(continuation.CodexDispatch) {
				t.Fatal("tool continuation reused the preceding dispatch-state handle")
			}
			firstMetadata, err := first.CodexDispatch.TurnMetadataJSON()
			if err != nil {
				return err
			}
			continuationMetadata, err := continuation.CodexDispatch.TurnMetadataJSON()
			if err != nil {
				return err
			}
			if firstMetadata != continuationMetadata {
				t.Fatalf("continuation identity changed:\nfirst=%s\n next=%s", firstMetadata, continuationMetadata)
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("run active dispatch fixture: %v", err)
	}
}

func TestCompactionWindowAdvancesOnlyAfterCommittedReplacement(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{
		compactionErrors: []error{
			errors.New("terminal compaction failure"),
		},
		compactionResponses: []llm.CompactionResponse{
			remoteCompactionReplacement(1_000, 100, 2_500),
		},
	}
	engine := mustNewTestEngine(
		t,
		store,
		client,
		newTestToolRegistry(t),
		Config{Model: "gpt-5", CompactionMode: "native"},
	)
	if err := engine.steer("input", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}

	withCompactionRetryDelays(t, nil)
	if err := engine.CompactContext(context.Background(), ""); err == nil {
		t.Fatal("failed compaction unexpectedly succeeded")
	}
	if len(client.compactionCalls) != 1 {
		t.Fatalf("failed compaction calls = %d, want one", len(client.compactionCalls))
	}
	assertDispatchWindow(t, client.compactionCalls[0].CodexDispatch, store.Meta().SessionID+":0")

	client.compactionErr = nil
	if err := engine.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("successful compaction: %v", err)
	}
	if len(client.compactionCalls) != 2 {
		t.Fatalf("total compaction calls = %d, want two", len(client.compactionCalls))
	}
	assertDispatchWindow(t, client.compactionCalls[1].CodexDispatch, store.Meta().SessionID+":0")

	err := engine.stepLifecycle.Run(
		context.Background(),
		exclusiveStepOptions{ActiveKind: ActiveKindUserTurn},
		func(ctx context.Context, stepID string) error {
			request, buildErr := engine.buildActiveTurnDispatchRequest(ctx, stepID, nil, true)
			if buildErr != nil {
				return buildErr
			}
			assertDispatchWindow(t, request.CodexDispatch, store.Meta().SessionID+":1")
			return nil
		},
	)
	if err != nil {
		t.Fatalf("build post-compaction dispatch: %v", err)
	}
}

func assertDispatchWindow(t *testing.T, dispatch *llm.CodexDispatchContext, want string) {
	t.Helper()
	if dispatch == nil {
		t.Fatal("dispatch context is absent")
	}
	metadata, err := dispatch.TurnMetadataJSON()
	if err != nil {
		t.Fatalf("turn metadata: %v", err)
	}
	var fields struct {
		WindowID string `json:"window_id"`
	}
	if err := json.Unmarshal([]byte(metadata), &fields); err != nil {
		t.Fatalf("decode turn metadata: %v", err)
	}
	if fields.WindowID != want {
		t.Fatalf("window ID = %q, want %q", fields.WindowID, want)
	}
}
