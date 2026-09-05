package client

import (
	"testing"

	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
)

func TestSessionRemovalInternalFailurePreservesServerCause(t *testing.T) {
	cause := "server-authored Session removal failure"
	internal := &sharedpb.InternalFailureDetails{Cause: &cause}
	failures := []error{
		sessionArchiveGeneratedError(&sessionlaunchpb.SessionArchiveError{
			Code: "internal_failure",
			Detail: &sessionlaunchpb.SessionArchiveError_InternalFailure{
				InternalFailure: internal,
			},
		}),
		sessionDeleteGeneratedError(&sessionlaunchpb.SessionDeleteError{
			Code: "internal_failure",
			Detail: &sessionlaunchpb.SessionDeleteError_InternalFailure{
				InternalFailure: internal,
			},
		}),
	}
	for _, err := range failures {
		if err == nil || err.Error() != cause {
			t.Fatalf("Session removal error = %v, want server cause", err)
		}
	}
}
