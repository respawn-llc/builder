package main

// Package serverbridge is the documented CLI binary composition bridge for
// local server startup/lifecycle wiring. Command handlers should depend on this
// package or shared client contracts instead of server packages directly.

import (
	"context"
)

type ServeServer interface {
	Close() error
	Serve(ctx context.Context) error
}
