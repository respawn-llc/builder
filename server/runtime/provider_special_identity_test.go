package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"core/server/llm"
	"core/shared/textutil"
)

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
		if call.SessionID == nil || *call.SessionID != wantSessionID {
			t.Fatalf("Reviewer call %d SessionID = %v, want %q", index+1, call.SessionID, wantSessionID)
		}
	}
	if reviewerClient.calls[0].CodexDispatch != reviewerClient.calls[1].CodexDispatch {
		t.Fatal("unchanged Reviewer retry did not reuse retry-local state")
	}
	if reviewerClient.calls[1].CodexDispatch == reviewerClient.calls[2].CodexDispatch {
		t.Fatal("later Reviewer request reused preceding retry-local state")
	}
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
