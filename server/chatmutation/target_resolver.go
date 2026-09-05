package chatmutation

import (
	"context"
	"errors"
	"fmt"

	"core/server/session"
	"core/shared/protoapi"
	chatpb "core/shared/protoapi/gen/kent/api/chat"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	"core/shared/runtimeids"
	"core/shared/serverapi"

	"google.golang.org/protobuf/proto"
)

type SessionCreationService interface {
	PlanSession(context.Context, *sessionlaunchpb.SessionPlanRequest) (*sessionlaunchpb.SessionPlanSuccess, error)
}

type SessionLaunchServiceResolver func(
	context.Context,
	string,
	string,
) (SessionCreationService, error)

type TargetResolutionRequest struct {
	Target       *chatpb.ChatTarget
	InitialDraft *string
}

type ResolvedTarget struct {
	SessionID runtimeids.SessionID
	Created   bool
}

type TargetResolver struct {
	persistedSessions session.PersistedSessionResolver
	sessionLaunch     SessionLaunchServiceResolver
}

func NewTargetResolver(
	persistedSessions session.PersistedSessionResolver,
	sessionLaunch SessionLaunchServiceResolver,
) *TargetResolver {
	return &TargetResolver{
		persistedSessions: persistedSessions,
		sessionLaunch:     sessionLaunch,
	}
}

func (r *TargetResolver) Resolve(
	ctx context.Context,
	request TargetResolutionRequest,
) (ResolvedTarget, error) {
	if err := protoapi.Validate(request.Target); err != nil {
		return ResolvedTarget{}, fmt.Errorf("Chat target: %w", err)
	}
	if r == nil {
		return ResolvedTarget{}, errors.New("Chat target resolver is required")
	}
	if sessionTarget := request.Target.GetSession(); sessionTarget != nil {
		sessionID, err := runtimeids.ParseSessionID(sessionTarget.SessionId)
		if err != nil {
			return ResolvedTarget{}, err
		}
		if _, err := session.ResolvePersistedSessionRecord(
			ctx,
			r.persistedSessions,
			sessionID.String(),
		); err != nil {
			return ResolvedTarget{}, err
		}
		return ResolvedTarget{SessionID: sessionID}, nil
	}
	newChat := request.Target.GetNewChat()
	if newChat == nil {
		return ResolvedTarget{}, errors.New("Chat target selection is required")
	}
	if r.sessionLaunch == nil {
		return ResolvedTarget{}, errors.New("Session launch service resolver is required")
	}
	service, err := r.sessionLaunch(ctx, newChat.ProjectId, newChat.WorkspaceId)
	if err != nil {
		return ResolvedTarget{}, err
	}
	if service == nil {
		return ResolvedTarget{}, errors.New("Session launch service is required")
	}
	intent, err := protoapi.SessionLaunchIntentToProto(
		serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
	)
	if err != nil {
		return ResolvedTarget{}, err
	}
	settings := proto.Clone(newChat.InitialSettings).(*chatpb.InitialChatSettings)
	response, err := service.PlanSession(ctx, &sessionlaunchpb.SessionPlanRequest{
		Mode:                sessionlaunchpb.SessionLaunchMode_SESSION_LAUNCH_MODE_INTERACTIVE,
		Intent:              intent,
		InitialChatSettings: settings,
		InitialInputDraft:   request.InitialDraft,
	})
	if err != nil {
		return ResolvedTarget{}, err
	}
	if response == nil || response.Plan == nil {
		return ResolvedTarget{}, errors.New("ordinary Session creation result is required")
	}
	sessionID, err := runtimeids.ParseSessionID(response.Plan.SessionId)
	if err != nil {
		return ResolvedTarget{}, fmt.Errorf("ordinary Session creation result: %w", err)
	}
	return ResolvedTarget{SessionID: sessionID, Created: true}, nil
}
