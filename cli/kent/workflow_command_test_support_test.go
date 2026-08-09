package main

import (
	"context"
	"testing"

	"core/shared/config"
)

func installWorkflowCommandRemote(t *testing.T, remote workflowCommandRemote) {
	t.Helper()
	previous := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		return config.App{}, remote, nil
	}
	t.Cleanup(func() {
		workflowCommandRemoteOpener = previous
	})
}
