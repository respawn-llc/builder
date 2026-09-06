package protoapi

import (
	"errors"
	"testing"

	pb "core/shared/protoapi/gen/kent/api/chat_settings"
	"core/shared/runtimeids"
	"core/shared/sessioncontract"
)

func TestChatSettingsErrorFromProtoPreservesSessionNotFound(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	err := ChatSettingsErrorFromProto(&pb.ReadError{
		Code: "session_not_found",
		Detail: &pb.ReadError_SessionNotFound{
			SessionNotFound: &pb.SessionNotFoundDetails{SessionId: sessionID.String()},
		},
	})
	if !errors.Is(err, sessioncontract.ErrSessionNotFound) {
		t.Fatalf("error = %v, want ErrSessionNotFound", err)
	}
	if err.Error() == sessioncontract.ErrSessionNotFound.Error() {
		t.Fatalf("error = %q, want Session identity detail", err)
	}
}
