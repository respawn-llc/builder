package chatcontext

import (
	"context"

	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type workspaceOwnerFixture struct{}

func (workspaceOwnerFixture) ReadWorkspaceChatContext(context.Context) (serverapi.ChatContext, error) {
	return serverapi.ChatContext{}, nil
}

type sessionOwnerFixture struct{}

func (sessionOwnerFixture) ReadSessionChatContext(context.Context, runtimeids.SessionID) (serverapi.ChatContext, error) {
	return serverapi.ChatContext{}, nil
}

var _ WorkspaceOwner = workspaceOwnerFixture{}
var _ SessionOwner = sessionOwnerFixture{}
