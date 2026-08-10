package runtime

import (
	"context"
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
		wantContinue bool
		wantPending  int
	}{
		{name: "Step input eligibility", mode: agentStepBoundaryModeStep, continueTurn: true, queueInputs: true, wantContinue: true, wantPending: 1},
		{name: "Turn input eligibility", mode: agentStepBoundaryModeTurn, queueInputs: true, wantContinue: true},
		{name: "Turn finish", mode: agentStepBoundaryModeTurn},
	} {
		t.Run(test.name, func(t *testing.T) {
			scopeID := runtimeids.NewExecutionScopeID()
			lifecycle := &manualCompactionRunnerLifecycle{scopeID: scopeID}
			client := &fakeCompactionClient{responses: []llm.Response{{
				Assistant: llm.Message{
					Role:    llm.RoleAssistant,
					Content: textutil.Value("summary"),
				},
			}}}
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

			started := make(chan struct{})
			release := make(chan struct{})
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
						func() {
							close(started)
							<-release
						},
						completion,
					)
				},
			)
			if err != nil {
				t.Fatalf("submit manual compaction: %v", err)
			}
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
			if got := lifecycle.initialRelease.Load(); got != 1 {
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
			if got := lifecycle.freshAcquisitions.Load(); got != 1 {
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
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	scopeID := runtimeids.NewExecutionScopeID()
	engine.agentSteps.scopeID = scopeID
	decisionReady := make(chan awaitManualCompactionSelectionDecision, 1)
	deferred, err := runtimecommand.SubmitBound(
		engine.lifecycleCtx,
		engine.runtimeEvents,
		struct{}{},
		func(
			_ runtimecommand.Admission,
			_ struct{},
			completion runtimecommand.CompletionBinding[session.CommitReceipt],
		) error {
			selection := &manualCompactionSelection{
				id:         "manual-compaction:close",
				completion: completion,
				scopeID:    scopeID,
			}
			engine.longBoundary.selected = selection
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
	decision := <-decisionReady
	if err := engine.ApplyRuntimeCloseUnderAdmission(); err != nil {
		t.Fatalf("apply runtime close: %v", err)
	}
	if _, err := deferred.Await(context.Background()); err != runtimecommand.ErrUnavailable {
		t.Fatalf("manual command after runtime close = %v, want unavailable", err)
	}
	if _, err := decision.Deferred.Await(context.Background()); err != runtimecommand.ErrUnavailable {
		t.Fatalf("manual Deferred view after runtime close = %v, want unavailable", err)
	}
	resolved, err := engine.resolveAgentStepBoundaryDecision(
		decision,
		agentStepBoundaryModeStep,
	)
	if err != nil {
		t.Fatalf("runner retirement after runtime close: %v", err)
	}
	if _, retire := resolved.(retireAgentTurnDecision); !retire {
		t.Fatalf("runner decision after runtime close = %T, want retire", resolved)
	}
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
