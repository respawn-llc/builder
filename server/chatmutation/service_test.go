package chatmutation

import (
	"context"
	"errors"
	"testing"

	"core/server/launch"
	"core/server/session"
	"core/server/sessionlaunch"
	"core/server/sessionruntime"
	"core/shared/config"
	chatpb "core/shared/protoapi/gen/kent/api/chat"
	chatsettingspb "core/shared/protoapi/gen/kent/api/chat_settings"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
)

type steerTargetResolver struct {
	resolved ResolvedTarget
	request  TargetResolutionRequest
	err      error
}

func (r *steerTargetResolver) Resolve(
	_ context.Context,
	request TargetResolutionRequest,
) (ResolvedTarget, error) {
	r.request = request
	return r.resolved, r.err
}

type steerRuntimePlanner struct {
	attachment RuntimeAttachment
	sessionID  runtimeids.SessionID
	err        error
}

func (p *steerRuntimePlanner) Open(
	_ context.Context,
	sessionID runtimeids.SessionID,
) (RuntimeAttachment, error) {
	p.sessionID = sessionID
	return p.attachment, p.err
}

type steerRuntimeAttachment struct {
	sessionID         runtimeids.SessionID
	policy            sessionruntime.RuntimeReleasePolicy
	releaseErr        error
	releaseContextErr error
}

func (a *steerRuntimeAttachment) SessionID() runtimeids.SessionID {
	return a.sessionID
}

func (a *steerRuntimeAttachment) Release(
	ctx context.Context,
	policy sessionruntime.RuntimeReleasePolicy,
) error {
	a.policy = policy
	a.releaseContextErr = context.Cause(ctx)
	return a.releaseErr
}

type steerAdmission struct {
	request  serverapi.RuntimeSubmitUserTurnRequest
	result   serverapi.RuntimeSubmitUserTurnResponse
	accepted bool
	err      error
	onAdmit  func()
}

func (a *steerAdmission) AdmitUserTurn(
	_ context.Context,
	request serverapi.RuntimeSubmitUserTurnRequest,
) (serverapi.RuntimeSubmitUserTurnResponse, bool, error) {
	a.request = request
	if a.onAdmit != nil {
		a.onAdmit()
	}
	return a.result, a.accepted, a.err
}

func TestServiceSteerAcceptsExistingSessionInputAndDetachesItsRuntime(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	queueItemID := runtimeids.NewQueueItemID()
	resolver := &steerTargetResolver{resolved: ResolvedTarget{SessionID: sessionID}}
	attachment := &steerRuntimeAttachment{sessionID: sessionID}
	planner := &steerRuntimePlanner{attachment: attachment}
	admission := &steerAdmission{accepted: true, result: serverapi.RuntimeSubmitUserTurnResponse{
		ResultKind:  "queued",
		Steered:     true,
		QueueItemID: queueItemID.String(),
	}}
	service := NewService(resolver, planner, admission)
	target := &chatpb.ChatTarget{
		Target: &chatpb.ChatTarget_Session{
			Session: &chatpb.ExistingSessionTarget{SessionId: sessionID.String()},
		},
	}
	activation := &chatpb.Activation{
		Input: &chatpb.Activation_Text{Text: "  exact Chat input  "},
	}

	result, err := service.Steer(t.Context(), &chatpb.SteerRequest{
		Target:     target,
		Activation: activation,
	})
	if err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if result.GetSession().GetSessionId() != sessionID.String() ||
		result.GetAccepted().GetQueueItem().GetId() != queueItemID.String() {
		t.Fatalf("Steer result = %+v", result)
	}
	if resolver.request.InitialDraft == nil ||
		*resolver.request.InitialDraft != "  exact Chat input  " {
		t.Fatalf("target-resolution draft = %v", resolver.request.InitialDraft)
	}
	if planner.sessionID != sessionID {
		t.Fatalf("Runtime planner Session = %s, want %s", planner.sessionID, sessionID)
	}
	if admission.request.SessionID != sessionID.String() ||
		admission.request.Input.Kind != runtimeinput.KindText ||
		admission.request.Input.Text == nil ||
		*admission.request.Input.Text != "  exact Chat input  " {
		t.Fatalf("Runtime admission request = %+v", admission.request)
	}
	if attachment.policy != sessionruntime.RuntimeReleaseDetach {
		t.Fatalf("Runtime release policy = %v, want detach", attachment.policy)
	}
}

func TestServiceSteerPreparesDormantExistingSessionAndNewChat(t *testing.T) {
	for _, newChat := range []bool{false, true} {
		name := "existing Session"
		if newChat {
			name = "New Chat"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newRuntimePlannerFixture(t)
			creation := &targetResolverSessionLaunch{result: &sessionlaunchpb.SessionPlanSuccess{
				Plan: &sessionlaunchpb.SessionPlan{SessionId: fixture.sessionID.String()},
			}}
			targets := NewTargetResolver(
				targetResolverPersistedSessions{record: session.PersistedSessionRecord{
					SessionDir: "/tmp/" + fixture.sessionID.String(),
					Meta:       &session.Meta{SessionID: fixture.sessionID.String()},
				}},
				func(context.Context, string, string) (SessionCreationService, error) {
					return creation, nil
				},
			)
			persistedPlan := &runtimePlannerSessionLaunch{
				result: sessionlaunch.PlanResult{Plan: launch.SessionPlan{
					Descriptor:     mustRuntimePlannerDescriptor(t, fixture.sessionID),
					ActiveSettings: config.DefaultOnboardingSettings(),
				}},
			}
			runtimeAPI := &runtimePlannerRuntimeAPI{}
			runtimes := NewRuntimePlanner(
				fixture.authority,
				func(context.Context, runtimeids.SessionID) (PersistedSessionPlanner, error) {
					return persistedPlan, nil
				},
				runtimeAPI,
			)
			queueItemID := runtimeids.NewQueueItemID()
			service := NewService(targets, runtimes, &steerAdmission{
				accepted: true,
				result: serverapi.RuntimeSubmitUserTurnResponse{
					ResultKind:  "queued",
					Steered:     true,
					QueueItemID: queueItemID.String(),
				},
			})
			target := &chatpb.ChatTarget{
				Target: &chatpb.ChatTarget_Session{
					Session: &chatpb.ExistingSessionTarget{SessionId: fixture.sessionID.String()},
				},
			}
			if newChat {
				target.Target = &chatpb.ChatTarget_NewChat{NewChat: validSteerNewChatTarget()}
			}

			result, err := service.Steer(t.Context(), &chatpb.SteerRequest{
				Target:     target,
				Activation: &chatpb.Activation{Input: &chatpb.Activation_Text{Text: "ship it"}},
			})
			if err != nil {
				t.Fatalf("Steer: %v", err)
			}
			if result.GetSession().GetSessionId() != fixture.sessionID.String() ||
				result.GetAccepted().GetQueueItem().GetId() != queueItemID.String() {
				t.Fatalf("Steer result = %+v", result)
			}
			if runtimeAPI.activateCalls != 1 ||
				runtimeAPI.activate.SessionID != fixture.sessionID.String() {
				t.Fatalf("Runtime activation = %+v", runtimeAPI.activate)
			}
			if newChat && creation.request == nil {
				t.Fatal("New Chat did not use ordinary Session creation")
			}
			if !newChat && creation.request != nil {
				t.Fatal("existing Session used Session creation")
			}
		})
	}
}

func TestServiceSteerReturnsCreatedSessionWhenPendingWorkCapacityRejectsAdmission(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	resolver := &steerTargetResolver{resolved: ResolvedTarget{SessionID: sessionID, Created: true}}
	attachment := &steerRuntimeAttachment{sessionID: sessionID}
	service := NewService(
		resolver,
		&steerRuntimePlanner{attachment: attachment},
		&steerAdmission{err: &serverapi.PendingWorkCapacityError{}},
	)

	result, err := service.Steer(t.Context(), &chatpb.SteerRequest{
		Target: &chatpb.ChatTarget{
			Target: &chatpb.ChatTarget_NewChat{NewChat: validSteerNewChatTarget()},
		},
		Activation: &chatpb.Activation{Input: &chatpb.Activation_Text{Text: "ship it"}},
	})
	if err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if result.GetSession().GetSessionId() != sessionID.String() ||
		result.GetNotAccepted().GetPendingWorkCapacity() == nil {
		t.Fatalf("Steer result = %+v, want created Session and capacity rejection", result)
	}
	if attachment.policy != sessionruntime.RuntimeReleaseCloseIfIdle {
		t.Fatalf("Runtime release policy = %v, want close if idle", attachment.policy)
	}
}

func TestServiceSteerCreatesNewChatFromExactCommandDraftAndCanonicalInput(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	queueItemID := runtimeids.NewQueueItemID()
	resolver := &steerTargetResolver{resolved: ResolvedTarget{SessionID: sessionID, Created: true}}
	attachment := &steerRuntimeAttachment{sessionID: sessionID}
	admission := &steerAdmission{accepted: true, result: serverapi.RuntimeSubmitUserTurnResponse{
		ResultKind:  "queued",
		Steered:     true,
		QueueItemID: queueItemID.String(),
	}}
	service := NewService(resolver, &steerRuntimePlanner{attachment: attachment}, admission)
	command := &chatpb.CommandInvocation{
		CatalogIdentity:     "prompt:review",
		Token:               "/review",
		SeparatorWhitespace: "\t ",
		Arguments:           "keep  exact\narguments",
	}

	result, err := service.Steer(t.Context(), &chatpb.SteerRequest{
		Target: &chatpb.ChatTarget{
			Target: &chatpb.ChatTarget_NewChat{NewChat: validSteerNewChatTarget()},
		},
		Activation: &chatpb.Activation{
			Input: &chatpb.Activation_Command{Command: command},
		},
	})
	if err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if result.GetSession().GetSessionId() != sessionID.String() ||
		result.GetAccepted().GetQueueItem().GetId() != queueItemID.String() {
		t.Fatalf("Steer result = %+v", result)
	}
	if resolver.request.InitialDraft == nil ||
		*resolver.request.InitialDraft != "/review\t keep  exact\narguments" {
		t.Fatalf("New Chat draft = %v", resolver.request.InitialDraft)
	}
	if admission.request.Input.Kind != runtimeinput.KindPromptCommand ||
		admission.request.Input.PromptCommand == nil ||
		admission.request.Input.PromptCommand.Name != "prompt:review" ||
		admission.request.Input.PromptCommand.Arguments != "keep  exact\narguments" {
		t.Fatalf("Runtime command input = %+v", admission.request.Input)
	}
}

func TestServiceSteerReturnsCommittedSessionWhenRuntimeOpeningFails(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	resolver := &steerTargetResolver{resolved: ResolvedTarget{SessionID: sessionID, Created: true}}
	service := NewService(
		resolver,
		&steerRuntimePlanner{err: errors.Join(serverapi.ErrRuntimeUnavailable, errors.New("open failed"))},
		&steerAdmission{},
	)

	result, err := service.Steer(t.Context(), &chatpb.SteerRequest{
		Target: &chatpb.ChatTarget{
			Target: &chatpb.ChatTarget_NewChat{NewChat: validSteerNewChatTarget()},
		},
		Activation: &chatpb.Activation{Input: &chatpb.Activation_Text{Text: "ship it"}},
	})
	if err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if result.GetSession().GetSessionId() != sessionID.String() ||
		result.GetNotAccepted().GetRuntimeUnavailable() == nil {
		t.Fatalf("Steer result = %+v, want committed Session and unavailable Runtime", result)
	}
}

func TestServiceSteerCancellationDuringTargetResolutionCreatesNoResult(t *testing.T) {
	for name, target := range map[string]*chatpb.ChatTarget{
		"existing Session lookup": {
			Target: &chatpb.ChatTarget_Session{
				Session: &chatpb.ExistingSessionTarget{SessionId: runtimeids.NewSessionID().String()},
			},
		},
		"New Chat creation": {
			Target: &chatpb.ChatTarget_NewChat{NewChat: validSteerNewChatTarget()},
		},
	} {
		t.Run(name, func(t *testing.T) {
			service := NewService(
				&steerTargetResolver{err: context.Canceled},
				&steerRuntimePlanner{},
				&steerAdmission{},
			)

			result, err := service.Steer(t.Context(), &chatpb.SteerRequest{
				Target:     target,
				Activation: &chatpb.Activation{Input: &chatpb.Activation_Text{Text: "ship it"}},
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Steer error = %v, want cancellation", err)
			}
			if result != nil {
				t.Fatalf("Steer result = %+v, want no result before target resolution", result)
			}
		})
	}
}

func TestServiceSteerReturnsCommittedSessionWhenRuntimePreparationIsCanceled(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	service := NewService(
		&steerTargetResolver{resolved: ResolvedTarget{SessionID: sessionID, Created: true}},
		&steerRuntimePlanner{err: context.Canceled},
		&steerAdmission{},
	)

	result, err := service.Steer(t.Context(), &chatpb.SteerRequest{
		Target: &chatpb.ChatTarget{
			Target: &chatpb.ChatTarget_NewChat{NewChat: validSteerNewChatTarget()},
		},
		Activation: &chatpb.Activation{Input: &chatpb.Activation_Text{Text: "ship it"}},
	})
	if err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if result.GetSession().GetSessionId() != sessionID.String() ||
		result.GetNotAccepted().GetCanceled() == nil {
		t.Fatalf("Steer result = %+v, want committed Session and cancellation", result)
	}
}

func TestServiceSteerCancellationDuringAdmissionClosesIdleRuntime(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	attachment := &steerRuntimeAttachment{sessionID: sessionID}
	service := NewService(
		&steerTargetResolver{resolved: ResolvedTarget{SessionID: sessionID}},
		&steerRuntimePlanner{attachment: attachment},
		&steerAdmission{err: context.Canceled},
	)

	result, err := service.Steer(t.Context(), &chatpb.SteerRequest{
		Target: &chatpb.ChatTarget{
			Target: &chatpb.ChatTarget_Session{
				Session: &chatpb.ExistingSessionTarget{SessionId: sessionID.String()},
			},
		},
		Activation: &chatpb.Activation{Input: &chatpb.Activation_Text{Text: "ship it"}},
	})
	if err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if result.GetNotAccepted().GetCanceled() == nil {
		t.Fatalf("Steer result = %+v, want canceled admission", result)
	}
	if attachment.policy != sessionruntime.RuntimeReleaseCloseIfIdle {
		t.Fatalf("Runtime release policy = %v, want close if idle", attachment.policy)
	}
}

func TestServiceSteerAcceptedWorkSurvivesCallerCancellation(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	queueItemID := runtimeids.NewQueueItemID()
	ctx, cancel := context.WithCancel(t.Context())
	attachment := &steerRuntimeAttachment{sessionID: sessionID}
	admission := &steerAdmission{
		accepted: true,
		result: serverapi.RuntimeSubmitUserTurnResponse{
			ResultKind:  "queued",
			Steered:     true,
			QueueItemID: queueItemID.String(),
		},
		onAdmit: cancel,
	}
	service := NewService(
		&steerTargetResolver{resolved: ResolvedTarget{SessionID: sessionID}},
		&steerRuntimePlanner{attachment: attachment},
		admission,
	)

	result, err := service.Steer(ctx, &chatpb.SteerRequest{
		Target: &chatpb.ChatTarget{
			Target: &chatpb.ChatTarget_Session{
				Session: &chatpb.ExistingSessionTarget{SessionId: sessionID.String()},
			},
		},
		Activation: &chatpb.Activation{Input: &chatpb.Activation_Text{Text: "ship it"}},
	})
	if err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if result.GetAccepted().GetQueueItem().GetId() != queueItemID.String() {
		t.Fatalf("Steer result = %+v, want accepted Queue Item", result)
	}
	if attachment.policy != sessionruntime.RuntimeReleaseDetach {
		t.Fatalf("Runtime release policy = %v, want detach", attachment.policy)
	}
	if attachment.releaseContextErr != nil {
		t.Fatalf("release inherited caller cancellation: %v", attachment.releaseContextErr)
	}
}

func TestServiceSteerReturnsAcceptedReleaseFailureAsDiagnostic(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	queueItemID := runtimeids.NewQueueItemID()
	attachment := &steerRuntimeAttachment{
		sessionID:  sessionID,
		releaseErr: errors.New("release failed"),
	}
	service := NewService(
		&steerTargetResolver{resolved: ResolvedTarget{SessionID: sessionID}},
		&steerRuntimePlanner{attachment: attachment},
		&steerAdmission{
			accepted: true,
			result: serverapi.RuntimeSubmitUserTurnResponse{
				ResultKind:  "queued",
				Steered:     true,
				QueueItemID: queueItemID.String(),
			},
		},
	)

	result, err := service.Steer(t.Context(), &chatpb.SteerRequest{
		Target: &chatpb.ChatTarget{
			Target: &chatpb.ChatTarget_Session{
				Session: &chatpb.ExistingSessionTarget{SessionId: sessionID.String()},
			},
		},
		Activation: &chatpb.Activation{Input: &chatpb.Activation_Text{Text: "ship it"}},
	})
	if err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if result.GetAccepted().GetQueueItem().GetId() != queueItemID.String() ||
		result.GetAccepted().GetDiagnostic().GetInternalFailure() == nil {
		t.Fatalf("Steer result = %+v, want accepted Queue Item and release diagnostic", result)
	}
}

func TestServiceSteerRetainsAcceptedIdentityWithSynchronousDiagnostic(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	queueItemID := runtimeids.NewQueueItemID()
	service := NewService(
		&steerTargetResolver{resolved: ResolvedTarget{SessionID: sessionID}},
		&steerRuntimePlanner{attachment: &steerRuntimeAttachment{sessionID: sessionID}},
		&steerAdmission{
			accepted: true,
			err:      errors.New("synchronous post-acceptance failure"),
			result: serverapi.RuntimeSubmitUserTurnResponse{
				ResultKind:  "queued",
				Steered:     true,
				QueueItemID: queueItemID.String(),
			},
		},
	)

	result, err := service.Steer(t.Context(), &chatpb.SteerRequest{
		Target: &chatpb.ChatTarget{
			Target: &chatpb.ChatTarget_Session{
				Session: &chatpb.ExistingSessionTarget{SessionId: sessionID.String()},
			},
		},
		Activation: &chatpb.Activation{Input: &chatpb.Activation_Text{Text: "ship it"}},
	})
	if err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if result.GetSession().GetSessionId() != sessionID.String() ||
		result.GetAccepted().GetQueueItem().GetId() != queueItemID.String() ||
		result.GetAccepted().GetDiagnostic().GetInternalFailure() == nil {
		t.Fatalf("Steer result = %+v, want accepted identities and diagnostic", result)
	}
}

func TestServiceSteerReturnsNonAcceptedReleaseFailureAsInternalDiagnostic(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	attachment := &steerRuntimeAttachment{
		sessionID:  sessionID,
		releaseErr: errors.New("release failed"),
	}
	service := NewService(
		&steerTargetResolver{resolved: ResolvedTarget{SessionID: sessionID}},
		&steerRuntimePlanner{attachment: attachment},
		&steerAdmission{err: &serverapi.PendingWorkCapacityError{}},
	)

	result, err := service.Steer(t.Context(), &chatpb.SteerRequest{
		Target: &chatpb.ChatTarget{
			Target: &chatpb.ChatTarget_Session{
				Session: &chatpb.ExistingSessionTarget{SessionId: sessionID.String()},
			},
		},
		Activation: &chatpb.Activation{Input: &chatpb.Activation_Text{Text: "ship it"}},
	})
	if err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if result.GetNotAccepted().GetInternalFailure() == nil {
		t.Fatalf("Steer result = %+v, want release failure diagnostic", result)
	}
}

func validSteerNewChatTarget() *chatpb.NewChatTarget {
	questions, autoCompaction := true, true
	return &chatpb.NewChatTarget{
		ProjectId:   "project-1",
		WorkspaceId: "workspace-1",
		InitialSettings: &chatpb.InitialChatSettings{
			AgentRole:             "default",
			Supervisor:            chatsettingspb.SupervisorValue_SUPERVISOR_VALUE_OFF,
			QuestionsEnabled:      &questions,
			AutoCompactionEnabled: &autoCompaction,
		},
	}
}
