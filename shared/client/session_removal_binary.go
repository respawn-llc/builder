package client

import (
	"context"
	"errors"
	"fmt"

	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"

	"google.golang.org/protobuf/reflect/protoreflect"
)

type SessionArchiveFailureError struct {
	Failure *sessionlaunchpb.SessionArchiveError
}

func (e *SessionArchiveFailureError) Error() string {
	if e == nil || e.Failure == nil {
		return "Session archive failed"
	}
	return fmt.Sprintf("Session archive failed with code %q", e.Failure.Code)
}

type SessionDeleteFailureError struct {
	Failure *sessionlaunchpb.SessionDeleteError
}

func (e *SessionDeleteFailureError) Error() string {
	if e == nil || e.Failure == nil {
		return "Session deletion failed"
	}
	return fmt.Sprintf("Session deletion failed with code %q", e.Failure.Code)
}

func sessionLifecycleMethod(name protoreflect.Name) protoreflect.MethodDescriptor {
	return bootstrapMethod(
		sessionlaunchpb.File_kent_api_session_launch_session_lifecycle_proto,
		"SessionLifecycleService",
		name,
	)
}

func (c *Remote) ArchiveSession(
	ctx context.Context,
	request *sessionlaunchpb.SessionArchiveRequest,
) (*sessionlaunchpb.SessionArchiveSuccess, error) {
	response, err := callGeneratedBinary(
		c,
		ctx,
		sessionLifecycleMethod("Archive"),
		request,
		&sessionlaunchpb.SessionArchiveResult{},
		func(failure *sessionlaunchpb.SessionArchiveError) error {
			return &SessionArchiveFailureError{Failure: failure}
		},
	)
	if err != nil {
		return nil, err
	}
	if response.SessionId != request.SessionId || response.OutputPath != request.OutputPath {
		return nil, invalidResponseError(
			"Session archive",
			errors.New("response does not match the requested Session and output path"),
		)
	}
	return response, nil
}

func (c *Remote) DeleteSession(
	ctx context.Context,
	request *sessionlaunchpb.SessionDeleteRequest,
) (*sessionlaunchpb.SessionDeleteSuccess, error) {
	response, err := callGeneratedBinary(
		c,
		ctx,
		sessionLifecycleMethod("Delete"),
		request,
		&sessionlaunchpb.SessionDeleteResult{},
		func(failure *sessionlaunchpb.SessionDeleteError) error {
			return &SessionDeleteFailureError{Failure: failure}
		},
	)
	if err != nil {
		return nil, err
	}
	if response.SessionId != request.SessionId {
		return nil, invalidResponseError(
			"Session deletion",
			errors.New("response does not match the requested Session"),
		)
	}
	return response, nil
}
