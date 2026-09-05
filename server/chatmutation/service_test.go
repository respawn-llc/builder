package chatmutation

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/sessionruntime"
	chatpb "core/shared/protoapi/gen/kent/api/chat"
	chatsettingspb "core/shared/protoapi/gen/kent/api/chat_settings"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
)

type steerTargetResolver struct {
	resolved ResolvedTarget
	request  TargetResolutionRequest
	err      error
	calls    int
}

func (r *steerTargetResolver) Resolve(
	_ context.Context,
	request TargetResolutionRequest,
) (ResolvedTarget, error) {
	r.calls++
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
	releaseTimed      bool
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
	_, a.releaseTimed = ctx.Deadline()
	return a.releaseErr
}

type steerAdmission struct {
	request            serverapi.RuntimeSubmitUserTurnRequest
	result             serverapi.ChatInputAdmissionResult
	err                error
	queueRequest       serverapi.RuntimeSubmitUserTurnRequest
	queueResult        serverapi.ChatInputAdmissionResult
	queueErr           error
	compactionRequest  serverapi.RuntimeCompactContextRequest
	compactionAccepted bool
	compactionErr      error
}

func (a *steerAdmission) AdmitManualCompaction(
	_ context.Context,
	request serverapi.RuntimeCompactContextRequest,
) (bool, error) {
	a.compactionRequest = request
	return a.compactionAccepted, a.compactionErr
}

func (a *steerAdmission) AdmitChatUserTurn(
	_ context.Context,
	request serverapi.RuntimeSubmitUserTurnRequest,
) (serverapi.ChatInputAdmissionResult, error) {
	a.request = request
	return a.result, a.err
}

func (a *steerAdmission) AdmitChatQueuedUserInput(
	_ context.Context,
	request serverapi.RuntimeSubmitUserTurnRequest,
) (serverapi.ChatInputAdmissionResult, error) {
	a.queueRequest = request
	return a.queueResult, a.queueErr
}

func newTestService(
	targets TargetResolutionService,
	runtimes RuntimeOpeningService,
	admissions RuntimeAdmissionService,
) *Service {
	return NewService(newTestOperationOwner(), targets, runtimes, admissions)
}

func newTestOperationOwner() *OperationOwner {
	owner, err := NewOperationOwner(time.Second)
	if err != nil {
		panic(err)
	}
	return owner
}

func TestServiceInputMutationReleasePolicy(t *testing.T) {
	tests := []struct {
		name       string
		accepted   bool
		admission  error
		historyErr error
		wantPolicy sessionruntime.RuntimeReleasePolicy
	}{
		{
			name:       "accepted work detaches",
			accepted:   true,
			wantPolicy: sessionruntime.RuntimeReleaseDetach,
		},
		{
			name:       "accepted prompt-history failure stays typed",
			accepted:   true,
			historyErr: errors.New("prompt history unavailable"),
			wantPolicy: sessionruntime.RuntimeReleaseDetach,
		},
		{
			name:       "definite rejection closes idle Runtime",
			admission:  &serverapi.PendingWorkCapacityError{},
			wantPolicy: sessionruntime.RuntimeReleaseCloseIfIdle,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sessionID := runtimeids.NewSessionID()
			queueItemID := runtimeids.NewQueueItemID()
			attachment := &steerRuntimeAttachment{sessionID: sessionID}
			admission := &steerAdmission{
				err: test.admission,
				result: serverapi.ChatInputAdmissionResult{
					QueueItemID:          queueItemID,
					Accepted:             test.accepted,
					PromptHistoryFailure: test.historyErr,
				},
			}
			service := newTestService(
				&steerTargetResolver{resolved: ResolvedTarget{
					SessionID: sessionID,
					Created:   !test.accepted,
				}},
				&steerRuntimePlanner{attachment: attachment},
				admission,
			)

			result, err := service.Steer(t.Context(), &chatpb.SteerRequest{
				Target: &chatpb.ChatTarget{
					Target: &chatpb.ChatTarget_Session{
						Session: &chatpb.ExistingSessionTarget{SessionId: sessionID.String()},
					},
				},
				Activation: &chatpb.Activation{
					Input: &chatpb.Activation_Text{Text: "  exact Chat input  "},
				},
			})
			if err != nil {
				t.Fatalf("Steer: %v", err)
			}
			if result.GetSession().GetSessionId() != sessionID.String() {
				t.Fatalf("Steer Session = %+v, want %s", result.GetSession(), sessionID)
			}
			if test.accepted {
				if result.GetAccepted().GetQueueItem().GetId() != queueItemID.String() {
					t.Fatalf("Steer result = %+v, want accepted Queue Item", result)
				}
				if test.historyErr != nil &&
					result.GetAccepted().GetDiagnostic().GetPromptHistoryFailure() == nil {
					t.Fatalf("Steer result = %+v, want typed prompt-history diagnostic", result)
				}
			} else if result.GetNotAccepted().GetPendingWorkCapacity() == nil {
				t.Fatalf("Steer result = %+v, want capacity rejection", result)
			}
			if admission.request.Input.Kind != runtimeinput.KindText ||
				admission.request.Input.Text == nil ||
				*admission.request.Input.Text != "  exact Chat input  " {
				t.Fatalf("Runtime admission = %+v", admission.request)
			}
			if attachment.policy != test.wantPolicy || !attachment.releaseTimed {
				t.Fatalf(
					"Runtime release = policy %v timed=%t, want %v and bounded",
					attachment.policy,
					attachment.releaseTimed,
					test.wantPolicy,
				)
			}
		})
	}
}

func TestServiceQueuePreservesExactDraftAndCanonicalCommandIdentity(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	queueItemID := runtimeids.NewQueueItemID()
	historyErr := errors.New("prompt history unavailable")
	resolver := &steerTargetResolver{resolved: ResolvedTarget{SessionID: sessionID, Created: true}}
	attachment := &steerRuntimeAttachment{sessionID: sessionID}
	admission := &steerAdmission{queueResult: serverapi.ChatInputAdmissionResult{
		QueueItemID:          queueItemID,
		Accepted:             true,
		PromptHistoryFailure: historyErr,
	}}
	service := newTestService(
		resolver,
		&steerRuntimePlanner{attachment: attachment},
		admission,
	)

	result, err := service.Queue(t.Context(), &chatpb.QueueRequest{
		Target: &chatpb.ChatTarget{
			Target: &chatpb.ChatTarget_NewChat{NewChat: validSteerNewChatTarget()},
		},
		Activation: &chatpb.Activation{
			Input: &chatpb.Activation_Command{Command: &chatpb.CommandInvocation{
				CatalogIdentity:     "prompt:review",
				Token:               "/review",
				SeparatorWhitespace: "\t ",
				Arguments:           "keep  exact\narguments",
			}},
		},
	})
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	if result.GetSession().GetSessionId() != sessionID.String() ||
		result.GetAccepted().GetQueueItem().GetId() != queueItemID.String() ||
		result.GetAccepted().GetDiagnostic().GetPromptHistoryFailure() == nil {
		t.Fatalf("Queue result = %+v", result)
	}
	if resolver.request.InitialDraft == nil ||
		*resolver.request.InitialDraft != "/review\t keep  exact\narguments" {
		t.Fatalf("New Chat draft = %v", resolver.request.InitialDraft)
	}
	if admission.queueRequest.Input.Kind != runtimeinput.KindPromptCommand ||
		admission.queueRequest.Input.PromptCommand == nil ||
		admission.queueRequest.Input.PromptCommand.Name != "prompt:review" ||
		admission.queueRequest.Input.PromptCommand.Arguments != "keep  exact\narguments" {
		t.Fatalf("Runtime Queue input = %+v", admission.queueRequest.Input)
	}
	if attachment.policy != sessionruntime.RuntimeReleaseDetach {
		t.Fatalf("Runtime release policy = %v, want detach", attachment.policy)
	}
}

func TestServiceCompactionAdmission(t *testing.T) {
	tests := []struct {
		name       string
		accepted   bool
		admission  error
		wantPolicy sessionruntime.RuntimeReleasePolicy
	}{
		{
			name:       "accepted",
			accepted:   true,
			wantPolicy: sessionruntime.RuntimeReleaseDetach,
		},
		{
			name:       "fresh New Chat is too soon",
			admission:  serverapi.ErrManualCompactionTooSoon,
			wantPolicy: sessionruntime.RuntimeReleaseCloseIfIdle,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sessionID := runtimeids.NewSessionID()
			resolver := &steerTargetResolver{resolved: ResolvedTarget{
				SessionID: sessionID,
				Created:   !test.accepted,
			}}
			attachment := &steerRuntimeAttachment{sessionID: sessionID}
			admission := &steerAdmission{
				compactionAccepted: test.accepted,
				compactionErr:      test.admission,
			}
			service := newTestService(
				resolver,
				&steerRuntimePlanner{attachment: attachment},
				admission,
			)

			result, err := service.Compact(t.Context(), &chatpb.CompactRequest{
				Target: &chatpb.ChatTarget{
					Target: &chatpb.ChatTarget_NewChat{NewChat: validSteerNewChatTarget()},
				},
				Invocation: &chatpb.CompactionInvocation{
					Token:               "/compact",
					SeparatorWhitespace: "\t ",
					RawGuidance:         "preserve  exact\ninput ",
				},
			})
			if err != nil {
				t.Fatalf("Compact: %v", err)
			}
			if result.GetSession().GetSessionId() != sessionID.String() {
				t.Fatalf("Compact Session = %+v, want %s", result.GetSession(), sessionID)
			}
			if test.accepted {
				if result.GetAccepted().GetRequest().GetId() == "" {
					t.Fatalf("Compact result = %+v, want accepted identity", result)
				}
			} else if result.GetNotAccepted().GetTooSoon() == nil {
				t.Fatalf("Compact result = %+v, want too-soon rejection", result)
			}
			if resolver.request.InitialDraft == nil ||
				*resolver.request.InitialDraft != "/compact\t preserve  exact\ninput " {
				t.Fatalf("compaction draft = %v", resolver.request.InitialDraft)
			}
			if admission.compactionRequest.Admission.Guidance == nil ||
				*admission.compactionRequest.Admission.Guidance != "preserve exact input" {
				t.Fatalf("compaction admission = %+v", admission.compactionRequest)
			}
			if attachment.policy != test.wantPolicy {
				t.Fatalf("Runtime release policy = %v, want %v", attachment.policy, test.wantPolicy)
			}
		})
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
