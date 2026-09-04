package transport

import (
	"context"
	"errors"

	"core/server/metadata"
	"core/server/session"
	"core/server/sessionruntime"
	"core/shared/apicontract"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"

	"google.golang.org/protobuf/proto"
)

func registerSessionRemovalGatewayBinaryBindings(bindings map[string]gatewayBinaryBinding) error {
	service := sessionlaunchpb.File_kent_api_session_launch_session_lifecycle_proto.Services().
		ByName("SessionLifecycleService")
	return errors.Join(
		registerGatewayBinaryUnary(
			bindings,
			service,
			"Archive",
			gatewayBinaryCoreActiveOrdinary,
			func() *sessionlaunchpb.SessionArchiveRequest {
				return &sessionlaunchpb.SessionArchiveRequest{}
			},
			nil,
			func(
				g *Gateway,
				ctx context.Context,
				_ *connectionState,
				request *sessionlaunchpb.SessionArchiveRequest,
			) (*sessionlaunchpb.SessionArchiveSuccess, error) {
				return apicontract.SessionLifecycleService.ArchiveSession(
					g.deps.SessionLifecycleClient(),
					ctx,
					request,
				)
			},
			func(
				_ *Gateway,
				_ *connectionState,
				request *sessionlaunchpb.SessionArchiveRequest,
				err error,
			) proto.Message {
				return sessionRemovalFailureDetails(request, err)
			},
		),
		registerGatewayBinaryUnary(
			bindings,
			service,
			"Delete",
			gatewayBinaryCoreActiveOrdinary,
			func() *sessionlaunchpb.SessionDeleteRequest {
				return &sessionlaunchpb.SessionDeleteRequest{}
			},
			nil,
			func(
				g *Gateway,
				ctx context.Context,
				_ *connectionState,
				request *sessionlaunchpb.SessionDeleteRequest,
			) (*sessionlaunchpb.SessionDeleteSuccess, error) {
				return apicontract.SessionLifecycleService.DeleteSession(
					g.deps.SessionLifecycleClient(),
					ctx,
					request,
				)
			},
			func(
				_ *Gateway,
				_ *connectionState,
				request *sessionlaunchpb.SessionDeleteRequest,
				err error,
			) proto.Message {
				return sessionRemovalFailureDetails(request, err)
			},
		),
	)
}

func sessionRemovalRequestID(request proto.Message) *string {
	var sessionID string
	switch typed := request.(type) {
	case *sessionlaunchpb.SessionArchiveRequest:
		if typed == nil {
			return nil
		}
		sessionID = typed.SessionId
	case *sessionlaunchpb.SessionDeleteRequest:
		if typed == nil {
			return nil
		}
		sessionID = typed.SessionId
	default:
		return nil
	}
	return &sessionID
}

func sessionRemovalFailureDetails(request proto.Message, err error) proto.Message {
	sessionID := sessionRemovalRequestID(request)
	_, deleteRequest := request.(*sessionlaunchpb.SessionDeleteRequest)
	if errors.Is(err, session.ErrSessionNotFound) {
		if sessionID == nil {
			return binaryInternalFailure(err)
		}
		return &sessionlaunchpb.SessionNotFoundDetails{SessionId: *sessionID}
	}
	var runtimeInUse *sessionruntime.SessionInUseError
	var metadataInUse *metadata.SessionInUseError
	if errors.As(err, &runtimeInUse) ||
		(deleteRequest && errors.As(err, &metadataInUse)) {
		if sessionID == nil {
			return binaryInternalFailure(err)
		}
		return &sessionlaunchpb.SessionInUseDetails{SessionId: *sessionID}
	}
	var invalidPath *session.InvalidArchiveOutputPathError
	if errors.As(err, &invalidPath) {
		return &sessionlaunchpb.InvalidArchiveOutputPathDetails{
			Path:   invalidPath.Path,
			Reason: invalidArchiveOutputPathReason(invalidPath.Reason),
		}
	}
	var outputExists *session.ArchiveOutputExistsError
	if errors.As(err, &outputExists) {
		return &sessionlaunchpb.ArchiveOutputExistsDetails{Path: outputExists.Path}
	}
	return binaryInternalFailure(err)
}

func invalidArchiveOutputPathReason(
	reason session.InvalidArchiveOutputPathReason,
) sessionlaunchpb.InvalidArchiveOutputPathReason {
	switch reason {
	case session.InvalidArchiveOutputPathReasonAbsolute:
		return sessionlaunchpb.InvalidArchiveOutputPathReason_INVALID_ARCHIVE_OUTPUT_PATH_REASON_NOT_ABSOLUTE
	case session.InvalidArchiveOutputPathReasonSuffix:
		return sessionlaunchpb.InvalidArchiveOutputPathReason_INVALID_ARCHIVE_OUTPUT_PATH_REASON_INVALID_SUFFIX
	default:
		return sessionlaunchpb.InvalidArchiveOutputPathReason_INVALID_ARCHIVE_OUTPUT_PATH_REASON_UNSPECIFIED
	}
}
