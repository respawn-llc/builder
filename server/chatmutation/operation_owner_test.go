package chatmutation

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/sessionruntime"
	chatpb "core/shared/protoapi/gen/kent/api/chat"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestOperationOwnerCallerCancellationStopsWaitingWithoutCancelingOperation(t *testing.T) {
	owner, err := NewOperationOwner(time.Second)
	if err != nil {
		t.Fatalf("NewOperationOwner: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })

	started := make(chan struct{})
	finish := make(chan struct{})
	completed := make(chan struct{})
	callerCtx, cancelCaller := context.WithCancel(t.Context())
	operation, err := owner.Start(callerCtx, func(scope OperationScope) error {
		close(started)
		select {
		case <-finish:
			close(completed)
			return nil
		case <-scope.Context().Done():
			return context.Cause(scope.Context())
		}
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-started
	cancelCaller()

	if err := operation.Await(callerCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Await error = %v, want caller cancellation", err)
	}
	select {
	case <-completed:
		t.Fatal("caller cancellation completed the server-owned operation")
	default:
	}

	close(finish)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := operation.Await(ctx); err != nil {
		t.Fatalf("Await completed operation: %v", err)
	}
}

func TestOperationOwnerShutdownRejectsStartsAndCancelsAndJoinsInFlightOperation(t *testing.T) {
	owner, err := NewOperationOwner(time.Second)
	if err != nil {
		t.Fatalf("NewOperationOwner: %v", err)
	}
	started := make(chan struct{})
	canceled := make(chan error, 1)
	allowExit := make(chan struct{})
	operation, err := owner.Start(t.Context(), func(scope OperationScope) error {
		close(started)
		<-scope.Context().Done()
		canceled <- context.Cause(scope.Context())
		<-allowExit
		return context.Cause(scope.Context())
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-started

	closed := make(chan error, 1)
	go func() { closed <- owner.Close() }()
	if err := <-canceled; !errors.Is(err, ErrOperationOwnerClosed) {
		t.Fatalf("operation cancellation = %v, want owner shutdown", err)
	}
	select {
	case err := <-closed:
		t.Fatalf("Close returned before operation joined: %v", err)
	default:
	}
	if _, err := owner.Start(t.Context(), func(OperationScope) error {
		return nil
	}); !errors.Is(err, ErrOperationOwnerClosed) {
		t.Fatalf("Start after shutdown error = %v, want closed owner", err)
	}

	close(allowExit)
	if err := <-closed; err != nil {
		t.Fatalf("Close: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := operation.Await(ctx); !errors.Is(err, ErrOperationOwnerClosed) {
		t.Fatalf("operation result = %v, want shutdown cancellation", err)
	}
}

func TestOperationOwnerBoundsAttachmentFinalizationDuringShutdown(t *testing.T) {
	const finalizationTimeout = 20 * time.Millisecond
	owner, err := NewOperationOwner(finalizationTimeout)
	if err != nil {
		t.Fatalf("NewOperationOwner: %v", err)
	}
	finalizing := make(chan struct{})
	operation, err := owner.Start(t.Context(), func(scope OperationScope) error {
		finalizationErr := scope.FinalizeAttachment(func(ctx context.Context) error {
			close(finalizing)
			<-ctx.Done()
			return context.Cause(ctx)
		})
		return finalizationErr
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-finalizing

	startedAt := time.Now()
	closeErr := owner.Close()
	if !errors.Is(closeErr, context.DeadlineExceeded) {
		t.Fatalf("Close error = %v, want bounded finalization timeout", closeErr)
	}
	if elapsed := time.Since(startedAt); elapsed < finalizationTimeout {
		t.Fatalf("Close elapsed = %v, want at least %v", elapsed, finalizationTimeout)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := operation.Await(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("operation finalization error = %v, want deadline exceeded", err)
	}
}

func TestServiceRejectsEveryChatMutationAfterOperationOwnerShutdown(t *testing.T) {
	owner, err := NewOperationOwner(time.Second)
	if err != nil {
		t.Fatalf("NewOperationOwner: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	sessionID := runtimeids.NewSessionID()
	targets := &steerTargetResolver{}
	service := NewService(owner, targets, &steerRuntimePlanner{}, &steerAdmission{})

	requests := []struct {
		name string
		run  func() error
	}{
		{
			name: "Steer",
			run: func() error {
				_, callErr := service.Steer(t.Context(), validOperationSteerRequest(sessionID))
				return callErr
			},
		},
		{
			name: "Queue",
			run: func() error {
				_, callErr := service.Queue(t.Context(), &chatpb.QueueRequest{
					Target:     validOperationSteerRequest(sessionID).Target,
					Activation: validOperationSteerRequest(sessionID).Activation,
				})
				return callErr
			},
		},
		{
			name: "Compact",
			run: func() error {
				_, callErr := service.Compact(t.Context(), &chatpb.CompactRequest{
					Target:     validOperationSteerRequest(sessionID).Target,
					Invocation: &chatpb.CompactionInvocation{Token: "/compact"},
				})
				return callErr
			},
		},
	}
	for _, request := range requests {
		t.Run(request.name, func(t *testing.T) {
			if err := request.run(); !errors.Is(err, ErrOperationOwnerClosed) {
				t.Fatalf("%s error = %v, want owner shutdown rejection", request.name, err)
			}
		})
	}
	if targets.calls != 0 {
		t.Fatalf("target resolution calls = %d, want no starts after shutdown", targets.calls)
	}
}

type coordinatorLifecyclePhase string

const (
	coordinatorTargetResolution coordinatorLifecyclePhase = "target resolution"
	coordinatorSessionCreation  coordinatorLifecyclePhase = "Session creation"
	coordinatorRuntimeOpening   coordinatorLifecyclePhase = "Runtime preparation"
	coordinatorAdmission        coordinatorLifecyclePhase = "admission"
	coordinatorFinalization     coordinatorLifecyclePhase = "attachment finalization"
)

type coordinatorPhaseGate struct {
	entered chan context.Context
	proceed chan struct{}
}

func newCoordinatorPhaseGate() *coordinatorPhaseGate {
	return &coordinatorPhaseGate{
		entered: make(chan context.Context, 1),
		proceed: make(chan struct{}),
	}
}

func (g *coordinatorPhaseGate) block(ctx context.Context) error {
	g.entered <- ctx
	select {
	case <-g.proceed:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

type coordinatorPhaseResolver struct {
	phase  coordinatorLifecyclePhase
	gate   *coordinatorPhaseGate
	target ResolvedTarget
}

func (r *coordinatorPhaseResolver) Resolve(
	ctx context.Context,
	_ TargetResolutionRequest,
) (ResolvedTarget, error) {
	if r.phase == coordinatorTargetResolution || r.phase == coordinatorSessionCreation {
		if err := r.gate.block(ctx); err != nil {
			return ResolvedTarget{}, err
		}
	}
	return r.target, nil
}

type coordinatorSessionCreationService struct {
	gate      *coordinatorPhaseGate
	sessionID runtimeids.SessionID
}

func (s *coordinatorSessionCreationService) PlanSession(
	ctx context.Context,
	_ *sessionlaunchpb.SessionPlanRequest,
) (*sessionlaunchpb.SessionPlanSuccess, error) {
	if err := s.gate.block(ctx); err != nil {
		return nil, err
	}
	return &sessionlaunchpb.SessionPlanSuccess{
		Plan: &sessionlaunchpb.SessionPlan{SessionId: s.sessionID.String()},
	}, nil
}

type coordinatorPhasePlanner struct {
	phase      coordinatorLifecyclePhase
	gate       *coordinatorPhaseGate
	attachment RuntimeAttachment
}

func (p *coordinatorPhasePlanner) Open(
	ctx context.Context,
	_ runtimeids.SessionID,
) (RuntimeAttachment, error) {
	if p.phase == coordinatorRuntimeOpening {
		if err := p.gate.block(ctx); err != nil {
			return nil, err
		}
	}
	return p.attachment, nil
}

type coordinatorPhaseAdmission struct {
	phase       coordinatorLifecyclePhase
	gate        *coordinatorPhaseGate
	queueItemID runtimeids.QueueItemID
}

func (a *coordinatorPhaseAdmission) AdmitUserTurn(
	ctx context.Context,
	_ serverapi.RuntimeSubmitUserTurnRequest,
) (serverapi.RuntimeSubmitUserTurnResponse, bool, error) {
	if a.phase == coordinatorAdmission {
		if err := a.gate.block(ctx); err != nil {
			return serverapi.RuntimeSubmitUserTurnResponse{}, false, err
		}
	}
	return serverapi.RuntimeSubmitUserTurnResponse{
		ResultKind:  "queued",
		Steered:     true,
		QueueItemID: a.queueItemID.String(),
	}, true, nil
}

func (*coordinatorPhaseAdmission) AdmitQueuedUserInput(
	context.Context,
	serverapi.RuntimeSubmitUserTurnRequest,
) (runtimeids.QueueItemID, bool, error) {
	panic("unexpected Queue admission")
}

func (*coordinatorPhaseAdmission) AdmitManualCompaction(
	context.Context,
	serverapi.RuntimeCompactContextRequest,
) (bool, error) {
	panic("unexpected compaction admission")
}

type coordinatorPhaseAttachment struct {
	phase     coordinatorLifecyclePhase
	gate      *coordinatorPhaseGate
	sessionID runtimeids.SessionID
	released  chan sessionruntime.RuntimeReleasePolicy
}

func (a *coordinatorPhaseAttachment) SessionID() runtimeids.SessionID {
	return a.sessionID
}

func (a *coordinatorPhaseAttachment) Release(
	ctx context.Context,
	policy sessionruntime.RuntimeReleasePolicy,
) error {
	a.released <- policy
	if a.phase == coordinatorFinalization {
		return a.gate.block(ctx)
	}
	return nil
}

func TestServiceCallerDisconnectDoesNotCancelAnyChatOperationPhase(t *testing.T) {
	phases := []coordinatorLifecyclePhase{
		coordinatorTargetResolution,
		coordinatorSessionCreation,
		coordinatorRuntimeOpening,
		coordinatorAdmission,
		coordinatorFinalization,
	}
	for _, phase := range phases {
		t.Run(string(phase), func(t *testing.T) {
			owner, service, request, gate, released := newCoordinatorPhaseFixture(t, phase)
			callerCtx, cancelCaller := context.WithCancel(t.Context())
			callResult := make(chan error, 1)
			go func() {
				_, err := service.Steer(callerCtx, request)
				callResult <- err
			}()

			phaseCtx := <-gate.entered
			cancelCaller()
			if err := <-callResult; !errors.Is(err, context.Canceled) {
				t.Fatalf("Steer error = %v, want caller cancellation", err)
			}
			if err := context.Cause(phaseCtx); err != nil {
				t.Fatalf("%s context canceled by caller disconnect: %v", phase, err)
			}

			close(gate.proceed)
			select {
			case policy := <-released:
				if policy != sessionruntime.RuntimeReleaseDetach {
					t.Fatalf("release policy = %v, want detach", policy)
				}
			case <-time.After(time.Second):
				t.Fatalf("%s did not continue through attachment finalization", phase)
			}
			if err := owner.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
		})
	}
}

func TestServiceDefiniteNonAcceptanceAfterCallerDisconnectClosesIdleRuntime(t *testing.T) {
	owner, err := NewOperationOwner(time.Second)
	if err != nil {
		t.Fatalf("NewOperationOwner: %v", err)
	}
	sessionID := runtimeids.NewSessionID()
	admissionStarted := make(chan struct{})
	proceed := make(chan struct{})
	released := make(chan sessionruntime.RuntimeReleasePolicy, 1)
	service := NewService(
		owner,
		&steerTargetResolver{resolved: ResolvedTarget{SessionID: sessionID}},
		&steerRuntimePlanner{attachment: &coordinatorPhaseAttachment{
			sessionID: sessionID,
			released:  released,
		}},
		&steerAdmission{
			err: &serverapi.PendingWorkCapacityError{},
			onAdmit: func() {
				close(admissionStarted)
				<-proceed
			},
		},
	)
	callerCtx, cancelCaller := context.WithCancel(t.Context())
	callResult := make(chan error, 1)
	go func() {
		_, callErr := service.Steer(callerCtx, validOperationSteerRequest(sessionID))
		callResult <- callErr
	}()
	<-admissionStarted
	cancelCaller()
	if err := <-callResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("Steer error = %v, want caller cancellation", err)
	}

	close(proceed)
	select {
	case policy := <-released:
		if policy != sessionruntime.RuntimeReleaseCloseIfIdle {
			t.Fatalf("release policy = %v, want close if idle", policy)
		}
	case <-time.After(time.Second):
		t.Fatal("definite non-acceptance did not finalize its attachment")
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestServiceShutdownCancelsAndJoinsChatOperationAtEveryPhase(t *testing.T) {
	phases := []coordinatorLifecyclePhase{
		coordinatorTargetResolution,
		coordinatorSessionCreation,
		coordinatorRuntimeOpening,
		coordinatorAdmission,
		coordinatorFinalization,
	}
	for _, phase := range phases {
		t.Run(string(phase), func(t *testing.T) {
			owner, service, request, gate, _ := newCoordinatorPhaseFixture(t, phase)
			callResult := make(chan error, 1)
			go func() {
				_, err := service.Steer(t.Context(), request)
				callResult <- err
			}()
			phaseCtx := <-gate.entered
			closed := make(chan error, 1)
			go func() { closed <- owner.Close() }()

			if phase == coordinatorFinalization {
				select {
				case err := <-closed:
					t.Fatalf("Close returned before attachment finalization joined: %v", err)
				default:
				}
				if err := context.Cause(phaseCtx); err != nil {
					t.Fatalf("attachment finalization context canceled before its bound: %v", err)
				}
				close(gate.proceed)
			} else {
				select {
				case <-phaseCtx.Done():
					if err := context.Cause(phaseCtx); !errors.Is(err, ErrOperationOwnerClosed) {
						t.Fatalf("%s cancellation = %v, want owner shutdown", phase, err)
					}
				case <-time.After(time.Second):
					t.Fatalf("%s was not canceled by shutdown", phase)
				}
			}
			if err := <-closed; err != nil {
				t.Fatalf("Close: %v", err)
			}
			select {
			case <-callResult:
			case <-time.After(time.Second):
				t.Fatalf("%s caller did not finish after shutdown joined", phase)
			}
		})
	}
}

func newCoordinatorPhaseFixture(
	t *testing.T,
	phase coordinatorLifecyclePhase,
) (
	*OperationOwner,
	*Service,
	*chatpb.SteerRequest,
	*coordinatorPhaseGate,
	<-chan sessionruntime.RuntimeReleasePolicy,
) {
	t.Helper()
	owner, err := NewOperationOwner(time.Second)
	if err != nil {
		t.Fatalf("NewOperationOwner: %v", err)
	}
	sessionID := runtimeids.NewSessionID()
	gate := newCoordinatorPhaseGate()
	released := make(chan sessionruntime.RuntimeReleasePolicy, 1)
	attachment := &coordinatorPhaseAttachment{
		phase:     phase,
		gate:      gate,
		sessionID: sessionID,
		released:  released,
	}
	var targets TargetResolutionService = &coordinatorPhaseResolver{
		phase:  phase,
		gate:   gate,
		target: ResolvedTarget{SessionID: sessionID},
	}
	if phase == coordinatorSessionCreation {
		targets = NewTargetResolver(
			nil,
			func(
				context.Context,
				string,
				string,
			) (SessionCreationService, error) {
				return &coordinatorSessionCreationService{
					gate:      gate,
					sessionID: sessionID,
				}, nil
			},
		)
	}
	service := NewService(
		owner,
		targets,
		&coordinatorPhasePlanner{phase: phase, gate: gate, attachment: attachment},
		&coordinatorPhaseAdmission{
			phase:       phase,
			gate:        gate,
			queueItemID: runtimeids.NewQueueItemID(),
		},
	)
	request := validOperationSteerRequest(sessionID)
	if phase == coordinatorSessionCreation {
		request.Target = &chatpb.ChatTarget{
			Target: &chatpb.ChatTarget_NewChat{NewChat: validSteerNewChatTarget()},
		}
	}
	return owner, service, request, gate, released
}

func validOperationSteerRequest(sessionID runtimeids.SessionID) *chatpb.SteerRequest {
	return &chatpb.SteerRequest{
		Target: &chatpb.ChatTarget{
			Target: &chatpb.ChatTarget_Session{
				Session: &chatpb.ExistingSessionTarget{SessionId: sessionID.String()},
			},
		},
		Activation: &chatpb.Activation{
			Input: &chatpb.Activation_Text{Text: "keep going"},
		},
	}
}
