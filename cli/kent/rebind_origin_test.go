package main

import (
	"testing"

	"core/shared/sessionenv"
)

func TestSessionRetargetRuntimeOriginUsesOnlyTheInvokingSession(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-a")
	t.Setenv(sessionenv.RunIDEnv, "11111111-1111-4111-8111-111111111111")
	t.Setenv(sessionenv.StepIDEnv, "22222222-2222-4222-8222-222222222222")

	origin, err := sessionRetargetRuntimeOrigin("session-a")
	if err != nil {
		t.Fatalf("sessionRetargetRuntimeOrigin matching Session: %v", err)
	}
	if origin == nil {
		t.Fatal("matching Session omitted runtime origin")
	}

	origin, err = sessionRetargetRuntimeOrigin("session-b")
	if err != nil {
		t.Fatalf("sessionRetargetRuntimeOrigin other Session: %v", err)
	}
	if origin != nil {
		t.Fatalf("other Session origin = %+v, want nil", origin)
	}
}
