package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/tools"
)

type cancelAwareModelClient struct {
	started chan struct{}
}

func (c cancelAwareModelClient) Generate(ctx context.Context, _ llm.Request) (llm.Response, error) {
	close(c.started)
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

func TestGenerateWithRetryPropagatesContextCancellation(t *testing.T) {
	store := mustCreateTestSession(t)
	started := make(chan struct{})
	client := cancelAwareModelClient{started: started}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := eng.generateWithRetryClient(ctx, "step-1", eng.llm, llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "gpt-5"}, nil, nil, nil)
		done <- err
	}()

	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("generateWithRetry error=%v, want context.Canceled", err)
	}
}
