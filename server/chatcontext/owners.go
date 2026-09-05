package chatcontext

import (
	"context"

	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type SessionOwner interface {
	ReadSessionChatContext(context.Context, runtimeids.SessionID) (serverapi.ChatContext, error)
}
