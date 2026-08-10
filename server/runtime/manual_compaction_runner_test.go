package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"core/server/llm"
	"core/server/runtimecommand"
	"core/server/session"
	"core/server/tools"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"

	"github.com/google/uuid"
)

func TestActiveManualCompactionUsesFreshReducerHandoff(t *testing.T) {
	for _, test := range []struct {
		name         string
		mode         agentStepBoundaryMode
		continueTurn bool
		queueInputs  bool
		worktree     bool
		wantContinue bool
		wantPending  int
	}{
		{name: "Step input eligibility", mode: agentStepBoundaryModeStep, continueTurn: true, queueInputs: true, wantContinue: true, wantPending: 1},
		{name: "Turn input eligibility", mode: agentStepBoundaryModeTurn, queueInputs: true, wantContinue: true},
		{name: "Turn finish", mode: agentStepBoundaryModeTurn},
		{name: "current Worktree wins fresh reduction", mode: agentStepBoundaryModeStep, continueTurn: true, queueInputs: true, worktree: true, wantPending: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			scopeID := runtimeids.NewExecutionScopeID()
			baseLifecycle := &manualCompactionRunnerLifecycle{scopeID: scopeID}
			var lifecycle StepLifecycleSink = baseLifecycle
			var worktreeLifecycle *worktreeClaimedManualCompactionLifecycle
			if test.worktree {
				worktreeLifecycle = &worktreeClaimedManualCompactionLifecycle{
					manualCompactionRunnerLifecycle: baseLifecycle,
				}
				lifecycle = worktreeLifecycle
			}
			client := &fakeCompactionClient{responses: []llm.Response{{
				Assistant: llm.Message{
					Role:    llm.RoleAssistant,
					Content: textutil.Value("summary"),
				},
			}}}
			engine := newActiveManualCompactionRunnerEngine(
				t,
				scopeID,
				lifecycle,
				client,
			)
			origin := engine.agentSteps.current.origin

			started := make(chan struct{})
			release := make(chan struct{})
			deferred := submitManualCompactionRunnerCommand(t, engine, func() {
				close(started)
				<-release
			})
			decision, err := engine.completeAgentProviderBoundary(
				context.Background(),
				test.continueTurn,
			)
			if err != nil {
				t.Fatalf("select manual compaction: %v", err)
			}
			await, ok := decision.(awaitManualCompactionSelectionDecision)
			if !ok || await.Scope != scopeID {
				t.Fatalf("Boundary decision = %+v, want exact-scope await", decision)
			}
			if engine.agentSteps.current != nil || engine.agentSteps.boundary != nil {
				t.Fatal("manual selection retained a completion-eligible Agent Step")
			}
			if got := baseLifecycle.initialRelease.Load(); got != 1 {
				t.Fatalf("initial reducer releases = %d, want one", got)
			}
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("manual compaction did not start")
			}
			if test.queueInputs {
				engine.beginLiveRunStep(&RunSnapshot{
					RunID:      origin.RunID,
					StepID:     origin.StepID,
					ActiveKind: ActiveKindUserTurn,
					Status:     RunStatusRunning,
					StartedAt:  time.Now().UTC(),
				})
				if _, accepted, err := engine.QueueUserMessageForActiveRun(
					context.Background(),
					"Step input",
					nil,
				); err != nil || !accepted {
					t.Fatalf("queue Step input during compaction = (%t, %v)", accepted, err)
				}
				if _, err := engine.QueueUserMessage("Queue input"); err != nil {
					t.Fatalf("queue Turn input during compaction: %v", err)
				}
			}
			waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			if _, err := deferred.Await(waitCtx); err == nil {
				t.Fatal("manual command settled before selected work completed")
			}
			close(release)

			resolved, err := engine.resolveAgentStepBoundaryDecision(decision, test.mode)
			if err != nil {
				t.Fatalf("fresh reducer handoff: %v", err)
			}
			receipt, err := deferred.Await(context.Background())
			if err != nil || !receipt.Committed {
				t.Fatalf("manual command result = (%+v, %v), want committed", receipt, err)
			}
			viewReceipt, err := await.Deferred.Await(context.Background())
			if err != nil || viewReceipt != receipt {
				t.Fatalf("manual Deferred view = (%+v, %v), want %+v", viewReceipt, err, receipt)
			}
			if test.worktree {
				if got := worktreeLifecycle.claimArbitrations.Load(); got != 1 {
					t.Fatalf("Worktree claim arbitrations = %d, want one", got)
				}
			} else if got := baseLifecycle.freshAcquisitions.Load(); got != 1 {
				t.Fatalf("fresh reducer acquisitions = %d, want one", got)
			}
			_, continued := resolved.(prepareNextAgentStepDecision)
			if continued != test.wantContinue {
				t.Fatalf("fresh reducer decision = %T, want continue=%t", resolved, test.wantContinue)
			}
			if pending := engine.boundaryAgenda.pendingHuman(); len(pending) != test.wantPending {
				t.Fatalf("pending human input after %s continuation = %+v, want %d", test.name, pending, test.wantPending)
			}
		})
	}
}

func TestSelectedManualCompactionRuntimeCloseSettlesCommandAndRetiresRunner(t *testing.T) {
	scopeID := runtimeids.NewExecutionScopeID()
	lifecycle := &manualCompactionRunnerLifecycle{scopeID: scopeID}
	started := make(chan struct{})
	release := make(chan struct{})
	engine := newActiveManualCompactionRunnerEngine(
		t,
		scopeID,
		lifecycle,
		&fakeCompactionClient{},
	)
	defer close(release)
	deferred := submitManualCompactionRunnerCommand(t, engine, func() {
		close(started)
		<-release
	})
	boundaryDeferred := submitManualCompactionRunnerBoundary(t, engine, true)
	decision, err := boundaryDeferred.Await(context.Background())
	if err != nil {
		t.Fatalf("await selected Boundary: %v", err)
	}
	await, ok := decision.(awaitManualCompactionSelectionDecision)
	if !ok {
		t.Fatalf("Boundary decision = %T, want manual-compaction await", decision)
	}
	<-started
	if err := engine.ApplyRuntimeCloseUnderAdmission(); err != nil {
		t.Fatalf("apply runtime close: %v", err)
	}
	if _, err := deferred.Await(context.Background()); err != runtimecommand.ErrUnavailable {
		t.Fatalf("manual command after runtime close = %v, want unavailable", err)
	}
	if _, err := await.Deferred.Await(context.Background()); err != runtimecommand.ErrUnavailable {
		t.Fatalf("manual Deferred view after runtime close = %v, want unavailable", err)
	}
	repeated, err := boundaryDeferred.Await(context.Background())
	if err != nil {
		t.Fatalf("re-await settled Boundary decision: %v", err)
	}
	if _, ok := repeated.(awaitManualCompactionSelectionDecision); !ok {
		t.Fatalf("Boundary decision after runtime close = %T, want preserved await", repeated)
	}
	resolved, err := engine.resolveAgentStepBoundaryDecision(
		decision,
		agentStepBoundaryModeStep,
	)
	if !errors.Is(err, ErrEngineClosed) || !errors.Is(err, runtimecommand.ErrUnavailable) {
		t.Fatalf("runner fresh submission after runtime close = (%T, %v), want unavailable", resolved, err)
	}
}

func TestActiveManualCompactionOperationalFailureWakesFreshReducer(t *testing.T) {
	scopeID := runtimeids.NewExecutionScopeID()
	lifecycle := &manualCompactionRunnerLifecycle{scopeID: scopeID}
	engine := newActiveManualCompactionRunnerEngine(
		t,
		scopeID,
		lifecycle,
		&fakeCompactionClient{},
	)
	manualDeferred := submitManualCompactionRunnerCommand(t, engine, nil)
	boundaryDeferred := submitManualCompactionRunnerBoundary(t, engine, true)

	decision, err := boundaryDeferred.Await(context.Background())
	if err != nil {
		t.Fatalf("await Boundary selection: %v", err)
	}
	if _, ok := decision.(awaitManualCompactionSelectionDecision); !ok {
		t.Fatalf("Boundary decision = %T, want manual-compaction await", decision)
	}
	resolved, err := engine.resolveAgentStepBoundaryDecision(
		decision,
		agentStepBoundaryModeStep,
	)
	if err != nil {
		t.Fatalf("fresh reducer after operational failure: %v", err)
	}
	if _, ok := resolved.(prepareNextAgentStepDecision); !ok {
		t.Fatalf("fresh reducer decision = %T, want prepare", resolved)
	}
	if _, err := manualDeferred.Await(context.Background()); err == nil {
		t.Fatal("manual command unexpectedly succeeded")
	}
	repeated, err := boundaryDeferred.Await(context.Background())
	if err != nil {
		t.Fatalf("re-await Boundary decision: %v", err)
	}
	if _, ok := repeated.(awaitManualCompactionSelectionDecision); !ok {
		t.Fatalf("repeated Boundary decision = %T, want preserved await decision", repeated)
	}
}

func TestSelectedManualCompactionCancellationWakesFreshReducer(t *testing.T) {
	scopeID := runtimeids.NewExecutionScopeID()
	lifecycle := &manualCompactionRunnerLifecycle{scopeID: scopeID}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5", StepLifecycle: lifecycle},
	)
	engine.agentSteps.scopeID = scopeID
	manualDeferred, decision, _ := submitSelectedManualCompaction(t, engine, scopeID)
	engine.longBoundary.close(context.Canceled)

	resolved, err := engine.resolveAgentStepBoundaryDecision(
		decision,
		agentStepBoundaryModeStep,
	)
	if err != nil {
		t.Fatalf("fresh reducer after selected cancellation: %v", err)
	}
	if _, ok := resolved.(prepareNextAgentStepDecision); !ok {
		t.Fatalf("fresh reducer decision = %T, want prepare", resolved)
	}
	if _, err := manualDeferred.Await(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("manual command error = %v, want cancellation", err)
	}
	if _, err := decision.Deferred.Await(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("manual Deferred view error = %v, want cancellation", err)
	}
}

func TestManualCompactionCloseBeforeSelectionSettlesBothEventsUnavailable(t *testing.T) {
	scopeID := runtimeids.NewExecutionScopeID()
	lifecycle := &manualCompactionRunnerLifecycle{scopeID: scopeID}
	engine := newActiveManualCompactionRunnerEngine(
		t,
		scopeID,
		lifecycle,
		&fakeCompactionClient{},
	)
	blockerStarted := make(chan struct{})
	closeObserved := make(chan struct{})
	releaseBlocker := make(chan struct{})
	blockerDeferred, err := runtimecommand.Submit(
		context.Background(),
		engine.runtimeEvents,
		struct{}{},
		func(
			admission runtimecommand.Admission,
			_ struct{},
			complete func(struct{}, error),
		) error {
			close(blockerStarted)
			<-admission.Context().Done()
			close(closeObserved)
			<-releaseBlocker
			complete(struct{}{}, nil)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("submit blocker: %v", err)
	}
	<-blockerStarted
	manualDeferred := submitManualCompactionRunnerCommand(t, engine, nil)
	boundaryDeferred := submitManualCompactionRunnerBoundary(t, engine, true)

	closeDone := make(chan struct{})
	go func() {
		engine.runtimeEvents.Close()
		close(closeDone)
	}()
	<-closeObserved
	close(releaseBlocker)
	<-closeDone
	if _, err := blockerDeferred.Await(context.Background()); err != runtimecommand.ErrUnavailable {
		t.Fatalf("blocker result after close = %v, want unavailable", err)
	}
	if _, err := manualDeferred.Await(context.Background()); err != runtimecommand.ErrUnavailable {
		t.Fatalf("manual command after close = %v, want unavailable", err)
	}
	if _, err := boundaryDeferred.Await(context.Background()); err != runtimecommand.ErrUnavailable {
		t.Fatalf("Boundary event after close = %v, want unavailable", err)
	}
	if err := engine.ApplyRuntimeCloseUnderAdmission(); err != nil {
		t.Fatalf("settle runtime state after Queue close: %v", err)
	}
}

func TestAcceptedFreshManualCompactionReducerRacingCloseSettlesUnavailable(t *testing.T) {
	scopeID := runtimeids.NewExecutionScopeID()
	lifecycle := &closingFreshReducerLifecycle{
		manualCompactionRunnerLifecycle: &manualCompactionRunnerLifecycle{scopeID: scopeID},
		started:                         make(chan struct{}),
	}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5", StepLifecycle: lifecycle},
	)
	engine.agentSteps.scopeID = scopeID
	manualDeferred, decision, selection := submitSelectedManualCompaction(t, engine, scopeID)
	if _, err := engine.longBoundary.release(boundaryLongWorkResult{id: selection.id}); err != nil {
		t.Fatalf("release selected manual work: %v", err)
	}
	selection.completion.Complete(session.CommitReceipt{Committed: true}, nil)
	if _, err := manualDeferred.Await(context.Background()); err != nil {
		t.Fatalf("settle manual command: %v", err)
	}
	result := make(chan error, 1)
	go func() {
		_, resolveErr := engine.resolveAgentStepBoundaryDecision(
			decision,
			agentStepBoundaryModeStep,
		)
		result <- resolveErr
	}()
	<-lifecycle.started
	closeDone := make(chan struct{})
	go func() {
		engine.runtimeEvents.Close()
		close(closeDone)
	}()
	if err := <-result; !errors.Is(err, ErrEngineClosed) ||
		!errors.Is(err, runtimecommand.ErrUnavailable) {
		t.Fatalf("fresh reducer result racing close = %v, want unavailable", err)
	}
	<-closeDone
	if err := engine.ApplyRuntimeCloseUnderAdmission(); err != nil {
		t.Fatalf("settle runtime state after Queue close: %v", err)
	}
}

func newActiveManualCompactionRunnerEngine(
	t *testing.T,
	scopeID runtimeids.ExecutionScopeID,
	lifecycle StepLifecycleSink,
	client llm.Client,
) *Engine {
	t.Helper()
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		tools.NewRegistry(),
		Config{
			Model:          "gpt-5",
			CompactionMode: "local",
			StepLifecycle:  lifecycle,
		},
	)
	if err := engine.steer("", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventDefault,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("context")}},
	)); err != nil {
		t.Fatalf("seed compaction input: %v", err)
	}
	origin := serverapi.RuntimeStepOrigin{
		RunID:  uuid.NewString(),
		StepID: uuid.NewString(),
	}
	engine.agentSteps.scopeID = scopeID
	engine.agentSteps.current = &activeAgentStep{
		scopeID: scopeID,
		origin:  origin,
		phase:   agentStepProviderRunning,
	}
	return engine
}

func submitManualCompactionRunnerCommand(
	t *testing.T,
	engine *Engine,
	onActive func(),
) *runtimecommand.Deferred[session.CommitReceipt] {
	t.Helper()
	deferred, err := runtimecommand.SubmitBound(
		engine.lifecycleCtx,
		engine.runtimeEvents,
		struct{}{},
		func(
			command runtimecommand.Admission,
			_ struct{},
			completion runtimecommand.CompletionBinding[session.CommitReceipt],
		) error {
			return engine.admitManualCompaction(
				runtimeEventAdmission{engine: engine, command: command},
				compactionInstructionsInput{},
				onActive,
				completion,
			)
		},
	)
	if err != nil {
		t.Fatalf("submit manual compaction: %v", err)
	}
	return deferred
}

func submitManualCompactionRunnerBoundary(
	t *testing.T,
	engine *Engine,
	continueTurn bool,
) *runtimecommand.Deferred[agentStepBoundaryDecision] {
	t.Helper()
	deferred, err := runtimecommand.Submit(
		engine.lifecycleCtx,
		engine.runtimeEvents,
		agentStepBoundaryRequest{continueTurn: continueTurn},
		engine.admitAgentStepBoundary,
	)
	if err != nil {
		t.Fatalf("submit Agent Step Boundary: %v", err)
	}
	return deferred
}

func submitSelectedManualCompaction(
	t *testing.T,
	engine *Engine,
	scopeID runtimeids.ExecutionScopeID,
) (
	*runtimecommand.Deferred[session.CommitReceipt],
	awaitManualCompactionSelectionDecision,
	*manualCompactionSelection,
) {
	t.Helper()
	decisionReady := make(chan awaitManualCompactionSelectionDecision, 1)
	var selected *manualCompactionSelection
	deferred, err := runtimecommand.SubmitBound(
		engine.lifecycleCtx,
		engine.runtimeEvents,
		struct{}{},
		func(
			_ runtimecommand.Admission,
			_ struct{},
			completion runtimecommand.CompletionBinding[session.CommitReceipt],
		) error {
			selected = &manualCompactionSelection{
				id:         "manual-compaction:selected",
				completion: completion,
				scopeID:    scopeID,
			}
			engine.longBoundary.selected = selected
			decisionReady <- awaitManualCompactionSelectionDecision{
				Scope:    scopeID,
				Deferred: completion.Deferred(),
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("submit selected manual command: %v", err)
	}
	return deferred, <-decisionReady, selected
}

type manualCompactionRunnerLifecycle struct {
	scopeID           runtimeids.ExecutionScopeID
	initialRelease    atomic.Int32
	freshAcquisitions atomic.Int32
}

func (*manualCompactionRunnerLifecycle) StepBegan(context.Context, StepLifecycleSnapshot) error {
	return nil
}

func (*manualCompactionRunnerLifecycle) StepEnded(context.Context, StepLifecycleSnapshot) error {
	return nil
}

func (l *manualCompactionRunnerLifecycle) AgentStepBoundary(
	context.Context,
	serverapi.RuntimeStepOrigin,
) (AgentStepBoundaryTransfer, error) {
	return AgentStepReducerBoundary{Grant: manualCompactionRunnerGrant{
		scopeID:  l.scopeID,
		releases: &l.initialRelease,
	}}, nil
}

func (l *manualCompactionRunnerLifecycle) AgentStepBegan(
	context.Context,
	serverapi.RuntimeStepOrigin,
) (runtimeids.ExecutionScopeID, error) {
	return l.scopeID, nil
}

func (l *manualCompactionRunnerLifecycle) AgentStepScopeLive(
	context.Context,
	runtimeids.ExecutionScopeID,
) bool {
	return true
}

func (l *manualCompactionRunnerLifecycle) CurrentAgentExecutionScope(
	context.Context,
) (runtimeids.ExecutionScopeID, bool) {
	return l.scopeID, true
}

func (l *manualCompactionRunnerLifecycle) TryAcquireAgentStepReducerBoundary(
	context.Context,
	runtimeids.ExecutionScopeID,
) (AgentStepReducerGrant, bool, error) {
	l.freshAcquisitions.Add(1)
	return manualCompactionRunnerGrant{scopeID: l.scopeID}, true, nil
}

type manualCompactionRunnerGrant struct {
	scopeID  runtimeids.ExecutionScopeID
	releases *atomic.Int32
}

func (g manualCompactionRunnerGrant) RegisterNext(
	context.Context,
	serverapi.RuntimeStepOrigin,
) (runtimeids.ExecutionScopeID, error) {
	return g.scopeID, nil
}

func (g manualCompactionRunnerGrant) Release() error {
	if g.releases != nil {
		g.releases.Add(1)
	}
	return nil
}

type closingFreshReducerLifecycle struct {
	*manualCompactionRunnerLifecycle
	started chan struct{}
}

func (l *closingFreshReducerLifecycle) TryAcquireAgentStepReducerBoundary(
	ctx context.Context,
	_ runtimeids.ExecutionScopeID,
) (AgentStepReducerGrant, bool, error) {
	close(l.started)
	<-ctx.Done()
	return nil, false, serverapi.ErrRuntimeUnavailable
}

type worktreeClaimedManualCompactionLifecycle struct {
	*manualCompactionRunnerLifecycle
	claimArbitrations atomic.Int32
}

func (l *worktreeClaimedManualCompactionLifecycle) TryAcquireAgentStepReducerBoundary(
	context.Context,
	runtimeids.ExecutionScopeID,
) (AgentStepReducerGrant, bool, error) {
	l.claimArbitrations.Add(1)
	return nil, false, nil
}
