package serverapi

import (
	"strings"
	"testing"

	"core/shared/textutil"
)

func TestSessionRuntimeActivateRequestRequiresPreparedOwner(t *testing.T) {
	request := SessionRuntimeActivateRequest{
		ClientRequestID:       "request-1",
		SessionID:             "session-1",
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
	}

	if err := request.Validate(); err == nil || !strings.Contains(err.Error(), "owner_id") {
		t.Fatalf("Validate error = %v, want owner_id required", err)
	}
}

func TestSessionRuntimeReleaseRequestRequiresPreparedOwner(t *testing.T) {
	request := SessionRuntimeReleaseRequest{
		ClientRequestID: "request-1",
		Attachment: SessionRuntimeAttachment{
			SessionID:  "session-1",
			Generation: 1,
		},
	}

	if err := request.Validate(); err == nil || !strings.Contains(err.Error(), "owner_id") {
		t.Fatalf("Validate error = %v, want owner_id required", err)
	}
}
