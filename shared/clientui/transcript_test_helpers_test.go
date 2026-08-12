package clientui

import (
	"testing"

	"core/shared/runtimeids"
)

func testGoalAvailability() *GoalAvailability { value := GoalAvailabilityAvailable; return &value }

func transcriptTestSessionID(t *testing.T) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID("session-1")
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	return id
}

func transcriptTestStepID(t *testing.T) runtimeids.StepID {
	t.Helper()
	stepID, err := runtimeids.ParseStepID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("parse step id: %v", err)
	}
	return stepID
}

func transcriptTestRunID(t *testing.T) runtimeids.RunID {
	t.Helper()
	runID, err := runtimeids.ParseRunID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("parse run id: %v", err)
	}
	return runID
}

func transcriptTestClientRequestID(t *testing.T) runtimeids.RuntimeClientRequestID {
	t.Helper()
	id, err := runtimeids.ParseRuntimeClientRequestID("33333333-3333-4333-8333-333333333333")
	if err != nil {
		t.Fatalf("parse client request id: %v", err)
	}
	return id
}

func transcriptTestQueueItemID(t *testing.T) runtimeids.QueueItemID {
	t.Helper()
	id, err := runtimeids.ParseQueueItemID("55555555-5555-4555-8555-555555555555")
	if err != nil {
		t.Fatalf("parse queue item id: %v", err)
	}
	return id
}

func transcriptTestAssistantStreamID(t *testing.T) runtimeids.AssistantStreamID {
	t.Helper()
	id, err := runtimeids.ParseAssistantStreamID("44444444-4444-4444-8444-444444444444")
	if err != nil {
		t.Fatalf("parse assistant stream id: %v", err)
	}
	return id
}

func transcriptTestBackgroundActivityID(t *testing.T) runtimeids.BackgroundActivityID {
	t.Helper()
	id, err := runtimeids.ParseBackgroundActivityID("66666666-6666-4666-8666-666666666666")
	if err != nil {
		t.Fatalf("parse background activity id: %v", err)
	}
	return id
}

func transcriptTestRuntimeReadModelUpdate(t *testing.T) RuntimeReadModelUpdate {
	t.Helper()
	version, err := NewReadModelVersion("epoch-1", 1, 1)
	if err != nil {
		t.Fatalf("new read model version: %v", err)
	}
	return RuntimeReadModelUpdate{
		Version: version,
		Activity: RuntimeActivity{
			State: RuntimeActivityRunning,
			ActiveStep: &RuntimeActiveStep{
				RunID:      transcriptTestRunID(t),
				StepID:     transcriptTestStepID(t),
				ActiveKind: RuntimeActivityActiveKindUserTurn,
			},
		},
	}
}

func transcriptTestSessionIdentity(t *testing.T) TranscriptSessionIdentity {
	t.Helper()
	return TranscriptSessionIdentity{
		SessionID:             transcriptTestSessionID(t),
		ConversationFreshness: ConversationFreshnessEstablished,
	}
}

func transcriptTestSessionStatus() TranscriptSessionStatus {
	return TranscriptSessionStatus{
		ReviewerFrequency: "off",
		ThinkingLevel:     "medium",
		CompactionMode:    "auto",
	}
}
