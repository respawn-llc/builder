package main

import (
	"context"
	"testing"

	"core/shared/config"
)

func installWorkflowCommandRemote(t *testing.T, remote workflowCommandRemote) {
	t.Helper()
	previous := workflowCommandRemoteOpener
	previousObservation := workflowObservationRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		return config.App{}, remote, nil
	}
	workflowObservationRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, func() error, error) {
		return config.App{}, remote, remote.Close, nil
	}
	t.Cleanup(func() {
		workflowCommandRemoteOpener = previous
		workflowObservationRemoteOpener = previousObservation
	})
}
