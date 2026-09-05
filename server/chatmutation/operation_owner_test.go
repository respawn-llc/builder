package chatmutation

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/session"
	"core/server/sessionruntime"
	chatpb "core/shared/protoapi/gen/kent/api/chat"
	chatsettingspb "core/shared/protoapi/gen/kent/api/chat_settings"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	"core/shared/runtimeids"
	"core/shared/serverapi"

	"google.golang.org/protobuf/proto"
)

func TestOperationOwnerShutdownRejectsCancelsAndJoins(t *testing.T) {
	owner := newTestOperationOwner()
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
	if _, err := owner.Start(t.Context(), func(OperationScope) error { return nil }); !errors.Is(err, ErrOperationOwnerClosed) {
		t.Fatalf("Start after shutdown error = %v, want closed owner", err)
	}
	close(allowExit)
	if err := <-closed; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := operation.Await(t.Context()); !errors.Is(err, ErrOperationOwnerClosed) {
		t.Fatalf("operation result = %v, want shutdown cancellation", err)
	}
}

func TestOperationOwnerBoundsAttachmentFinalization(t *testing.T) {
	const timeout = 20 * time.Millisecond
	owner, err := NewOperationOwner(timeout)
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
	if err := owner.Close(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close error = %v, want bounded finalization timeout", err)
	}
	if err := operation.Await(t.Context()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("operation result = %v, want deadline exceeded", err)
	}
}

type lifecycleContextKey struct{}

type lifecycleRecorder struct {
	t         *testing.T
	caller    context.Context
	marker    *struct{}
	operation context.Context
	stages    map[string]struct{}
}

func (r *lifecycleRecorder) operationStage(stage string, ctx context.Context) {
	r.t.Helper()
	if ctx == r.caller || ctx.Value(lifecycleContextKey{}) != r.marker {
		r.t.Fatalf("%s did not receive the server-owned operation context", stage)
	}
	if r.operation == nil {
		r.operation = ctx
	} else if ctx != r.operation {
		r.t.Fatalf("%s received a different operation context", stage)
	}
	r.stages[stage] = struct{}{}
}

func (r *lifecycleRecorder) finalizationStage(ctx context.Context) {
	r.t.Helper()
	if _, bounded := ctx.Deadline(); !bounded ||
		context.Cause(ctx) != nil ||
		ctx.Value(lifecycleContextKey{}) != r.marker {
		r.t.Fatal("attachment finalization did not receive the bounded operation context")
	}
	r.stages["attachment finalization"] = struct{}{}
}

type lifecyclePersistedSessions struct {
	recorder  *lifecycleRecorder
	sessionID runtimeids.SessionID
}

func (s lifecyclePersistedSessions) ResolvePersistedSession(
	ctx context.Context,
	requestedSessionID string,
) (session.PersistedSessionRecord, error) {
	s.recorder.operationStage("target resolution", ctx)
	if requestedSessionID != s.sessionID.String() {
		s.recorder.t.Fatalf(
			"resolved Session ID = %q, want %q",
			requestedSessionID,
			s.sessionID,
		)
	}
	return session.PersistedSessionRecord{
		SessionDir: "/tmp/" + s.sessionID.String(),
		Meta:       &session.Meta{SessionID: s.sessionID.String()},
	}, nil
}

type lifecycleCreation struct {
	recorder         *lifecycleRecorder
	sessionID        runtimeids.SessionID
	expectedSettings *chatsettingspb.InitialChatSettings
	expectedDraft    string
}

func (s lifecycleCreation) PlanSession(
	ctx context.Context,
	request *sessionlaunchpb.SessionPlanRequest,
) (*sessionlaunchpb.SessionPlanSuccess, error) {
	s.recorder.operationStage("Session creation", ctx)
	if !proto.Equal(request.InitialChatSettings, s.expectedSettings) ||
		request.InitialInputDraft == nil ||
		*request.InitialInputDraft != s.expectedDraft {
		s.recorder.t.Fatalf(
			"initial Chat creation = settings:%+v draft:%v",
			request.InitialChatSettings,
			request.InitialInputDraft,
		)
	}
	return &sessionlaunchpb.SessionPlanSuccess{
		Plan: &sessionlaunchpb.SessionPlan{SessionId: s.sessionID.String()},
	}, nil
}

type lifecyclePlanner struct {
	recorder   *lifecycleRecorder
	attachment RuntimeAttachment
}

func (p lifecyclePlanner) Open(
	ctx context.Context,
	_ runtimeids.SessionID,
) (RuntimeAttachment, error) {
	p.recorder.operationStage("Runtime opening", ctx)
	return p.attachment, nil
}

type lifecycleAdmission struct {
	recorder *lifecycleRecorder
	result   serverapi.ChatInputAdmissionResult
	err      error
}

func (a lifecycleAdmission) AdmitChatUserTurn(
	ctx context.Context,
	_ serverapi.RuntimeSubmitUserTurnRequest,
) (serverapi.ChatInputAdmissionResult, error) {
	a.recorder.operationStage("admission", ctx)
	return a.result, a.err
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
	recorder  *lifecycleRecorder
	sessionID runtimeids.SessionID
	policy    sessionruntime.RuntimeReleasePolicy
}

func (a *lifecycleAttachment) SessionID() runtimeids.SessionID {
	return a.sessionID
}

func (a *lifecycleAttachment) Release(
	ctx context.Context,
	policy sessionruntime.RuntimeReleasePolicy,
) error {
	a.policy = policy
	a.recorder.finalizationStage(ctx)
	return nil
}

func TestServiceCarriesOneOperationContextAcrossChatLifecycle(t *testing.T) {
	tests := []struct {
		name         string
		newChat      bool
		admission    serverapi.ChatInputAdmissionResult
		admissionErr error
		wantPolicy   sessionruntime.RuntimeReleasePolicy
		wantStages   []string
	}{
		{
			name:    "New Chat accepted with typed prompt-history diagnostic",
			newChat: true,
			admission: serverapi.ChatInputAdmissionResult{
				QueueItemID:          runtimeids.NewQueueItemID(),
				Accepted:             true,
				PromptHistoryFailure: errors.New("prompt history unavailable"),
			},
			wantPolicy: sessionruntime.RuntimeReleaseDetach,
			wantStages: []string{
				"target resolution",
				"Session creation",
				"Runtime opening",
				"admission",
				"attachment finalization",
			},
		},
		{
			name:         "existing Session rejected before acceptance",
			admissionErr: &serverapi.PendingWorkCapacityError{},
			wantPolicy:   sessionruntime.RuntimeReleaseCloseIfIdle,
			wantStages: []string{
				"target resolution",
				"Runtime opening",
				"admission",
				"attachment finalization",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			owner := newTestOperationOwner()
			t.Cleanup(func() { _ = owner.Close() })
			sessionID := runtimeids.NewSessionID()
			marker := &struct{}{}
			caller := context.WithValue(t.Context(), lifecycleContextKey{}, marker)
			recorder := &lifecycleRecorder{
				t:      t,
				caller: caller,
				marker: marker,
				stages: make(map[string]struct{}),
			}
			newChatTarget := validNewChatTarget()
			targets := NewTargetResolver(
				lifecyclePersistedSessions{recorder: recorder, sessionID: sessionID},
				func(ctx context.Context, projectID, workspaceID string) (SessionCreationService, error) {
					recorder.operationStage("target resolution", ctx)
					if projectID != newChatTarget.ProjectId ||
						workspaceID != newChatTarget.WorkspaceId {
						t.Fatalf(
							"Session creation target = %q/%q, want %q/%q",
							projectID,
							workspaceID,
							newChatTarget.ProjectId,
							newChatTarget.WorkspaceId,
						)
					}
					return lifecycleCreation{
						recorder:         recorder,
						sessionID:        sessionID,
						expectedSettings: newChatTarget.InitialSettings,
						expectedDraft:    "continue",
					}, nil
				},
			)
			attachment := &lifecycleAttachment{recorder: recorder, sessionID: sessionID}
			service := NewService(
				owner,
				targets,
				lifecyclePlanner{recorder: recorder, attachment: attachment},
				lifecycleAdmission{
					recorder: recorder,
					result:   test.admission,
					err:      test.admissionErr,
				},
			)
			target := &chatpb.ChatTarget{
				Target: &chatpb.ChatTarget_Session{
					Session: &chatpb.ExistingSessionTarget{SessionId: sessionID.String()},
				},
			}
			if test.newChat {
				target.Target = &chatpb.ChatTarget_NewChat{NewChat: newChatTarget}
			}

			result, err := service.Steer(caller, &chatpb.SteerRequest{
				Target: target,
				Activation: &chatpb.Activation{
					Input: &chatpb.Activation_Text{Text: "continue"},
				},
			})
			if err != nil {
				t.Fatalf("Steer: %v", err)
			}
			if result.GetSession().GetSessionId() != sessionID.String() {
				t.Fatalf("Steer Session = %+v, want %s", result.GetSession(), sessionID)
			}
			if test.admission.Accepted {
				if result.GetAccepted().GetDiagnostic().GetPromptHistoryFailure() == nil {
					t.Fatalf("Steer result = %+v, want typed prompt-history diagnostic", result)
				}
			} else if result.GetNotAccepted().GetPendingWorkCapacity() == nil {
				t.Fatalf("Steer result = %+v, want capacity rejection", result)
			}
			if attachment.policy != test.wantPolicy {
				t.Fatalf("release policy = %v, want %v", attachment.policy, test.wantPolicy)
			}
			for _, stage := range test.wantStages {
				if _, observed := recorder.stages[stage]; !observed {
					t.Errorf("%s did not receive the operation context", stage)
				}
			}
		})
	}
}

func newTestOperationOwner() *OperationOwner {
	owner, err := NewOperationOwner(time.Second)
	if err != nil {
		panic(err)
	}
	return owner
}

func validNewChatTarget() *chatpb.NewChatTarget {
	questions, autoCompaction := true, true
	return &chatpb.NewChatTarget{
		ProjectId:   "project-1",
		WorkspaceId: "workspace-1",
		InitialSettings: &chatsettingspb.InitialChatSettings{
			AgentRole:             "default",
			Supervisor:            chatsettingspb.SupervisorValue_SUPERVISOR_VALUE_OFF,
			QuestionsEnabled:      &questions,
			AutoCompactionEnabled: &autoCompaction,
		},
	}
}
