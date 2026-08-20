package protoapi

import (
	"fmt"

	"core/shared/clientui"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	"core/shared/serverapi"
)

func WorkspaceBindingAmbiguousToProto(value serverapi.WorkspaceBindingAmbiguousError) (*projectpb.WorkspaceBindingAmbiguousDetails, error) {
	details := &projectpb.WorkspaceBindingAmbiguousDetails{
		CanonicalRoot: value.CanonicalRoot,
		ProjectIds:    append([]string(nil), value.ProjectIDs...),
	}
	if err := Validate(details); err != nil {
		return nil, fmt.Errorf("convert ambiguous workspace binding to protobuf: %w", err)
	}
	return details, nil
}

func WorkspaceBindingAmbiguousFromProto(details *projectpb.WorkspaceBindingAmbiguousDetails) (serverapi.WorkspaceBindingAmbiguousError, error) {
	if err := Validate(details); err != nil {
		return serverapi.WorkspaceBindingAmbiguousError{}, fmt.Errorf("convert ambiguous workspace binding from protobuf: %w", err)
	}
	return serverapi.WorkspaceBindingAmbiguousError{
		CanonicalRoot: details.CanonicalRoot,
		ProjectIDs:    append([]string(nil), details.ProjectIds...),
	}, nil
}

func ProjectUnavailableToProto(value serverapi.ProjectUnavailableError) (*projectpb.ProjectUnavailableDetails, error) {
	availability, err := projectAvailabilityToProto(value.Availability)
	if err != nil {
		return nil, err
	}
	details := &projectpb.ProjectUnavailableDetails{
		ProjectId:    value.ProjectID,
		RootPath:     value.RootPath,
		Availability: availability,
	}
	if err := Validate(details); err != nil {
		return nil, fmt.Errorf("convert unavailable project to protobuf: %w", err)
	}
	return details, nil
}

func ProjectUnavailableFromProto(details *projectpb.ProjectUnavailableDetails) (serverapi.ProjectUnavailableError, error) {
	if err := Validate(details); err != nil {
		return serverapi.ProjectUnavailableError{}, fmt.Errorf("convert unavailable project from protobuf: %w", err)
	}
	availability, err := projectAvailabilityFromProto(details.Availability)
	if err != nil {
		return serverapi.ProjectUnavailableError{}, err
	}
	return serverapi.ProjectUnavailableError{
		ProjectID:    details.ProjectId,
		RootPath:     details.RootPath,
		Availability: availability,
	}, nil
}

func WorkspaceNotRegisteredFromProto(details *projectpb.WorkspaceNotRegisteredDetails) error {
	if err := Validate(details); err != nil {
		return err
	}
	return serverapi.ErrWorkspaceNotRegistered
}

func WorkspacePathIdentityFromProto(details *projectpb.WorkspacePathIdentityDetails) error {
	if err := Validate(details); err != nil {
		return err
	}
	return serverapi.WorkspacePathIdentityError{WorkspaceRoot: details.WorkspaceRoot}
}

func WorkspaceMutationFromProto(details *projectpb.WorkspaceMutationDetails) error {
	if err := Validate(details); err != nil {
		return err
	}
	return &serverapi.WorkspaceMutationError{ProjectID: details.ProjectId, WorkspaceID: details.WorkspaceId}
}

func WorkspaceDetachConflictFromProto(details *projectpb.WorkspaceDetachConflictDetails) error {
	if err := Validate(details); err != nil {
		return err
	}
	return &serverapi.WorkspaceDetachConflictError{ProjectID: details.ProjectId, WorkspaceID: details.WorkspaceId}
}

func WorkspaceBindingAmbiguousMutationFromProto(details *projectpb.WorkspaceBindingAmbiguousMutationDetails) error {
	if err := Validate(details); err != nil {
		return err
	}
	return serverapi.WorkspaceBindingAmbiguousError{ProjectIDs: append([]string(nil), details.ProjectIds...)}
}

func projectAvailabilityToProto(value clientui.ProjectAvailability) (projectpb.ProjectAvailability, error) {
	switch value {
	case clientui.ProjectAvailabilityAvailable:
		return projectpb.ProjectAvailability_PROJECT_AVAILABILITY_AVAILABLE, nil
	case clientui.ProjectAvailabilityMissing:
		return projectpb.ProjectAvailability_PROJECT_AVAILABILITY_MISSING, nil
	case clientui.ProjectAvailabilityInaccessible:
		return projectpb.ProjectAvailability_PROJECT_AVAILABILITY_INACCESSIBLE, nil
	case clientui.ProjectAvailabilityUnlinked:
		return projectpb.ProjectAvailability_PROJECT_AVAILABILITY_UNLINKED, nil
	default:
		return projectpb.ProjectAvailability_PROJECT_AVAILABILITY_UNSPECIFIED, fmt.Errorf("project availability %q is unsupported", value)
	}
}

func projectAvailabilityFromProto(value projectpb.ProjectAvailability) (clientui.ProjectAvailability, error) {
	switch value {
	case projectpb.ProjectAvailability_PROJECT_AVAILABILITY_AVAILABLE:
		return clientui.ProjectAvailabilityAvailable, nil
	case projectpb.ProjectAvailability_PROJECT_AVAILABILITY_MISSING:
		return clientui.ProjectAvailabilityMissing, nil
	case projectpb.ProjectAvailability_PROJECT_AVAILABILITY_INACCESSIBLE:
		return clientui.ProjectAvailabilityInaccessible, nil
	case projectpb.ProjectAvailability_PROJECT_AVAILABILITY_UNLINKED:
		return clientui.ProjectAvailabilityUnlinked, nil
	default:
		return "", fmt.Errorf("protobuf project availability %v is unsupported", value)
	}
}
