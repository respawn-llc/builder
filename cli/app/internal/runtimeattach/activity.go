package runtimeattach

import (
	"context"
	"errors"

	servicecontract "core/shared/apicontract"
	"core/shared/serverapi"
)

type ActivityRequest struct {
	SessionID       string
	OwnerID         string
	Runtime         servicecontract.SessionRuntimeService
	SessionActivity servicecontract.SessionActivityService
	PromptActivity  servicecontract.PromptActivityService
}

type Activities struct {
	Session serverapi.SessionActivitySubscription
	Prompt  serverapi.PromptActivitySubscription
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
		_ = sessionSub.Close()
		Release(req.Runtime, req.SessionID, req.OwnerID)
		return Activities{}, err
	}
	return Activities{Session: sessionSub, Prompt: promptSub}, nil
}
