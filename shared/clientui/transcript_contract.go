package clientui

import (
	"fmt"
	"strings"

	"core/shared/runtimeids"
	"core/shared/transcript"

	"github.com/google/uuid"
)

type MessagePhase string

const (
	MessagePhaseCommentary MessagePhase = MessagePhase(transcript.AssistantPhaseCommentary)
	MessagePhaseFinal      MessagePhase = MessagePhase(transcript.AssistantPhaseFinal)
)

func NormalizeMessagePhase(raw string) MessagePhase {
	phase, ok := transcript.ParseExplicitAssistantPhase(raw)
	if !ok {
		return ""
	}
	return MessagePhase(phase)
}

type MessageType string

const (
	MessageTypeAgentsMD                  MessageType = "agents.md"
	MessageTypeSkills                    MessageType = "skills"
	MessageTypeSubagents                 MessageType = "subagents"
	MessageTypeEnvironment               MessageType = "environment"
	MessageTypeCompactionSummary         MessageType = "compaction_summary"
	MessageTypeInterruption              MessageType = "interruption"
	MessageTypeErrorFeedback             MessageType = "error_feedback"
	MessageTypeCompactionSoonReminder    MessageType = "compaction_soon_reminder"
	MessageTypeHandoffFutureMessage      MessageType = "handoff_future_message"
	MessageTypeReviewerFeedback          MessageType = "reviewer_feedback"
	MessageTypeBackgroundNotice          MessageType = "background_notice"
	MessageTypeCustomToolCallOutput      MessageType = "custom_tool_call_output"
	MessageTypeManualCompactionCarryover MessageType = "manual_compaction_carryover"
	MessageTypeHeadlessMode              MessageType = "headless_mode"
	MessageTypeHeadlessModeExit          MessageType = "headless_mode_exit"
	MessageTypeWorkflowMode              MessageType = "workflow_mode"
	MessageTypeWorktreeMode              MessageType = "worktree_mode"
	MessageTypeWorktreeModeExit          MessageType = "worktree_mode_exit"
	MessageTypeGoal                      MessageType = "goal"
)

type TranscriptMessageKind string

const (
	TranscriptMessageHydration                   TranscriptMessageKind = "hydration"
	TranscriptMessageCommittedRow                TranscriptMessageKind = "committed_row"
	TranscriptMessageAssistantDelta              TranscriptMessageKind = "assistant_delta"
	TranscriptMessageAssistantStreamAbort        TranscriptMessageKind = "assistant_stream_abort"
	TranscriptMessageToolStart                   TranscriptMessageKind = "tool_start"
	TranscriptMessageToolAbort                   TranscriptMessageKind = "tool_abort"
	TranscriptMessageQueuedOrSteeredMessageState TranscriptMessageKind = "queued_or_steered_message_state"
	TranscriptMessageRunState                    TranscriptMessageKind = "run_state"
	TranscriptMessageRuntimeActivity             TranscriptMessageKind = "runtime_activity"
	TranscriptMessageInputReconciliation         TranscriptMessageKind = "input_reconciliation"
	TranscriptMessageSessionStatus               TranscriptMessageKind = "session_status"
	TranscriptMessageSessionIdentity             TranscriptMessageKind = "session_identity"
	TranscriptMessageCompactionStatus            TranscriptMessageKind = "compaction_status"
	TranscriptMessageContextUsage                TranscriptMessageKind = "context_usage"
	TranscriptMessageGoalStatus                  TranscriptMessageKind = "goal_status"
	TranscriptMessageBackgroundActivity          TranscriptMessageKind = "background_activity"
	TranscriptMessagePendingSessionPrompt        TranscriptMessageKind = "pending_session_prompt"
)

type TranscriptRowKind string

const (
	TranscriptRowUser      TranscriptRowKind = "user"
	TranscriptRowAssistant TranscriptRowKind = "assistant"
	TranscriptRowTool      TranscriptRowKind = "tool"
	TranscriptRowNotice    TranscriptRowKind = "notice"
)

type TranscriptAssistantStreamAbortReason string

const (
	TranscriptAssistantStreamAbortInterrupted TranscriptAssistantStreamAbortReason = "interrupted"
	TranscriptAssistantStreamAbortFailed      TranscriptAssistantStreamAbortReason = "failed"
	TranscriptAssistantStreamAbortSuperseded  TranscriptAssistantStreamAbortReason = "superseded"
)

type TranscriptToolAbortReason string

const (
	TranscriptToolAbortCanceled TranscriptToolAbortReason = "canceled"
	TranscriptToolAbortFailed   TranscriptToolAbortReason = "failed"
)

type TranscriptNoticeReason string

const (
	TranscriptNoticeCacheWarning        TranscriptNoticeReason = TranscriptNoticeReason(transcript.NoticeReasonCacheWarning)
	TranscriptNoticeRuntimeDiagnostic   TranscriptNoticeReason = TranscriptNoticeReason(transcript.NoticeReasonRuntimeDiagnostic)
	TranscriptNoticeLegacyUntypedNotice TranscriptNoticeReason = TranscriptNoticeReason(transcript.NoticeReasonLegacyUntypedNotice)
)

type TranscriptNoticeSeverity string

const (
	TranscriptNoticeInfo    TranscriptNoticeSeverity = TranscriptNoticeSeverity(transcript.NoticeSeverityInfo)
	TranscriptNoticeWarning TranscriptNoticeSeverity = TranscriptNoticeSeverity(transcript.NoticeSeverityWarning)
	TranscriptNoticeError   TranscriptNoticeSeverity = TranscriptNoticeSeverity(transcript.NoticeSeverityError)
)

type TranscriptPromptKind string

const (
	TranscriptPromptAsk      TranscriptPromptKind = "ask"
	TranscriptPromptQuestion TranscriptPromptKind = "question"
	TranscriptPromptApproval TranscriptPromptKind = "approval"
)

type TranscriptPromptState string

const (
	TranscriptPromptPending  TranscriptPromptState = "pending"
	TranscriptPromptResolved TranscriptPromptState = "resolved"
)

type TranscriptMessage struct {
	Sequence                    uint64
	Kind                        TranscriptMessageKind
	Hydration                   *TranscriptHydration
	CommittedRow                *TranscriptCommittedRow
	AssistantDelta              *TranscriptAssistantDelta
	AssistantStreamAbort        *TranscriptAssistantStreamAbort
	ToolStart                   *TranscriptToolStart
	ToolAbort                   *TranscriptToolAbort
	QueuedOrSteeredMessageState *TranscriptQueuedOrSteeredMessageState
	RunState                    *RunState
	RuntimeActivity             *RuntimeActivity
	InputReconciliation         *RuntimeInputReconciliationSnapshot
	SessionStatus               *TranscriptSessionStatus
	SessionIdentity             *TranscriptSessionIdentity
	CompactionStatus            *TranscriptCompactionStatus
	ContextUsage                *RuntimeContextUsage
	GoalStatus                  *TranscriptGoalStatus
	BackgroundActivity          *TranscriptBackgroundActivity
	PendingSessionPrompt        *TranscriptPendingSessionPrompt
}

type TranscriptHydration struct {
	CommittedRows           []TranscriptCommittedRow
	ActiveAssistantStream   *TranscriptAssistantStream
	InFlightTools           []TranscriptToolStart
	QueuedOrSteeredMessages []TranscriptQueuedOrSteeredMessageState
	RunState                *RunState
	RuntimeActivity         *RuntimeActivity
	InputReconciliation     *RuntimeInputReconciliationSnapshot
	SessionStatus           TranscriptSessionStatus
	SessionIdentity         TranscriptSessionIdentity
	CompactionStatus        *TranscriptCompactionStatus
	ContextUsage            *RuntimeContextUsage
	GoalStatus              *TranscriptGoalStatus
	BackgroundActivities    []TranscriptBackgroundActivity
	PendingSessionPrompts   []TranscriptPendingSessionPrompt
}

type TranscriptCommittedRow struct {
	Visibility EntryVisibility
	Integrity  transcript.RowIntegrity
	Kind       TranscriptRowKind
	User       *TranscriptUserRow
	Assistant  *TranscriptAssistantRow
	Tool       *TranscriptToolRow
	Notice     *TranscriptNoticeRow
}

type TranscriptUserRow struct {
	Text          string
	CondensedText string
}

type TranscriptAssistantRow struct {
	Text          string
	CondensedText string
	Phase         transcript.AssistantPhase
	StreamID      *uuid.UUID
}

type TranscriptToolRow struct {
	ToolCallID       string
	ToolName         string
	Text             string
	IsError          bool
	ResultSummary    string
	CondensedText    string
	ToolPresentation *ToolCallMeta
}

func (row *TranscriptToolRow) HasToolCallID() bool {
	return row != nil && strings.TrimSpace(row.ToolCallID) != ""
}

type TranscriptNoticeRow struct {
	Reason     TranscriptNoticeReason
	Severity   TranscriptNoticeSeverity
	Data       TranscriptNoticeData
	Diagnostic *TranscriptDiagnosticData
}

func TranscriptCommittedRowEqual(left, right TranscriptCommittedRow) bool {
	if left.Visibility != right.Visibility ||
		left.Integrity != right.Integrity ||
		left.Kind != right.Kind {
		return false
	}
	return transcriptUserRowEqual(left.User, right.User) &&
		transcriptAssistantRowEqual(left.Assistant, right.Assistant) &&
		transcriptToolRowEqual(left.Tool, right.Tool) &&
		transcriptNoticeRowEqual(left.Notice, right.Notice)
}

func transcriptUserRowEqual(left, right *TranscriptUserRow) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func transcriptAssistantRowEqual(left, right *TranscriptAssistantRow) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Text == right.Text &&
		left.CondensedText == right.CondensedText &&
		left.Phase == right.Phase &&
		pointersEqual(left.StreamID, right.StreamID)
}

func transcriptToolRowEqual(left, right *TranscriptToolRow) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.ToolCallID == right.ToolCallID &&
		left.ToolName == right.ToolName &&
		left.Text == right.Text &&
		left.IsError == right.IsError &&
		left.ResultSummary == right.ResultSummary &&
		left.CondensedText == right.CondensedText &&
		transcript.ToolCallMetaEqual(
			transcriptToolCallMeta(left.ToolPresentation),
			transcriptToolCallMeta(right.ToolPresentation),
		)
}

func transcriptNoticeRowEqual(left, right *TranscriptNoticeRow) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Reason == right.Reason &&
		left.Severity == right.Severity &&
		pointersEqual(left.Data.LegacyText, right.Data.LegacyText) &&
		pointersEqual(left.Data.NoticeID, right.Data.NoticeID) &&
		pointersEqual(left.Data.CacheWarning, right.Data.CacheWarning) &&
		pointersEqual(left.Data.RuntimeDiagnostic, right.Data.RuntimeDiagnostic) &&
		left.Data.MessageType == right.Data.MessageType &&
		left.Data.SourcePath == right.Data.SourcePath &&
		left.Data.CondensedText == right.Data.CondensedText &&
		left.Data.CompactLabel == right.Data.CompactLabel &&
		pointersEqual(left.Data.BackgroundExitCode, right.Data.BackgroundExitCode) &&
		pointersEqual(left.Diagnostic, right.Diagnostic)
}

func pointersEqual[T comparable](left, right *T) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func transcriptToolCallMeta(meta *ToolCallMeta) *transcript.ToolCallMeta {
	if meta == nil {
		return nil
	}
	out := transcript.ToolCallMeta{
		ToolName:               meta.ToolName,
		Presentation:           transcript.ToolPresentationKind(meta.Presentation),
		RenderBehavior:         transcript.ToolCallRenderBehavior(meta.RenderBehavior),
		IsShell:                meta.IsShell,
		UserInitiated:          meta.UserInitiated,
		Command:                meta.Command,
		CompactText:            meta.CompactText,
		InlineMeta:             meta.InlineMeta,
		TimeoutLabel:           meta.TimeoutLabel,
		PatchSummary:           meta.PatchSummary,
		PatchDetail:            meta.PatchDetail,
		PatchRender:            meta.PatchRender,
		Question:               meta.Question,
		Suggestions:            append([]string(nil), meta.Suggestions...),
		RecommendedOptionIndex: meta.RecommendedOptionIndex,
		OmitSuccessfulResult:   meta.OmitSuccessfulResult,
		RawOutputRequested:     meta.RawOutputRequested,
		OutputTruncated:        meta.OutputTruncated,
		MovedToBackground:      meta.MovedToBackground,
	}
	if meta.RenderHint != nil {
		out.RenderHint = &transcript.ToolRenderHint{
			Kind:         transcript.ToolRenderKind(meta.RenderHint.Kind),
			Path:         meta.RenderHint.Path,
			ResultOnly:   meta.RenderHint.ResultOnly,
			ShellDialect: transcript.ToolShellDialect(meta.RenderHint.ShellDialect),
		}
	}
	return &out
}

func ValidateTranscriptCommittedRow(row TranscriptCommittedRow) error {
	if !row.Integrity.Valid() {
		return fmt.Errorf("committed row has invalid integrity %d", row.Integrity)
	}
	switch row.Visibility {
	case EntryVisibilityOngoing, EntryVisibilityOngoingCollapsed, EntryVisibilityDetail, EntryVisibilityHidden:
	default:
		return fmt.Errorf("committed row has unresolved visibility %q", row.Visibility)
	}

	payloads := 0
	expectedKind := TranscriptRowKind("")
	if row.User != nil {
		payloads++
		expectedKind = TranscriptRowUser
	}
	if row.Assistant != nil {
		payloads++
		expectedKind = TranscriptRowAssistant
	}
	if row.Tool != nil {
		payloads++
		expectedKind = TranscriptRowTool
	}
	if row.Notice != nil {
		payloads++
		expectedKind = TranscriptRowNotice
	}
	if payloads != 1 {
		return fmt.Errorf("committed row kind %q has %d payloads, want exactly one", row.Kind, payloads)
	}
	if row.Kind == "" {
		return fmt.Errorf("committed row kind is required")
	}
	if row.Kind != expectedKind {
		return fmt.Errorf("committed row kind %q does not match payload kind %q", row.Kind, expectedKind)
	}
	if row.Integrity != transcript.RowIntegrityValid {
		return nil
	}
	if row.Assistant != nil && row.Assistant.StreamID != nil && *row.Assistant.StreamID == uuid.Nil {
		return fmt.Errorf("committed assistant row has zero stream_id")
	}
	if row.Tool != nil && !row.Tool.HasToolCallID() {
		return fmt.Errorf("committed tool row has empty tool_call_id")
	}
	return nil
}

func ValidateTranscriptPage(page TranscriptPage) error {
	if _, err := runtimeids.ParseSessionID(page.SessionID); err != nil {
		return fmt.Errorf("transcript page session identity: %w", err)
	}
	if err := validateTranscriptPageCursor("older", page.HasMoreAbove, page.OlderCursor); err != nil {
		return err
	}
	if err := validateTranscriptPageCursor("newer", page.HasMoreBelow, page.NewerCursor); err != nil {
		return err
	}
	for index, row := range page.Entries {
		if err := ValidateTranscriptCommittedRow(row); err != nil {
			return fmt.Errorf("transcript page entry %d: %w", index, err)
		}
	}
	return nil
}

func validateTranscriptPageCursor(name string, hasMore bool, cursor *int64) error {
	if !hasMore {
		if cursor != nil {
			return fmt.Errorf("transcript page %s cursor is present without more entries", name)
		}
		return nil
	}
	if cursor == nil {
		return fmt.Errorf("transcript page %s cursor is required when more entries exist", name)
	}
	if *cursor <= 0 {
		return fmt.Errorf("transcript page %s cursor must be positive, got %d", name, *cursor)
	}
	return nil
}

type TranscriptNoticeData struct {
	LegacyText         *string
	NoticeID           *string
	CacheWarning       *TranscriptCacheWarningData
	RuntimeDiagnostic  *TranscriptDiagnosticData
	MessageType        MessageType
	SourcePath         string
	CondensedText      string
	CompactLabel       string
	BackgroundExitCode *int
}

type TranscriptCacheWarningData struct {
	Scope           string
	Reason          string
	LostInputTokens int
	Visibility      EntryVisibility
}

type TranscriptDiagnosticData struct {
	Code   string
	Detail string
}

type TranscriptAssistantStream struct {
	StreamID uuid.UUID
	Text     string
	Phase    MessagePhase
}

type TranscriptAssistantDelta struct {
	StreamID uuid.UUID
	Delta    string
	Phase    MessagePhase
}

type TranscriptAssistantStreamAbort struct {
	StreamID   uuid.UUID
	Reason     TranscriptAssistantStreamAbortReason
	Diagnostic *TranscriptDiagnosticData
}

type TranscriptToolStart struct {
	ToolCallID       string
	ToolName         string
	ToolPresentation *ToolCallMeta
}

type TranscriptToolAbort struct {
	ToolCallID string
	Reason     TranscriptToolAbortReason
	Diagnostic *TranscriptDiagnosticData
}

type TranscriptQueuedOrSteeredMessageState struct {
	SessionID       string
	QueueItemID     string
	ClientRequestID string
	Status          QueuedUserMessageStatus
	FailureReason   QueuedUserMessageFailureReason
	UserText        string
}

type TranscriptSessionStatus = RuntimeStatus

type TranscriptSessionIdentity struct {
	SessionID             string
	SessionName           string
	ConversationFreshness ConversationFreshness
	ExecutionTarget       SessionExecutionTarget
}

type TranscriptCompactionStatus struct {
	Mode  string
	Count int
	State string
}

type TranscriptGoalStatus struct {
	ID        string
	Objective string
	Status    RuntimeGoalStatus
	Cleared   bool
}

type TranscriptBackgroundActivity struct {
	ID                string
	State             string
	Command           string
	Workdir           string
	LogPath           string
	Preview           string
	Removed           bool
	ExitCode          *int
	UserRequestedKill bool
}

type TranscriptPendingSessionPrompt struct {
	ID        string
	Kind      TranscriptPromptKind
	State     TranscriptPromptState
	SessionID string
	Data      TranscriptPendingSessionPromptData
}

type TranscriptPendingSessionPromptData struct {
	ToolCallID string
	ToolName   string
	Question   string
}

func (m TranscriptMessage) ValidatePayload() error {
	count := 0
	add := func(present bool) {
		if present {
			count++
		}
	}
	add(m.Hydration != nil)
	add(m.CommittedRow != nil)
	add(m.AssistantDelta != nil)
	add(m.AssistantStreamAbort != nil)
	add(m.ToolStart != nil)
	add(m.ToolAbort != nil)
	add(m.QueuedOrSteeredMessageState != nil)
	add(m.RunState != nil)
	add(m.RuntimeActivity != nil)
	add(m.InputReconciliation != nil)
	add(m.SessionStatus != nil)
	add(m.SessionIdentity != nil)
	add(m.CompactionStatus != nil)
	add(m.ContextUsage != nil)
	add(m.GoalStatus != nil)
	add(m.BackgroundActivity != nil)
	add(m.PendingSessionPrompt != nil)
	if count != 1 {
		return fmt.Errorf("transcript message kind %q has %d payloads, want exactly one", m.Kind, count)
	}
	switch m.Kind {
	case TranscriptMessageHydration:
		if m.Hydration == nil {
			return fmt.Errorf("transcript message kind %q requires hydration payload", m.Kind)
		}
	case TranscriptMessageCommittedRow:
		if m.CommittedRow == nil {
			return fmt.Errorf("transcript message kind %q requires committed row payload", m.Kind)
		}
	case TranscriptMessageAssistantDelta:
		if m.AssistantDelta == nil {
			return fmt.Errorf("transcript message kind %q requires assistant delta payload", m.Kind)
		}
	case TranscriptMessageAssistantStreamAbort:
		if m.AssistantStreamAbort == nil {
			return fmt.Errorf("transcript message kind %q requires assistant stream abort payload", m.Kind)
		}
	case TranscriptMessageToolStart:
		if m.ToolStart == nil {
			return fmt.Errorf("transcript message kind %q requires tool start payload", m.Kind)
		}
	case TranscriptMessageToolAbort:
		if m.ToolAbort == nil {
			return fmt.Errorf("transcript message kind %q requires tool abort payload", m.Kind)
		}
	case TranscriptMessageQueuedOrSteeredMessageState:
		if m.QueuedOrSteeredMessageState == nil {
			return fmt.Errorf("transcript message kind %q requires queued/steered message payload", m.Kind)
		}
	case TranscriptMessageRunState:
		if m.RunState == nil {
			return fmt.Errorf("transcript message kind %q requires run state payload", m.Kind)
		}
	case TranscriptMessageRuntimeActivity:
		if m.RuntimeActivity == nil {
			return fmt.Errorf("transcript message kind %q requires runtime activity payload", m.Kind)
		}
	case TranscriptMessageInputReconciliation:
		if m.InputReconciliation == nil {
			return fmt.Errorf("transcript message kind %q requires input reconciliation payload", m.Kind)
		}
	case TranscriptMessageSessionStatus:
		if m.SessionStatus == nil {
			return fmt.Errorf("transcript message kind %q requires session status payload", m.Kind)
		}
	case TranscriptMessageSessionIdentity:
		if m.SessionIdentity == nil {
			return fmt.Errorf("transcript message kind %q requires session identity payload", m.Kind)
		}
	case TranscriptMessageCompactionStatus:
		if m.CompactionStatus == nil {
			return fmt.Errorf("transcript message kind %q requires compaction status payload", m.Kind)
		}
	case TranscriptMessageContextUsage:
		if m.ContextUsage == nil {
			return fmt.Errorf("transcript message kind %q requires context usage payload", m.Kind)
		}
	case TranscriptMessageGoalStatus:
		if m.GoalStatus == nil {
			return fmt.Errorf("transcript message kind %q requires goal status payload", m.Kind)
		}
	case TranscriptMessageBackgroundActivity:
		if m.BackgroundActivity == nil {
			return fmt.Errorf("transcript message kind %q requires background activity payload", m.Kind)
		}
	case TranscriptMessagePendingSessionPrompt:
		if m.PendingSessionPrompt == nil {
			return fmt.Errorf("transcript message kind %q requires pending session prompt payload", m.Kind)
		}
	default:
		return fmt.Errorf("unknown transcript message kind %q", m.Kind)
	}
	return nil
}

type TranscriptSubscriptionMessage = TranscriptMessage
type TranscriptSubscriptionMessageKind = TranscriptMessageKind
type TranscriptRowGroupKind = TranscriptRowKind

const (
	TranscriptRowGroupUser      = TranscriptRowUser
	TranscriptRowGroupAssistant = TranscriptRowAssistant
	TranscriptRowGroupTool      = TranscriptRowTool
	TranscriptRowGroupNotice    = TranscriptRowNotice
)
