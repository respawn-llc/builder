package serverapi

import (
	"encoding/json"
	"testing"

	"core/shared/config"
	"core/shared/textutil"
)

func TestSessionRuntimeRequestsHaveNoGenericClientRequestIdentity(t *testing.T) {
	activate := SessionRuntimeActivateRequest{
		SessionID:             "session-1",
		OwnerID:               "owner-1",
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
		ActiveSettings:        config.Settings{},
	}
	release := SessionRuntimeReleaseRequest{
		Attachment: SessionRuntimeAttachment{SessionID: "session-1", Generation: 1},
		OwnerID:    "owner-1",
	}
	for name, request := range map[string]any{
		"activate": activate,
		"release":  release,
	} {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(request)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatalf("decode request fields: %v", err)
			}
			if _, exists := fields["client_request_id"]; exists {
				t.Fatalf("request retained generic client identity: %s", data)
			}
		})
	}
}
