package client

import "testing"

func TestRemoteAttachmentIntentRejectsMismatchedResponses(t *testing.T) {
	projectIntent, err := newRemoteProjectWorkspaceIDAttachmentIntent("project-1", "workspace-1")
	if err != nil {
		t.Fatalf("new project intent: %v", err)
	}
	projectMismatch := testProjectAttachResponse(t, "project-2", "workspace-1", "/workspace")
	if err := projectIntent.validateResponse(projectMismatch); err == nil {
		t.Fatal("expected project mismatch error")
	}
	workspaceMismatch := testProjectAttachResponse(t, "project-1", "workspace-2", "/workspace")
	if err := projectIntent.validateResponse(workspaceMismatch); err == nil {
		t.Fatal("expected workspace mismatch error")
	}

	rootIntent, err := newRemoteProjectWorkspaceRootAttachmentIntent("project-1", "/workspace-a")
	if err != nil {
		t.Fatalf("new root intent: %v", err)
	}
	rootRequest, err := newRemoteProjectWorkspaceRootAttachmentIntent("project-1", "/workspace-b")
	if err != nil {
		t.Fatalf("new mismatched root intent: %v", err)
	}
	rootResponse := testProjectAttachResponseForIntent(t, rootRequest, "workspace-2", "/workspace-b")
	if err := rootIntent.validateResponse(rootResponse); err == nil {
		t.Fatal("expected workspace root mismatch error")
	}

	sessionIntent, err := newRemoteSessionAttachmentIntent("session-1")
	if err != nil {
		t.Fatalf("new session intent: %v", err)
	}
	sessionMismatch := testSessionAttachResponse(t, "project-1", "workspace-1", "/workspace", "session-2")
	if err := sessionIntent.validateResponse(sessionMismatch); err == nil {
		t.Fatal("expected session mismatch error")
	}
}

func TestValidateReattachedBindingRejectsChangedAuthority(t *testing.T) {
	expected := testProjectAttachResponse(t, "project-1", "workspace-1", "/workspace")
	changed := testProjectAttachResponse(t, "project-1", "workspace-1", "/other-workspace")
	if err := validateReattachedBinding(&expected, &changed); err == nil {
		t.Fatal("expected changed reconnect attachment error")
	}
}
