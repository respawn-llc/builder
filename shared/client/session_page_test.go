package client

import (
	"context"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/protoapi"
	projectpb "core/shared/protoapi/gen/kent/api/project"
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
		acceptRemoteHandshake(t, ws)
		acceptRemoteProjectAttachment(t, ws, "workspace-1", "/workspace")
		method := sessionCatalogMethod("Page")
		request := &projectpb.SessionPageRequest{}
		correlation := receiveRemoteDescriptorCall(t, ws, method, request)
		if request.ProjectId != "project-1" ||
			request.Category != projectpb.SessionCategory_SESSION_CATEGORY_SUBAGENT ||
			request.Offset == nil || *request.Offset != 50 ||
			request.Limit == nil || *request.Limit != 20 {
			t.Fatalf("session page request = %+v", request)
		}
		nextOffset := 70
		success, err := protoapi.SessionPageToProto(serverapi.SessionPageResponse{
			ProjectID:  "project-1",
			Category:   sessioncontract.SessionCategorySubagent,
			NextOffset: &nextOffset,
			Sessions: []clientui.SessionSummary{{
				SessionID: sessionID,
				Category:  sessioncontract.SessionCategorySubagent,
				UpdatedAt: time.Unix(1, 0).UTC(),
			}},
		})
		if err != nil {
			t.Fatalf("encode session page response: %v", err)
		}
		sendRemoteDescriptorResult(t, ws, method, correlation, &projectpb.SessionPageResult{
			Outcome: &projectpb.SessionPageResult_Success{Success: success},
		})
	})

	remote, err := DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], "project-1")
	if err != nil {
		t.Fatalf("DialRemoteURLForProject: %v", err)
	}
	defer func() { _ = remote.Close() }()
	response, err := remote.ListSessionPage(context.Background(), serverapi.SessionPageRequest{
		ProjectID: "project-1",
		Category:  sessioncontract.SessionCategorySubagent,
		Offset:    remoteTestIntPointer(50),
		Limit:     remoteTestIntPointer(20),
	})
	if err != nil {
		t.Fatalf("ListSessionPage: %v", err)
	}
	if len(response.Sessions) != 1 || response.Sessions[0].SessionID.String() != "session-1" {
		t.Fatalf("session page response = %+v", response)
	}
	if response.NextOffset == nil || *response.NextOffset != 70 {
		t.Fatalf("session page next offset = %v, want 70", response.NextOffset)
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
				acceptRemoteHandshake(t, ws)
				acceptRemoteProjectAttachment(t, ws, "workspace-1", "/workspace")
				method := sessionCatalogMethod("Page")
				correlation := receiveRemoteDescriptorCall(t, ws, method, &projectpb.SessionPageRequest{})
				success, err := protoapi.SessionPageToProto(test.response)
				if err != nil {
					t.Fatalf("encode session page response: %v", err)
				}
				sendRemoteDescriptorResult(t, ws, method, correlation, &projectpb.SessionPageResult{
					Outcome: &projectpb.SessionPageResult_Success{Success: success},
				})
			})

			remote, err := DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], "project-1")
			if err != nil {
				t.Fatalf("DialRemoteURLForProject: %v", err)
			}
			defer func() { _ = remote.Close() }()

			if _, err := remote.ListSessionPage(context.Background(), serverapi.SessionPageRequest{
				ProjectID: "project-1",
				Category:  sessioncontract.SessionCategoryMain,
				Offset:    remoteTestIntPointer(0),
				Limit:     remoteTestIntPointer(20),
			}); err == nil {
				t.Fatal("ListSessionPage accepted mismatched response identity")
			}
		})
	}
}
