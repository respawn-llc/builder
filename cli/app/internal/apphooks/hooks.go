package apphooks

import (
	"context"

	"core/cli/app/internal/embeddedattach"
	"core/cli/app/internal/runner"
)

type Options struct {
	StartupOptions                  embeddedattach.StartupOptions
	TerminalPhaseMarkerEncoder      runner.TerminalPhaseMarkerEncoder
	TerminalPhaseMarkerSinkObserver runner.TerminalPhaseMarkerSinkObserver
}

type contextKey struct{}

func WithOptions(ctx context.Context, opts Options) context.Context {
	return context.WithValue(ctx, contextKey{}, opts)
}

func FromContext(ctx context.Context) (Options, bool) {
	opts, ok := ctx.Value(contextKey{}).(Options)
	return opts, ok
}
