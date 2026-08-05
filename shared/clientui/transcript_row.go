package clientui

import (
	"fmt"
	"strings"

	"core/shared/runtimeids"
	"core/shared/transcript"
)

type TranscriptRowKind string

const (
	TranscriptRowUser           TranscriptRowKind = "user"
	TranscriptRowAssistant      TranscriptRowKind = "assistant"
	TranscriptRowTool           TranscriptRowKind = "tool"
	TranscriptRowReasoningTrace TranscriptRowKind = "reasoning_trace"
	TranscriptRowNotice         TranscriptRowKind = "notice"
)

type TranscriptCommittedRow struct {
	Visibility     transcript.EntryVisibility
	Integrity      transcript.RowIntegrity
	Kind           TranscriptRowKind
	User           *TranscriptUserRow
	Assistant      *TranscriptAssistantRow
	Tool           *TranscriptToolRow
	ReasoningTrace *TranscriptReasoningTraceRow
	Notice         *TranscriptNoticeRow
}

type TranscriptUserRow struct {
	StepID           runtimeids.StepID
	Text             string
	CondensedText    *string
	RollbackTargetID *string
}

type TranscriptAssistantRow struct {
	StepID        runtimeids.StepID
	StreamID      *runtimeids.AssistantStreamID
	Text          string
	CondensedText *string
	Phase         transcript.AssistantPhase
}

type TranscriptToolRow struct {
	StepID        runtimeids.StepID
	ToolCallID    ToolCallID
	ToolName      string
	Text          string
	IsError       bool
	ResultSummary *string
	CondensedText *string
	Presentation  *transcript.ToolCallMeta
}

type TranscriptReasoningTraceRow struct {
	StepID              runtimeids.StepID
	CompactText         string
	Text                string
	ProvisionalIdentity *TranscriptReasoningTraceIdentity
}

type TranscriptNoticeReason string

const (
	TranscriptNoticeCacheWarning        TranscriptNoticeReason = transcript.NoticeReasonCacheWarning
	TranscriptNoticeCompaction          TranscriptNoticeReason = transcript.NoticeReasonCompaction
	TranscriptNoticeLegacyUntypedNotice TranscriptNoticeReason = transcript.NoticeReasonLegacyUntypedNotice
	TranscriptNoticeRuntimeDiagnostic   TranscriptNoticeReason = transcript.NoticeReasonRuntimeDiagnostic
)

type TranscriptNoticeSeverity string

const (
	TranscriptNoticeInfo    TranscriptNoticeSeverity = transcript.NoticeSeverityInfo
	TranscriptNoticeWarning TranscriptNoticeSeverity = transcript.NoticeSeverityWarning
	TranscriptNoticeError   TranscriptNoticeSeverity = transcript.NoticeSeverityError
)

type TranscriptMessageType string

const (
	TranscriptMessageAgentsMD               TranscriptMessageType = "agents.md"
	TranscriptMessageSkills                 TranscriptMessageType = "skills"
	TranscriptMessageSubagents              TranscriptMessageType = "subagents"
	TranscriptMessageEnvironment            TranscriptMessageType = "environment"
	TranscriptMessageCompactionSummary      TranscriptMessageType = "compaction_summary"
	TranscriptMessageInterruption           TranscriptMessageType = "interruption"
	TranscriptMessageErrorFeedback          TranscriptMessageType = "error_feedback"
	TranscriptMessageCompactionSoonReminder TranscriptMessageType = "compaction_soon_reminder"
	TranscriptMessageHandoffFutureMessage   TranscriptMessageType = "handoff_future_message"
	TranscriptMessageReviewerFeedback       TranscriptMessageType = "reviewer_feedback"
	TranscriptMessageBackgroundNotice       TranscriptMessageType = "background_notice"
	TranscriptMessageCustomToolCallOutput   TranscriptMessageType = "custom_tool_call_output"
	// TranscriptMessageCompactionPreservedUserMessage retains the legacy wire
	// value used by existing Session logs.
	TranscriptMessageCompactionPreservedUserMessage TranscriptMessageType = "manual_compaction_carryover"
	TranscriptMessageHeadlessMode                   TranscriptMessageType = "headless_mode"
	TranscriptMessageHeadlessModeExit               TranscriptMessageType = "headless_mode_exit"
	TranscriptMessageWorkflowMode                   TranscriptMessageType = "workflow_mode"
	TranscriptMessageWorktreeMode                   TranscriptMessageType = "worktree_mode"
	TranscriptMessageWorktreeModeExit               TranscriptMessageType = "worktree_mode_exit"
	TranscriptMessageGoal                           TranscriptMessageType = "goal"
	TranscriptMessageActiveGoalContinuation         TranscriptMessageType = "active_goal_continuation"
)

type NoticeID string

type TranscriptNoticeRow struct {
	StepID        *runtimeids.StepID
	Reason        TranscriptNoticeReason
	Severity      TranscriptNoticeSeverity
	MessageType   *TranscriptMessageType
	LegacyText    *string
	NoticeID      *NoticeID
	SourcePath    *string
	Worktree      *TranscriptWorktreeContext
	CacheWarning  *TranscriptCacheWarning
	Compaction    *TranscriptCompactionNotice
	Diagnostic    *TranscriptDiagnostic
	Background    *TranscriptBackgroundNoticeIdentity
	CondensedText *string
	CompactLabel  *string
}

type TranscriptWorktreeContext struct {
	Branch        *string
	WorktreePath  string
	WorkspaceRoot string
	EffectiveCwd  string
}

type TranscriptCacheWarning struct {
	Scope           string
	Reason          string
	LostInputTokens *int
	Visibility      transcript.EntryVisibility
}

type TranscriptCompactionNotice struct {
	Count  *int
	Detail *string
}

func (n TranscriptCompactionNotice) Validate() error {
	if n.Count != nil && *n.Count <= 0 {
		return fmt.Errorf("transcript compaction count must be positive when present")
	}
	return validateOptionalNonEmptyString("transcript compaction detail", n.Detail)
}

type TranscriptBackgroundNoticeIdentity struct {
	ActivityID runtimeids.BackgroundActivityID
	ProcessID  ProcessID
	ExitCode   *int
}

func (r TranscriptCommittedRow) Validate() error {
	switch r.Visibility {
	case transcript.EntryVisibilityOngoing,
		transcript.EntryVisibilityOngoingCollapsed,
		transcript.EntryVisibilityDetail,
		transcript.EntryVisibilityHidden:
	default:
		return fmt.Errorf("unknown or implicit transcript row visibility %q", r.Visibility)
	}
	if !r.Integrity.Valid() {
		return fmt.Errorf("unknown transcript row integrity %d", r.Integrity)
	}
	count := 0
	if r.User != nil {
		count++
	}
	if r.Assistant != nil {
		count++
	}
	if r.Tool != nil {
		count++
	}
	if r.ReasoningTrace != nil {
		count++
	}
	if r.Notice != nil {
		count++
	}
	if count != 1 {
		return fmt.Errorf("transcript committed row kind %q has %d payloads, want exactly one", r.Kind, count)
	}
	switch r.Kind {
	case TranscriptRowUser:
		if r.User == nil {
			return fmt.Errorf("transcript user row payload is required")
		}
		return r.User.Validate()
	case TranscriptRowAssistant:
		if r.Assistant == nil {
			return fmt.Errorf("transcript assistant row payload is required")
		}
		return r.Assistant.Validate()
	case TranscriptRowTool:
		if r.Tool == nil {
			return fmt.Errorf("transcript tool row payload is required")
		}
		return r.Tool.Validate()
	case TranscriptRowReasoningTrace:
		if r.ReasoningTrace == nil {
			return fmt.Errorf("transcript reasoning trace row payload is required")
		}
		return r.ReasoningTrace.Validate()
	case TranscriptRowNotice:
		if r.Notice == nil {
			return fmt.Errorf("transcript notice row payload is required")
		}
		return r.Notice.Validate()
	default:
		return fmt.Errorf("unknown transcript row kind %q", r.Kind)
	}
}

func (r TranscriptReasoningTraceRow) Validate() error {
	if r.StepID.IsZero() {
		return fmt.Errorf("transcript reasoning trace row step id is required")
	}
	if strings.TrimSpace(r.CompactText) == "" {
		return fmt.Errorf("transcript reasoning trace row compact text is required")
	}
	if strings.TrimSpace(r.Text) == "" {
		return fmt.Errorf("transcript reasoning trace row text is required")
	}
	if r.ProvisionalIdentity != nil {
		return r.ProvisionalIdentity.Validate()
	}
	return nil
}

func (r TranscriptUserRow) Validate() error {
	if r.StepID.IsZero() {
		return fmt.Errorf("transcript user row step id is required")
	}
	if strings.TrimSpace(r.Text) == "" {
		return fmt.Errorf("transcript user row text is required")
	}
	if r.RollbackTargetID != nil && strings.TrimSpace(*r.RollbackTargetID) == "" {
		return fmt.Errorf("transcript user row rollback target id cannot be empty when present")
	}
	if err := validateOptionalNonEmptyString("transcript user row condensed text", r.CondensedText); err != nil {
		return err
	}
	return nil
}

func (r TranscriptAssistantRow) Validate() error {
	if r.StepID.IsZero() {
		return fmt.Errorf("transcript assistant row step id is required")
	}
	if r.StreamID != nil && r.StreamID.IsZero() {
		return fmt.Errorf("transcript assistant row stream id is invalid")
	}
	if strings.TrimSpace(r.Text) == "" {
		return fmt.Errorf("transcript assistant row text is required")
	}
	if err := validateOptionalNonEmptyString("transcript assistant row condensed text", r.CondensedText); err != nil {
		return err
	}
	switch r.Phase {
	case transcript.AssistantPhaseCommentary, transcript.AssistantPhaseFinal:
		return nil
	default:
		return fmt.Errorf("unknown transcript assistant row phase %q", r.Phase)
	}
}

func (r TranscriptToolRow) Validate() error {
	if r.StepID.IsZero() {
		return fmt.Errorf("transcript tool row step id is required")
	}
	if err := r.ToolCallID.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.ToolName) == "" {
		return fmt.Errorf("transcript tool row tool name is required")
	}
	if err := validateOptionalNonEmptyString("transcript tool row result summary", r.ResultSummary); err != nil {
		return err
	}
	if err := validateOptionalNonEmptyString("transcript tool row condensed text", r.CondensedText); err != nil {
		return err
	}
	return nil
}

func (r TranscriptNoticeRow) Validate() error {
	if r.StepID != nil && r.StepID.IsZero() {
		return fmt.Errorf("transcript notice row step id is invalid")
	}
	switch r.Reason {
	case TranscriptNoticeCacheWarning,
		TranscriptNoticeCompaction,
		TranscriptNoticeLegacyUntypedNotice,
		TranscriptNoticeRuntimeDiagnostic:
	default:
		return fmt.Errorf("unknown transcript notice reason %q", r.Reason)
	}
	switch r.Severity {
	case TranscriptNoticeInfo, TranscriptNoticeWarning, TranscriptNoticeError:
	default:
		return fmt.Errorf("unknown transcript notice severity %q", r.Severity)
	}
	if r.LegacyText != nil && strings.TrimSpace(*r.LegacyText) == "" {
		return fmt.Errorf("transcript notice legacy text cannot be empty when present")
	}
	if r.MessageType != nil {
		if err := r.MessageType.Validate(); err != nil {
			return err
		}
	}
	if r.NoticeID != nil && strings.TrimSpace(string(*r.NoticeID)) == "" {
		return fmt.Errorf("transcript notice id cannot be empty when present")
	}
	if err := validateOptionalNonEmptyString("transcript notice source path", r.SourcePath); err != nil {
		return err
	}
	if err := validateOptionalNonEmptyString("transcript notice condensed text", r.CondensedText); err != nil {
		return err
	}
	if err := validateOptionalNonEmptyString("transcript notice compact label", r.CompactLabel); err != nil {
		return err
	}
	if r.Worktree != nil {
		if err := r.Worktree.Validate(); err != nil {
			return err
		}
	}
	if r.CacheWarning != nil {
		if err := r.CacheWarning.Validate(); err != nil {
			return err
		}
	}
	if r.Compaction != nil {
		if err := r.Compaction.Validate(); err != nil {
			return err
		}
	}
	if r.Diagnostic != nil {
		if err := r.Diagnostic.Validate(); err != nil {
			return err
		}
	}
	if r.Background != nil {
		if err := r.Background.Validate(); err != nil {
			return err
		}
	}
	switch r.Reason {
	case TranscriptNoticeCacheWarning:
		if r.CacheWarning == nil {
			return fmt.Errorf("cache-warning notice requires cache-warning facts")
		}
		if r.LegacyText != nil || r.Compaction != nil || r.Diagnostic != nil || r.Background != nil {
			return fmt.Errorf("cache-warning notice cannot carry another notice reason payload")
		}
	case TranscriptNoticeCompaction:
		if r.Compaction == nil {
			return fmt.Errorf("compaction notice requires compaction facts")
		}
		if r.MessageType == nil || *r.MessageType != TranscriptMessageCompactionSummary {
			return fmt.Errorf("compaction notice requires compaction-summary message type")
		}
		if r.LegacyText != nil || r.CacheWarning != nil || r.Diagnostic != nil || r.Background != nil {
			return fmt.Errorf("compaction notice cannot carry another notice reason payload")
		}
	case TranscriptNoticeRuntimeDiagnostic:
		if r.Diagnostic == nil {
			return fmt.Errorf("runtime-diagnostic notice requires diagnostic facts")
		}
		if r.LegacyText != nil || r.CacheWarning != nil || r.Compaction != nil {
			return fmt.Errorf("runtime-diagnostic notice cannot carry another notice reason payload")
		}
	case TranscriptNoticeLegacyUntypedNotice:
		if r.LegacyText == nil && r.MessageType == nil {
			return fmt.Errorf("legacy notice requires text or typed message metadata")
		}
		if r.CacheWarning != nil || r.Compaction != nil || r.Diagnostic != nil || r.Background != nil {
			return fmt.Errorf("legacy notice cannot carry a typed notice reason payload")
		}
	}
	return nil
}

func (t TranscriptMessageType) Validate() error {
	switch t {
	case TranscriptMessageAgentsMD,
		TranscriptMessageSkills,
		TranscriptMessageSubagents,
		TranscriptMessageEnvironment,
		TranscriptMessageCompactionSummary,
		TranscriptMessageInterruption,
		TranscriptMessageErrorFeedback,
		TranscriptMessageCompactionSoonReminder,
		TranscriptMessageHandoffFutureMessage,
		TranscriptMessageReviewerFeedback,
		TranscriptMessageBackgroundNotice,
		TranscriptMessageCustomToolCallOutput,
		TranscriptMessageCompactionPreservedUserMessage,
		TranscriptMessageHeadlessMode,
		TranscriptMessageHeadlessModeExit,
		TranscriptMessageWorkflowMode,
		TranscriptMessageWorktreeMode,
		TranscriptMessageWorktreeModeExit,
		TranscriptMessageGoal,
		TranscriptMessageActiveGoalContinuation:
		return nil
	default:
		return fmt.Errorf("unknown transcript message type %q", t)
	}
}

func (c TranscriptWorktreeContext) Validate() error {
	if err := validateOptionalNonEmptyString("transcript worktree branch", c.Branch); err != nil {
		return err
	}
	if strings.TrimSpace(c.WorktreePath) == "" {
		return fmt.Errorf("transcript worktree path is required")
	}
	if strings.TrimSpace(c.WorkspaceRoot) == "" {
		return fmt.Errorf("transcript worktree workspace root is required")
	}
	if strings.TrimSpace(c.EffectiveCwd) == "" {
		return fmt.Errorf("transcript worktree effective cwd is required")
	}
	return nil
}

func (c TranscriptCacheWarning) Validate() error {
	if strings.TrimSpace(c.Scope) == "" {
		return fmt.Errorf("transcript cache-warning scope is required")
	}
	if strings.TrimSpace(c.Reason) == "" {
		return fmt.Errorf("transcript cache-warning reason is required")
	}
	if c.LostInputTokens != nil && *c.LostInputTokens <= 0 {
		return fmt.Errorf("transcript cache-warning lost input tokens must be positive when present")
	}
	switch c.Visibility {
	case transcript.EntryVisibilityOngoing,
		transcript.EntryVisibilityOngoingCollapsed,
		transcript.EntryVisibilityDetail,
		transcript.EntryVisibilityHidden:
		return nil
	default:
		return fmt.Errorf("unknown or implicit transcript cache-warning visibility %q", c.Visibility)
	}
}

func (i TranscriptBackgroundNoticeIdentity) Validate() error {
	if i.ActivityID.IsZero() {
		return fmt.Errorf("transcript background notice activity id is required")
	}
	if strings.TrimSpace(string(i.ProcessID)) == "" {
		return fmt.Errorf("transcript background notice process id is required")
	}
	return nil
}
