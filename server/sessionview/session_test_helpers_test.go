package sessionview

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"core/server/llm"
	"core/server/metadata"
	"core/server/registry"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionruntime"
	"core/server/tools"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/sessioncontract"
)

var sessionViewTestPersistence = sessiontest.NewPersistence()

type testSessionResolver struct {
	store *session.Store
}

func newTestSessionResolver(store *session.Store) SessionStoreResolver {
	if store == nil {
		return nil
	}
	return testSessionResolver{store: store}
}

func (r testSessionResolver) ResolveSessionStore(_ context.Context, sessionID string) (*session.Store, error) {
	if r.store == nil {
		return nil, errors.New("session store is required")
	}
	if strings.TrimSpace(sessionID) != strings.TrimSpace(r.store.Meta().SessionID) {
		return nil, fmt.Errorf("session %q not available", strings.TrimSpace(sessionID))
	}
	return r.store, nil
}

type sessionViewRuntimeFixture struct {
	authority *sessionruntime.Authority
	activity  *registry.RuntimeRegistry
	sessionID runtimeids.SessionID
}

func newSessionViewRuntimeFixture(t *testing.T, store *session.Store, client llm.Client) sessionViewRuntimeFixture {
	t.Helper()
	if store == nil {
		t.Fatal("session store is required")
	}
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	settings := config.DefaultOnboardingSettings()
	settings.ProviderOverride = "openai"
	settings.Model = "gpt-5"
	settings.Reviewer.Frequency = "off"
	plan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings: settings,
		FilesystemContext: func() tools.FilesystemContext {
			context, err := runtimewire.NewFilesystemContext(store.Meta().WorkspaceRoot, store.Meta().WorkspaceRoot, metadata.ProjectWorkspaceBoundary{ProjectID: "test"})
			if err != nil {
				t.Fatalf("NewFilesystemContext: %v", err)
			}
			return context
		}(),
		Client: client,
	})
	if err != nil {
		t.Fatalf("new runtime plan: %v", err)
	}
	activity := registry.NewRuntimeRegistry()
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot:   t.TempDir(),
		StoreOptions:      sessionViewTestPersistence.Options(),
		ResourceLifecycle: activity,
		EventFeed: func(resource runtimeids.SessionResourceRef, event runtime.Event) {
			activity.PublishAuthorityRuntimeEvent(resource, event)
		},
	})
	if _, err := authority.OpenRuntime(t.Context(), sessionruntime.RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "sessionview-test",
		Runtime:   &plan,
	}); err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	return sessionViewRuntimeFixture{
		authority: authority,
		activity:  activity,
		sessionID: sessionID,
	}
}

func (f sessionViewRuntimeFixture) startUserTurn(t *testing.T, prompt string) sessionruntime.ExecutionHandle {
	t.Helper()
	descriptor, err := session.NewOpenSessionDescriptor(f.sessionID)
	if err != nil {
		t.Fatalf("new session descriptor: %v", err)
	}
	handle, err := f.authority.StartAgentExecution(t.Context(), sessionruntime.AgentExecutionRequest{
		Descriptor: descriptor,
		Resource:   sessionruntime.CurrentAgentResource{},
		Runner: func(ctx context.Context, _ sessionruntime.ExecutionScope, bridge sessionruntime.AgentRuntimeBridge) error {
			return bridge.WithEngine(ctx, func(callbackCtx context.Context, engine *runtime.Engine) error {
				_, err := engine.SubmitUserMessage(callbackCtx, prompt)
				return err
			})
		},
	})
	if err != nil {
		t.Fatalf("start user turn: %v", err)
	}
	return handle
}

func newSessionViewStore(t *testing.T, containerDir, containerName, workspaceRoot string) *session.Store {
	t.Helper()
	store, err := session.Create(containerDir, containerName, workspaceRoot, sessioncontract.SessionCategoryMain, sessionViewTestPersistence.Options()...)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	return store
}

func appendSessionViewRecord(
	t *testing.T,
	store *session.Store,
	stepID string,
	payload session.EventRecordPayload,
) session.EventRecord {
	t.Helper()
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("ensure session is durable: %v", err)
	}
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	step := stepID
	record, receipt, err := eventLog.AppendRecord(&step, payload)
	if err != nil || !receipt.Committed {
		t.Fatalf("append typed event: receipt=%+v error=%v", receipt, err)
	}
	return record
}

func appendSessionViewRecordWithCursor(
	t *testing.T,
	store *session.Store,
	stepID string,
	payload session.EventRecordPayload,
) session.EventRecordAppendResult {
	t.Helper()
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("ensure session is durable: %v", err)
	}
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	step := stepID
	result, err := eventLog.AppendRecordWithEndByteCursor(&step, payload)
	if err != nil || !result.Committed {
		t.Fatalf("append typed event with cursor: result=%+v error=%v", result, err)
	}
	return result
}

func appendSessionViewMessage(
	t *testing.T,
	store *session.Store,
	stepID string,
	role session.MessageRole,
	content string,
	phase *session.MessagePhase,
	messageType *session.MessageType,
) session.EventRecord {
	t.Helper()
	return appendSessionViewRecord(t, store, stepID, session.MessageRecord{
		Role:        role,
		Content:     &content,
		Phase:       phase,
		MessageType: messageType,
	})
}

func appendSessionViewHistoryReplacement(
	t *testing.T,
	store *session.Store,
	stepID string,
	record session.HistoryReplacementRecord,
) session.EventRecord {
	t.Helper()
	return appendSessionViewRecord(t, store, stepID, record)
}

func sessionViewStringPointer(value string) *string {
	return &value
}

func sessionViewMessageTypePointer(value session.MessageType) *session.MessageType {
	return &value
}

func sessionViewMessagePhasePointer(value session.MessagePhase) *session.MessagePhase {
	return &value
}

func newSessionViewParentAgentChild(t *testing.T, containerDir, containerName, workspaceRoot string) (*session.Store, string) {
	t.Helper()
	parent := newSessionViewStore(t, containerDir, containerName, workspaceRoot)
	child, err := session.NewLazy(containerDir, containerName, workspaceRoot, sessioncontract.SessionCategoryMain, sessionViewTestPersistence.Options()...)
	if err != nil {
		t.Fatalf("create lazy child: %v", err)
	}
	if err := session.InitializeCreationContext(child, parent, session.SessionCreationSourceParentAgent, session.ChildContextOptions{}); err != nil {
		t.Fatalf("initialize child provenance: %v", err)
	}
	return child, parent.Meta().SessionID
}
