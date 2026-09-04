package client

import (
	"errors"
	"fmt"
	"strings"
)

type remoteProjectWorkspaceSelection struct {
	workspaceID   *string
	workspaceRoot *string
}

type remoteProjectAttachmentIntent struct {
	projectID string
	workspace *remoteProjectWorkspaceSelection
}

type remoteSessionAttachmentIntent struct {
	sessionID          string
	reattachCapability *string
}

type remoteAttachmentIntent struct {
	project *remoteProjectAttachmentIntent
	session *remoteSessionAttachmentIntent
}

type remoteAttachment struct {
	project *ProjectAttachment
	session *remoteSessionAttachment
}

// ProjectAttachment is the caller-facing binding established by Connection
// setup. The generated Connection messages remain confined to the wire edge.
type ProjectAttachment struct {
	ProjectID     string
	WorkspaceID   string
	WorkspaceRoot string
	selection     *remoteProjectAttachmentSelection
}

type remoteSessionAttachment struct {
	projectID          string
	workspaceID        string
	workspaceRoot      string
	sessionID          string
	reattachCapability string
}

type remoteProjectAttachmentSelection struct {
	workspaceID   *string
	requestedRoot *string
	canonicalRoot *string
}

func newRemoteDefaultProjectAttachmentIntent(projectID string) (*remoteAttachmentIntent, error) {
	if err := validateRemoteAttachmentField("project ID", projectID); err != nil {
		return nil, err
	}
	return newRemoteProjectAttachmentIntent(remoteProjectAttachmentIntent{projectID: projectID}), nil
}

func newRemoteProjectWorkspaceIDAttachmentIntent(projectID string, workspaceID string) (*remoteAttachmentIntent, error) {
	if err := validateRemoteAttachmentField("project ID", projectID); err != nil {
		return nil, err
	}
	if err := validateRemoteAttachmentField("workspace ID", workspaceID); err != nil {
		return nil, err
	}
	return newRemoteProjectAttachmentIntent(remoteProjectAttachmentIntent{
		projectID: projectID,
		workspace: &remoteProjectWorkspaceSelection{workspaceID: &workspaceID},
	}), nil
}

func newRemoteProjectWorkspaceRootAttachmentIntent(projectID string, workspaceRoot string) (*remoteAttachmentIntent, error) {
	if err := validateRemoteAttachmentField("project ID", projectID); err != nil {
		return nil, err
	}
	if err := validateRemoteAttachmentField("workspace root", workspaceRoot); err != nil {
		return nil, err
	}
	return newRemoteProjectAttachmentIntent(remoteProjectAttachmentIntent{
		projectID: projectID,
		workspace: &remoteProjectWorkspaceSelection{workspaceRoot: &workspaceRoot},
	}), nil
}

func newRemoteProjectAttachmentIntent(request remoteProjectAttachmentIntent) *remoteAttachmentIntent {
	return &remoteAttachmentIntent{project: &request}
}

func newRemoteSessionAttachmentIntent(sessionID string) (*remoteAttachmentIntent, error) {
	if err := validateRemoteAttachmentField("session ID", sessionID); err != nil {
		return nil, errRemoteSessionIDRequired
	}
	return &remoteAttachmentIntent{session: &remoteSessionAttachmentIntent{sessionID: sessionID}}, nil
}

func newRemoteSessionReattachmentIntent(attachment remoteSessionAttachment) (*remoteAttachmentIntent, error) {
	if err := validateRemoteAttachmentField("session ID", attachment.sessionID); err != nil {
		return nil, err
	}
	if err := validateRemoteAttachmentField("Session reattach capability", attachment.reattachCapability); err != nil {
		return nil, err
	}
	capability := attachment.reattachCapability
	return &remoteAttachmentIntent{session: &remoteSessionAttachmentIntent{
		sessionID:          attachment.sessionID,
		reattachCapability: &capability,
	}}, nil
}

func (i *remoteAttachmentIntent) projectRequest() (remoteProjectAttachmentIntent, bool) {
	if i == nil || i.project == nil || i.session != nil {
		return remoteProjectAttachmentIntent{}, false
	}
	return *i.project, true
}

func (i *remoteAttachmentIntent) sessionRequest() (remoteSessionAttachmentIntent, bool) {
	if i == nil || i.session == nil || i.project != nil {
		return remoteSessionAttachmentIntent{}, false
	}
	return *i.session, true
}

func (i *remoteAttachmentIntent) sessionID() (string, bool) {
	request, present := i.sessionRequest()
	return request.sessionID, present
}

func (i *remoteAttachmentIntent) validateResponse(response remoteAttachment) error {
	if request, present := i.projectRequest(); present {
		if response.project == nil || response.session != nil {
			return errors.New("project attach returned a non-project attachment")
		}
		binding := response.project
		if binding.ProjectID != request.projectID {
			return fmt.Errorf("project attach returned project %q, want %q", binding.ProjectID, request.projectID)
		}
		if (request.workspace == nil) != (binding.selection == nil) {
			return errors.New("project attach returned a different workspace selection")
		}
		if request.workspace == nil {
			return nil
		}
		if request.workspace.workspaceID != nil {
			if binding.selection.workspaceID == nil || *binding.selection.workspaceID != *request.workspace.workspaceID {
				return fmt.Errorf("project attach returned workspace %q, want %q", binding.WorkspaceID, *request.workspace.workspaceID)
			}
			return nil
		}
		if binding.selection.requestedRoot == nil ||
			binding.selection.canonicalRoot == nil ||
			*binding.selection.requestedRoot != *request.workspace.workspaceRoot {
			return fmt.Errorf("project attach returned a different requested workspace root")
		}
		if *binding.selection.canonicalRoot != binding.WorkspaceRoot {
			return errors.New("project attach returned inconsistent canonical workspace root")
		}
		return nil
	}
	request, present := i.sessionRequest()
	if !present {
		return errors.New("remote attachment intent is invalid")
	}
	if response.session == nil || response.project != nil {
		return errors.New("session attach returned a non-session attachment")
	}
	if response.session.sessionID != request.sessionID {
		return fmt.Errorf("session attach returned session %q, want %q", response.session.sessionID, request.sessionID)
	}
	if err := validateRemoteAttachmentField("Session reattach capability", response.session.reattachCapability); err != nil {
		return err
	}
	return nil
}

func remoteAttachmentProjectBinding(response *remoteAttachment) (ProjectAttachment, bool) {
	if response == nil {
		return ProjectAttachment{}, false
	}
	if response.project != nil && response.session == nil {
		return *response.project, true
	}
	if response.session != nil && response.project == nil {
		return ProjectAttachment{
			ProjectID:     response.session.projectID,
			WorkspaceID:   response.session.workspaceID,
			WorkspaceRoot: response.session.workspaceRoot,
		}, true
	}
	return ProjectAttachment{}, false
}

func validateReattachedBinding(expected *remoteAttachment, actual *remoteAttachment) error {
	switch {
	case expected == nil && actual == nil:
		return nil
	case expected == nil:
		return errors.New("unscoped remote reconnect unexpectedly attached")
	case actual == nil:
		return errors.New("attached remote reconnect omitted attachment")
	case expected.session != nil &&
		expected.project == nil &&
		actual.session != nil &&
		actual.project == nil &&
		expected.session.sessionID == actual.session.sessionID:
		return nil
	case expected.equal(*actual):
		return nil
	default:
		return errors.New("remote reconnect attachment changed")
	}
}

func (a remoteAttachment) equal(other remoteAttachment) bool {
	switch {
	case a.project != nil && a.session == nil && other.project != nil && other.session == nil:
		return a.project.equal(*other.project)
	case a.session != nil && a.project == nil && other.session != nil && other.project == nil:
		return *a.session == *other.session
	default:
		return false
	}
}

func (a ProjectAttachment) equal(other ProjectAttachment) bool {
	if a.ProjectID != other.ProjectID ||
		a.WorkspaceID != other.WorkspaceID ||
		a.WorkspaceRoot != other.WorkspaceRoot {
		return false
	}
	switch {
	case a.selection == nil && other.selection == nil:
		return true
	case a.selection == nil || other.selection == nil:
		return false
	case a.selection.workspaceID != nil || other.selection.workspaceID != nil:
		return a.selection.workspaceID != nil &&
			other.selection.workspaceID != nil &&
			*a.selection.workspaceID == *other.selection.workspaceID
	default:
		return a.selection.requestedRoot != nil &&
			other.selection.requestedRoot != nil &&
			a.selection.canonicalRoot != nil &&
			other.selection.canonicalRoot != nil &&
			*a.selection.requestedRoot == *other.selection.requestedRoot &&
			*a.selection.canonicalRoot == *other.selection.canonicalRoot
	}
}

func validateRemoteAttachmentField(name string, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must not have leading or trailing whitespace", name)
	}
	return nil
}
