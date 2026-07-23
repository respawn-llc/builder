package client

import (
	"testing"

	"core/shared/protocol"
)

func testProjectAttachResponse(t testing.TB, projectID string, workspaceID string, workspaceRoot string) protocol.AttachResponse {
	t.Helper()
	response, err := protocol.ProjectAttachResponse(projectID, workspaceID, workspaceRoot)
	if err != nil {
		t.Fatalf("ProjectAttachResponse: %v", err)
	}
	return response
}

func testProjectAttachResponseForIntent(t testing.TB, intent *remoteAttachmentIntent, workspaceID string, workspaceRoot string) protocol.AttachResponse {
	t.Helper()
	request, present := intent.projectRequest()
	if !present {
		t.Fatal("project attachment intent is required")
	}
	response, err := protocol.ProjectAttachResponseForRequest(request, workspaceID, workspaceRoot)
	if err != nil {
		t.Fatalf("ProjectAttachResponseForRequest: %v", err)
	}
	return response
}

func testSessionAttachResponse(t testing.TB, projectID string, workspaceID string, workspaceRoot string, sessionID string) protocol.AttachResponse {
	t.Helper()
	response, err := protocol.SessionAttachResponse(projectID, workspaceID, workspaceRoot, sessionID)
	if err != nil {
		t.Fatalf("SessionAttachResponse: %v", err)
	}
	return response
}
