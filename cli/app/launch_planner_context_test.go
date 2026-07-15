package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/shared/client"
	"core/shared/serverapi"
)

type contextObservingProjectViewClient struct {
	client.ProjectViewClient
	received chan context.Context
}

func (c *contextObservingProjectViewClient) ListSessionPage(
	ctx context.Context,
	_ serverapi.SessionPageRequest,
) (serverapi.SessionPageResponse, error) {
	c.received <- ctx
	<-ctx.Done()
	return serverapi.SessionPageResponse{}, ctx.Err()
}

func TestProjectScopedSessionPageLoaderHonorsRequestContext(t *testing.T) {
	t.Parallel()

	projectView := &contextObservingProjectViewClient{received: make(chan context.Context, 1)}
	loader := projectScopedSessionPageLoader{
		projectID: "request-context-project",
		client:    projectView,
	}
	type contextKey struct{}
	requestContext, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "request-owned"))
	completed := make(chan error, 1)
	go func() {
		_, err := loader.ListSessionPage(requestContext, serverapi.SessionPageRequest{})
		completed <- err
	}()

	select {
	case received := <-projectView.received:
		if got := received.Value(contextKey{}); got != "request-owned" {
			t.Fatalf("loader request context value = %v, want request-owned", got)
		}
	case <-time.After(time.Second):
		t.Fatal("project view client did not receive page request")
	}
	cancel()
	select {
	case err := <-completed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ListSessionPage error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("project-scoped page request did not observe cancellation")
	}
}
