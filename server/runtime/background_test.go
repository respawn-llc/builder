package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/runtimecommand"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"

	"github.com/google/uuid"
)

func TestBackgroundAgendaAdapterRejectsDuplicateDomainIdentity(t *testing.T) {
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	engine.agentSteps.current = &activeAgentStep{
		scopeID: runtimeids.NewExecutionScopeID(),
		origin:  newTestAgentStepOrigin(t),
		phase:   agentStepProviderRunning,
	}
	event := BackgroundShellEvent{
		Type:       BackgroundShellEventCompleted,
		ID:         "42",
		ActivityID: uuid.New(),
		State:      "completed",
	}
	adapter := &defaultBackgroundAgendaAdapter{engine: engine}
	if err := adapter.QueueDeveloperNotice(backgroundShellDeveloperNotice(event)); err != nil {
		t.Fatalf("accept first terminal notice: %v", err)
	}
	if err := adapter.QueueDeveloperNotice(backgroundShellDeveloperNotice(event)); err == nil {
		t.Fatal("duplicate terminal notice identity was accepted")
	}
	if pending := pendingBackgroundNotices(engine.boundaryAgenda); len(pending) != 1 {
		t.Fatalf("pending duplicate domain items = %d, want 1", len(pending))
	}
}

func TestBackgroundNoticeAppendCertaintyDoesNotReinsertAfterCommittedPublicationError(t *testing.T) {
	observerErr := errors.New("background notice observer failed")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	scopeID := runtimeids.NewExecutionScopeID()
	origin := newTestAgentStepOrigin(t)
	engine.agentSteps.current = &activeAgentStep{
		scopeID: scopeID,
		origin:  origin,
		phase:   agentStepProviderRunning,
	}
	engine.ensureOrchestrationCollaborators()
	if err := engine.backgroundFlow.QueueDeveloperNotice(backgroundShellDeveloperNotice(BackgroundShellEvent{
		Type:       BackgroundShellEventCompleted,
		ID:         "42",
		ActivityID: uuid.New(),
		State:      "completed",
	})); err != nil {
		t.Fatalf("queue background continuation: %v", err)
	}
	gate.FailNext(observerErr)

	_, err := submitRuntimeEvent(
		engine,
		struct{}{},
		func(admission runtimeEventAdmission, _ struct{}) (struct{}, error) {
			stepID := origin.StepID
			_, applyErr := engine.applyBackgroundNoticeBoundary(
				admission,
				&stepID,
				stepBoundarySelection(scopeID, origin),
			)
			return struct{}{}, applyErr
		},
	)
	if !errors.Is(err, observerErr) {
		t.Fatalf("apply error = %v, want observer failure", err)
	}
	if pending := pendingBackgroundNotices(engine.boundaryAgenda); len(pending) != 0 {
		t.Fatalf("committed notice was reinserted: %+v", pending)
	}
}

func TestWriteStdinCompletionConsumesCanonicalBackgroundAgendaItem(t *testing.T) {
	for _, tt := range []struct {
		name        string
		blockAppend bool
		wantPending bool
	}{
		{name: "committed"},
		{name: "uncommitted", blockAppend: true, wantPending: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
			engine.agentSteps.current = &activeAgentStep{
				scopeID: runtimeids.NewExecutionScopeID(),
				origin:  newTestAgentStepOrigin(t),
				phase:   agentStepProviderRunning,
			}
			engine.ensureOrchestrationCollaborators()
			if err := engine.backgroundFlow.QueueDeveloperNotice(backgroundShellDeveloperNotice(BackgroundShellEvent{
				Type:       BackgroundShellEventCompleted,
				ID:         "42",
				ActivityID: uuid.New(),
				State:      "completed",
			})); err != nil {
				t.Fatalf("queue background continuation: %v", err)
			}
			if tt.blockAppend {
				mustBlockTestEventLogAppends(t, store)
			}

			presentation := transcript.NormalizeToolCallMeta(transcript.ToolCallMeta{
				ToolName: string(toolspec.ToolWriteStdin),
			})
			_, _, _, err := engine.persistFinalizedToolCompletionRaw(
				"step",
				finalizedToolCompletion{Result: tools.Result{
					CallID: "write-stdin-call",
					Name:   toolspec.ToolWriteStdin,
					Output: json.RawMessage(
						`{"background_session_id":42,"background_running":false,"backgrounded":true}`,
					),
					Presentation: &presentation,
				}},
			)
			if tt.blockAppend && err == nil {
				t.Fatal("uncommitted completion did not fail")
			}
			if !tt.blockAppend && err != nil {
				t.Fatalf("persist completion: %v", err)
			}
			if got := len(pendingBackgroundNotices(engine.boundaryAgenda)); (got > 0) != tt.wantPending {
				t.Fatalf("pending = %d, want pending=%t", got, tt.wantPending)
			}
		})
	}
}

func TestIdleBackgroundSelectionLaunchesFreshScopeAndOrigin(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("done"),
			Phase:   textutil.Value(llm.MessagePhaseFinal),
		},
		Usage: llm.Usage{WindowTokens: 200000},
	}}}
	launcher := newBackgroundExecutionLauncher()
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model:         "gpt-5",
		StepLifecycle: launcher,
	})
	launcher.engine = engine
	engine.ensureOrchestrationCollaborators()

	event := BackgroundShellEvent{
		Type:       BackgroundShellEventCompleted,
		ID:         "42",
		ActivityID: uuid.New(),
		State:      "completed",
	}
	if err := engine.backgroundFlow.QueueDeveloperNotice(backgroundShellDeveloperNotice(event)); err != nil {
		t.Fatalf("queue background continuation: %v", err)
	}
	launch := launcher.awaitLaunch(t)
	if launch.scopeID.IsZero() || launch.origin.Validate() != nil {
		t.Fatalf("fresh launch identity = scope:%s origin:%+v", launch.scopeID, launch.origin)
	}
	client.mu.Lock()
	requests := append([]llm.Request(nil), client.calls...)
	client.mu.Unlock()
	if len(requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(requests))
	}
	found := false
	for _, message := range requestMessages(requests[0]) {
		if message.BackgroundActivityID != nil &&
			*message.BackgroundActivityID == event.ActivityID.String() {
			found = true
		}
	}
	if !found {
		t.Fatal("idle launch omitted selected background notice")
	}
}

func pendingBackgroundNotices(agenda *boundaryAgenda) []*backgroundNoticeAgendaItem {
	var pending []*backgroundNoticeAgendaItem
	for _, item := range agenda.pending() {
		if notice, ok := item.(*backgroundNoticeAgendaItem); ok {
			pending = append(pending, notice)
		}
	}
	return pending
}

type backgroundExecutionLaunch struct {
	scopeID runtimeids.ExecutionScopeID
	origin  serverapi.RuntimeStepOrigin
}

type backgroundExecutionLauncher struct {
	engine   *Engine
	mu       sync.Mutex
	active   bool
	scopeID  runtimeids.ExecutionScopeID
	origin   serverapi.RuntimeStepOrigin
	launched chan backgroundExecutionLaunch
}

func newBackgroundExecutionLauncher() *backgroundExecutionLauncher {
	return &backgroundExecutionLauncher{
		launched: make(chan backgroundExecutionLaunch, 4),
	}
}

func (*backgroundExecutionLauncher) StepBegan(context.Context, StepLifecycleSnapshot) error {
	return nil
}

func (*backgroundExecutionLauncher) StepEnded(context.Context, StepLifecycleSnapshot) error {
	return nil
}

func (l *backgroundExecutionLauncher) AgentStepBegan(
	_ context.Context,
	origin serverapi.RuntimeStepOrigin,
) (runtimeids.ExecutionScopeID, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.active || l.scopeID.IsZero() {
		l.scopeID = runtimeids.NewExecutionScopeID()
	}
	l.origin = origin
	return l.scopeID, nil
}

func (l *backgroundExecutionLauncher) AgentStepScopeLive(
	context.Context,
	runtimeids.ExecutionScopeID,
) bool {
	return true
}

func (l *backgroundExecutionLauncher) CurrentAgentExecutionScope(
	context.Context,
) (runtimeids.ExecutionScopeID, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.scopeID, l.active
}

func (l *backgroundExecutionLauncher) AgentStepBoundary(
	context.Context,
	serverapi.RuntimeStepOrigin,
) (AgentStepBoundaryTransfer, error) {
	return AgentStepReducerBoundary{Grant: backgroundReducerGrant{}}, nil
}

func (l *backgroundExecutionLauncher) LaunchRuntimeBoundExecution(
	_ runtimecommand.Admission,
	work func(context.Context, *Engine) error,
	_ func(error),
) error {
	l.mu.Lock()
	l.scopeID = runtimeids.NewExecutionScopeID()
	l.active = true
	scopeID := l.scopeID
	l.mu.Unlock()
	go func() {
		if err := work(context.Background(), l.engine); err != nil {
			l.engine.surfaceRunError(err)
		}
		l.mu.Lock()
		launch := backgroundExecutionLaunch{
			scopeID: scopeID,
			origin:  l.origin,
		}
		l.active = false
		l.mu.Unlock()
		l.launched <- launch
	}()
	return nil
}

func (l *backgroundExecutionLauncher) awaitLaunch(t *testing.T) backgroundExecutionLaunch {
	t.Helper()
	select {
	case launch := <-l.launched:
		return launch
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for background execution")
		return backgroundExecutionLaunch{}
	}
}

type backgroundReducerGrant struct{}

func (backgroundReducerGrant) RegisterNext(
	context.Context,
	serverapi.RuntimeStepOrigin,
) (runtimeids.ExecutionScopeID, error) {
	return runtimeids.NewExecutionScopeID(), nil
}

func (backgroundReducerGrant) Release() error {
	return nil
}
