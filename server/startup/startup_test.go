package startup

import (
	"context"
	"testing"

	"core/server/auth"
	"core/server/authservice"
	"core/shared/config"
)

type stubAuthHandler struct {
	lookupEnv func(string) string
}

func (h stubAuthHandler) WrapStore(base auth.Store) auth.Store { return base }

func (stubAuthHandler) NeedsInteraction(authservice.FlowInteractionRequest) bool { return false }

func (stubAuthHandler) Interact(_ context.Context, _ authservice.FlowInteractionRequest) (authservice.FlowInteractionOutcome, error) {
	return authservice.FlowInteractionOutcome{}, nil
}

func (h stubAuthHandler) LookupEnv(key string) string {
	if h.lookupEnv == nil {
		return ""
	}
	return h.lookupEnv(key)
}

func TestBuildRequestMapsStartupOptionsAndLookupEnv(t *testing.T) {
	handler := stubAuthHandler{lookupEnv: func(string) string { return "lookup-value" }}
	req := buildRequest(Request{
		WorkspaceRoot:         "/tmp/workspace",
		WorkspaceRootExplicit: true,
		SessionID:             "session-123",
		Model:                 "gpt-5",
		ProviderOverride:      "openai",
		ThinkingLevel:         "high",
		Theme:                 "dark",
		ModelTimeoutSeconds:   45,
		Tools:                 "shell,patch",
		OpenAIBaseURL:         "http://example.test/v1",
		OpenAIBaseURLExplicit: true,
	}, handler)

	if req.WorkspaceRoot != "/tmp/workspace" || !req.WorkspaceRootExplicit || req.SessionID != "session-123" {
		t.Fatalf("unexpected request mapping: %+v", req)
	}
	if req.OpenAIBaseURL != "http://example.test/v1" || !req.OpenAIBaseURLExplicit {
		t.Fatalf("unexpected base URL mapping: %+v", req)
	}
	if req.LoadOptions != (config.LoadOptions{
		Model:               "gpt-5",
		ProviderOverride:    "openai",
		ThinkingLevel:       "high",
		Theme:               "dark",
		ModelTimeoutSeconds: 45,
		Tools:               "shell,patch",
	}) {
		t.Fatalf("unexpected load options: %+v", req.LoadOptions)
	}
	if got := req.LookupEnv("KENT_LOOKUP_TEST"); got != "lookup-value" {
		t.Fatalf("lookup env returned %q", got)
	}
}
