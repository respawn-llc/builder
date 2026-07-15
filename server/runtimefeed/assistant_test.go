package runtimefeed

import (
	"testing"

	"core/shared/runtimeids"
	"core/shared/transcript"
)

func TestTranscriptAssistantDeltaCarriesStepAndStreamIdentity(t *testing.T) {
	streamID, err := runtimeids.ParseAssistantStreamID("44444444-4444-4444-8444-444444444444")
	if err != nil {
		t.Fatalf("parse assistant stream id: %v", err)
	}
	delta := TranscriptAssistantDelta{
		StepID:   runtimefeedTestStepID(t),
		StreamID: streamID,
		Delta:    "hello",
		Phase:    transcript.AssistantPhaseFinal,
	}
	if err := delta.Validate(); err != nil {
		t.Fatalf("validate assistant delta: %v", err)
	}
}

func TestTranscriptAssistantDeltaRejectsInvalidIdentityContentOrPhase(t *testing.T) {
	streamID, err := runtimeids.ParseAssistantStreamID("44444444-4444-4444-8444-444444444444")
	if err != nil {
		t.Fatalf("parse assistant stream id: %v", err)
	}
	base := TranscriptAssistantDelta{
		StepID:   runtimefeedTestStepID(t),
		StreamID: streamID,
		Delta:    "hello",
		Phase:    transcript.AssistantPhaseFinal,
	}
	tests := []TranscriptAssistantDelta{
		func() TranscriptAssistantDelta {
			delta := base
			delta.StepID = runtimeids.StepID{}
			return delta
		}(),
		func() TranscriptAssistantDelta {
			delta := base
			delta.StreamID = runtimeids.AssistantStreamID{}
			return delta
		}(),
		func() TranscriptAssistantDelta {
			delta := base
			delta.Delta = ""
			return delta
		}(),
		func() TranscriptAssistantDelta {
			delta := base
			delta.Phase = transcript.AssistantPhase("unknown")
			return delta
		}(),
	}
	for _, delta := range tests {
		if err := delta.Validate(); err == nil {
			t.Fatalf("accepted invalid assistant delta: %+v", delta)
		}
	}
}

func TestTranscriptAssistantStreamHydrationRequiresContent(t *testing.T) {
	streamID, err := runtimeids.ParseAssistantStreamID("44444444-4444-4444-8444-444444444444")
	if err != nil {
		t.Fatalf("parse assistant stream id: %v", err)
	}
	stream := TranscriptAssistantStream{
		StepID:   runtimefeedTestStepID(t),
		StreamID: streamID,
		Text:     "hello",
		Phase:    transcript.AssistantPhaseCommentary,
	}
	if err := stream.Validate(); err != nil {
		t.Fatalf("validate assistant stream: %v", err)
	}
	stream.Text = ""
	if err := stream.Validate(); err == nil {
		t.Fatal("accepted assistant stream hydration without content")
	}
}

func TestTranscriptAssistantAbortRequiresTypedFailureDiagnostic(t *testing.T) {
	streamID, err := runtimeids.ParseAssistantStreamID("44444444-4444-4444-8444-444444444444")
	if err != nil {
		t.Fatalf("parse assistant stream id: %v", err)
	}
	abort := TranscriptAssistantStreamAbort{
		StepID:   runtimefeedTestStepID(t),
		StreamID: streamID,
		Reason:   AssistantStreamAbortFailed,
	}
	if err := abort.Validate(); err == nil {
		t.Fatal("accepted failed assistant abort without diagnostic")
	}
	abort.Diagnostic = &TranscriptDiagnostic{
		Code:   TranscriptDiagnosticCode("assistant_stream_failed"),
		Detail: "provider stream failed",
	}
	if err := abort.Validate(); err != nil {
		t.Fatalf("validate failed assistant abort: %v", err)
	}
}
