package chatcontext

import (
	"context"

	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type WorkspaceOwner interface {
	ReadWorkspaceChatContext(context.Context) (serverapi.ChatContext, error)
}

type SessionOwner interface {
	ReadSessionChatContext(context.Context, runtimeids.SessionID) (serverapi.ChatContext, error)
}
