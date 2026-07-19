package app

import (
	"context"
	"testing"

	"core/cli/app/internal/startupconfig"
)

func startSessionServerForTest(
	t *testing.T,
	ctx context.Context,
	opts Options,
	interactor authInteractor,
	interactive bool,
) (interactiveSessionServer, error) {
	t.Helper()
	cfg, err := startupconfig.ResolveSessionConfig(startupConfigRequest(opts))
	if err != nil {
		return nil, err
	}
	return startSessionServer(ctx, opts, interactor, interactive, cfg)
}
