package main

import (
	"context"
	"testing"

	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessionenv"

	"github.com/google/uuid"
)

func TestTaskCompleteAgentRequestUsesExactEnvironmentOrigin(t *testing.T) {
	runID := uuid.NewString()
	stepID := uuid.NewString()
	t.Setenv(sessionenv.RunIDEnv, runID)
	t.Setenv(sessionenv.StepIDEnv, stepID)

	request, err := (taskCompleteArgs{TransitionID: "done"}).request(
		context.Background(),
		config.App{},
		nil,
		uuid.NewString(),
		true,
	)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	want := serverapi.RuntimeStepOrigin{RunID: runID, StepID: stepID}
	if request.Origin == nil || *request.Origin != want {
		t.Fatalf("completion origin = %+v, want %+v", request.Origin, want)
	}
}

func TestTaskCompleteAgentRequestRejectsMissingOrInvalidEnvironmentOrigin(t *testing.T) {
	tests := []struct {
		name   string
		runID  string
		stepID string
	}{
		{name: "missing run", stepID: uuid.NewString()},
		{name: "missing step", runID: uuid.NewString()},
		{name: "invalid run", runID: "invalid", stepID: uuid.NewString()},
		{name: "invalid step", runID: uuid.NewString(), stepID: "invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(sessionenv.RunIDEnv, test.runID)
			t.Setenv(sessionenv.StepIDEnv, test.stepID)
			_, err := (taskCompleteArgs{TransitionID: "done"}).request(
				context.Background(),
				config.App{},
				nil,
				uuid.NewString(),
				true,
			)
			if err == nil {
				t.Fatal("request succeeded without an exact environment origin")
			}
		})
	}
}
