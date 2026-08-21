package serverapi

import (
	"errors"
	"fmt"
	"strings"

	"core/shared/clientui"
)

var ErrWorkspaceNotRegistered = errors.New("workspace is not registered")
var ErrWorkspaceBindingAmbiguous = errors.New("workspace binding is ambiguous")
var ErrProjectNotFound = errors.New("project not found")
var ErrProjectUnavailable = errors.New("project is unavailable")
var ErrWorkspacePathIdentity = errors.New("workspace path identity could not be recovered")
var ErrProjectKeyConflict = errors.New("project key is already in use")
var ErrWorkspaceAlreadyBound = errors.New("workspace is already bound")
var ErrWorkspacePathMissing = errors.New("workspace path does not exist")

type ProjectKeyConflictError struct {
	ProjectKey string
}

func (e ProjectKeyConflictError) Error() string {
	return fmt.Sprintf("%s: %q", ErrProjectKeyConflict, strings.TrimSpace(e.ProjectKey))
}

func (e ProjectKeyConflictError) Is(target error) bool {
	return target == ErrProjectKeyConflict
}

func AsProjectKeyConflict(err error) (ProjectKeyConflictError, bool) {
	var conflict ProjectKeyConflictError
	if !errors.As(err, &conflict) {
		return ProjectKeyConflictError{}, false
	}
	return conflict, true
}

type WorkspaceBindingAmbiguousError struct {
	CanonicalRoot string
	ProjectIDs    []string
}

func (e WorkspaceBindingAmbiguousError) Error() string {
	trimmedRoot := strings.TrimSpace(e.CanonicalRoot)
	if len(e.ProjectIDs) == 0 {
		return fmt.Sprintf("%s: %q", ErrWorkspaceBindingAmbiguous, trimmedRoot)
	}
	return fmt.Sprintf("%s: %q is attached to projects %s", ErrWorkspaceBindingAmbiguous, trimmedRoot, strings.Join(e.ProjectIDs, ", "))
}

func (e WorkspaceBindingAmbiguousError) Is(target error) bool {
	return target == ErrWorkspaceBindingAmbiguous
}

func AsWorkspaceBindingAmbiguous(err error) (WorkspaceBindingAmbiguousError, bool) {
	var ambiguous WorkspaceBindingAmbiguousError
	if !errors.As(err, &ambiguous) {
		return WorkspaceBindingAmbiguousError{}, false
	}
	return ambiguous, true
}

type ProjectUnavailableError struct {
	ProjectID    string
	RootPath     string
	Availability clientui.ProjectAvailability
}

func (e ProjectUnavailableError) Error() string {
	trimmedProjectID := strings.TrimSpace(e.ProjectID)
	trimmedRootPath := strings.TrimSpace(e.RootPath)
	availability := strings.TrimSpace(string(e.Availability))
	if availability == "" {
		availability = string(clientui.ProjectAvailabilityInaccessible)
	}
	if trimmedProjectID == "" {
		return fmt.Sprintf("project root %q is %s", trimmedRootPath, availability)
	}
	return fmt.Sprintf("project %q root %q is %s", trimmedProjectID, trimmedRootPath, availability)
}

func (e ProjectUnavailableError) Is(target error) bool {
	return target == ErrProjectUnavailable
}

func AsProjectUnavailable(err error) (ProjectUnavailableError, bool) {
	var unavailable ProjectUnavailableError
	if !errors.As(err, &unavailable) {
		return ProjectUnavailableError{}, false
	}
	return unavailable, true
}

type WorkspacePathIdentityError struct {
	WorkspaceRoot string
	Cause         error
}

func (e WorkspacePathIdentityError) Error() string {
	root := strings.TrimSpace(e.WorkspaceRoot)
	if e.Cause == nil {
		return fmt.Sprintf("%s: %q", ErrWorkspacePathIdentity, root)
	}
	return fmt.Sprintf("%s: %q: %v", ErrWorkspacePathIdentity, root, e.Cause)
}

func (e WorkspacePathIdentityError) Unwrap() error {
	return e.Cause
}

func (e WorkspacePathIdentityError) Is(target error) bool {
	return target == ErrWorkspacePathIdentity
}

func AsWorkspacePathIdentity(err error) (WorkspacePathIdentityError, bool) {
	var identity WorkspacePathIdentityError
	if !errors.As(err, &identity) {
		return WorkspacePathIdentityError{}, false
	}
	return identity, true
}
