package runtimecontrol

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/requestmemo"
	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/runtimecommand"
	"core/server/runtimeops"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionruntime"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
	"core/shared/transcript"
)

type admittedGoalRouter struct {
	release    chan struct{}
	admissions chan struct{}

	mu      sync.Mutex
	calls   int
	effects int
	runErr  error
}

func newAdmittedGoalRouter() *admittedGoalRouter {
	return &admittedGoalRouter{
		release:    make(chan struct{}),
		admissions: make(chan struct{}, 4096),
	}
}

func (r *admittedGoalRouter) RouteSessionAgentOperation(
	context.Context,
	runtimeids.SessionID,
	runtimecommand.SessionAgentOperationDriver,
) (bool, runtimecommand.SessionAgentOperationOutcome, error) {
	return false, nil, nil
}

func (r *admittedGoalRouter) RouteAdmittedSessionAgentOperation(
	_ context.Context,
	_ runtimeids.SessionID,
	_ runtimecommand.SessionAgentOperationDriver,
	admit runtimecommand.SessionAgentOperationAdmitter,
) (bool, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	err := admit(func() (runtimecommand.SessionAgentOperationOutcome, error) {
		r.admissions <- struct{}{}
		<-r.release
		r.mu.Lock()
		r.effects++
		r.mu.Unlock()
		goal := session.GoalState{
			ID:        "goal-admitted-router",
			Objective: "retained admitted goal",
			Status:    session.GoalStatusActive,
		}
		return runtimecommand.GoalMutationOperationOutcome{Result: runtimecommand.GoalCommandResult{
			Goal:        &goal,
			Disposition: runtime.GoalCommandQueued,
		}}, r.runErr
	})
	return err == nil, err
}

func (r *admittedGoalRouter) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, r.effects
}

func admittedGoalService(router *admittedGoalRouter) *Service {
	execution := runtimecommand.NewExecutionAdapter(nil, router)
	return NewServiceWithGoalCommands(
		nil,
		execution,
		runtimecommand.NewGoalAuthority(nil, execution),
	)
}

func TestServiceAdmittedGoalRequestLossRetryJoinsOneDriver(t *testing.T) {
	router := newAdmittedGoalRouter()
	service := admittedGoalService(router)
	sessionID := runtimeids.NewSessionID().String()
	request := serverapi.RuntimeGoalSetRequest{
		ClientRequestID: "admitted-goal-request-loss",
		SessionID:       sessionID,
		Objective:       "retained admitted goal",
		Actor:           string(session.GoalActorUser),
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, err := service.SetGoal(firstCtx, request)
		firstDone <- err
	}()
	select {
	case <-router.admissions:
	case <-time.After(3 * time.Second):
		t.Fatal("admitted Goal driver did not start")
	}
	cancelFirst()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled first Goal request error = %v, want context canceled", err)
	}

	retryDone := make(chan error, 1)
	go func() {
		_, err := service.SetGoal(context.Background(), request)
		retryDone <- err
	}()
	select {
	case err := <-retryDone:
		t.Fatalf("same-ID Goal retry returned before admitted driver completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(router.release)
	if err := <-retryDone; err != nil {
		t.Fatalf("same-ID Goal retry: %v", err)
	}
	calls, effects := router.counts()
	if calls != 1 || effects != 1 {
		t.Fatalf("admitted Goal router calls/effects = %d/%d, want 1/1", calls, effects)
	}
}

func TestServiceAdmittedGoalAcceptedWithErrorReplaysOneTypedResult(t *testing.T) {
	router := newAdmittedGoalRouter()
	acceptedErr := errors.New("accepted Goal observer failed")
	router.runErr = acceptedErr
	service := admittedGoalService(router)
	request := serverapi.RuntimeGoalSetRequest{
		ClientRequestID: "admitted-goal-accepted-with-error",
		SessionID:       runtimeids.NewSessionID().String(),
		Objective:       "retained admitted goal",
		Actor:           string(session.GoalActorUser),
	}
	firstDone := make(chan error, 1)
	go func() {
		_, err := service.SetGoal(context.Background(), request)
		firstDone <- err
	}()
	select {
	case <-router.admissions:
	case <-time.After(3 * time.Second):
		t.Fatal("accepted-with-error Goal was not admitted")
	}
	close(router.release)
	if err := <-firstDone; !errors.Is(err, acceptedErr) {
		t.Fatalf("first accepted-with-error Goal error = %v, want %v", err, acceptedErr)
	}
	if _, err := service.SetGoal(context.Background(), request); !errors.Is(err, acceptedErr) {
		t.Fatalf("replayed accepted-with-error Goal error = %v, want %v", err, acceptedErr)
	}
	calls, effects := router.counts()
	if calls != 1 || effects != 1 {
		t.Fatalf("accepted-with-error Goal calls/effects = %d/%d, want 1/1", calls, effects)
	}
}

func TestServiceAdmittedGoalSaturationPreservesDuplicatePrecedenceAndReclaimsCapacity(t *testing.T) {
	router := newAdmittedGoalRouter()
	service := admittedGoalService(router)
	sessionID := runtimeids.NewSessionID().String()
	type result struct {
		err error
	}
	var admitted []<-chan result
	var first serverapi.RuntimeGoalSetRequest
	for index := 0; ; index++ {
		request := serverapi.RuntimeGoalSetRequest{
			ClientRequestID: fmt.Sprintf("admitted-goal-%d", index),
			SessionID:       sessionID,
			Objective:       "retained admitted goal",
			Actor:           string(session.GoalActorUser),
		}
		done := make(chan result, 1)
		go func() {
			_, err := service.SetGoal(context.Background(), request)
			done <- result{err: err}
		}()
		select {
		case <-router.admissions:
			if len(admitted) == 0 {
				first = request
			}
			admitted = append(admitted, done)
		case completed := <-done:
			if !errors.Is(completed.err, requestmemo.ErrCapacityUnavailable) {
				t.Fatalf("new Goal at saturation error = %v, want capacity unavailable", completed.err)
			}
			goto saturated
		case <-time.After(3 * time.Second):
			t.Fatal("Goal saturation entry neither admitted nor rejected")
		}
	}

saturated:
	duplicateDone := make(chan error, 1)
	go func() {
		_, err := service.SetGoal(context.Background(), first)
		duplicateDone <- err
	}()
	select {
	case err := <-duplicateDone:
		t.Fatalf("matching duplicate returned before owner completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	mismatched := first
	mismatched.Objective = "different retained goal"
	if _, err := service.SetGoal(context.Background(), mismatched); !errors.Is(err, requestmemo.ErrClientRequestIDReused) {
		t.Fatalf("mismatched duplicate error = %v, want request-ID reuse", err)
	}
	if _, err := service.SetGoal(context.Background(), serverapi.RuntimeGoalSetRequest{
		ClientRequestID: "genuinely-new-saturated-goal",
		SessionID:       sessionID,
		Objective:       "retained admitted goal",
		Actor:           string(session.GoalActorUser),
	}); !errors.Is(err, requestmemo.ErrCapacityUnavailable) {
		t.Fatalf("genuinely new saturated Goal error = %v, want capacity unavailable", err)
	}

	close(router.release)
	for _, done := range admitted {
		if completed := <-done; completed.err != nil {
			t.Fatalf("admitted Goal completion: %v", completed.err)
		}
	}
	if err := <-duplicateDone; err != nil {
		t.Fatalf("matching duplicate completion: %v", err)
	}
	if _, err := service.SetGoal(context.Background(), serverapi.RuntimeGoalSetRequest{
		ClientRequestID: "goal-after-capacity-reclamation",
		SessionID:       sessionID,
		Objective:       "retained admitted goal",
		Actor:           string(session.GoalActorUser),
	}); err != nil {
		t.Fatalf("Goal after completed-entry reclamation: %v", err)
	}
}

var runtimeControlOpenAICapabilities = llm.ProviderCapabilities{
	ProviderID:               "openai",
	SupportsResponsesAPI:     true,
	SupportsResponsesCompact: true,
	IsOpenAIFirstParty:       true,
}

type sessionStatusCountingResolver struct {
	publishCount int
	publishErr   error
}

func (*sessionStatusCountingResolver) RuntimeActivity(string) (clientui.RuntimeActivity, error) {
	return clientui.RuntimeActivity{State: clientui.RuntimeActivityUnavailable}, nil
}

func (r *sessionStatusCountingResolver) RuntimeReadModelSnapshot(context.Context, string, []clientui.RuntimeOperationRef) (runtimeactivity.ResponseSnapshot, error) {
	return runtimeactivity.ResponseSnapshot{}, nil
}

func (r *sessionStatusCountingResolver) PublishSessionStatus(string) error {
	r.publishCount++
	return r.publishErr
}

func TestServiceSetThinkingLevelDedupesSuccessfulRetry(t *testing.T) {
	store, engine, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{})
	req := serverapi.RuntimeSetThinkingLevelRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID, Level: "high"}

	if err := service.SetThinkingLevel(context.Background(), req); err != nil {
		t.Fatalf("SetThinkingLevel first: %v", err)
	}
	if err := service.SetThinkingLevel(context.Background(), req); err != nil {
		t.Fatalf("SetThinkingLevel replay: %v", err)
	}
	if got := engine.ThinkingLevel(); got != "high" {
		t.Fatalf("thinking level = %q, want high", got)
	}
}

func TestServiceCommittedRuntimeMutationReturnsAndCachesSessionStatusPublishError(t *testing.T) {
	statusErr := errors.New("session status publish failed")
	store, engine, service := newRuntimeControlTestService(t, &runtimeControlFakeClient{}, nil, runtime.Config{
		Model:                        "gpt-5",
		ProviderCapabilitiesOverride: &runtimeControlOpenAICapabilities,
	})
	resolver := &sessionStatusCountingResolver{publishErr: statusErr}
	service.WithRuntimeActivityResolver(resolver)
	req := serverapi.RuntimeSetFastModeEnabledRequest{
		ClientRequestID: "status-publish-error",
		SessionID:       store.Meta().SessionID,
		Enabled:         true,
	}

	first, err := service.SetFastModeEnabled(context.Background(), req)
	if !errors.Is(err, statusErr) {
		t.Fatalf("first SetFastModeEnabled error = %v, want status publish error", err)
	}
	second, err := service.SetFastModeEnabled(context.Background(), req)
	if !errors.Is(err, statusErr) {
		t.Fatalf("replayed SetFastModeEnabled error = %v, want status publish error", err)
	}
	if !first.Changed || first != second || !engine.FastModeEnabled() {
		t.Fatalf("responses = (%+v, %+v), fast mode = %t", first, second, engine.FastModeEnabled())
	}
	if resolver.publishCount != 1 {
		t.Fatalf("session status publish count = %d, want 1", resolver.publishCount)
	}
}

type sequenceRuntimeActivityResolver struct {
	snapshots []runtimeactivity.ResponseSnapshot
	calls     int
}

func (r *sequenceRuntimeActivityResolver) RuntimeActivity(string) (clientui.RuntimeActivity, error) {
	if len(r.snapshots) == 0 {
		return clientui.RuntimeActivity{State: clientui.RuntimeActivityUnavailable}, nil
	}
	index := min(r.calls, len(r.snapshots)-1)
	return r.snapshots[index].Activity, nil
}

func (r *sequenceRuntimeActivityResolver) RuntimeReadModelSnapshot(context.Context, string, []clientui.RuntimeOperationRef) (runtimeactivity.ResponseSnapshot, error) {
	if r.calls >= len(r.snapshots) {
		return r.snapshots[len(r.snapshots)-1], nil
	}
	snapshot := r.snapshots[r.calls]
	r.calls++
	return snapshot, nil
}

func TestServiceInterruptRetryReturnsFreshActivitySnapshot(t *testing.T) {
	runningVersion := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 1}
	idleVersion := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 2}
	runID := mustRuntimeControlRunID(t)
	stepID := mustRuntimeControlStepID(t)
	resolver := &sequenceRuntimeActivityResolver{snapshots: []runtimeactivity.ResponseSnapshot{
		{
			Version: runningVersion,
			Activity: clientui.RuntimeActivity{
				State: clientui.RuntimeActivityRunning,
				ActiveStep: &clientui.RuntimeActiveStep{
					ActiveKind: clientui.RuntimeActivityActiveKindGoalLoop,
					RunID:      runID,
					StepID:     stepID,
				},
			},
			InputReconciliation: clientui.RuntimeInputReconciliationSnapshot{},
		},
		{
			Version: idleVersion,
			Activity: clientui.RuntimeActivity{
				State:          clientui.RuntimeActivityRegisteredIdle,
				QueueAccepting: true,
			},
			InputReconciliation: clientui.RuntimeInputReconciliationSnapshot{},
		},
	}}
	service := NewService(sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})).WithRuntimeActivityResolver(resolver)
	req := serverapi.RuntimeInterruptRequest{ClientRequestID: "interrupt-retry", SessionID: "018fdd67-89ab-4cde-8123-456789abcdef"}

	first, err := service.Interrupt(context.Background(), req)
	if err != nil {
		t.Fatalf("Interrupt first: %v", err)
	}
	second, err := service.Interrupt(context.Background(), req)
	if err != nil {
		t.Fatalf("Interrupt retry: %v", err)
	}
	if !first.Activity.ActiveForControl() {
		t.Fatalf("first activity = %+v, want active", first.Activity)
	}
	if second.Activity.ActiveForControl() || second.Version != idleVersion {
		t.Fatalf("retry activity/version = %+v/%+v, want fresh idle %+v", second.Activity, second.Version, idleVersion)
	}
	if resolver.calls != 2 {
		t.Fatalf("snapshot calls = %d, want fresh composition on retry", resolver.calls)
	}
}

func TestServiceInterruptCachedAbsentTargetDoesNotCancelLaterOperation(t *testing.T) {
	operations := runtimeops.NewCoordinator()
	service := NewService(sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})).
		WithOperationCoordinator(operations)
	sessionID := "018fdd67-89ab-4cde-8123-456789abcdef"
	target := runtimeControlOperationRef(clientui.RuntimeOperationKindSubmit)
	request := serverapi.RuntimeInterruptRequest{
		ClientRequestID:    "018fdd67-89ab-4cde-8123-456789abcdea",
		SessionID:          sessionID,
		TargetOperationRef: &target,
	}
	if _, err := service.Interrupt(context.Background(), request); err != nil {
		t.Fatalf("Interrupt absent target: %v", err)
	}

	attemptReady := make(chan runtimeops.Attempt, 1)
	release := make(chan struct{})
	operationDone := make(chan error, 1)
	go func() {
		_, err := runtimeops.Do(
			operations,
			context.Background(),
			sessionID,
			target,
			"later operation",
			func(left, right string) bool { return left == right },
			func(_ context.Context, attempt runtimeops.Attempt) (struct{}, error) {
				attemptReady <- attempt
				<-release
				return struct{}{}, nil
			},
		)
		operationDone <- err
	}()
	attempt := <-attemptReady

	if _, err := service.Interrupt(context.Background(), request); err != nil {
		t.Fatalf("Interrupt replay: %v", err)
	}
	select {
	case <-attempt.Context().Done():
		t.Fatal("cached absent-target Interrupt canceled a later operation")
	default:
	}
	close(release)
	if err := <-operationDone; err != nil {
		t.Fatalf("later operation: %v", err)
	}
}

func TestServiceInterruptCanceledBehindOperationCommitBarrierIsRetryable(t *testing.T) {
	operations := runtimeops.NewCoordinator()
	service := NewService(sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})).
		WithOperationCoordinator(operations)
	sessionID := "018fdd67-89ab-4cde-8123-456789abcdef"
	target := runtimeControlOperationRef(clientui.RuntimeOperationKindSubmit)
	mutationStarted := make(chan struct{})
	releaseMutation := make(chan struct{})
	mutationErr := errors.New("target commit rejected")
	operationDone := make(chan error, 1)
	go func() {
		_, err := runtimeops.Do(
			operations,
			context.Background(),
			sessionID,
			target,
			"target operation",
			func(left, right string) bool { return left == right },
			func(context.Context, runtimeops.Attempt) (struct{}, error) {
				_, commitErr := operations.TryCommitOperationMutation(sessionID, target, func() error {
					close(mutationStarted)
					<-releaseMutation
					return mutationErr
				})
				return struct{}{}, commitErr
			},
		)
		operationDone <- err
	}()
	<-mutationStarted

	request := serverapi.RuntimeInterruptRequest{
		ClientRequestID:    runtimeids.NewRuntimeClientRequestID().String(),
		SessionID:          sessionID,
		TargetOperationRef: &target,
	}
	interruptCtx, cancelInterrupt := context.WithCancel(context.Background())
	interruptDone := make(chan error, 1)
	go func() {
		_, err := service.Interrupt(interruptCtx, request)
		interruptDone <- err
	}()
	cancelInterrupt()
	select {
	case err := <-interruptDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled Interrupt error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled Interrupt remained blocked behind target commit barrier")
	}

	close(releaseMutation)
	if err := <-operationDone; !errors.Is(err, mutationErr) {
		t.Fatalf("target operation error = %v, want rejected commit", err)
	}
	response, err := service.Interrupt(context.Background(), request)
	if err != nil {
		t.Fatalf("retry Interrupt: %v", err)
	}
	for _, record := range response.InputReconciliation.Operations {
		if record.Operation == target &&
			record.State == clientui.RuntimeInputReconciliationCanceledNotCommitted {
			return
		}
	}
	t.Fatalf("retry reconciliation = %+v, want canceled target", response.InputReconciliation)
}

type saturatingWorkflowInterruptor struct {
	entered chan chan struct{}
}

func (i *saturatingWorkflowInterruptor) InterruptWorkflowSession(
	ctx context.Context,
	_ sessionruntime.WorkflowSessionInterruptRequest,
	_ func(sessionruntime.WorkflowCommittedInterruptCleanup) error,
) (sessionruntime.WorkflowSessionInterruptOutcome, error) {
	release := make(chan struct{})
	select {
	case i.entered <- release:
	case <-ctx.Done():
		return sessionruntime.WorkflowSessionInterruptUnhandled, context.Cause(ctx)
	}
	select {
	case <-release:
		return sessionruntime.WorkflowSessionInterruptUnhandled, nil
	case <-ctx.Done():
		return sessionruntime.WorkflowSessionInterruptUnhandled, context.Cause(ctx)
	}
}

func TestServiceInterruptSaturationPreservesDuplicatePrecedenceAndReclaimsCompletion(t *testing.T) {
	interruptor := &saturatingWorkflowInterruptor{entered: make(chan chan struct{})}
	service := NewService(sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})).
		WithWorkflowSessionInterruptor(interruptor)
	sessionID := "018fdd67-89ab-4cde-8123-456789abcdef"
	type heldInterrupt struct {
		request serverapi.RuntimeInterruptRequest
		release chan struct{}
		done    chan error
	}
	held := make([]heldInterrupt, 0)
	var saturated serverapi.RuntimeInterruptRequest
	for {
		request := serverapi.RuntimeInterruptRequest{
			ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
			SessionID:       sessionID,
		}
		done := make(chan error, 1)
		go func() {
			_, err := service.Interrupt(context.Background(), request)
			done <- err
		}()
		select {
		case release := <-interruptor.entered:
			held = append(held, heldInterrupt{request: request, release: release, done: done})
		case err := <-done:
			if !errors.Is(err, requestmemo.ErrCapacityUnavailable) {
				t.Fatalf("new Interrupt at saturation error = %v, want capacity unavailable", err)
			}
			saturated = request
		}
		if saturated.ClientRequestID != "" {
			break
		}
	}
	defer func() {
		for _, item := range held {
			select {
			case <-item.release:
			default:
				close(item.release)
			}
			select {
			case <-item.done:
			case <-time.After(3 * time.Second):
				t.Errorf("held Interrupt did not finish during cleanup")
			}
		}
	}()

	duplicateCtx, cancelDuplicate := context.WithCancel(context.Background())
	cancelDuplicate()
	if _, err := service.Interrupt(duplicateCtx, held[0].request); !errors.Is(err, context.Canceled) {
		t.Fatalf("matching duplicate at saturation error = %v, want canceled waiter", err)
	}
	mismatch := held[0].request
	mismatch.PendingOperationRefs = []clientui.RuntimeOperationRef{
		runtimeControlOperationRef(clientui.RuntimeOperationKindSubmit),
	}
	if _, err := service.Interrupt(context.Background(), mismatch); !errors.Is(err, requestmemo.ErrClientRequestIDReused) {
		t.Fatalf("mismatched duplicate at saturation error = %v, want request ID reuse", err)
	}

	close(held[0].release)
	if err := <-held[0].done; err != nil {
		t.Fatalf("completed held Interrupt: %v", err)
	}
	held = held[1:]

	reclaimed := serverapi.RuntimeInterruptRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
		SessionID:       sessionID,
	}
	reclaimedDone := make(chan error, 1)
	go func() {
		_, err := service.Interrupt(context.Background(), reclaimed)
		reclaimedDone <- err
	}()
	var reclaimedRelease chan struct{}
	select {
	case reclaimedRelease = <-interruptor.entered:
	case err := <-reclaimedDone:
		t.Fatalf("new Interrupt after completion was not admitted: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("new Interrupt after completion neither admitted nor returned")
	}
	close(reclaimedRelease)
	if err := <-reclaimedDone; err != nil {
		t.Fatalf("reclaimed Interrupt: %v", err)
	}
}

func TestServiceCommittedControlObserverErrorIsMemoized(t *testing.T) {
	type controlResult struct {
		changed bool
		applied bool
	}
	testCases := []struct {
		name string
		cfg  runtime.Config
		run  func(*Service, *runtime.Engine, string, string) (controlResult, error)
	}{
		{
			name: "fast mode",
			cfg: runtime.Config{
				Model:                        "gpt-5",
				ProviderCapabilitiesOverride: &runtimeControlOpenAICapabilities,
			},
			run: func(service *Service, engine *runtime.Engine, sessionID string, requestID string) (controlResult, error) {
				resp, err := service.SetFastModeEnabled(context.Background(), serverapi.RuntimeSetFastModeEnabledRequest{
					ClientRequestID: requestID,
					SessionID:       sessionID,
					Enabled:         true,
				})
				return controlResult{changed: resp.Changed, applied: engine.FastModeEnabled()}, err
			},
		},
		{
			name: "reviewer",
			cfg: runtime.Config{
				Model: "gpt-5",
				Reviewer: runtime.ReviewerConfig{
					Model: "gpt-5",
					ClientFactory: func() (llm.Client, error) {
						return &runtimeControlFakeClient{}, nil
					},
				},
			},
			run: func(service *Service, engine *runtime.Engine, sessionID string, requestID string) (controlResult, error) {
				resp, err := service.SetReviewerEnabled(context.Background(), serverapi.RuntimeSetReviewerEnabledRequest{
					ClientRequestID: requestID,
					SessionID:       sessionID,
					Enabled:         true,
				})
				return controlResult{changed: resp.Changed, applied: resp.Mode == "edits" && engine.ReviewerFrequency() == "edits"}, err
			},
		},
		{
			name: "questions",
			cfg:  runtime.Config{Model: "gpt-5"},
			run: func(service *Service, engine *runtime.Engine, sessionID string, requestID string) (controlResult, error) {
				resp, err := service.SetQuestionsEnabled(context.Background(), serverapi.RuntimeSetQuestionsEnabledRequest{
					ClientRequestID: requestID,
					SessionID:       sessionID,
					Enabled:         false,
				})
				return controlResult{changed: resp.Changed, applied: !resp.Enabled && !engine.QuestionsEnabled()}, err
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			observerErr := errors.New("control feedback observer failed")
			gate := sessiontest.NewPersistenceGate(runtimeControlTestSessionPersistence)
			store, engine, service := newRuntimeControlTestService(
				t,
				&runtimeControlFakeClient{},
				nil,
				testCase.cfg,
				session.WithPersistenceObserver(gate),
			)
			resolver := &sessionStatusCountingResolver{}
			service.WithRuntimeActivityResolver(resolver)
			gate.FailNext(observerErr)

			first, err := testCase.run(service, engine, store.Meta().SessionID, "req-committed-control")
			if !errors.Is(err, observerErr) {
				t.Fatalf("first control error = %v, want observer error", err)
			}
			second, err := testCase.run(service, engine, store.Meta().SessionID, "req-committed-control")
			if !errors.Is(err, observerErr) {
				t.Fatalf("replayed control error = %v, want cached observer error", err)
			}
			if first != second || !first.changed || !first.applied {
				t.Fatalf("control results = (%+v, %+v), want identical committed result", first, second)
			}
			if got := len(localEntryEvents(t, store)); got != 1 {
				t.Fatalf("durable control feedback count = %d, want 1", got)
			}
			if resolver.publishCount != 1 {
				t.Fatalf("session status publish count = %d, want 1", resolver.publishCount)
			}
		})
	}
}

func TestServiceCompactionConsumesCommittedObserverError(t *testing.T) {
	observerErr := errors.New("history replacement observer failed")
	gate := sessiontest.NewPersistenceGate(runtimeControlTestSessionPersistence)
	store, _, client, service := newRuntimeControlCompactionFixture(t, session.WithPersistenceObserver(gate))
	operations := runtimeops.NewCoordinator()
	service.WithOperationCoordinator(operations)
	ref := runtimeControlOperationRef(clientui.RuntimeOperationKindCompact)
	request := serverapi.RuntimeCompactContextRequest{
		ClientRequestID: ref.ClientRequestID.String(),
		SessionID:       store.Meta().SessionID,
		Args:            "compact now",
		OperationRef:    ref,
	}
	gate.FailNext(observerErr)
	if err := service.CompactContext(context.Background(), request); !errors.Is(err, observerErr) {
		t.Fatalf("first compaction error = %v, want observer error", err)
	}
	if err := service.CompactContext(context.Background(), request); !errors.Is(err, observerErr) {
		t.Fatalf("replayed compaction error = %v, want cached observer error", err)
	}
	if client.compactionCalls != 1 {
		t.Fatalf("compaction call count = %d, want 1", client.compactionCalls)
	}
	if got := countEventsByKind(t, store, "history_replaced"); got != 1 {
		t.Fatalf("history_replaced event count = %d, want 1", got)
	}
	snapshot := runtimeControlFeedSnapshot(t, operations, store.Meta().SessionID, []clientui.RuntimeOperationRef{ref})
	if len(snapshot.Operations) != 1 || snapshot.Operations[0].State != clientui.RuntimeInputReconciliationCommitted {
		t.Fatalf("compaction reconciliation = %+v, want committed", snapshot)
	}
}

func TestServiceSubmitUserTurnRunsParentOwnedPreSubmitCompaction(t *testing.T) {
	store, engine, client, service := newRuntimeControlCompactionFixture(t)
	if shouldCompact, compactErr := engine.ShouldCompactBeforeUserMessage(context.Background(), "after compaction"); !shouldCompact || compactErr != nil {
		t.Fatalf("pre-submit compaction precondition = (%t, %v), usage=%+v", shouldCompact, compactErr, engine.ContextUsage())
	}
	resp, err := service.SubmitUserTurn(context.Background(), runtimeControlUserTurnRequest(store, "parent-compaction", "after compaction"))
	if err != nil || !resp.Compacted || resp.Message != "done" {
		t.Fatalf("SubmitUserTurn = (%+v, %v), usage=%+v, want compacted assistant response", resp, err, engine.ContextUsage())
	}
	if client.compactionCalls != 1 ||
		countEventsByKind(t, store, "history_replaced") != 1 ||
		countUserMessagesWithContent(t, store, "after compaction") != 1 {
		t.Fatalf("pre-submit compaction calls/events/submits = %d/%d/%d, want 1/1/1",
			client.compactionCalls,
			countEventsByKind(t, store, "history_replaced"),
			countUserMessagesWithContent(t, store, "after compaction"))
	}
}

func newRuntimeControlCompactionFixture(t *testing.T, options ...session.StoreOption) (*session.Store, *runtime.Engine, *runtimeControlFakeClient, *Service) {
	t.Helper()
	trimmed := 1
	client := &runtimeControlFakeClient{
		responses: []llm.Response{
			{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("seeded"), Phase: textutil.Value(llm.MessagePhaseFinal)}, Usage: llm.Usage{InputTokens: 330000, WindowTokens: 372000}},
			{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)}, Usage: llm.Usage{WindowTokens: 1000}},
		},
		compactionResponses: []llm.CompactionResponse{{
			OutputItems: []llm.ResponseItem{
				{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleUser), MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("summary")},
				{Type: llm.ResponseItemTypeCompaction, EncryptedContent: textutil.Value("checkpoint")},
			},
			Usage:             llm.Usage{WindowTokens: 200000},
			TrimmedItemsCount: &trimmed,
		}},
	}
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{
		Model:                        "gpt-5",
		ProviderCapabilitiesOverride: &runtimeControlOpenAICapabilities,
	}, options...)
	if _, err := engine.SubmitUserMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("seed runtime transcript: %v", err)
	}
	return store, engine, client, service
}

func countEventsByKind(t *testing.T, store *session.Store, kind string) int {
	t.Helper()
	events, err := sessiontest.CollectRecords(store)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	count := 0
	for _, evt := range events {
		eventKind, kindErr := evt.Kind()
		if kindErr != nil {
			t.Fatalf("event kind: %v", kindErr)
		}
		if string(eventKind) == kind {
			count++
		}
	}
	return count
}

func localEntryEvents(t *testing.T, store *session.Store) []runtime.ChatEntry {
	t.Helper()
	events, err := sessiontest.CollectRecords(store)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	entries := make([]runtime.ChatEntry, 0)
	for _, evt := range events {
		kind, kindErr := evt.Kind()
		if kindErr != nil {
			t.Fatalf("event kind: %v", kindErr)
		}
		if kind != session.EventKindLocalEntry {
			continue
		}
		payload, payloadErr := evt.Payload()
		if payloadErr != nil {
			t.Fatalf("local_entry payload: %v", payloadErr)
		}
		entryRecord, ok := payload.(session.LocalEntryRecord)
		if !ok {
			t.Fatalf("local_entry payload = %T, want session.LocalEntryRecord", payload)
		}
		text, _ := textutil.OptionalValue(entryRecord.Text)
		entries = append(entries, runtime.ChatEntry{
			Role: entryRecord.Role,
			Text: text,
			Visibility: transcript.NormalizeEntryVisibility(
				transcript.EntryVisibility(entryRecord.Visibility),
			),
		})
	}
	return entries
}

func TestServiceAppendCommittedEntryReplaysVisibility(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{})
	req := serverapi.RuntimeAppendCommittedEntryRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID, Role: "warning", Text: "visible warning", Visibility: string(transcript.EntryVisibilityOngoing)}

	if err := service.AppendCommittedEntry(context.Background(), req); err != nil {
		t.Fatalf("AppendCommittedEntry first: %v", err)
	}
	if err := service.AppendCommittedEntry(context.Background(), req); err != nil {
		t.Fatalf("AppendCommittedEntry replay: %v", err)
	}
	count := 0
	for _, entry := range localEntryEvents(t, store) {
		if entry.Role == "warning" && entry.Text == "visible warning" {
			count++
			if entry.Visibility != transcript.EntryVisibilityOngoing {
				t.Fatalf("entry visibility = %q, want ongoing", entry.Visibility)
			}
		}
	}
	if count != 1 {
		t.Fatalf("visible warning entry count = %d, want 1", count)
	}
}

func TestServiceDiscardQueuedUserMessageDedupesSuccessfulRetry(t *testing.T) {
	store, engine, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{})
	firstQueued := mustQueueRuntimeControlMessage(t, engine, "same")
	otherQueued := mustQueueRuntimeControlMessage(t, engine, "other")
	duplicateQueued := mustQueueRuntimeControlMessage(t, engine, "same")
	req := serverapi.RuntimeDiscardQueuedUserMessageRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID, QueueItemID: duplicateQueued.ID}

	first, err := service.DiscardQueuedUserMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("DiscardQueuedUserMessage first: %v", err)
	}
	second, err := service.DiscardQueuedUserMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("DiscardQueuedUserMessage replay: %v", err)
	}
	if !first.Discarded || !second.Discarded {
		t.Fatalf("discard results = (%t, %t), want both true", first.Discarded, second.Discarded)
	}
	if !engine.DiscardQueuedUserMessage(firstQueued.ID) {
		t.Fatal("expected first duplicate text item to remain")
	}
	if !engine.DiscardQueuedUserMessage(otherQueued.ID) {
		t.Fatal("expected other queued item to remain")
	}
	if engine.DiscardQueuedUserMessage(duplicateQueued.ID) {
		t.Fatal("did not expect discarded queue item to remain")
	}
}

func TestServiceRecordPromptHistoryDedupesSuccessfulRetry(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{})
	history := newRuntimeControlPromptHistoryStore(store.Meta().SessionID)
	service.WithPromptHistoryStore(history)
	req := serverapi.RuntimeRecordPromptHistoryRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID, Text: "/resume"}

	if err := service.RecordPromptHistory(context.Background(), req); err != nil {
		t.Fatalf("RecordPromptHistory first: %v", err)
	}
	if err := service.RecordPromptHistory(context.Background(), req); err != nil {
		t.Fatalf("RecordPromptHistory replay: %v", err)
	}
	if got := countPromptHistoryEvents(t, store, "/resume"); got != 1 {
		t.Fatalf("prompt history count = %d, want 1", got)
	}
}

func runtimeControlOperationRef(kind clientui.RuntimeOperationKind) clientui.RuntimeOperationRef {
	return clientui.RuntimeOperationRef{
		Kind:            kind,
		ClientRequestID: runtimeids.NewRuntimeClientRequestID(),
	}
}

func runtimeControlFeedSnapshot(t *testing.T, operations *runtimeops.Coordinator, sessionID string, refs []clientui.RuntimeOperationRef) clientui.RuntimeInputReconciliationSnapshot {
	t.Helper()
	snapshot, err := operations.FeedSnapshot(sessionID, refs)
	if err != nil {
		t.Fatalf("FeedSnapshot: %v", err)
	}
	return snapshot
}

func mustRuntimeControlRunID(t *testing.T) runtimeids.RunID {
	t.Helper()
	id, err := runtimeids.ParseRunID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("ParseRunID: %v", err)
	}
	return id
}

func mustRuntimeControlStepID(t *testing.T) runtimeids.StepID {
	t.Helper()
	id, err := runtimeids.ParseStepID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("ParseStepID: %v", err)
	}
	return id
}

func mustRuntimeControlQueueItemID(t *testing.T, raw string) runtimeids.QueueItemID {
	t.Helper()
	id, err := runtimeids.ParseQueueItemID(raw)
	if err != nil {
		t.Fatalf("ParseQueueItemID: %v", err)
	}
	return id
}
