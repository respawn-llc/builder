package chatmutation

import (
	"context"
	"errors"

	"core/server/launch"
	"core/server/sessionlaunch"
	"core/server/sessionruntime"
	"core/shared/apicontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

type SessionRuntimePlannerResolver func(
	context.Context,
	runtimeids.SessionID,
) (PersistedSessionPlanner, error)

type PersistedSessionPlanner interface {
	PlanLaunchSession(context.Context, sessionlaunch.PlanRequest) (sessionlaunch.PlanResult, error)
}

type RuntimeAttachment interface {
	SessionID() runtimeids.SessionID
	Release(context.Context, sessionruntime.RuntimeReleasePolicy) error
}

type RuntimePlanner struct {
	authority     *sessionruntime.Authority
	sessionLaunch SessionRuntimePlannerResolver
	runtimeAPI    apicontract.SessionRuntimeService
}

func NewRuntimePlanner(
	authority *sessionruntime.Authority,
	sessionLaunch SessionRuntimePlannerResolver,
	runtimeAPI apicontract.SessionRuntimeService,
) *RuntimePlanner {
	return &RuntimePlanner{
		authority:     authority,
		sessionLaunch: sessionLaunch,
		runtimeAPI:    runtimeAPI,
	}
}

func (p *RuntimePlanner) Open(
	ctx context.Context,
	sessionID runtimeids.SessionID,
) (RuntimeAttachment, error) {
	if p == nil || p.authority == nil {
		return nil, errors.New("session Runtime authority is required")
	}
	ownerID := uuid.NewString()
	attachment, err := p.authority.OpenRuntime(ctx, sessionruntime.RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   ownerID,
	})
	if err == nil {
		return authorityRuntimeAttachment{attachment: attachment}, nil
	}
	if !errors.Is(err, sessionruntime.ErrAgentRuntimePlanRequired) {
		return nil, err
	}
	if p.sessionLaunch == nil {
		return nil, errors.New("persisted Session launch resolver is required")
	}
	service, err := p.sessionLaunch(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if service == nil {
		return nil, errors.New("persisted Session launch service is required")
	}
	planned, err := service.PlanLaunchSession(ctx, sessionlaunch.PlanRequest{
		Mode:   launch.ModeInteractive,
		Intent: serverapi.OpenExistingSessionLaunchIntent(sessionID),
	})
	if err != nil {
		return nil, err
	}
	activation, err := sessionruntime.ActivationRequestFromSessionPlan(planned.Plan, ownerID)
	if err != nil {
		return nil, err
	}
	if p.runtimeAPI == nil {
		return nil, errors.New("Session Runtime service is required")
	}
	response, err := p.runtimeAPI.ActivateSessionRuntime(ctx, activation)
	if err != nil {
		return nil, err
	}
	serviceAttachment := serviceRuntimeAttachment{
		sessionID:  sessionID,
		attachment: response.Attachment,
		ownerID:    ownerID,
		runtimeAPI: p.runtimeAPI,
	}
	if err := response.ValidateForSession(sessionID.String()); err != nil {
		return serviceAttachment, err
	}
	return serviceAttachment, nil
}

type authorityRuntimeAttachment struct {
	attachment sessionruntime.RuntimeAttachment
}

func (a authorityRuntimeAttachment) SessionID() runtimeids.SessionID {
	return a.attachment.Resource().SessionID()
}

func (a authorityRuntimeAttachment) Release(
	ctx context.Context,
	policy sessionruntime.RuntimeReleasePolicy,
) error {
	_, err := a.attachment.Release(ctx, policy)
	return err
}

type serviceRuntimeAttachment struct {
	sessionID  runtimeids.SessionID
	attachment serverapi.SessionRuntimeAttachment
	ownerID    string
	runtimeAPI apicontract.SessionRuntimeService
}

func (a serviceRuntimeAttachment) SessionID() runtimeids.SessionID {
	return a.sessionID
}

func (a serviceRuntimeAttachment) Release(
	ctx context.Context,
	policy sessionruntime.RuntimeReleasePolicy,
) error {
	var closePolicy serverapi.SessionRuntimeReleaseClosePolicy
	switch policy {
	case sessionruntime.RuntimeReleaseDetach:
		closePolicy = serverapi.SessionRuntimeReleaseClosePolicyDetachOnly
	case sessionruntime.RuntimeReleaseCloseIfIdle:
		closePolicy = serverapi.SessionRuntimeReleaseClosePolicyCloseIfIdle
	default:
		return errors.New("Chat Runtime attachment release policy must detach or close if idle")
	}
	_, err := a.runtimeAPI.ReleaseSessionRuntime(ctx, serverapi.SessionRuntimeReleaseRequest{
		Attachment:  a.attachment,
		DropOwner:   true,
		ClosePolicy: closePolicy,
		OwnerID:     a.ownerID,
	})
	return err
}
