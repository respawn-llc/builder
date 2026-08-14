package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/tools"
)

func TestGenerateWithRetryPropagatesContextCancellation(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	started := make(chan struct{})
	engine := mustNewTestEngine(
		t,
		store,
		cancellationAwareModelClient{started: started},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := engine.generateWithRetryClient(
			ctx,
			"step",
			engine.llm,
			llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "gpt-5"},
			nil,
			nil,
			nil,
		)
		done <- err
	}()

	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("generate with retry error = %v, want context cancellation", err)
	}
}

type cancellationAwareModelClient struct {
	started chan struct{}
}

func (cancellationAwareModelClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return defaultTestProviderCapabilities(), nil
}

func (c cancellationAwareModelClient) Generate(ctx context.Context, _ llm.Request) (llm.Response, error) {
	close(c.started)
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}
