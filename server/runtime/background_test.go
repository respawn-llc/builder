package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"core/server/llm"
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

func TestBackgroundAgendaAdapterOwnsRuntimeBoundTerminalProjection(t *testing.T) {
	event := BackgroundShellEvent{
		Type:        BackgroundShellEventCompleted,
		ID:          "42",
		ActivityID:  uuid.New(),
		State:       "completed",
		NoticeText:  "terminal detail",
		CompactText: "terminal compact",
	}
	item, err := newBackgroundNoticeAgendaItem(backgroundShellDeveloperNotice(event))
	if err != nil {
		t.Fatalf("new item: %v", err)
	}
	if _, runtimeBound := item.agendaBinding().(runtimeAgendaBinding); !runtimeBound {
		t.Fatalf("binding = %T, want runtime binding", item.agendaBinding())
	}
	if item.agendaEligibility() != boundaryEligibilitySafe {
		t.Fatalf("eligibility = %v, want safe", item.agendaEligibility())
	}
	if item.agendaID() != boundaryAgendaItemID("background-notice:"+event.ActivityID.String()) ||
		item.sessionID != event.ID ||
		item.message.MessageType == nil ||
		*item.message.MessageType != llm.MessageTypeBackgroundNotice ||
		item.message.BackgroundActivityID == nil ||
		*item.message.BackgroundActivityID != event.ActivityID.String() ||
		item.message.Content == nil ||
		*item.message.Content != event.NoticeText ||
		item.message.CompactContent == nil ||
		*item.message.CompactContent != event.CompactText {
		t.Fatalf("background item lost domain projection: %+v", item)
	}
}

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
	if err := adapter.QueueBackgroundShellContinuation(event); err != nil {
		t.Fatalf("accept first terminal notice: %v", err)
	}
	if err := adapter.QueueBackgroundShellContinuation(event); err == nil {
		t.Fatal("duplicate terminal notice identity was accepted")
	}
	if pending := pendingBackgroundNotices(engine.boundaryAgenda); len(pending) != 1 {
		t.Fatalf("pending duplicate domain items = %d, want 1", len(pending))
	}
}

func TestBackgroundNoticeAppliesAtMatchingStepBoundaryAndSurvivesSourceCleanup(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	scopeID := runtimeids.NewExecutionScopeID()
	origin := newTestAgentStepOrigin(t)
	engine.agentSteps.current = &activeAgentStep{
		scopeID: scopeID,
		origin:  origin,
		phase:   agentStepProviderRunning,
	}

	event := BackgroundShellEvent{
		Type:       BackgroundShellEventCompleted,
		ID:         "42",
		ActivityID: uuid.New(),
		State:      "completed",
	}
	engine.HandleBackgroundShellUpdate(event, true)
	engine.boundaryAgenda.finalizeScope(scopeID, errBoundaryScopeFinalized)

	pending := pendingBackgroundNotices(engine.boundaryAgenda)
	if len(pending) != 1 {
		t.Fatalf("pending after source cleanup = %d, want 1", len(pending))
	}
	_, err := submitRuntimeEvent(
		engine,
		struct{}{},
		func(admission runtimeEventAdmission, _ struct{}) (struct{}, error) {
			applied, applyErr := engine.applyBackgroundNoticeBoundary(
				admission,
				origin.StepID,
				stepBoundarySelection(scopeID, origin),
			)
			if applied != 1 {
				t.Fatalf("applied = %d, want 1", applied)
			}
			return struct{}{}, applyErr
		},
	)
	if err != nil {
		t.Fatalf("apply background boundary: %v", err)
	}
	if pending := pendingBackgroundNotices(engine.boundaryAgenda); len(pending) != 0 {
		t.Fatalf("pending after apply = %d, want 0", len(pending))
	}
	messages := engine.transcriptRuntimeState().SnapshotMessages()
	if len(messages) != 1 ||
		messages[0].MessageType == nil ||
		*messages[0].MessageType != llm.MessageTypeBackgroundNotice {
		t.Fatalf("applied messages = %+v", messages)
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
			_, applyErr := engine.applyBackgroundNoticeBoundary(
				admission,
				origin.StepID,
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

func TestBackgroundNoticePreservesGlobalAdmissionOrderWithHumanAndWorkflowAssignment(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	step := activeAgentStep{
		scopeID: runtimeids.NewExecutionScopeID(),
		origin:  newTestAgentStepOrigin(t),
		phase:   agentStepProviderRunning,
	}
	engine.agentSteps.current = &step

	human, err := newQueuedUserMessage(
		llm.Message{Role: llm.RoleUser, Content: textutil.Value("human first")},
		"",
	)
	if err != nil {
		t.Fatalf("new human item: %v", err)
	}
	if _, err := engine.acceptHumanAgendaItem(human, boundaryEligibilityStep, true); err != nil {
		t.Fatalf("accept human: %v", err)
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
	assignment, err := engine.SteerWorkflowAssignment(workflowAssignmentForCommitReceiptTest())
	if err != nil {
		t.Fatalf("accept Workflow assignment: %v", err)
	}

	engine.agentSteps.current = nil
	engine.agentSteps.boundary = &step
	_, err = submitRuntimeEvent(
		engine,
		struct{}{},
		func(admission runtimeEventAdmission, _ struct{}) (struct{}, error) {
			_, reduceErr := engine.acceptReducerBoundaryGrant(
				admission,
				localAgentStepReducerGrant{engine: engine},
				true,
				step,
			)
			return struct{}{}, reduceErr
		},
	)
	if err != nil {
		t.Fatalf("reduce mixed agenda: %v", err)
	}
	if receipt, err := assignment.Wait(context.Background()); err != nil || !receipt.Committed {
		t.Fatalf("Workflow assignment settlement = %+v, %v", receipt, err)
	}

	var ordered []string
	recent, err := engine.eventLog.ReadRecentRecords(100)
	if err != nil {
		t.Fatalf("read records: %v", err)
	}
	for _, record := range recent.Records {
		payload, err := record.Payload()
		if err != nil {
			t.Fatalf("record payload: %v", err)
		}
		message, ok := payload.(session.MessageRecord)
		if !ok {
			continue
		}
		switch {
		case message.Role == session.MessageRoleUser:
			ordered = append(ordered, "human")
		case message.MessageType != nil &&
			*message.MessageType == session.MessageTypeBackgroundNotice:
			ordered = append(ordered, "background")
		case message.MessageType != nil &&
			*message.MessageType == session.MessageTypeWorkflowMode:
			ordered = append(ordered, "workflow")
		}
	}
	want := []string{"human", "background", "workflow"}
	if len(ordered) != len(want) {
		t.Fatalf("ordered messages = %v, want %v", ordered, want)
	}
	for index := range want {
		if ordered[index] != want[index] {
			t.Fatalf("ordered messages = %v, want %v", ordered, want)
		}
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
			_, _, err := engine.persistToolCompletionRaw("step", tools.Result{
				CallID: "write-stdin-call",
				Name:   toolspec.ToolWriteStdin,
				Output: json.RawMessage(
					`{"background_session_id":42,"background_running":false,"backgrounded":true}`,
				),
				Presentation: &presentation,
			})
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

func (l *backgroundExecutionLauncher) RegisterRuntimeBoundLongExecution(
	context.Context,
) (RuntimeBoundLongExecution, error) {
	return &backgroundTestExecution{launcher: l}, nil
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

type backgroundTestExecution struct {
	launcher *backgroundExecutionLauncher
}

func (e *backgroundTestExecution) Launch(
	ctx context.Context,
	work func(context.Context, *Engine) error,
) (runtimeids.ExecutionScopeID, error) {
	err := work(ctx, e.launcher.engine)
	e.launcher.mu.Lock()
	launch := backgroundExecutionLaunch{
		scopeID: e.launcher.scopeID,
		origin:  e.launcher.origin,
	}
	e.launcher.mu.Unlock()
	e.launcher.launched <- launch
	return launch.scopeID, err
}

func (*backgroundTestExecution) Cancel(context.Context) error {
	return nil
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
