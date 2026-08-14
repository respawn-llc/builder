package serverapi

import (
	"errors"
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

func TestSessionRuntimeRequestsRejectPathLikeSessionIDs(t *testing.T) {
	activate := SessionRuntimeActivateRequest{
		ClientRequestID:       "request-1",
		SessionID:             "../session",
		OwnerID:               "owner-1",
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
	}
	if err := activate.Validate(); !errors.Is(err, ErrSessionIDNotSingle) {
		t.Fatalf("activate Validate error = %v, want ErrSessionIDNotSingle", err)
	}

	release := SessionRuntimeReleaseRequest{
		ClientRequestID: "request-1",
		Attachment: SessionRuntimeAttachment{
			SessionID:  "../session",
			Generation: 1,
		},
		OwnerID: "owner-1",
	}
	if err := release.Validate(); !errors.Is(err, ErrSessionIDNotSingle) {
		t.Fatalf("release Validate error = %v, want ErrSessionIDNotSingle", err)
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
