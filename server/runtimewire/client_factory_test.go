package runtimewire

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/runtime"
	"core/shared/config"
	"core/shared/toolspec"
)

func TestRuntimeClientFactoryCreatesMainAndReviewerClients(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := newRuntimeWireSession(t, root, "factory")
	var purposes []RuntimeClientPurpose
	factory := RuntimeClientFactoryFunc(func(_ context.Context, req RuntimeClientRequest) (llm.Client, error) {
		purposes = append(purposes, req.Purpose)
		return &runtimewireCaptureClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "ok", Phase: llm.MessagePhaseFinal}, Usage: llm.Usage{WindowTokens: 200000}}}}, nil
	})

	wiring, err := NewRuntimeWiringWithBackground(
		store,
		config.Settings{
			Model:              "gpt-5",
			ModelContextWindow: 200000,
			Reviewer:           config.ReviewerSettings{Frequency: "all", Model: "gpt-5"},
			Timeouts:           config.Timeouts{ModelRequestSeconds: 1},
		},
		[]toolspec.ID{toolspec.ToolExecCommand},
		root,
		nil,
		nil,
		nil,
		RuntimeWiringOptions{ClientFactory: factory},
	)
	if err != nil {
		t.Fatalf("NewRuntimeWiringWithBackground: %v", err)
	}
	t.Cleanup(func() { _ = wiring.Close() })
	if len(purposes) != 2 || purposes[0] != RuntimeClientPurposeMain || purposes[1] != RuntimeClientPurposeReviewer {
		t.Fatalf("factory purposes = %#v, want main then reviewer", purposes)
	}
}

func TestRuntimeClientFactoryRejectsDirectClientOverride(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := newRuntimeWireSession(t, root, "factory-conflict")
	_, err := NewRuntimeWiringWithBackground(
		store,
		config.Settings{Model: "gpt-5", ModelContextWindow: 200000, Timeouts: config.Timeouts{ModelRequestSeconds: 1}},
		nil,
		root,
		nil,
		nil,
		nil,
		RuntimeWiringOptions{
			Client:        &runtimewireCaptureClient{},
			ClientFactory: RuntimeClientFactoryFunc(func(context.Context, RuntimeClientRequest) (llm.Client, error) { return nil, nil }),
		},
	)
	if !errors.Is(err, ErrRuntimeClientFactoryConflict) {
		t.Fatalf("error = %v, want ErrRuntimeClientFactoryConflict", err)
	}
}

func TestReviewerRuntimeClientFactoryCanPairWithDirectMainClient(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := newRuntimeWireSession(t, root, "reviewer-factory")
	reviewerCalls := 0
	factory := RuntimeClientFactoryFunc(func(_ context.Context, req RuntimeClientRequest) (llm.Client, error) {
		reviewerCalls++
		if req.Purpose != RuntimeClientPurposeReviewer {
			t.Fatalf("factory purpose = %v, want reviewer", req.Purpose)
		}
		return &runtimewireCaptureClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "review", Phase: llm.MessagePhaseFinal}, Usage: llm.Usage{WindowTokens: 200000}}}}, nil
	})

	wiring, err := NewRuntimeWiringWithBackground(
		store,
		config.Settings{
			Model:              "gpt-5",
			ModelContextWindow: 200000,
			Reviewer:           config.ReviewerSettings{Frequency: "all", Model: "gpt-5"},
			Timeouts:           config.Timeouts{ModelRequestSeconds: 1},
		},
		nil,
		root,
		nil,
		nil,
		nil,
		RuntimeWiringOptions{
			Client:                &runtimewireCaptureClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "ok", Phase: llm.MessagePhaseFinal}, Usage: llm.Usage{WindowTokens: 200000}}}},
			ReviewerClientFactory: factory,
		},
	)
	if err != nil {
		t.Fatalf("NewRuntimeWiringWithBackground: %v", err)
	}
	t.Cleanup(func() { _ = wiring.Close() })
	if reviewerCalls != 1 {
		t.Fatalf("reviewer factory calls = %d, want 1", reviewerCalls)
	}
}

func TestRuntimeClientFactoryReceivesActivationContext(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := newRuntimeWireSession(t, root, "factory-context")
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "activation")
	factory := RuntimeClientFactoryFunc(func(got context.Context, req RuntimeClientRequest) (llm.Client, error) {
		if got.Value(contextKey{}) != "activation" {
			t.Fatalf("factory context value = %v, want activation", got.Value(contextKey{}))
		}
		return &runtimewireCaptureClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "ok", Phase: llm.MessagePhaseFinal}, Usage: llm.Usage{WindowTokens: 200000}}}}, nil
	})

	wiring, err := NewRuntimeWiringWithBackground(
		store,
		config.Settings{
			Model:              "gpt-5",
			ModelContextWindow: 200000,
			Reviewer:           config.ReviewerSettings{Frequency: "off"},
			Timeouts:           config.Timeouts{ModelRequestSeconds: 1},
		},
		nil,
		root,
		nil,
		nil,
		nil,
		RuntimeWiringOptions{Context: ctx, ClientFactory: factory},
	)
	if err != nil {
		t.Fatalf("NewRuntimeWiringWithBackground: %v", err)
	}
	t.Cleanup(func() { _ = wiring.Close() })
}

func TestRuntimeClientFactoryErrorDoesNotFallBackToProvider(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := newRuntimeWireSession(t, root, "factory-error")
	wantErr := errors.New("factory failed")
	calls := 0
	_, err := NewRuntimeWiringWithBackground(
		store,
		config.Settings{
			Model:              "",
			ProviderOverride:   "openai",
			ModelContextWindow: 200000,
			Timeouts:           config.Timeouts{ModelRequestSeconds: 1},
		},
		nil,
		root,
		nil,
		nil,
		nil,
		RuntimeWiringOptions{ClientFactory: RuntimeClientFactoryFunc(func(context.Context, RuntimeClientRequest) (llm.Client, error) {
			calls++
			return nil, wantErr
		})},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want factory error", err)
	}
	if calls != 1 {
		t.Fatalf("factory calls = %d, want 1", calls)
	}
	if errors.Is(err, runtime.ErrModelRequired) {
		t.Fatalf("factory error fell through to runtime provider/model validation: %v", err)
	}
}
