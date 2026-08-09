package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"

	"github.com/google/uuid"
)

func TestManualCompactionValidationUsesRuntimeEventAdmission(t *testing.T) {
	client := &fakeCompactionClient{
		responses: []llm.Response{{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("summary"),
			},
		}},
	}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		tools.NewRegistry(),
		Config{Model: "gpt-5", CompactionMode: "local"},
	)
	engine.compactionRuntimeState().SetManualCompactionEligible(false)
	release := blockRuntimeEventAdmission(t, engine.runtimeEvents)
	blocked := true
	defer func() {
		if blocked {
			release()
		}
	}()

	done := make(chan error, 1)
	go func() {
		done <- engine.CompactContext(context.Background(), "")
	}()
	select {
	case err := <-done:
		t.Fatalf("manual compaction validation bypassed Runtime Event admission: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	release()
	blocked = false
	select {
	case err := <-done:
		if err != ErrManualCompactionTooSoon {
			t.Fatalf("manual compaction error = %v, want too-soon", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("manual compaction did not settle after Runtime Event admission")
	}
}

func TestManualCompactionAdmissionBindsActiveAgentStepOrIdleRuntime(t *testing.T) {
	for _, test := range []struct {
		name            string
		active          bool
		wantEligibility boundaryEligibility
	}{
		{
			name:            "active Agent Step",
			active:          true,
			wantEligibility: boundaryEligibilityStep,
		},
		{
			name:            "idle runtime",
			wantEligibility: boundaryEligibilityIdle,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			engine := mustNewTestEngine(
				t,
				mustCreateTestSession(t),
				&fakeClient{},
				tools.NewRegistry(),
				Config{Model: "gpt-5", CompactionMode: "local"},
			)
			engine.compactionRuntimeState().SetManualCompactionEligible(!test.active)
			var scopeID runtimeids.ExecutionScopeID
			var origin serverapi.RuntimeStepOrigin
			if test.active {
				scopeID = runtimeids.NewExecutionScopeID()
				origin = serverapi.RuntimeStepOrigin{
					RunID:  uuid.NewString(),
					StepID: uuid.NewString(),
				}
				engine.agentSteps.current = &activeAgentStep{
					scopeID: scopeID,
					origin:  origin,
					phase:   agentStepProviderRunning,
				}
			}

			runtimeEvents := engine.runtimeEvents
			engine.runtimeEvents = nil
			resolver, err := engine.admitManualCompaction(
				runtimeEventAdmission{engine: engine},
				compactionInstructionsInput{},
				nil,
			)
			engine.runtimeEvents = runtimeEvents
			if err != nil {
				t.Fatalf("admit manual compaction: %v", err)
			}
			if resolver == nil {
				t.Fatal("admission returned no typed resolver")
			}
			pending := engine.boundaryAgenda.pending()
			if len(pending) != 1 {
				t.Fatalf("admission pending items = %d, want one", len(pending))
			}
			item, ok := pending[0].(*manualCompactionAgendaItem)
			if !ok {
				t.Fatalf("pending item = %T", pending[0])
			}
			if item.eligibility != test.wantEligibility {
				t.Fatalf(
					"eligibility = %v, want %v",
					item.eligibility,
					test.wantEligibility,
				)
			}
			if test.active {
				binding, ok := item.binding.(scopeAgendaBinding)
				if !ok || binding.scopeID != scopeID || binding.origin != origin {
					t.Fatalf("scope binding = %+v, want scope=%s origin=%+v", item.binding, scopeID, origin)
				}
				return
			}
			if _, ok := item.binding.(runtimeAgendaBinding); !ok {
				t.Fatalf("idle binding = %T, want runtime binding", item.binding)
			}
		})
	}
}

func TestManualCompactionRejectsWhileSelectedCompactionIsActive(t *testing.T) {
	client := newScriptedGoalLoopClient()
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		tools.NewRegistry(),
		Config{Model: "gpt-5", CompactionMode: "local"},
	)
	if err := engine.steer(
		"seed",
		steerMessagesWithPersistenceIntent(
			steeringPriorityNormal,
			steeringMessageEventNone,
			true,
			[]llm.Message{{
				Role:    llm.RoleUser,
				Content: textutil.Value("compact me"),
			}},
		),
	); err != nil {
		t.Fatalf("seed transcript: %v", err)
	}
	engine.compactionRuntimeState().SetManualCompactionEligible(true)

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- engine.CompactContext(context.Background(), "")
	}()
	client.waitStarted(t, 1)

	if err := engine.CompactContext(context.Background(), ""); !errors.Is(err, ErrManualCompactionActive) {
		t.Fatalf("concurrent manual compaction error = %v, want active rejection", err)
	}
	client.releaseCall(1)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first manual compaction: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first manual compaction did not finish")
	}
}

func TestIdleManualCompactionSurvivesCallerCancellation(t *testing.T) {
	client := newScriptedGoalLoopClient()
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		tools.NewRegistry(),
		Config{Model: "gpt-5", CompactionMode: "local"},
	)
	if err := engine.steer(
		"seed",
		steerMessagesWithPersistenceIntent(
			steeringPriorityNormal,
			steeringMessageEventNone,
			true,
			[]llm.Message{{
				Role:    llm.RoleUser,
				Content: textutil.Value("compact me"),
			}},
		),
	); err != nil {
		t.Fatalf("seed transcript: %v", err)
	}
	engine.compactionRuntimeState().SetManualCompactionEligible(true)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- engine.CompactContext(ctx, "")
	}()
	client.waitStarted(t, 1)
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("caller cancellation error = %v, want context canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("caller wait did not cancel")
	}
	client.releaseCall(1)
	deadline := time.Now().Add(3 * time.Second)
	for engine.CompactionCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := engine.CompactionCount(); got != 1 {
		t.Fatalf("compaction count after caller cancellation = %d, want one", got)
	}
}

func TestIdleManualCompactionWaitsForIdleBoundaryOwnership(t *testing.T) {
	client := newScriptedGoalLoopClient()
	boundary := &manualCompactionIdleBoundaryLifecycle{}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		tools.NewRegistry(),
		Config{
			Model:          "gpt-5",
			CompactionMode: "local",
			StepLifecycle:  boundary,
		},
	)
	if err := engine.steer(
		"seed",
		steerMessagesWithPersistenceIntent(
			steeringPriorityNormal,
			steeringMessageEventNone,
			true,
			[]llm.Message{{
				Role:    llm.RoleUser,
				Content: textutil.Value("compact me"),
			}},
		),
	); err != nil {
		t.Fatalf("seed transcript: %v", err)
	}
	engine.compactionRuntimeState().SetManualCompactionEligible(true)

	done := make(chan error, 1)
	go func() {
		done <- engine.CompactContext(context.Background(), "")
	}()
	client.assertNotStarted(t, 1)
	if pending := engine.boundaryAgenda.pending(); len(pending) != 1 {
		t.Fatalf("pending compaction items = %d, want one", len(pending))
	}

	boundary.allow()
	if err := engine.AgentExecutionScopeReleased(runtimeids.NewExecutionScopeID()); err != nil {
		t.Fatalf("trigger idle reduction: %v", err)
	}
	client.waitStarted(t, 1)
	client.releaseCall(1)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("manual compaction: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("manual compaction did not finish after idle Boundary grant")
	}
}

func TestSelectedManualCompactionRuntimeCloseSettlesBoundaryOnce(t *testing.T) {
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5", CompactionMode: "local"},
	)
	step := activeAgentStep{
		scopeID: runtimeids.NewExecutionScopeID(),
		origin: serverapi.RuntimeStepOrigin{
			RunID:  uuid.NewString(),
			StepID: uuid.NewString(),
		},
		phase: agentStepProviderRunning,
	}
	engine.agentSteps.boundary = &step
	grant := &manualCompactionTestGrant{}
	completed := make(chan error, 2)
	item := &manualCompactionAgendaItem{
		id:          "selected-close",
		stepID:      uuid.NewString(),
		binding:     scopeBoundaryBinding(step.scopeID, step.origin),
		eligibility: boundaryEligibilityStep,
		resolver:    newManualCompactionResolver(),
		boundary: &manualCompactionBoundaryContinuation{
			engine:       engine,
			grant:        grant,
			continueTurn: true,
			step:         step,
			complete: func(_ agentStepBoundaryDecision, err error) {
				completed <- err
			},
		},
	}
	if err := engine.boundaryAgenda.accept(item); err != nil {
		t.Fatalf("accept compaction: %v", err)
	}
	if _, err := engine.longBoundary.selectNext(
		engine.boundaryAgenda,
		stepBoundarySelection(step.scopeID, step.origin),
	); err != nil {
		t.Fatalf("select compaction: %v", err)
	}

	engine.longBoundary.close(errBoundaryRuntimeClosed)
	engine.longBoundary.close(errBoundaryRuntimeClosed)

	if grant.releaseCount() != 1 {
		t.Fatalf("grant releases = %d, want one", grant.releaseCount())
	}
	if engine.agentSteps.boundary != nil {
		t.Fatalf("runtime close retained Agent Step Boundary: %+v", engine.agentSteps.boundary)
	}
	select {
	case err := <-completed:
		if !errors.Is(err, errBoundaryRuntimeClosed) {
			t.Fatalf("Boundary completion error = %v, want runtime close", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runtime close did not settle the Boundary resolver")
	}
	select {
	case err := <-completed:
		t.Fatalf("Boundary resolver settled twice: %v", err)
	default:
	}
	receipt, err := item.resolver.wait(context.Background())
	if receipt != (session.CommitReceipt{}) ||
		!errors.Is(err, errBoundaryRuntimeClosed) {
		t.Fatalf("manual resolver settlement = %+v, %v", receipt, err)
	}
}

type manualCompactionTestGrant struct {
	mu       sync.Mutex
	releases int
}

type manualCompactionIdleBoundaryLifecycle struct {
	mu      sync.Mutex
	allowed bool
}

func (*manualCompactionIdleBoundaryLifecycle) StepBegan(
	context.Context,
	StepLifecycleSnapshot,
) error {
	return nil
}

func (*manualCompactionIdleBoundaryLifecycle) StepEnded(
	context.Context,
	StepLifecycleSnapshot,
) error {
	return nil
}

func (l *manualCompactionIdleBoundaryLifecycle) TryAcquireIdleBoundary(
	context.Context,
) (IdleBoundaryReducerGrant, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.allowed {
		return nil, false, nil
	}
	return manualCompactionIdleBoundaryGrant{}, true, nil
}

func (l *manualCompactionIdleBoundaryLifecycle) allow() {
	l.mu.Lock()
	l.allowed = true
	l.mu.Unlock()
}

type manualCompactionIdleBoundaryGrant struct{}

func (manualCompactionIdleBoundaryGrant) Release() (bool, error) {
	return false, nil
}

func (*manualCompactionTestGrant) RegisterNext(
	context.Context,
	serverapi.RuntimeStepOrigin,
) (runtimeids.ExecutionScopeID, error) {
	return runtimeids.ExecutionScopeID{}, errors.New("unexpected next Agent Step registration")
}

func (g *manualCompactionTestGrant) Release() error {
	g.mu.Lock()
	g.releases++
	g.mu.Unlock()
	return nil
}

func (g *manualCompactionTestGrant) releaseCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.releases
}
