//go:build !windows

package main

import "context"

func installServiceShutdownTrigger(ctx context.Context) context.Context {
	return ctx
}
