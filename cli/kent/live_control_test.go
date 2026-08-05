package main

import (
	"context"
	"testing"

	"core/cli/app"
	"core/shared/runtimeids"
)

func TestRunLiveSteerRejectsMalformedCallerContextBeforeApp(t *testing.T) {
	t.Setenv("KENT_SESSION_ID", "not-a-uuid")
	if _, err := app.LiveSteerCallerSessionID(); err == nil {
		t.Fatal("shared caller-context validation accepted malformed value")
	}
	previous := runLiveSteerApp
	t.Cleanup(func() { runLiveSteerApp = previous })
	called := false
	runLiveSteerApp = func(context.Context, app.Options, runtimeids.SessionID, string) (app.RunLiveSteerResult, error) {
		called = true
		return app.RunLiveSteerResult{}, nil
	}

	if code := runLiveSteerSubcommand([]string{
		"018fdd67-89ab-4cde-8123-456789abcdef",
		"probe",
	}); code != 2 {
		t.Fatalf("exit code = %d, want malformed-context usage error", code)
	}
	if called {
		t.Fatal("runLiveSteerApp called with malformed caller context")
	}
}
