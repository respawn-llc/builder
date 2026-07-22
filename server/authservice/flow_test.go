package authservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/auth"
)

type stubHandler struct {
	needs    func(FlowInteractionRequest) bool
	interact func(context.Context, FlowInteractionRequest) (FlowInteractionOutcome, error)
}

func (h stubHandler) NeedsInteraction(req FlowInteractionRequest) bool {
	if h.needs == nil {
		return false
	}
	return h.needs(req)
}

func (h stubHandler) Interact(ctx context.Context, req FlowInteractionRequest) (FlowInteractionOutcome, error) {
	if h.interact == nil {
		return FlowInteractionOutcome{}, nil
	}
	return h.interact(ctx, req)
}

func TestEnsureReadyReturnsStartupErrorWithoutInteractiveHandler(t *testing.T) {
	mgr := auth.NewManager(auth.NewMemoryStore(auth.EmptyState()), nil, time.Now)
	err := EnsureFlowReady(context.Background(), mgr, auth.OpenAIOAuthOptions{}, "dark", func(string) string { return "" }, true, false, stubHandler{
		needs: func(FlowInteractionRequest) bool { return false },
	})
	if !errors.Is(err, auth.ErrAuthNotConfigured) {
		t.Fatalf("expected auth not configured, got %v", err)
	}
}

func TestEnsureReadyLoopsAfterInteractionUntilAuthConfigured(t *testing.T) {
	mgr := auth.NewManager(auth.NewMemoryStore(auth.EmptyState()), nil, time.Now)
	callCount := 0
	err := EnsureFlowReady(context.Background(), mgr, auth.OpenAIOAuthOptions{}, "dark", func(key string) string {
		if key == "OPENAI_API_KEY" {
			return "sk-env"
		}
		return ""
	}, true, false, stubHandler{
		needs: func(req FlowInteractionRequest) bool { return !req.Gate.Ready },
		interact: func(ctx context.Context, req FlowInteractionRequest) (FlowInteractionOutcome, error) {
			callCount++
			if !req.HasEnvAPIKey {
				t.Fatal("expected env api key to be visible in interaction request")
			}
			_, err := req.Manager.SwitchMethod(ctx, auth.Method{
				Type:   auth.MethodAPIKey,
				APIKey: &auth.APIKeyMethod{Key: "sk-after"},
			}, true)
			return FlowInteractionOutcome{}, err
		},
	})
	if err != nil {
		t.Fatalf("ensure ready: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("expected one interaction, got %d", callCount)
	}
	state, err := mgr.Load(context.Background())
	if err != nil {
		t.Fatalf("load auth state: %v", err)
	}
	if state.Method.APIKey == nil || state.Method.APIKey.Key != "sk-after" {
		t.Fatalf("expected configured auth state, got %+v", state.Method.APIKey)
	}
}

func TestEnsureReadyAllowsOptionalStartupWithoutConfiguredAuth(t *testing.T) {
	mgr := auth.NewManager(auth.NewMemoryStore(auth.EmptyState()), nil, time.Now)
	interacted := false
	err := EnsureFlowReady(context.Background(), mgr, auth.OpenAIOAuthOptions{}, "dark", func(string) string { return "" }, false, false, stubHandler{
		needs: func(FlowInteractionRequest) bool { return false },
		interact: func(context.Context, FlowInteractionRequest) (FlowInteractionOutcome, error) {
			interacted = true
			return FlowInteractionOutcome{}, nil
		},
	})
	if err != nil {
		t.Fatalf("ensure ready: %v", err)
	}
	if interacted {
		t.Fatal("did not expect optional startup to prompt for auth")
	}
}
