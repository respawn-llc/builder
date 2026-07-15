package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"

	"golang.org/x/net/websocket"
)

func TestRemoteSessionPageRoundTripUsesProjectAttachment(t *testing.T) {
	sessionID, err := runtimeids.ParseSessionID("session-1")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		for {
			if err := websocket.JSON.Receive(ws, &req); err != nil {
				if errors.Is(err, io.EOF) {
					return
				}
				t.Fatalf("receive session page request: %v", err)
			}
			switch req.Method {
			case protocol.MethodAttachProject:
				if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.AttachResponse{Kind: "project", ProjectID: "project-1"})); err != nil {
					t.Fatalf("send attach response: %v", err)
				}
			case protocol.MethodSessionPage:
				var params serverapi.SessionPageRequest
				if err := json.Unmarshal(req.Params, &params); err != nil {
					t.Fatalf("decode session page request: %v", err)
				}
				if err := params.Validate(); err != nil {
					t.Fatalf("validate session page request: %v", err)
				}
				if params.ProjectID != "project-1" || params.Category != sessioncontract.SessionCategorySubagent || params.Position.Kind() != serverapi.SessionPagePositionNewest {
					t.Fatalf("session page request = %+v", params)
				}
				response := serverapi.SessionPageResponse{
					ProjectID: "project-1",
					Category:  sessioncontract.SessionCategorySubagent,
					Sessions: []clientui.SessionSummary{{
						SessionID: sessionID,
						Category:  sessioncontract.SessionCategorySubagent,
						UpdatedAt: time.Unix(1, 0).UTC(),
					}},
				}
				if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, response)); err != nil {
					t.Fatalf("send session page response: %v", err)
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
	response, err := remote.ListSessionPage(context.Background(), serverapi.SessionPageRequest{
		ProjectID: "project-1",
		Category:  sessioncontract.SessionCategorySubagent,
		PageSize:  20,
		Position:  serverapi.NewestSessionPagePosition(),
	})
	if err != nil {
		t.Fatalf("ListSessionPage: %v", err)
	}
	if len(response.Sessions) != 1 || response.Sessions[0].SessionID.String() != "session-1" {
		t.Fatalf("session page response = %+v", response)
	}
}

func TestRemoteSessionPageRejectsResponseIdentityMismatch(t *testing.T) {
	tests := []struct {
		name     string
		response serverapi.SessionPageResponse
	}{
		{
			name: "project",
			response: serverapi.SessionPageResponse{
				ProjectID: "different-project",
				Category:  sessioncontract.SessionCategoryMain,
			},
		},
		{
			name: "category",
			response: serverapi.SessionPageResponse{
				ProjectID: "project-1",
				Category:  sessioncontract.SessionCategorySubagent,
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
						t.Fatalf("receive session page request: %v", err)
					}
					switch req.Method {
					case protocol.MethodAttachProject:
						if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.AttachResponse{
							Kind:      "project",
							ProjectID: "project-1",
						})); err != nil {
							t.Fatalf("send attach response: %v", err)
						}
					case protocol.MethodSessionPage:
						if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, test.response)); err != nil {
							t.Fatalf("send session page response: %v", err)
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

			if _, err := remote.ListSessionPage(context.Background(), serverapi.SessionPageRequest{
				ProjectID: "project-1",
				Category:  sessioncontract.SessionCategoryMain,
				PageSize:  20,
				Position:  serverapi.NewestSessionPagePosition(),
			}); err == nil {
				t.Fatal("ListSessionPage accepted mismatched response identity")
			}
		})
	}
}
