//go:build !windows && !darwin

package main

import "context"

func installServiceShutdownTrigger(ctx context.Context) context.Context {
	return ctx
}
