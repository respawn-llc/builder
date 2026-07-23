package app

import (
	"context"
	"errors"
	"testing"

	"core/shared/serverapi"
)

func TestSessionLaunchInitialStateReturnsLifecycleError(t *testing.T) {
	lookupErr := errors.New("initial input lookup failed")
	server := narrowSessionLifecycleServer{
		lifecycle: &recordingSessionLifecycleClient{
			getInitialInput: func(context.Context, serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error) {
				return serverapi.SessionInitialInputResponse{}, lookupErr
			},
		},
	}

	state, err := sessionLaunchInitialStateFromServer(
		context.Background(),
		server,
		"parent-session",
		"child final",
		true,
	)
	if !errors.Is(err, lookupErr) {
		t.Fatalf("initial state error = %v, want %v", err, lookupErr)
	}
	if state.Input != "" || len(state.RecoveryBuffers) != 0 {
		t.Fatalf("failed initial state lookup returned state %+v", state)
	}
}
