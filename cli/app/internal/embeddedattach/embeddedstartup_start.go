package embeddedattach

import (
	"context"

	serverstartup "core/server/startup"
)

type Server = serverstartup.EmbeddedServer
type StartupOptions = serverstartup.Options
type StartupRequest = serverstartup.Request
type AuthHandler = serverstartup.AuthHandler

func Start(ctx context.Context, req StartupRequest, authHandler AuthHandler, opts StartupOptions) (*Server, error) {
	return serverstartup.StartWithOptions(ctx, req, authHandler, nil, opts)
}
