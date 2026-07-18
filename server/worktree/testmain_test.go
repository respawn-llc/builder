package worktree

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"testing"
)

var worktreeTestContext = context.Background()

func TestMain(m *testing.M) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	worktreeTestContext = ctx
	exitCode := m.Run()
	stop()
	os.Exit(exitCode)
}
