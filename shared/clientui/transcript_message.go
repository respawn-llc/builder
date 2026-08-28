package clientui

import (
	"bytes"
	"encoding/json"
	"fmt"

	"core/shared/runtimeinput"
)

type TranscriptMessageKind string

const (
	TranscriptMessageHydration                 TranscriptMessageKind = "hydration"
	TranscriptMessageCommittedRow              TranscriptMessageKind = "committed_row"
	TranscriptMessageAssistantDelta            TranscriptMessageKind = "assistant_delta"
	TranscriptMessageAssistantStreamAbort      TranscriptMessageKind = "assistant_stream_abort"
	TranscriptMessageThinkingStatusUpdate      TranscriptMessageKind = "thinking_status_update"
	TranscriptMessageReasoningTraceUpdate      TranscriptMessageKind = "reasoning_trace_update"
	TranscriptMessageReasoningTraceReset       TranscriptMessageKind = "reasoning_trace_reset"
	TranscriptMessageToolStart                 TranscriptMessageKind = "tool_start"
	TranscriptMessageToolAbort                 TranscriptMessageKind = "tool_abort"
	TranscriptMessageUserMessageFlushed        TranscriptMessageKind = "user_message_flushed"
	TranscriptMessageQueuedMessageState        TranscriptMessageKind = "queued_message_state"
	TranscriptMessagePendingWorkReplaced       TranscriptMessageKind = "pending_work_replaced"
	TranscriptMessagePendingWorkRestored       TranscriptMessageKind = "pending_work_restored"
	TranscriptMessageSessionSettingFeedback    TranscriptMessageKind = "session_setting_feedback"
	TranscriptMessageHumanInputInterrupted     TranscriptMessageKind = "human_input_interrupted"
	TranscriptMessageStepState                 TranscriptMessageKind = "step_state"
	TranscriptMessageRuntimeReadModelUpdate    TranscriptMessageKind = "runtime_read_model_update"
	TranscriptMessageSessionStatus             TranscriptMessageKind = "session_status"
	TranscriptMessageSessionIdentity           TranscriptMessageKind = "session_identity"
	TranscriptMessageCompactionStatus          TranscriptMessageKind = "compaction_status"
	TranscriptMessageContextUsage              TranscriptMessageKind = "context_usage"
	TranscriptMessageGoalStatus                TranscriptMessageKind = "goal_status"
	TranscriptMessageBackgroundActivity        TranscriptMessageKind = "background_activity"
	TranscriptMessagePrompt                    TranscriptMessageKind = "prompt"
	TranscriptMessageWorktreeTransitionOutcome TranscriptMessageKind = "worktree_transition_outcome"
	TranscriptMessageOperationalDiagnostic     TranscriptMessageKind = "operational_diagnostic"
	TranscriptMessageLiveRunFinished           TranscriptMessageKind = "live_run_finished"
)

type transcriptEventPayload interface {
	transcriptEventKind() TranscriptMessageKind
	Validate() error
}

// TranscriptEventPayload is the closed set of payloads that may be published
// on the session transcript feed.
type TranscriptEventPayload interface{ transcriptEventPayload }

type transcriptEventPayloadValue interface {
	transcriptEventPayload
	TranscriptHydration |
		TranscriptCommittedRow |
		TranscriptAssistantDelta |
		TranscriptAssistantStreamAbort |
		TranscriptThinkingStatusUpdate |
		TranscriptReasoningTraceUpdate |
		TranscriptReasoningTraceReset |
		TranscriptToolStart |
		TranscriptToolAbort |
		TranscriptUserMessageFlushed |
		TranscriptQueuedMessageState |
		TranscriptPendingWorkReplaced |
		TranscriptPendingWorkRestored |
		TranscriptSessionSettingFeedback |
		TranscriptHumanInputInterrupted |
		TranscriptStepState |
		RuntimeReadModelUpdate |
		TranscriptSessionStatus |
		TranscriptSessionIdentity |
		TranscriptCompactionStatus |
		TranscriptContextUsage |
		TranscriptGoalStatus |
		TranscriptBackgroundActivity |
		TranscriptPrompt |
		TranscriptWorktreeTransitionOutcome |
		TranscriptOperationalDiagnostic |
		TranscriptLiveRunResult
}

type TranscriptEvent struct {
	payload TranscriptEventPayload
}

func NewTranscriptEvent[T transcriptEventPayloadValue](payload T) TranscriptEvent {
	return TranscriptEvent{payload: payload}
}

func (e TranscriptEvent) IsZero() bool {
	return e.payload == nil
}

func (e TranscriptEvent) Payload() TranscriptEventPayload {
	return e.payload
}

func (e TranscriptEvent) Kind() TranscriptMessageKind {
	if e.payload == nil {
		panic("uninitialized transcript event has no kind")
	}
	return e.payload.transcriptEventKind()
}

func (e TranscriptEvent) Validate() error {
	if e.payload == nil {
		return fmt.Errorf("transcript event payload is uninitialized")
	}
	return e.payload.Validate()
}

type TranscriptMessage struct {
	Sequence uint64
	event    TranscriptEvent
}

type TranscriptHydration struct {
	SessionIdentity        TranscriptSessionIdentity
	SessionStatus          TranscriptSessionStatus
	RuntimeReadModelUpdate RuntimeReadModelUpdate
	CommittedRows          []TranscriptCommittedRow
	ActiveAssistant        *TranscriptAssistantStream
	ActiveThinkingStatus   *TranscriptThinkingStatusUpdate
	ActiveReasoningTraces  []TranscriptReasoningTraceUpdate
	ActiveStep             *TranscriptStepState
	ActiveCompaction       *TranscriptCompactionStatus
	InFlightTools          []TranscriptToolStart
	PendingWork            runtimeinput.PendingWork
	PendingPrompts         []TranscriptPrompt
	BackgroundActivities   []TranscriptBackgroundActivity
	ContextUsage           *TranscriptContextUsage
	GoalStatus             *TranscriptGoalStatus
}

func NewTranscriptMessage(sequence uint64, event TranscriptEvent) TranscriptMessage {
	return TranscriptMessage{Sequence: sequence, event: event}
}

func (m TranscriptMessage) Event() TranscriptEvent {
	return m.event
}

func (m TranscriptMessage) Payload() TranscriptEventPayload {
	return m.event.Payload()
}

func (m TranscriptMessage) Kind() TranscriptMessageKind {
	return m.event.Kind()
}

func (m TranscriptMessage) ValidatePayload() error {
	return m.event.Validate()
}

func (m TranscriptMessage) Validate() error {
	if err := m.ValidatePayload(); err != nil {
		return err
	}
	if m.Kind() == TranscriptMessageHydration {
		if m.Sequence != 1 {
			return fmt.Errorf("transcript hydration sequence = %d, want 1", m.Sequence)
		}
	} else if m.Sequence < 2 {
		return fmt.Errorf("live transcript sequence = %d, want at least 2", m.Sequence)
	}
	return nil
}

func (m TranscriptMessage) MarshalJSON() ([]byte, error) {
	if m.event.IsZero() {
		return nil, fmt.Errorf("cannot serialize uninitialized transcript message")
	}
	return json.Marshal(struct {
		Sequence uint64                 `json:"sequence"`
		Kind     TranscriptMessageKind  `json:"kind"`
		Payload  TranscriptEventPayload `json:"payload"`
	}{
		Sequence: m.Sequence,
		Kind:     m.Kind(),
		Payload:  m.Payload(),
	})
}

func (m *TranscriptMessage) UnmarshalJSON(data []byte) error {
	var envelope struct {
		Sequence uint64                 `json:"sequence"`
		Kind     *TranscriptMessageKind `json:"kind"`
		Payload  json.RawMessage        `json:"payload"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	if envelope.Kind == nil {
		return fmt.Errorf("transcript message kind is required")
	}
	if len(envelope.Payload) == 0 || bytes.Equal(envelope.Payload, []byte("null")) {
		return fmt.Errorf("transcript message payload is required")
	}
	event, err := unmarshalTranscriptEvent(*envelope.Kind, envelope.Payload)
	if err != nil {
		return err
	}
	*m = NewTranscriptMessage(envelope.Sequence, event)
	return nil
}

func unmarshalTranscriptEvent(kind TranscriptMessageKind, data []byte) (TranscriptEvent, error) {
	switch kind {
	case TranscriptMessageHydration:
		return decodeTranscriptPayload[TranscriptHydration](data)
	case TranscriptMessageCommittedRow:
		return decodeTranscriptPayload[TranscriptCommittedRow](data)
	case TranscriptMessageAssistantDelta:
		return decodeTranscriptPayload[TranscriptAssistantDelta](data)
	case TranscriptMessageAssistantStreamAbort:
		return decodeTranscriptPayload[TranscriptAssistantStreamAbort](data)
	case TranscriptMessageThinkingStatusUpdate:
		return decodeTranscriptPayload[TranscriptThinkingStatusUpdate](data)
	case TranscriptMessageReasoningTraceUpdate:
		return decodeTranscriptPayload[TranscriptReasoningTraceUpdate](data)
	case TranscriptMessageReasoningTraceReset:
		return decodeTranscriptPayload[TranscriptReasoningTraceReset](data)
	case TranscriptMessageToolStart:
		return decodeTranscriptPayload[TranscriptToolStart](data)
	case TranscriptMessageToolAbort:
		return decodeTranscriptPayload[TranscriptToolAbort](data)
	case TranscriptMessageUserMessageFlushed:
		return decodeTranscriptPayload[TranscriptUserMessageFlushed](data)
	case TranscriptMessageQueuedMessageState:
		return decodeTranscriptPayload[TranscriptQueuedMessageState](data)
	case TranscriptMessagePendingWorkReplaced:
		return decodeTranscriptPayload[TranscriptPendingWorkReplaced](data)
	case TranscriptMessagePendingWorkRestored:
		return decodeTranscriptPayload[TranscriptPendingWorkRestored](data)
	case TranscriptMessageSessionSettingFeedback:
		return decodeTranscriptPayload[TranscriptSessionSettingFeedback](data)
	case TranscriptMessageHumanInputInterrupted:
		return decodeTranscriptPayload[TranscriptHumanInputInterrupted](data)
	case TranscriptMessageStepState:
		return decodeTranscriptPayload[TranscriptStepState](data)
	case TranscriptMessageRuntimeReadModelUpdate:
		return decodeTranscriptPayload[RuntimeReadModelUpdate](data)
	case TranscriptMessageSessionStatus:
		return decodeTranscriptPayload[TranscriptSessionStatus](data)
	case TranscriptMessageSessionIdentity:
		return decodeTranscriptPayload[TranscriptSessionIdentity](data)
	case TranscriptMessageCompactionStatus:
		return decodeTranscriptPayload[TranscriptCompactionStatus](data)
	case TranscriptMessageContextUsage:
		return decodeTranscriptPayload[TranscriptContextUsage](data)
	case TranscriptMessageGoalStatus:
		return decodeTranscriptPayload[TranscriptGoalStatus](data)
	case TranscriptMessageBackgroundActivity:
		return decodeTranscriptPayload[TranscriptBackgroundActivity](data)
	case TranscriptMessagePrompt:
		return decodeTranscriptPayload[TranscriptPrompt](data)
	case TranscriptMessageWorktreeTransitionOutcome:
		return decodeTranscriptPayload[TranscriptWorktreeTransitionOutcome](data)
	case TranscriptMessageOperationalDiagnostic:
		return decodeTranscriptPayload[TranscriptOperationalDiagnostic](data)
	case TranscriptMessageLiveRunFinished:
		return decodeTranscriptPayload[TranscriptLiveRunResult](data)
	default:
		return TranscriptEvent{}, fmt.Errorf("unknown transcript message kind %q", kind)
	}
}

func decodeTranscriptPayload[T transcriptEventPayloadValue](data []byte) (TranscriptEvent, error) {
	var payload T
	if err := json.Unmarshal(data, &payload); err != nil {
		return TranscriptEvent{}, err
	}
	return NewTranscriptEvent(payload), nil
}

func (TranscriptHydration) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessageHydration
}

func (TranscriptCommittedRow) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessageCommittedRow
}

func (TranscriptAssistantDelta) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessageAssistantDelta
}

func (TranscriptAssistantStreamAbort) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessageAssistantStreamAbort
}

func (TranscriptThinkingStatusUpdate) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessageThinkingStatusUpdate
}

func (TranscriptReasoningTraceUpdate) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessageReasoningTraceUpdate
}

func (TranscriptReasoningTraceReset) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessageReasoningTraceReset
}

func (TranscriptToolStart) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessageToolStart
}

func (TranscriptToolAbort) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessageToolAbort
}

func (TranscriptUserMessageFlushed) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessageUserMessageFlushed
}

func (TranscriptQueuedMessageState) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessageQueuedMessageState
}

func (TranscriptPendingWorkReplaced) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessagePendingWorkReplaced
}

func (TranscriptPendingWorkRestored) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessagePendingWorkRestored
}

func (TranscriptSessionSettingFeedback) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessageSessionSettingFeedback
}

func (TranscriptHumanInputInterrupted) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessageHumanInputInterrupted
}

func (TranscriptStepState) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessageStepState
}

func (RuntimeReadModelUpdate) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessageRuntimeReadModelUpdate
}

func (TranscriptSessionStatus) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessageSessionStatus
}

func (TranscriptSessionIdentity) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessageSessionIdentity
}

func (TranscriptCompactionStatus) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessageCompactionStatus
}

func (TranscriptContextUsage) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessageContextUsage
}

func (TranscriptGoalStatus) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessageGoalStatus
}

func (TranscriptBackgroundActivity) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessageBackgroundActivity
}

func (TranscriptPrompt) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessagePrompt
}

func (TranscriptWorktreeTransitionOutcome) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessageWorktreeTransitionOutcome
}

func (TranscriptOperationalDiagnostic) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessageOperationalDiagnostic
}

func (TranscriptLiveRunResult) transcriptEventKind() TranscriptMessageKind {
	return TranscriptMessageLiveRunFinished
}
