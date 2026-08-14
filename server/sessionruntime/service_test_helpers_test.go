package sessionruntime

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/metadata"
	"core/server/session"
	"core/shared/config"
	"core/shared/sessioncontract"
	"core/shared/textutil"
)

type sessionRuntimeTestLLMClient struct {
	responses []llm.Response
	mu        sync.Mutex
	requests  []llm.Request
	final     chan struct{}
	finalOnce sync.Once
}

func (c *sessionRuntimeTestLLMClient) Generate(_ context.Context, request llm.Request) (llm.Response, error) {
	c.mu.Lock()
	c.requests = append(c.requests, llm.Request{Items: llm.CloneResponseItems(request.Items)})
	if len(c.responses) == 0 {
		c.mu.Unlock()
		return llm.Response{}, nil
	}
	response := c.responses[0]
	c.responses = c.responses[1:]
	c.mu.Unlock()
	if response.Assistant.Phase != nil && *response.Assistant.Phase == llm.MessagePhaseFinal && c.final != nil {
		c.finalOnce.Do(func() { close(c.final) })
	}
	return response, nil
}

func (c *sessionRuntimeTestLLMClient) requestSnapshot() []llm.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]llm.Request, len(c.requests))
	for index := range c.requests {
		out[index] = c.requests[index]
		out[index].Items = llm.CloneResponseItems(c.requests[index].Items)
	}
	return out
}

type blockingLLMClient struct {
	entered     chan struct{}
	enteredOnce sync.Once
	release     chan struct{}
}

func (c *blockingLLMClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	c.enteredOnce.Do(func() { close(c.entered) })
	<-c.release
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}, nil
}

type sessionRuntimeFixture struct {
	config    config.App
	metadata  *metadata.Store
	store     *session.Store
	api       *API
	authority *Authority
}

func newSessionRuntimeFixture(t *testing.T) sessionRuntimeFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	appConfig, err := config.Load(t.TempDir(), config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	metadataStore := testsetup.OpenStore(t, appConfig.PersistenceRoot)
	binding, err := metadataStore.RegisterWorkspaceBinding(context.Background(), appConfig.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	sessionsDir := filepath.Join(appConfig.PersistenceRoot, "projects", binding.ProjectID, "sessions")
	store, err := session.Create(sessionsDir, filepath.Base(sessionsDir), appConfig.WorkspaceRoot, sessioncontract.SessionCategoryMain, metadataStore.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := store.SetName("session-a"); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	authority := NewAuthority(AuthorityOptions{PersistenceRoot: appConfig.PersistenceRoot, StoreOptions: metadataStore.AuthoritativeSessionStoreOptions()})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close runtime authority: %v", err)
		}
	})
	return sessionRuntimeFixture{
		config: appConfig, metadata: metadataStore, store: store,
		api: NewAPI(metadataStore, authority, APIOptions{}), authority: authority,
	}
}
