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

func TestOperationOwnerShutdownRejectsCancelsAndJoinsInFlightOperation(t *testing.T) {
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
		return scope.FinalizeAttachment(func(ctx context.Context) error {
			close(finalizing)
			<-ctx.Done()
			return context.Cause(ctx)
		})
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

type lifecycleContextRecorder struct {
	operation context.Context
	caller    context.Context
	marker    *struct{}
	stages    map[string]struct{}
}

type lifecycleContextKey struct{}

func (r *lifecycleContextRecorder) operationStage(t *testing.T, stage string, ctx context.Context) {
	t.Helper()
	if ctx == r.caller {
		t.Fatalf("%s remained owned by the caller", stage)
	}
	if ctx.Value(lifecycleContextKey{}) != r.marker {
		t.Fatalf("%s lost the operation context values", stage)
	}
	if r.operation == nil {
		r.operation = ctx
	} else if ctx != r.operation {
		t.Fatalf("%s received a different operation context", stage)
	}
	if r.stages == nil {
		r.stages = make(map[string]struct{})
	}
	r.stages[stage] = struct{}{}
}

func (r *lifecycleContextRecorder) finalizationStage(t *testing.T, ctx context.Context) {
	t.Helper()
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("attachment finalization was not bounded")
	}
	if err := context.Cause(ctx); err != nil {
		t.Fatalf("attachment finalization inherited cancellation: %v", err)
	}
	if ctx.Value(lifecycleContextKey{}) != r.marker {
		t.Fatal("attachment finalization lost the operation context values")
	}
	if r.stages == nil {
		r.stages = make(map[string]struct{})
	}
	r.stages["attachment finalization"] = struct{}{}
}

type lifecycleSessionCreation struct {
	t         *testing.T
	recorder  *lifecycleContextRecorder
	sessionID runtimeids.SessionID
}

func (s lifecycleSessionCreation) PlanSession(
	ctx context.Context,
	_ *sessionlaunchpb.SessionPlanRequest,
) (*sessionlaunchpb.SessionPlanSuccess, error) {
	s.recorder.operationStage(s.t, "Session creation", ctx)
	return &sessionlaunchpb.SessionPlanSuccess{
		Plan: &sessionlaunchpb.SessionPlan{SessionId: s.sessionID.String()},
	}, nil
}

type lifecycleRuntimePlanner struct {
	t          *testing.T
	recorder   *lifecycleContextRecorder
	attachment RuntimeAttachment
}

func (p lifecycleRuntimePlanner) Open(
	ctx context.Context,
	_ runtimeids.SessionID,
) (RuntimeAttachment, error) {
	p.recorder.operationStage(p.t, "Runtime opening", ctx)
	return p.attachment, nil
}

type lifecycleAdmission struct {
	t           *testing.T
	recorder    *lifecycleContextRecorder
	queueItemID runtimeids.QueueItemID
}

func (a lifecycleAdmission) AdmitChatUserTurn(
	ctx context.Context,
	_ serverapi.RuntimeSubmitUserTurnRequest,
) (serverapi.ChatInputAdmissionResult, error) {
	a.recorder.operationStage(a.t, "admission", ctx)
	return serverapi.ChatInputAdmissionResult{
		QueueItemID: a.queueItemID,
		Accepted:    true,
	}, nil
}

func (lifecycleAdmission) AdmitChatQueuedUserInput(
	context.Context,
	serverapi.RuntimeSubmitUserTurnRequest,
) (serverapi.ChatInputAdmissionResult, error) {
	panic("unexpected Queue admission")
}

func (lifecycleAdmission) AdmitManualCompaction(
	context.Context,
	serverapi.RuntimeCompactContextRequest,
) (bool, error) {
	panic("unexpected compaction admission")
}

type lifecycleAttachment struct {
	t         *testing.T
	recorder  *lifecycleContextRecorder
	sessionID runtimeids.SessionID
}

func (a lifecycleAttachment) SessionID() runtimeids.SessionID {
	return a.sessionID
}

func (a lifecycleAttachment) Release(
	ctx context.Context,
	policy sessionruntime.RuntimeReleasePolicy,
) error {
	if policy != sessionruntime.RuntimeReleaseDetach {
		a.t.Fatalf("release policy = %v, want detach", policy)
	}
	a.recorder.finalizationStage(a.t, ctx)
	return nil
}

func TestServiceCarriesOneOperationContextThroughFiveStageLifecycle(t *testing.T) {
	owner := newTestOperationOwner()
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Errorf("close operation owner: %v", err)
		}
	})
	sessionID := runtimeids.NewSessionID()
	queueItemID := runtimeids.NewQueueItemID()
	marker := &struct{}{}
	callerCtx := context.WithValue(t.Context(), lifecycleContextKey{}, marker)
	recorder := &lifecycleContextRecorder{caller: callerCtx, marker: marker}
	creation := lifecycleSessionCreation{
		t:         t,
		recorder:  recorder,
		sessionID: sessionID,
	}
	targets := NewTargetResolver(
		nil,
		func(ctx context.Context, _, _ string) (SessionCreationService, error) {
			recorder.operationStage(t, "target resolution", ctx)
			return creation, nil
		},
	)
	attachment := lifecycleAttachment{t: t, recorder: recorder, sessionID: sessionID}
	service := NewService(
		owner,
		targets,
		lifecycleRuntimePlanner{t: t, recorder: recorder, attachment: attachment},
		lifecycleAdmission{t: t, recorder: recorder, queueItemID: queueItemID},
	)

	result, err := service.Steer(callerCtx, &chatpb.SteerRequest{
		Target: &chatpb.ChatTarget{
			Target: &chatpb.ChatTarget_NewChat{NewChat: validSteerNewChatTarget()},
		},
		Activation: &chatpb.Activation{
			Input: &chatpb.Activation_Text{Text: "continue"},
		},
	})
	if err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if result.GetSession().GetSessionId() != sessionID.String() ||
		result.GetAccepted().GetQueueItem().GetId() != queueItemID.String() {
		t.Fatalf("Steer result = %+v", result)
	}
	for _, stage := range []string{
		"target resolution",
		"Session creation",
		"Runtime opening",
		"admission",
		"attachment finalization",
	} {
		if _, observed := recorder.stages[stage]; !observed {
			t.Errorf("%s did not receive the operation context", stage)
		}
	}
}
