package runtime

import (
	"sort"
	"strings"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/transcript"

	"github.com/google/uuid"
)

type TranscriptCommittedRowFactKind string

const (
	TranscriptCommittedRowFactUser             TranscriptCommittedRowFactKind = "user"
	TranscriptCommittedRowFactAssistant        TranscriptCommittedRowFactKind = "assistant"
	TranscriptCommittedRowFactTool             TranscriptCommittedRowFactKind = "tool"
	TranscriptCommittedRowFactReasoningTrace   TranscriptCommittedRowFactKind = "reasoning_trace"
	TranscriptCommittedRowFactNotice           TranscriptCommittedRowFactKind = "notice"
	TranscriptCommittedRowFactReviewerFeedback TranscriptCommittedRowFactKind = "reviewer_feedback"
	TranscriptCommittedRowFactReviewerError    TranscriptCommittedRowFactKind = "reviewer_error"
)

type TranscriptCommittedRowFact struct {
	StepID           string
	Visibility       transcript.EntryVisibility
	Integrity        transcript.RowIntegrity
	Kind             TranscriptCommittedRowFactKind
	Locator          transcript.CommittedRowLocator
	Provenance       *TranscriptCommittedRowProvenance
	User             *TranscriptUserRowFact
	Assistant        *TranscriptAssistantRowFact
	Tool             *TranscriptToolRowFact
	ReasoningTrace   *TranscriptReasoningTraceRowFact
	Notice           *TranscriptNoticeRowFact
	ReviewerFeedback *TranscriptReviewerFeedbackRowFact
	ReviewerError    *TranscriptReviewerErrorRowFact
}

type TranscriptUserRowFact struct {
	Text             string
	CondensedText    string
	RollbackTargetID *string
}

type TranscriptAssistantRowFact struct {
	Text          string
	CondensedText string
	Phase         llm.MessagePhase
	StreamID      *uuid.UUID
}

type TranscriptToolRowFact struct {
	ToolCallID    string
	ToolName      string
	Text          string
	IsError       bool
	ResultSummary string
	CondensedText string
	Presentation  *transcript.ToolCallMeta
}

type TranscriptReasoningTraceRowFact struct {
	Text                string
	CompactText         string
	ProvisionalIdentity *TranscriptReasoningTraceIdentity
}

type TranscriptNoticeRowFact struct {
	Reason               string
	Severity             string
	LegacyText           *string
	NoticeID             *string
	MessageType          llm.MessageType
	SourcePath           string
	WorktreeContext      *session.WorktreeContext
	CondensedText        string
	CompactLabel         string
	BackgroundActivityID string
	BackgroundProcessID  string
	BackgroundExitCode   *int
	DiagnosticCode       string
	DiagnosticDetail     string
	CacheWarning         *TranscriptCacheWarningFact
	Compaction           *TranscriptCompactionNoticeFact
}

type TranscriptReviewerFeedbackRowFact struct {
	ID              runtimeids.ReviewerFeedbackID
	Suggestions     []string
	SuggestionCount int
}

type TranscriptReviewerErrorRowFact struct {
	ID     runtimeids.ReviewerErrorID
	Detail string
}

type TranscriptCacheWarningFact struct {
	Scope           string
	Reason          string
	LostInputTokens *int
	Visibility      transcript.EntryVisibility
}

type TranscriptCompactionNoticeFact struct {
	Count  *int
	Detail *string
}

func TranscriptCommittedRowFactsFromEvent(evt Event) []TranscriptCommittedRowFact {
	var facts []TranscriptCommittedRowFact
	switch evt.Kind {
	case EventConversationUpdated, EventAssistantMessage:
		facts = transcriptCommittedRowFactsFromMessage(
			evt.Message,
			evt.AssistantTranscriptStreamID,
			nil,
			nil,
			transcriptMessageProjectionContext{Provenance: evt.CommittedProvenance},
		)
	case EventUserMessageFlushed:
		if strings.TrimSpace(evt.UserMessage) == "" {
			return nil
		}
		facts = []TranscriptCommittedRowFact{{Kind: TranscriptCommittedRowFactUser, User: &TranscriptUserRowFact{Text: evt.UserMessage}, Visibility: transcript.EntryVisibilityOngoing}}
	case EventToolCallCompleted:
		if evt.ToolResult == nil {
			return nil
		}
		facts = []TranscriptCommittedRowFact{transcriptToolRowFactFromResult(*evt.ToolResult)}
	case EventCacheWarning:
		if evt.CacheWarning == nil {
			return nil
		}
		facts = []TranscriptCommittedRowFact{transcriptCacheWarningFact(*evt.CacheWarning, evt.CacheWarningVisibility)}
	case EventLocalEntryAdded:
		if evt.LocalEntry == nil {
			return nil
		}
		if evt.LocalEntryProjected {
			if fact, ok := transcriptCommittedRowFactFromChatEntry(*evt.LocalEntry); ok {
				if fact.Kind == TranscriptCommittedRowFactReasoningTrace && fact.ReasoningTrace != nil {
					fact.ReasoningTrace.ProvisionalIdentity = cloneTranscriptReasoningTraceIdentity(evt.ReasoningTraceIdentity)
				}
				facts = []TranscriptCommittedRowFact{fact}
				break
			}
			return nil
		}
		if strings.TrimSpace(evt.LocalEntry.Role) == string(transcript.EntryRoleReasoning) {
			if fact, ok := transcriptCommittedRowFactFromChatEntry(*evt.LocalEntry); ok {
				if fact.ReasoningTrace != nil {
					fact.ReasoningTrace.ProvisionalIdentity = cloneTranscriptReasoningTraceIdentity(evt.ReasoningTraceIdentity)
				}
				facts = []TranscriptCommittedRowFact{fact}
				break
			}
			return nil
		}
		if fact, ok := transcriptNoticeRowFactFromChatEntry(*evt.LocalEntry); ok {
			facts = []TranscriptCommittedRowFact{fact}
			break
		}
		return nil
	case EventInFlightClearFailed:
		return nil
	case EventBackgroundUpdated:
		return nil
	default:
		return nil
	}
	for index := range facts {
		if evt.CommittedProvenance != nil {
			facts[index].Provenance = cloneTranscriptCommittedRowProvenance(evt.CommittedProvenance)
		} else if facts[index].Provenance == nil {
			facts[index].Provenance = cloneTranscriptCommittedRowProvenance(evt.CommittedProvenance)
		}
	}
	facts = transcriptCommittedRowFactsForStep(evt.StepID, facts)
	return locateTranscriptCommittedRowFacts(facts)
}

func transcriptCommittedRowFactsForStep(stepID string, facts []TranscriptCommittedRowFact) []TranscriptCommittedRowFact {
	stepID = strings.TrimSpace(stepID)
	for index := range facts {
		existing := strings.TrimSpace(facts[index].StepID)
		if existing != "" && stepID != "" && existing != stepID {
			panic("transcript committed row step identity conflicts with its runtime event")
		}
		if stepID != "" {
			facts[index].StepID = stepID
		}
	}
	return facts
}

func locateTranscriptCommittedRowFacts(facts []TranscriptCommittedRowFact) []TranscriptCommittedRowFact {
	sort.SliceStable(facts, func(left, right int) bool {
		return transcriptCommittedProvenanceBefore(
			facts[left].Provenance,
			facts[right].Provenance,
		)
	})
	ordinals := make(map[int64]int64, len(facts))
	for index := range facts {
		provenance := facts[index].Provenance
		if provenance == nil {
			return facts
		}
		ordinal := ordinals[provenance.EventSequence] + 1
		if provenance.ProjectedOrdinal != nil {
			ordinal = *provenance.ProjectedOrdinal
		}
		ordinals[provenance.EventSequence] = ordinal
		facts[index].Locator = transcript.CommittedRowLocator{
			EventSequence: provenance.EventSequence,
			RowOrdinal:    ordinal,
		}
		facts[index].Provenance = cloneTranscriptCommittedRowProvenance(provenance)
		facts[index].Provenance.ProjectedOrdinal = nil
	}
	return facts
}

func TranscriptCommittedRowFactsFromSnapshot(snapshot ChatSnapshot) []TranscriptCommittedRowFact {
	facts := make([]TranscriptCommittedRowFact, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		if strings.TrimSpace(entry.Role) == "assistant" &&
			transcript.IsNoopFinalText(entry.Text) {
			continue
		}
		fact, ok := transcriptCommittedRowFactFromChatEntry(entry)
		if ok {
			facts = append(facts, fact)
		}
	}
	return locateTranscriptCommittedRowFacts(facts)
}

func TranscriptToolStartFactsFromEvent(evt Event) []TranscriptLiveToolStart {
	switch evt.Kind {
	case EventToolCallStarted:
		if evt.ToolCall == nil {
			return nil
		}
		start := transcriptLiveToolStartFromCall(evt.StepID, *evt.ToolCall)
		if strings.TrimSpace(start.ToolCallID) == "" {
			return nil
		}
		return []TranscriptLiveToolStart{start}
	default:
		return nil
	}
}

type transcriptMessageProjectionContext struct {
	Provenance           *TranscriptCommittedRowProvenance
	CompletionProvenance map[string]*TranscriptCommittedRowProvenance
}

func transcriptCommittedRowFactsFromMessage(
	msg llm.Message,
	streamID *uuid.UUID,
	completions map[string]tools.Result,
	materializedToolCalls map[string]struct{},
	contexts ...transcriptMessageProjectionContext,
) []TranscriptCommittedRowFact {
	facts := transcriptCommittedRowFactsFromMessageUnlocated(msg, streamID, completions, materializedToolCalls)
	var provenance *TranscriptCommittedRowProvenance
	var completionProvenance map[string]*TranscriptCommittedRowProvenance
	if len(contexts) > 0 {
		provenance = contexts[0].Provenance
		completionProvenance = contexts[0].CompletionProvenance
	}
	for index := range facts {
		facts[index].Provenance = cloneTranscriptCommittedRowProvenance(provenance)
		if facts[index].Tool != nil && completionProvenance != nil {
			if owner := completionProvenance[facts[index].Tool.ToolCallID]; owner != nil {
				facts[index].Provenance = cloneTranscriptCommittedRowProvenance(owner)
			}
		}
	}
	return facts
}

func transcriptCommittedRowFactsFromMessageUnlocated(msg llm.Message, streamID *uuid.UUID, completions map[string]tools.Result, materializedToolCalls map[string]struct{}) []TranscriptCommittedRowFact {
	switch msg.Role {
	case llm.RoleUser:
		if msg.MessageType != nil &&
			*msg.MessageType == llm.MessageTypeCompactionSummary {
			detail, _ := textutil.OptionalExact(msg.Content)
			return []TranscriptCommittedRowFact{transcriptCompactionNoticeFact(
				"",
				messageTypeTranscriptVisibility(msg.MessageType),
				nil,
				detail,
			)}
		}
		if msg.Content == nil || strings.TrimSpace(*msg.Content) == "" {
			return nil
		}
		return []TranscriptCommittedRowFact{{Kind: TranscriptCommittedRowFactUser, Visibility: transcript.EntryVisibilityOngoing, User: &TranscriptUserRowFact{Text: *msg.Content}}}
	case llm.RoleAssistant:
		out := make([]TranscriptCommittedRowFact, 0, 1+len(msg.ToolCalls))
		if msg.Content != nil && strings.TrimSpace(*msg.Content) != "" && !isNoopFinalAnswer(msg) {
			phase, _ := textutil.OptionalValue(msg.Phase)
			out = append(out, TranscriptCommittedRowFact{Kind: TranscriptCommittedRowFactAssistant, Visibility: assistantTranscriptVisibility(phase), Assistant: &TranscriptAssistantRowFact{
				Text:     *msg.Content,
				Phase:    phase,
				StreamID: cloneTranscriptStreamID(streamID),
			}})
		}
		for _, call := range msg.ToolCalls {
			if result, ok := synthesizedToolResultForCall(call, completions, materializedToolCalls); ok {
				out = append(out, transcriptToolRowFactFromResult(result))
			}
		}
		return out
	case llm.RoleTool:
		return []TranscriptCommittedRowFact{transcriptToolRowFactFromResult(resolvedToolResultForMessage(msg, completions))}
	case llm.RoleDeveloper:
		if msg.MessageType != nil &&
			*msg.MessageType == llm.MessageTypeCompactionSummary {
			detail, _ := textutil.OptionalExact(msg.Content)
			return []TranscriptCommittedRowFact{transcriptCompactionNoticeFact(
				"",
				messageTypeTranscriptVisibility(msg.MessageType),
				nil,
				detail,
			)}
		}
		if msg.MessageType != nil &&
			*msg.MessageType == llm.MessageTypeReviewerFeedback {
			return nil
		}
		if msg.Content == nil || strings.TrimSpace(*msg.Content) == "" {
			if isUnknownDeveloperMessageType(msg.MessageType) {
				return []TranscriptCommittedRowFact{emptyDeveloperMessageDiagnosticFact(msg)}
			}
			return nil
		}
		return []TranscriptCommittedRowFact{runtimeNoticeFactFromMessage(msg, transcript.NoticeSeverityInfo)}
	default:
		return nil
	}
}

func transcriptCommittedEntryCountFromMessage(msg llm.Message, completions map[string]tools.Result, materializedToolCalls map[string]struct{}) int {
	return len(visibleChatEntriesFromMessage(msg, completions, materializedToolCalls))
}

func transcriptCommittedRowFactFromChatEntry(entry ChatEntry) (TranscriptCommittedRowFact, bool) {
	fact, ok := transcriptCommittedRowFactFromChatEntryUnlocated(entry)
	if ok {
		fact.Provenance = cloneTranscriptCommittedRowProvenance(entry.CommittedProvenance)
	}
	return fact, ok
}

func transcriptCommittedRowFactFromChatEntryUnlocated(entry ChatEntry) (TranscriptCommittedRowFact, bool) {
	visibility := normalizeRuntimeEntryVisibility(entry.Visibility)
	if visibility == transcript.EntryVisibilityHidden {
		return TranscriptCommittedRowFact{}, false
	}
	if entry.ReviewerFeedback != nil {
		return TranscriptCommittedRowFact{
			StepID:     entry.StepID,
			Visibility: visibility,
			Kind:       TranscriptCommittedRowFactReviewerFeedback,
			ReviewerFeedback: &TranscriptReviewerFeedbackRowFact{
				ID:              entry.ReviewerFeedback.ID,
				Suggestions:     append([]string(nil), entry.ReviewerFeedback.Suggestions...),
				SuggestionCount: len(entry.ReviewerFeedback.Suggestions),
			},
		}, true
	}
	if entry.ReviewerError != nil {
		return TranscriptCommittedRowFact{
			StepID:     entry.StepID,
			Visibility: transcript.EntryVisibilityOngoing,
			Kind:       TranscriptCommittedRowFactReviewerError,
			ReviewerError: &TranscriptReviewerErrorRowFact{
				ID:     entry.ReviewerError.ID,
				Detail: entry.ReviewerError.Detail,
			},
		}, true
	}
	role := strings.TrimSpace(entry.Role)
	switch role {
	case "user":
		integrity := transcriptTextEntryIntegrity(entry.Text, entry.CondensedText, entry.CompactLabel)
		text := entry.Text
		if strings.TrimSpace(text) == "" {
			text = firstNonBlankTranscriptValue(entry.CondensedText, entry.CompactLabel)
		}
		if entry.RollbackTargetID != nil && strings.TrimSpace(*entry.RollbackTargetID) == "" {
			panic("transcript user entry has a present but empty rollback target id")
		}
		return TranscriptCommittedRowFact{
			StepID: entry.StepID,
			Kind:   TranscriptCommittedRowFactUser,
			User: &TranscriptUserRowFact{
				Text:             text,
				CondensedText:    entry.CondensedText,
				RollbackTargetID: textutil.Pointer(entry.RollbackTargetID),
			},
			Visibility: transcriptVisibilityForIntegrity(resolveTranscriptVisibility(visibility, transcript.EntryVisibilityOngoing), integrity),
			Integrity:  integrity,
		}, true
	case "assistant":
		integrity := transcriptTextEntryIntegrity(entry.Text, entry.CondensedText, entry.CompactLabel)
		text := entry.Text
		if strings.TrimSpace(text) == "" {
			text = firstNonBlankTranscriptValue(entry.CondensedText, entry.CompactLabel)
		}
		return TranscriptCommittedRowFact{
			StepID: entry.StepID,
			Kind:   TranscriptCommittedRowFactAssistant,
			Assistant: &TranscriptAssistantRowFact{
				Text:          text,
				CondensedText: entry.CondensedText,
				Phase:         entry.Phase,
			},
			Visibility: transcriptVisibilityForIntegrity(resolveTranscriptVisibility(visibility, transcript.EntryVisibilityOngoing), integrity),
			Integrity:  integrity,
		}, true
	case string(transcript.EntryRoleReasoning):
		if strings.TrimSpace(entry.Text) == "" {
			return TranscriptCommittedRowFact{}, false
		}
		presentation := ProjectReasoningTrace(entry.Text)
		return TranscriptCommittedRowFact{
			StepID:     entry.StepID,
			Kind:       TranscriptCommittedRowFactReasoningTrace,
			Visibility: transcript.EntryVisibilityDetail,
			Integrity:  transcript.RowIntegrityValid,
			ReasoningTrace: &TranscriptReasoningTraceRowFact{
				Text:        presentation.Text,
				CompactText: presentation.CompactText,
			},
		}, true
	case "tool_call":
		return TranscriptCommittedRowFact{}, false
	case "tool_result_ok", "tool_result_error":
		if strings.TrimSpace(entry.ToolCallID) == "" {
			return transcriptNoticeRowFactFromChatEntry(entry)
		}
		integrity := transcriptToolEntryIntegrity(entry)
		toolName := "tool"
		if entry.ToolCall != nil && strings.TrimSpace(entry.ToolCall.ToolName) != "" {
			toolName = strings.TrimSpace(entry.ToolCall.ToolName)
		}
		return TranscriptCommittedRowFact{
			StepID:     entry.StepID,
			Kind:       TranscriptCommittedRowFactTool,
			Visibility: transcriptVisibilityForIntegrity(resolveTranscriptVisibility(visibility, transcript.EntryVisibilityOngoingCollapsed), integrity),
			Integrity:  integrity,
			Tool: &TranscriptToolRowFact{
				ToolCallID:    strings.TrimSpace(entry.ToolCallID),
				ToolName:      toolName,
				Text:          entry.Text,
				IsError:       role == "tool_result_error",
				ResultSummary: strings.TrimSpace(entry.ToolResultSummary),
				CondensedText: strings.TrimSpace(firstNonBlankTranscriptValue(entry.CondensedText, entry.CompactLabel)),
				Presentation:  cloneTranscriptToolCallMeta(entry.ToolCall),
			},
		}, true
	default:
		return transcriptNoticeRowFactFromChatEntry(entry)
	}
}

func transcriptNoticeRowFactFromChatEntry(entry ChatEntry) (TranscriptCommittedRowFact, bool) {
	role := transcript.EntryRole(strings.TrimSpace(entry.Role))
	visibility := normalizeRuntimeEntryVisibility(entry.Visibility)
	if visibility == transcript.EntryVisibilityHidden {
		return TranscriptCommittedRowFact{}, false
	}
	if role == transcript.EntryRoleReviewerStatus {
		return TranscriptCommittedRowFact{}, false
	}
	fact, ok := transcriptNoticeRowFactFromChatEntryUnlocated(entry)
	if ok {
		fact.Provenance = cloneTranscriptCommittedRowProvenance(entry.CommittedProvenance)
	}
	return fact, ok
}

func transcriptNoticeRowFactFromChatEntryUnlocated(entry ChatEntry) (TranscriptCommittedRowFact, bool) {
	visibility := normalizeRuntimeEntryVisibility(entry.Visibility)
	if visibility == transcript.EntryVisibilityHidden {
		return TranscriptCommittedRowFact{}, false
	}
	role := transcript.EntryRole(strings.TrimSpace(entry.Role))
	if role == transcript.EntryRoleReviewerSuggestions || role == transcript.EntryRoleReviewerError {
		return legacyReviewerNoticeRowFactFromChatEntry(entry)
	}
	if entry.MessageType == llm.MessageTypeCompactionSummary {
		return transcriptCompactionNoticeFact(
			entry.StepID,
			visibility,
			entry.CompactionNumber,
			entry.Text,
		), true
	}
	integrity := transcriptNoticeEntryIntegrity(entry)
	fact := localEntryNoticeFact(entry)
	fact.StepID = entry.StepID
	fact.Visibility = transcriptVisibilityForIntegrity(
		resolveTranscriptVisibility(visibility, defaultTranscriptNoticeVisibility(entry)),
		integrity,
	)
	fact.Integrity = integrity
	return fact, true
}

// TODO(KENT-405): delete this reader and its reopen/page coverage in 2.7.0.
// It exists only for persisted pre-typed Reviewer local entries.
func legacyReviewerNoticeRowFactFromChatEntry(entry ChatEntry) (TranscriptCommittedRowFact, bool) {
	integrity := transcriptNoticeEntryIntegrity(entry)
	fact := localEntryNoticeFact(entry)
	fact.StepID = entry.StepID
	fact.Visibility = transcriptVisibilityForIntegrity(
		resolveTranscriptVisibility(
			normalizeRuntimeEntryVisibility(entry.Visibility),
			transcript.EntryVisibilityOngoing,
		),
		integrity,
	)
	fact.Integrity = integrity
	return fact, true
}

func transcriptCompactionNoticeFact(
	stepID string,
	visibility transcript.EntryVisibility,
	count *int,
	detail string,
) TranscriptCommittedRowFact {
	var detailPointer *string
	if strings.TrimSpace(detail) != "" {
		detailPointer = &detail
	}
	return TranscriptCommittedRowFact{
		StepID:     stepID,
		Kind:       TranscriptCommittedRowFactNotice,
		Visibility: resolveTranscriptVisibility(visibility, transcript.EntryVisibilityOngoing),
		Integrity:  transcript.RowIntegrityValid,
		Notice: &TranscriptNoticeRowFact{
			Reason:      transcript.NoticeReasonCompaction,
			Severity:    transcript.NoticeSeverityInfo,
			MessageType: llm.MessageTypeCompactionSummary,
			Compaction: &TranscriptCompactionNoticeFact{
				Count:  textutil.Pointer(count),
				Detail: detailPointer,
			},
		},
	}
}

func transcriptTextEntryIntegrity(primary string, alternatives ...string) transcript.RowIntegrity {
	if strings.TrimSpace(primary) != "" {
		return transcript.RowIntegrityValid
	}
	if strings.TrimSpace(firstNonBlankTranscriptValue(alternatives...)) != "" {
		return transcript.RowIntegrityRecoverableMalformed
	}
	return transcript.RowIntegrityUnrecoverableMalformed
}

func transcriptToolEntryIntegrity(entry ChatEntry) transcript.RowIntegrity {
	if !transcriptToolEntryHasRecoverableText(entry) {
		return transcript.RowIntegrityUnrecoverableMalformed
	}
	if !entry.ToolCall.Valid() {
		return transcript.RowIntegrityRecoverableMalformed
	}
	return transcript.RowIntegrityValid
}

func transcriptToolEntryHasRecoverableText(entry ChatEntry) bool {
	if firstNonBlankTranscriptValue(entry.Text, entry.CondensedText, entry.CompactLabel, entry.ToolResultSummary) != "" {
		return true
	}
	meta := entry.ToolCall
	if meta == nil {
		return false
	}
	return firstNonBlankTranscriptValue(
		meta.ToolName,
		meta.Command,
		meta.CompactText,
		meta.PatchSummary,
		meta.PatchDetail,
		meta.Question,
	) != "" || len(meta.Suggestions) > 0 || (meta.RenderHint != nil && strings.TrimSpace(meta.RenderHint.Path) != "")
}

func transcriptNoticeEntryIntegrity(entry ChatEntry) transcript.RowIntegrity {
	if firstNonBlankTranscriptValue(entry.Text, entry.CondensedText, entry.CompactLabel, entry.SourcePath) == "" {
		return transcript.RowIntegrityUnrecoverableMalformed
	}
	messageType := entry.MessageType
	if transcript.IsReviewerEntryRole(strings.TrimSpace(entry.Role)) {
		messageType = llm.MessageTypeReviewerFeedback
	}
	if !knownTranscriptNoticeRole(strings.TrimSpace(entry.Role)) ||
		(strings.TrimSpace(string(messageType)) != "" &&
			isUnknownDeveloperMessageType(&messageType)) {
		return transcript.RowIntegrityRecoverableMalformed
	}
	return transcript.RowIntegrityValid
}

func knownTranscriptNoticeRole(role string) bool {
	switch transcript.EntryRole(role) {
	case transcript.EntryRoleSystem,
		transcript.EntryRoleWarning,
		transcript.EntryRoleCacheWarning,
		transcript.EntryRoleCompactionSummary,
		transcript.EntryRoleCompactionPreservedUserMessage,
		transcript.EntryRoleDeveloperContext,
		transcript.EntryRoleDeveloperFeedback,
		transcript.EntryRoleDeveloperErrorFeedback,
		transcript.EntryRoleInterruption,
		transcript.EntryRoleGoalFeedback,
		transcript.EntryRoleReasoning,
		transcript.EntryRoleReviewerError,
		transcript.EntryRoleReviewerSuggestions:
		return true
	default:
		return false
	}
}

func transcriptVisibilityForIntegrity(original transcript.EntryVisibility, integrity transcript.RowIntegrity) transcript.EntryVisibility {
	switch integrity {
	case transcript.RowIntegrityValid:
		return original
	case transcript.RowIntegrityRecoverableMalformed:
		return transcript.EntryVisibilityOngoing
	case transcript.RowIntegrityUnrecoverableMalformed:
		return transcript.EntryVisibilityDetail
	default:
		panic("transcript row has invalid integrity classification")
	}
}

func resolveTranscriptVisibility(visibility transcript.EntryVisibility, fallback transcript.EntryVisibility) transcript.EntryVisibility {
	switch normalizeRuntimeEntryVisibility(visibility) {
	case transcript.EntryVisibilityOngoing:
		return transcript.EntryVisibilityOngoing
	case transcript.EntryVisibilityOngoingCollapsed:
		return transcript.EntryVisibilityOngoingCollapsed
	case transcript.EntryVisibilityDetail:
		return transcript.EntryVisibilityDetail
	case transcript.EntryVisibilityHidden:
		return transcript.EntryVisibilityHidden
	default:
		return fallback
	}
}

func defaultTranscriptNoticeVisibility(entry ChatEntry) transcript.EntryVisibility {
	messageType := entry.MessageType
	if transcript.IsReviewerEntryRole(strings.TrimSpace(entry.Role)) {
		messageType = llm.MessageTypeReviewerFeedback
	}
	if strings.TrimSpace(string(messageType)) != "" &&
		!isUnknownDeveloperMessageType(&messageType) {
		if messageType != llm.MessageTypeReviewerFeedback {
			return messageTypeTranscriptVisibility(&messageType)
		}
	}
	switch transcript.EntryRole(strings.TrimSpace(entry.Role)) {
	case transcript.EntryRoleCompactionPreservedUserMessage,
		transcript.EntryRoleDeveloperContext,
		transcript.EntryRoleReasoning:
		return transcript.EntryVisibilityDetail
	case transcript.EntryRoleReviewerSuggestions,
		transcript.EntryRoleReviewerError:
		return transcript.EntryVisibilityOngoing
	default:
		return transcript.EntryVisibilityOngoing
	}
}

func firstNonBlankTranscriptValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func localEntryNoticeFact(entry ChatEntry) TranscriptCommittedRowFact {
	if strings.TrimSpace(entry.Role) == "" {
		return legacyUntypedNoticeFactFromLocalEntry(entry)
	}
	return runtimeNoticeFactFromLocalEntry(entry)
}

func transcriptToolRowFactFromResult(result tools.Result) TranscriptCommittedRowFact {
	if strings.TrimSpace(result.CallID) == "" {
		entry := toolResultChatEntry(result)
		fact, _ := transcriptNoticeRowFactFromChatEntry(entry)
		return fact
	}
	resultSummary, _ := textutil.OptionalTrimmed(result.Summary)
	condensedText, _ := textutil.OptionalTrimmed(result.CondensedText)
	return TranscriptCommittedRowFact{Kind: TranscriptCommittedRowFactTool, Visibility: transcript.EntryVisibilityOngoingCollapsed, Tool: &TranscriptToolRowFact{
		ToolCallID:    strings.TrimSpace(result.CallID),
		ToolName:      strings.TrimSpace(string(result.Name)),
		Text:          tools.FormatToolResultByName(string(result.Name), result.Output, result.IsError),
		IsError:       result.IsError,
		ResultSummary: resultSummary,
		CondensedText: condensedText,
		Presentation:  cloneTranscriptToolCallMeta(result.Presentation),
	}}
}

func transcriptCacheWarningFact(warning transcript.CacheWarning, visibility transcript.EntryVisibility) TranscriptCommittedRowFact {
	normalized := resolveTranscriptVisibility(visibility, transcript.EntryVisibilityOngoing)
	return TranscriptCommittedRowFact{Kind: TranscriptCommittedRowFactNotice, Visibility: normalized, Notice: &TranscriptNoticeRowFact{
		Reason:   transcript.NoticeReasonCacheWarning,
		Severity: transcript.NoticeSeverityWarning,
		CacheWarning: &TranscriptCacheWarningFact{
			Scope:           string(warning.Scope),
			Reason:          string(warning.Reason),
			LostInputTokens: textutil.Pointer(warning.LostInputTokens),
			Visibility:      normalized,
		},
	}}
}

func legacyUntypedNoticeFactFromLocalEntry(entry ChatEntry) TranscriptCommittedRowFact {
	noticeID := strings.TrimSpace(entry.NoticeID)
	var noticeIDPtr *string
	if noticeID != "" {
		noticeIDPtr = &noticeID
	}
	legacyText := firstNonEmpty(entry.Text, entry.CondensedText, entry.CompactLabel, entry.ToolResultSummary)
	var legacyTextPtr *string
	if legacyText != "" {
		legacyTextPtr = &legacyText
	}
	return TranscriptCommittedRowFact{Kind: TranscriptCommittedRowFactNotice, Visibility: normalizeRuntimeEntryVisibility(entry.Visibility), Notice: &TranscriptNoticeRowFact{
		Reason:     transcript.NoticeReasonLegacyUntypedNotice,
		Severity:   transcript.LegacyNoticeSeverityForRole(entry.Role),
		LegacyText: legacyTextPtr,
		NoticeID:   noticeIDPtr,
	}}
}

func runtimeNoticeFactFromMessage(msg llm.Message, severity string) TranscriptCommittedRowFact {
	messageType, _ := textutil.OptionalValue(msg.MessageType)
	code := strings.TrimSpace(string(messageType))
	if code == "" {
		code = "runtime_notice"
	}
	sourcePath, _ := textutil.OptionalTrimmed(msg.SourcePath)
	condensedText, _ := textutil.OptionalTrimmed(msg.CompactContent)
	backgroundActivityID, _ := textutil.OptionalTrimmed(msg.BackgroundActivityID)
	backgroundProcessID, _ := textutil.OptionalTrimmed(msg.Name)
	return TranscriptCommittedRowFact{Kind: TranscriptCommittedRowFactNotice, Visibility: messageTypeTranscriptVisibility(msg.MessageType), Notice: &TranscriptNoticeRowFact{
		Reason:               transcript.NoticeReasonRuntimeDiagnostic,
		Severity:             normalizeTranscriptNoticeSeverity(severity),
		MessageType:          messageType,
		SourcePath:           sourcePath,
		WorktreeContext:      session.CloneWorktreeContext(msg.WorktreeContext),
		CondensedText:        condensedText,
		CompactLabel:         compactLabelForMessage(msg),
		BackgroundActivityID: backgroundActivityID,
		BackgroundProcessID:  backgroundProcessID,
		BackgroundExitCode:   textutil.Pointer(msg.BackgroundExitCode),
		DiagnosticCode:       code,
		DiagnosticDetail:     *msg.Content,
	}}
}

func runtimeNoticeFactFromLocalEntry(entry ChatEntry) TranscriptCommittedRowFact {
	noticeID := strings.TrimSpace(entry.NoticeID)
	var noticeIDPtr *string
	if noticeID != "" {
		noticeIDPtr = &noticeID
	}
	detail := firstNonEmpty(entry.Text, entry.CondensedText, entry.CompactLabel, entry.ToolResultSummary)
	messageType := entry.MessageType
	role := strings.TrimSpace(entry.Role)
	if transcript.IsReviewerEntryRole(role) {
		messageType = llm.MessageTypeReviewerFeedback
	}
	return TranscriptCommittedRowFact{Kind: TranscriptCommittedRowFactNotice, Visibility: normalizeRuntimeEntryVisibility(entry.Visibility), Notice: &TranscriptNoticeRowFact{
		Reason:               transcript.NoticeReasonRuntimeDiagnostic,
		Severity:             transcript.LegacyNoticeSeverityForRole(entry.Role),
		NoticeID:             noticeIDPtr,
		MessageType:          messageType,
		SourcePath:           strings.TrimSpace(entry.SourcePath),
		WorktreeContext:      session.CloneWorktreeContext(entry.WorktreeContext),
		CondensedText:        strings.TrimSpace(entry.CondensedText),
		CompactLabel:         strings.TrimSpace(entry.CompactLabel),
		BackgroundActivityID: strings.TrimSpace(entry.BackgroundActivityID),
		BackgroundProcessID:  strings.TrimSpace(entry.BackgroundProcessID),
		BackgroundExitCode:   textutil.Pointer(entry.BackgroundExitCode),
		DiagnosticCode:       role,
		DiagnosticDetail:     detail,
	}}
}

func emptyDeveloperMessageDiagnosticFact(msg llm.Message) TranscriptCommittedRowFact {
	messageType, _ := textutil.OptionalValue(msg.MessageType)
	code := strings.TrimSpace(string(messageType))
	sourcePath, _ := textutil.OptionalTrimmed(msg.SourcePath)
	return TranscriptCommittedRowFact{Kind: TranscriptCommittedRowFactNotice, Visibility: transcript.EntryVisibilityDetail, Notice: &TranscriptNoticeRowFact{
		Reason:           transcript.NoticeReasonRuntimeDiagnostic,
		Severity:         transcript.NoticeSeverityInfo,
		MessageType:      messageType,
		SourcePath:       sourcePath,
		CompactLabel:     compactLabelForMessage(msg),
		DiagnosticCode:   code,
		DiagnosticDetail: "empty developer message",
	}}
}

func runtimeDiagnosticNoticeFact(code string, severity string, detail string) TranscriptCommittedRowFact {
	code = strings.TrimSpace(code)
	if code == "" {
		code = "runtime_notice"
	}
	return TranscriptCommittedRowFact{Kind: TranscriptCommittedRowFactNotice, Visibility: transcript.EntryVisibilityOngoing, Notice: &TranscriptNoticeRowFact{
		Reason:           transcript.NoticeReasonRuntimeDiagnostic,
		Severity:         normalizeTranscriptNoticeSeverity(severity),
		DiagnosticCode:   code,
		DiagnosticDetail: detail,
	}}
}

func normalizeTranscriptNoticeSeverity(severity string) string {
	severity = strings.TrimSpace(severity)
	if severity == "" {
		return transcript.NoticeSeverityInfo
	}
	return severity
}
