package chatmutation

import (
	"context"
	"testing"

	"core/server/session"
	chatpb "core/shared/protoapi/gen/kent/api/chat"
	chatsettingspb "core/shared/protoapi/gen/kent/api/chat_settings"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	"core/shared/runtimeids"

	"google.golang.org/protobuf/proto"
)

type targetResolverPersistedSessions struct {
	record session.PersistedSessionRecord
	err    error
}

func (s targetResolverPersistedSessions) ResolvePersistedSession(
	context.Context,
	string,
) (session.PersistedSessionRecord, error) {
	return s.record, s.err
}

func TestTargetResolverResolvesExistingSessionWithoutCreatingAnother(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	resolver := NewTargetResolver(
		targetResolverPersistedSessions{record: session.PersistedSessionRecord{
			SessionDir: "/tmp/" + sessionID.String(),
			Meta:       &session.Meta{SessionID: sessionID.String()},
		}},
		func(context.Context, string, string) (SessionCreationService, error) {
			t.Fatal("existing Session resolution selected a creation service")
			return nil, nil
		},
	)

	resolved, err := resolver.Resolve(t.Context(), TargetResolutionRequest{
		Target: &chatpb.ChatTarget{
			Target: &chatpb.ChatTarget_Session{
				Session: &chatpb.ExistingSessionTarget{SessionId: sessionID.String()},
			},
		},
	})
	if err != nil {
		t.Fatalf("resolve existing Session: %v", err)
	}
	if resolved.SessionID != sessionID || resolved.Created {
		t.Fatalf("resolved target = %+v, want existing Session %s", resolved, sessionID)
	}
}

type targetResolverSessionLaunch struct {
	request *sessionlaunchpb.SessionPlanRequest
	result  *sessionlaunchpb.SessionPlanSuccess
}

func (s *targetResolverSessionLaunch) PlanSession(
	_ context.Context,
	request *sessionlaunchpb.SessionPlanRequest,
) (*sessionlaunchpb.SessionPlanSuccess, error) {
	s.request = request
	return s.result, nil
}

func TestTargetResolverCreatesNewChatThroughOrdinaryIndependentSessionLaunch(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	launchService := &targetResolverSessionLaunch{
		result: &sessionlaunchpb.SessionPlanSuccess{
			Plan: &sessionlaunchpb.SessionPlan{SessionId: sessionID.String()},
		},
	}
	resolver := NewTargetResolver(
		targetResolverPersistedSessions{},
		func(_ context.Context, projectID string, workspaceID string) (SessionCreationService, error) {
			if projectID != "project-1" || workspaceID != "workspace-1" {
				t.Fatalf("creation target = %q/%q, want project-1/workspace-1", projectID, workspaceID)
			}
			return launchService, nil
		},
	)
	draft := "  /review\t exact arguments  "
	questions := true
	autoCompaction := false
	target := &chatpb.ChatTarget{
		Target: &chatpb.ChatTarget_NewChat{
			NewChat: &chatpb.NewChatTarget{
				ProjectId:   "project-1",
				WorkspaceId: "workspace-1",
				InitialSettings: &chatpb.InitialChatSettings{
					AgentRole:             "default",
					Supervisor:            chatsettingspb.SupervisorValue_SUPERVISOR_VALUE_OFF,
					QuestionsEnabled:      &questions,
					AutoCompactionEnabled: &autoCompaction,
				},
			},
		},
	}

	resolved, err := resolver.Resolve(t.Context(), TargetResolutionRequest{
		Target:       target,
		InitialDraft: &draft,
	})
	if err != nil {
		t.Fatalf("resolve New Chat: %v", err)
	}
	if resolved.SessionID != sessionID || !resolved.Created {
		t.Fatalf("resolved target = %+v, want created Session %s", resolved, sessionID)
	}
	request := launchService.request
	if request == nil ||
		request.Mode != sessionlaunchpb.SessionLaunchMode_SESSION_LAUNCH_MODE_INTERACTIVE ||
		request.GetIntent().GetCreateNew().GetIndependent() == nil {
		t.Fatalf("ordinary creation request = %+v, want interactive independent-main creation", request)
	}
	if !proto.Equal(request.InitialChatSettings, target.GetNewChat().InitialSettings) {
		t.Fatal("ordinary creation did not carry the complete initial Chat settings")
	}
	if request.InitialInputDraft == nil || *request.InitialInputDraft != draft {
		t.Fatalf("ordinary creation draft = %v, want exact %q", request.InitialInputDraft, draft)
	}
}
