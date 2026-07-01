package runtimeattach

import (
	"context"
	"errors"

	servicecontract "core/shared/apicontract"
	"core/shared/serverapi"
)

type ActivityRequest struct {
	SessionID                       string
	OwnerID                         string
	Runtime                         servicecontract.SessionRuntimeService
	SessionActivity                 servicecontract.SessionActivityService
	Attention                       servicecontract.AttentionNotificationService
	AttentionNotificationsSupported bool
	PromptActivity                  servicecontract.PromptActivityService
}

type Activities struct {
	Session   serverapi.SessionActivitySubscription
	Prompt    serverapi.PromptActivitySubscription
	Attention serverapi.AttentionNotificationSubscription
}

func SubscribeActivities(ctx context.Context, req ActivityRequest) (Activities, error) {
	if req.SessionActivity == nil {
		Release(req.Runtime, req.SessionID, req.OwnerID)
		return Activities{}, errors.New("session activity service is required")
	}
	if req.PromptActivity == nil {
		Release(req.Runtime, req.SessionID, req.OwnerID)
		return Activities{}, errors.New("prompt activity service is required")
	}
	sessionSub, err := req.SessionActivity.SubscribeSessionActivity(ctx, serverapi.SessionActivitySubscribeRequest{SessionID: req.SessionID})
	if err != nil {
		Release(req.Runtime, req.SessionID, req.OwnerID)
		return Activities{}, err
	}
	promptSub, err := req.PromptActivity.SubscribePromptActivity(ctx, serverapi.PromptActivitySubscribeRequest{SessionID: req.SessionID})
	if err != nil {
		return Activities{}, errors.Join(err, sessionSub.Close(), Release(req.Runtime, req.SessionID, req.OwnerID))
	}
	if !req.AttentionNotificationsSupported {
		return Activities{Session: sessionSub, Prompt: promptSub}, nil
	}
	if req.Attention == nil {
		err := errors.New("attention notification service is required")
		return Activities{}, errors.Join(err, promptSub.Close(), sessionSub.Close(), Release(req.Runtime, req.SessionID, req.OwnerID))
	}
	attentionSub, err := req.Attention.SubscribeSessionAttentionNotifications(ctx, serverapi.AttentionSessionNotificationSubscribeRequest{SessionID: req.SessionID, IncludePendingPromptSnapshot: true})
	if err != nil {
		return Activities{}, errors.Join(err, promptSub.Close(), sessionSub.Close(), Release(req.Runtime, req.SessionID, req.OwnerID))
	}
	return Activities{Session: sessionSub, Prompt: promptSub, Attention: attentionSub}, nil
}
