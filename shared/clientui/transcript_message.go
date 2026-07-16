package clientui

import (
	"fmt"
)

type TranscriptMessageKind string

const (
	TranscriptMessageHydration                 TranscriptMessageKind = "hydration"
	TranscriptMessageCommittedRow              TranscriptMessageKind = "committed_row"
	TranscriptMessageAssistantDelta            TranscriptMessageKind = "assistant_delta"
	TranscriptMessageAssistantStreamAbort      TranscriptMessageKind = "assistant_stream_abort"
	TranscriptMessageReasoningUpdate           TranscriptMessageKind = "reasoning_update"
	TranscriptMessageReasoningReset            TranscriptMessageKind = "reasoning_reset"
	TranscriptMessageToolStart                 TranscriptMessageKind = "tool_start"
	TranscriptMessageToolAbort                 TranscriptMessageKind = "tool_abort"
	TranscriptMessageUserMessageFlushed        TranscriptMessageKind = "user_message_flushed"
	TranscriptMessageQueuedMessageState        TranscriptMessageKind = "queued_message_state"
	TranscriptMessageStepState                 TranscriptMessageKind = "step_state"
	TranscriptMessageReviewerState             TranscriptMessageKind = "reviewer_state"
	TranscriptMessageRuntimeReadModelUpdate    TranscriptMessageKind = "runtime_read_model_update"
	TranscriptMessageSessionStatus             TranscriptMessageKind = "session_status"
	TranscriptMessageSessionIdentity           TranscriptMessageKind = "session_identity"
	TranscriptMessageCompactionStatus          TranscriptMessageKind = "compaction_status"
	TranscriptMessageContextUsage              TranscriptMessageKind = "context_usage"
	TranscriptMessageGoalStatus                TranscriptMessageKind = "goal_status"
	TranscriptMessageBackgroundActivity        TranscriptMessageKind = "background_activity"
	TranscriptMessagePromptPending             TranscriptMessageKind = "prompt_pending"
	TranscriptMessagePromptResolved            TranscriptMessageKind = "prompt_resolved"
	TranscriptMessageWorktreeTransitionOutcome TranscriptMessageKind = "worktree_transition_outcome"
	TranscriptMessageOperationalDiagnostic     TranscriptMessageKind = "operational_diagnostic"
)

type TranscriptMessage struct {
	Sequence uint64
	Kind     TranscriptMessageKind
	Payload  TranscriptPayload
}

type TranscriptPayload struct {
	Hydration                 *TranscriptHydration
	CommittedRow              *TranscriptCommittedRow
	AssistantDelta            *TranscriptAssistantDelta
	AssistantStreamAbort      *TranscriptAssistantStreamAbort
	ReasoningUpdate           *TranscriptReasoningUpdate
	ReasoningReset            *TranscriptReasoningReset
	ToolStart                 *TranscriptToolStart
	ToolAbort                 *TranscriptToolAbort
	UserMessageFlushed        *TranscriptUserMessageFlushed
	QueuedMessageState        *TranscriptQueuedMessageState
	StepState                 *TranscriptStepState
	ReviewerState             *TranscriptReviewerState
	RuntimeReadModelUpdate    *RuntimeReadModelUpdate
	SessionStatus             *TranscriptSessionStatus
	SessionIdentity           *TranscriptSessionIdentity
	CompactionStatus          *TranscriptCompactionStatus
	ContextUsage              *TranscriptContextUsage
	GoalStatus                *TranscriptGoalStatus
	BackgroundActivity        *TranscriptBackgroundActivity
	PromptPending             *TranscriptPrompt
	PromptResolved            *TranscriptPrompt
	WorktreeTransitionOutcome *TranscriptWorktreeTransitionOutcome
	OperationalDiagnostic     *TranscriptOperationalDiagnostic
}

type TranscriptHydration struct {
	SessionIdentity        TranscriptSessionIdentity
	SessionStatus          TranscriptSessionStatus
	RuntimeReadModelUpdate RuntimeReadModelUpdate
	CommittedRows          []TranscriptCommittedRow
	ActiveAssistant        *TranscriptAssistantStream
	ActiveReasoning        *TranscriptReasoningUpdate
	ActiveStep             *TranscriptStepState
	ActiveReviewer         *TranscriptReviewerState
	ActiveCompaction       *TranscriptCompactionStatus
	InFlightTools          []TranscriptToolStart
	QueuedMessages         []TranscriptQueuedMessageState
	PendingPrompts         []TranscriptPrompt
	BackgroundActivities   []TranscriptBackgroundActivity
	ContextUsage           *TranscriptContextUsage
	GoalStatus             *TranscriptGoalStatus
}

func (m TranscriptMessage) ValidatePayload() error {
	if err := m.Payload.validateCardinality(m.Kind); err != nil {
		return err
	}
	validator, matched := m.Payload.validatorForKind(m.Kind)
	if !matched {
		return fmt.Errorf("transcript message kind %q does not match its payload", m.Kind)
	}
	switch m.Kind {
	case TranscriptMessagePromptPending:
		if m.Payload.PromptPending.State != TranscriptPromptStatePending {
			return fmt.Errorf("prompt-pending payload has state %q", m.Payload.PromptPending.State)
		}
	case TranscriptMessagePromptResolved:
		if m.Payload.PromptResolved.State != TranscriptPromptStateResolved {
			return fmt.Errorf("prompt-resolved payload has state %q", m.Payload.PromptResolved.State)
		}
	}
	return validator.Validate()
}

func (m TranscriptMessage) Validate() error {
	if err := m.ValidatePayload(); err != nil {
		return err
	}
	if m.Kind == TranscriptMessageHydration {
		if m.Sequence != 1 {
			return fmt.Errorf("transcript hydration sequence = %d, want 1", m.Sequence)
		}
	} else if m.Sequence < 2 {
		return fmt.Errorf("live transcript sequence = %d, want at least 2", m.Sequence)
	}
	return nil
}

func (p TranscriptPayload) validateCardinality(kind TranscriptMessageKind) error {
	count := 0
	for _, present := range []bool{
		p.Hydration != nil,
		p.CommittedRow != nil,
		p.AssistantDelta != nil,
		p.AssistantStreamAbort != nil,
		p.ReasoningUpdate != nil,
		p.ReasoningReset != nil,
		p.ToolStart != nil,
		p.ToolAbort != nil,
		p.UserMessageFlushed != nil,
		p.QueuedMessageState != nil,
		p.StepState != nil,
		p.ReviewerState != nil,
		p.RuntimeReadModelUpdate != nil,
		p.SessionStatus != nil,
		p.SessionIdentity != nil,
		p.CompactionStatus != nil,
		p.ContextUsage != nil,
		p.GoalStatus != nil,
		p.BackgroundActivity != nil,
		p.PromptPending != nil,
		p.PromptResolved != nil,
		p.WorktreeTransitionOutcome != nil,
		p.OperationalDiagnostic != nil,
	} {
		if present {
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("transcript message kind %q has %d payloads, want exactly one", kind, count)
	}
	return nil
}

type transcriptPayloadValidator interface {
	Validate() error
}

func (p TranscriptPayload) validatorForKind(kind TranscriptMessageKind) (transcriptPayloadValidator, bool) {
	switch kind {
	case TranscriptMessageHydration:
		return p.Hydration, p.Hydration != nil
	case TranscriptMessageCommittedRow:
		return p.CommittedRow, p.CommittedRow != nil
	case TranscriptMessageAssistantDelta:
		return p.AssistantDelta, p.AssistantDelta != nil
	case TranscriptMessageAssistantStreamAbort:
		return p.AssistantStreamAbort, p.AssistantStreamAbort != nil
	case TranscriptMessageReasoningUpdate:
		return p.ReasoningUpdate, p.ReasoningUpdate != nil
	case TranscriptMessageReasoningReset:
		return p.ReasoningReset, p.ReasoningReset != nil
	case TranscriptMessageToolStart:
		return p.ToolStart, p.ToolStart != nil
	case TranscriptMessageToolAbort:
		return p.ToolAbort, p.ToolAbort != nil
	case TranscriptMessageUserMessageFlushed:
		return p.UserMessageFlushed, p.UserMessageFlushed != nil
	case TranscriptMessageQueuedMessageState:
		return p.QueuedMessageState, p.QueuedMessageState != nil
	case TranscriptMessageStepState:
		return p.StepState, p.StepState != nil
	case TranscriptMessageReviewerState:
		return p.ReviewerState, p.ReviewerState != nil
	case TranscriptMessageRuntimeReadModelUpdate:
		return p.RuntimeReadModelUpdate, p.RuntimeReadModelUpdate != nil
	case TranscriptMessageSessionStatus:
		return p.SessionStatus, p.SessionStatus != nil
	case TranscriptMessageSessionIdentity:
		return p.SessionIdentity, p.SessionIdentity != nil
	case TranscriptMessageCompactionStatus:
		return p.CompactionStatus, p.CompactionStatus != nil
	case TranscriptMessageContextUsage:
		return p.ContextUsage, p.ContextUsage != nil
	case TranscriptMessageGoalStatus:
		return p.GoalStatus, p.GoalStatus != nil
	case TranscriptMessageBackgroundActivity:
		return p.BackgroundActivity, p.BackgroundActivity != nil
	case TranscriptMessagePromptPending:
		return p.PromptPending, p.PromptPending != nil
	case TranscriptMessagePromptResolved:
		return p.PromptResolved, p.PromptResolved != nil
	case TranscriptMessageWorktreeTransitionOutcome:
		return p.WorktreeTransitionOutcome, p.WorktreeTransitionOutcome != nil
	case TranscriptMessageOperationalDiagnostic:
		return p.OperationalDiagnostic, p.OperationalDiagnostic != nil
	default:
		return nil, false
	}
}
