package main

import (
	"context"
	"io"
	"testing"

	"core/shared/config"
	"core/shared/serverapi"
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

func taskWaitWithRemote(args []string, stdout io.Writer, stderr io.Writer, remote workflowCommandRemote) int {
	return taskObservationSubcommandWithOpener(args, stdout, stderr, serverapi.WorkflowTaskObservationWait, func(context.Context, string) (config.App, workflowCommandRemote, error) {
		return config.App{}, remote, nil
	})
}
