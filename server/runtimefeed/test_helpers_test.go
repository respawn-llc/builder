package runtimefeed

import (
	"testing"

	"core/shared/clientui"
	"core/shared/runtimeids"
)

func runtimefeedTestSessionID(t *testing.T) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID("session-1")
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	return id
}

func runtimefeedTestStepID(t *testing.T) runtimeids.StepID {
	t.Helper()
	stepID, err := runtimeids.ParseStepID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("parse step id: %v", err)
	}
	return stepID
}

func runtimefeedTestRunID(t *testing.T) runtimeids.RunID {
	t.Helper()
	runID, err := runtimeids.ParseRunID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("parse run id: %v", err)
	}
	return runID
}

func runtimefeedTestClientRequestID(t *testing.T) runtimeids.RuntimeClientRequestID {
	t.Helper()
	id, err := runtimeids.ParseRuntimeClientRequestID("33333333-3333-4333-8333-333333333333")
	if err != nil {
		t.Fatalf("parse client request id: %v", err)
	}
	return id
}

func runtimefeedTestQueueItemID(t *testing.T) runtimeids.QueueItemID {
	t.Helper()
	id, err := runtimeids.ParseQueueItemID("55555555-5555-4555-8555-555555555555")
	if err != nil {
		t.Fatalf("parse queue item id: %v", err)
	}
	return id
}

func runtimefeedTestAssistantStreamID(t *testing.T) runtimeids.AssistantStreamID {
	t.Helper()
	id, err := runtimeids.ParseAssistantStreamID("44444444-4444-4444-8444-444444444444")
	if err != nil {
		t.Fatalf("parse assistant stream id: %v", err)
	}
	return id
}

func runtimefeedTestBackgroundActivityID(t *testing.T) runtimeids.BackgroundActivityID {
	t.Helper()
	id, err := runtimeids.ParseBackgroundActivityID("66666666-6666-4666-8666-666666666666")
	if err != nil {
		t.Fatalf("parse background activity id: %v", err)
	}
	return id
}

func runtimefeedTestRuntimeReadModelUpdate(t *testing.T) RuntimeReadModelUpdate {
	t.Helper()
	version, err := clientui.NewReadModelVersion("epoch-1", 1, 1)
	if err != nil {
		t.Fatalf("new read model version: %v", err)
	}
	return RuntimeReadModelUpdate{
		Version: version,
		Activity: RuntimeActivity{
			State: clientui.RuntimeActivityRunning,
			ActiveStep: &RuntimeActiveStep{
				RunID:      runtimefeedTestRunID(t),
				StepID:     runtimefeedTestStepID(t),
				ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
			},
		},
		InputReconciliation: RuntimeInputReconciliationSnapshot{
			Operations: []RuntimeInputReconciliation{{
				Operation: RuntimeOperationRef{
					Kind:            clientui.RuntimeOperationKindSubmit,
					ClientRequestID: runtimefeedTestClientRequestID(t),
				},
				State: clientui.RuntimeInputReconciliationCommitted,
			}},
		},
	}
}

func runtimefeedTestSessionIdentity(t *testing.T) TranscriptSessionIdentity {
	t.Helper()
	return TranscriptSessionIdentity{
		SessionID:             runtimefeedTestSessionID(t),
		ConversationFreshness: clientui.ConversationFreshnessEstablished,
	}
}

func runtimefeedTestSessionStatus() TranscriptSessionStatus {
	return TranscriptSessionStatus{
		ReviewerFrequency: "off",
		ThinkingLevel:     "medium",
		CompactionMode:    "auto",
	}
}
