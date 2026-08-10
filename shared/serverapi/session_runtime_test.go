package serverapi

import "testing"

func TestSessionRuntimeActivateRequestRequiresTypedOperation(t *testing.T) {
	for _, operation := range []SessionRuntimeActivationOperation{
		"",
		"future_activation",
	} {
		request := SessionRuntimeActivateRequest{
			ClientRequestID: "request-1",
			SessionID:       "session-1",
			Operation:       operation,
		}
		if err := request.Validate(); err == nil {
			t.Fatalf("Validate operation %q succeeded, want error", operation)
		}
	}
	for _, operation := range []SessionRuntimeActivationOperation{
		SessionRuntimeActivationUserActivation,
		SessionRuntimeActivationTechnicalReattachment,
	} {
		request := SessionRuntimeActivateRequest{
			ClientRequestID: "request-1",
			SessionID:       "session-1",
			Operation:       operation,
		}
		if err := request.Validate(); err != nil {
			t.Fatalf("Validate operation %q: %v", operation, err)
		}
	}
}
