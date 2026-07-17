package client

import (
	"errors"
	"fmt"

	"core/shared/protocol"
)

type remoteProjectAttachmentIntent struct {
	request protocol.AttachProjectRequest
}

type remoteSessionAttachmentIntent struct {
	request protocol.AttachSessionRequest
}

type remoteAttachmentIntent struct {
	project *remoteProjectAttachmentIntent
	session *remoteSessionAttachmentIntent
}

func newRemoteDefaultProjectAttachmentIntent(projectID string) (*remoteAttachmentIntent, error) {
	request, err := protocol.AttachProjectRequestForDefaultWorkspace(projectID)
	if err != nil {
		return nil, err
	}
	return newRemoteProjectAttachmentIntent(request), nil
}

func newRemoteProjectWorkspaceIDAttachmentIntent(projectID string, workspaceID string) (*remoteAttachmentIntent, error) {
	request, err := protocol.AttachProjectRequestForWorkspaceID(projectID, workspaceID)
	if err != nil {
		return nil, err
	}
	return newRemoteProjectAttachmentIntent(request), nil
}

func newRemoteProjectWorkspaceRootAttachmentIntent(projectID string, workspaceRoot string) (*remoteAttachmentIntent, error) {
	request, err := protocol.AttachProjectRequestForWorkspaceRoot(projectID, workspaceRoot)
	if err != nil {
		return nil, err
	}
	return newRemoteProjectAttachmentIntent(request), nil
}

func newRemoteProjectAttachmentIntent(request protocol.AttachProjectRequest) *remoteAttachmentIntent {
	return &remoteAttachmentIntent{project: &remoteProjectAttachmentIntent{request: request}}
}

func newRemoteSessionAttachmentIntent(sessionID string) (*remoteAttachmentIntent, error) {
	request := protocol.AttachSessionRequest{SessionID: sessionID}
	if err := request.Validate(); err != nil {
		return nil, errRemoteSessionIDRequired
	}
	return &remoteAttachmentIntent{session: &remoteSessionAttachmentIntent{request: request}}, nil
}

func (i *remoteAttachmentIntent) projectRequest() (protocol.AttachProjectRequest, bool) {
	if i == nil || i.project == nil || i.session != nil {
		return protocol.AttachProjectRequest{}, false
	}
	return i.project.request, true
}

func (i *remoteAttachmentIntent) sessionRequest() (protocol.AttachSessionRequest, bool) {
	if i == nil || i.session == nil || i.project != nil {
		return protocol.AttachSessionRequest{}, false
	}
	return i.session.request, true
}

func (i *remoteAttachmentIntent) sessionID() (string, bool) {
	request, present := i.sessionRequest()
	return request.SessionID, present
}

func (i *remoteAttachmentIntent) validateResponse(response protocol.AttachResponse) error {
	if request, present := i.projectRequest(); present {
		binding, projectResponse := response.Project()
		if !projectResponse {
			return errors.New("project attach returned a non-project attachment")
		}
		if binding.ProjectID != request.ProjectID {
			return fmt.Errorf("project attach returned project %q, want %q", binding.ProjectID, request.ProjectID)
		}
		requestedWorkspace, requested := request.Workspace()
		responseWorkspace, returned := binding.WorkspaceSelection()
		if requested != returned {
			return errors.New("project attach returned a different workspace selection")
		}
		if !requested {
			return nil
		}
		if requestedID, selectedByID := requestedWorkspace.WorkspaceID(); selectedByID {
			responseID, returnedByID := responseWorkspace.WorkspaceID()
			if !returnedByID || responseID != requestedID {
				return fmt.Errorf("project attach returned workspace %q, want %q", responseID, requestedID)
			}
			return nil
		}
		requestedRoot, _ := requestedWorkspace.WorkspaceRoot()
		returnedRoot, canonicalRoot, returnedByRoot := responseWorkspace.WorkspaceRoot()
		if !returnedByRoot || returnedRoot != requestedRoot {
			return fmt.Errorf("project attach returned workspace root %q, want %q", returnedRoot, requestedRoot)
		}
		if canonicalRoot != binding.WorkspaceRoot {
			return errors.New("project attach returned inconsistent canonical workspace root")
		}
		return nil
	}
	request, present := i.sessionRequest()
	if !present {
		return errors.New("remote attachment intent is invalid")
	}
	binding, sessionResponse := response.Session()
	if !sessionResponse {
		return errors.New("session attach returned a non-session attachment")
	}
	if binding.SessionID != request.SessionID {
		return fmt.Errorf("session attach returned session %q, want %q", binding.SessionID, request.SessionID)
	}
	return nil
}

func remoteAttachmentProjectBinding(response *protocol.AttachResponse) (protocol.ProjectAttachment, bool) {
	if response == nil {
		return protocol.ProjectAttachment{}, false
	}
	if project, present := response.Project(); present {
		return project, true
	}
	if session, present := response.Session(); present {
		return protocol.ProjectAttachment{
			ProjectID:     session.ProjectID,
			WorkspaceID:   session.WorkspaceID,
			WorkspaceRoot: session.WorkspaceRoot,
		}, true
	}
	return protocol.ProjectAttachment{}, false
}

func validateReattachedBinding(expected *protocol.AttachResponse, actual *protocol.AttachResponse) error {
	switch {
	case expected == nil && actual == nil:
		return nil
	case expected == nil:
		return errors.New("unscoped remote reconnect unexpectedly attached")
	case actual == nil:
		return errors.New("attached remote reconnect omitted attachment")
	case expected.Equal(*actual):
		return nil
	default:
		return errors.New("remote reconnect attachment changed")
	}
}
