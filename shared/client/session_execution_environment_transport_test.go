package client

import (
	"context"
	"errors"
	"io"
	"testing"

	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"

	"golang.org/x/net/websocket"
)

func TestRemoteSessionExecutionEnvironmentRoundTripsAuthApplicability(t *testing.T) {
	sessionID, err := runtimeids.ParseSessionID("environment-session")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	tests := []struct {
		name     string
		response serverapi.SessionExecutionEnvironmentResponse
		assert   func(*testing.T, serverapi.SessionExecutionAuthField)
	}{
		{
			name: "explicit no auth",
			response: sessionExecutionEnvironmentTransportResponse(
				sessionID,
				"openai",
				serverapi.AvailableSessionExecutionAuth(serverapi.SessionExecutionAuth{
					Provider: "openai",
					Method:   serverapi.SessionExecutionAuthMethodNone,
				}),
			),
			assert: func(t *testing.T, field serverapi.SessionExecutionAuthField) {
				t.Helper()
				value, ok := field.Value()
				if !ok || value.Provider != "openai" || value.Method != serverapi.SessionExecutionAuthMethodNone {
					t.Fatalf("auth field = %+v/%v", value, ok)
				}
			},
		},
		{
			name: "provider not applicable",
			response: sessionExecutionEnvironmentTransportResponse(
				sessionID,
				"anthropic",
				serverapi.UnavailableSessionExecutionAuth(serverapi.SessionExecutionAuthUnavailableNotApplicable),
			),
			assert: func(t *testing.T, field serverapi.SessionExecutionAuthField) {
				t.Helper()
				reason, ok := field.UnavailableReason()
				if !ok || reason != serverapi.SessionExecutionAuthUnavailableNotApplicable {
					t.Fatalf("auth unavailable reason = %q/%v", reason, ok)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newRemoteTestServer(t, func(ws *websocket.Conn) {
				req := acceptRemoteHandshake(t, ws)
				for {
					if err := websocket.JSON.Receive(ws, &req); err != nil {
						if errors.Is(err, io.EOF) {
							return
						}
						t.Fatalf("receive execution environment request: %v", err)
					}
					switch req.Method {
					case protocol.MethodAttachProject:
						if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.AttachResponse{
							Kind:      "project",
							ProjectID: "project-1",
						})); err != nil {
							t.Fatalf("send attach response: %v", err)
						}
					case protocol.MethodSessionGetExecutionEnvironment:
						if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, test.response)); err != nil {
							t.Fatalf("send execution environment response: %v", err)
						}
					default:
						t.Fatalf("unexpected method %q", req.Method)
					}
				}
			})

			remote, err := DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], "project-1")
			if err != nil {
				t.Fatalf("DialRemoteURLForProject: %v", err)
			}
			defer func() { _ = remote.Close() }()

			response, err := remote.GetSessionExecutionEnvironment(
				context.Background(),
				serverapi.SessionExecutionEnvironmentRequest{SessionID: sessionID},
			)
			if err != nil {
				t.Fatalf("GetSessionExecutionEnvironment: %v", err)
			}
			if err := response.Validate(); err != nil {
				t.Fatalf("response validation: %v", err)
			}
			test.assert(t, response.Environment.Auth)
		})
	}
}

func sessionExecutionEnvironmentTransportResponse(
	sessionID runtimeids.SessionID,
	provider string,
	auth serverapi.SessionExecutionAuthField,
) serverapi.SessionExecutionEnvironmentResponse {
	return serverapi.SessionExecutionEnvironmentResponse{
		Environment: serverapi.SessionExecutionEnvironment{
			SessionID: sessionID,
			Workspace: serverapi.UnavailableSessionExecutionWorkspace(
				serverapi.SessionExecutionWorkspaceUnavailableNotConfigured,
			),
			Branch: serverapi.UnavailableSessionExecutionBranch(
				serverapi.SessionExecutionBranchUnavailableNotGitRepository,
			),
			Auth: auth,
			Model: serverapi.AvailableSessionExecutionModel(serverapi.SessionExecutionModel{
				Name:     "model",
				Provider: provider,
			}),
		},
	}
}
