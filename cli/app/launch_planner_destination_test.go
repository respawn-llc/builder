package app

import (
	"testing"

	"core/shared/serverapi"
)

func TestResolvedSessionPlanRequestConvertsTypedDestinationAtAPIClientBoundary(t *testing.T) {
	openDestination := sessionOpenDestinationForTest(t, " session-1 ")
	parent := sessionParentReferenceForTest(t, " parent-1 ")
	tests := []struct {
		name                string
		destination         sessionLaunchDestination
		wantSelectedSession string
		wantForceNew        bool
		wantParentSession   string
	}{
		{
			name:                "existing session",
			destination:         openDestination,
			wantSelectedSession: "session-1",
		},
		{
			name:              "new root session",
			destination:       sessionCreateDestination{},
			wantForceNew:      true,
			wantParentSession: "",
		},
		{
			name:              "new child session",
			destination:       sessionCreateDestination{Parent: parent},
			wantForceNew:      true,
			wantParentSession: "parent-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request, err := (resolvedSessionPlanRequest{
				destination: tt.destination,
				overrides:   serverapi.RunPromptOverrides{Model: "gpt-5"},
			}).apiRequest(launchModeInteractive)
			if err != nil {
				t.Fatalf("convert typed destination to API request: %v", err)
			}
			if request.SelectedSessionID != tt.wantSelectedSession ||
				request.ForceNewSession != tt.wantForceNew ||
				request.ParentSessionID != tt.wantParentSession {
				t.Fatalf("API request destination = %+v", request)
			}
			if request.Overrides.Model != "gpt-5" {
				t.Fatalf("API request overrides = %+v", request.Overrides)
			}
		})
	}
}
