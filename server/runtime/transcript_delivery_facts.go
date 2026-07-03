package runtime

import (
	"encoding/json"
	"strings"

	"core/server/llm"
	"core/server/tools"
	"core/shared/toolspec"
	"core/shared/transcript"

	"github.com/google/uuid"
)

type TranscriptCommittedRowFactKind string

const (
	TranscriptCommittedRowFactUser      TranscriptCommittedRowFactKind = "user"
	TranscriptCommittedRowFactAssistant TranscriptCommittedRowFactKind = "assistant"
	TranscriptCommittedRowFactTool      TranscriptCommittedRowFactKind = "tool"
	TranscriptCommittedRowFactNotice    TranscriptCommittedRowFactKind = "notice"
)

type TranscriptCommittedRowFact struct {
	Kind      TranscriptCommittedRowFactKind
	User      *TranscriptUserRowFact
	Assistant *TranscriptAssistantRowFact
	Tool      *TranscriptToolRowFact
	Notice    *TranscriptNoticeRowFact
}

type TranscriptUserRowFact struct {
	Text string
}

type TranscriptAssistantRowFact struct {
	Text     string
	Phase    llm.MessagePhase
	StreamID *uuid.UUID
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

type TranscriptNoticeRowFact struct {
	Reason           string
	Severity         string
	LegacyText       *string
	NoticeID         *string
	DiagnosticCode   string
	DiagnosticDetail string
	CacheWarning     *TranscriptCacheWarningFact
}

type TranscriptCacheWarningFact struct {
	Scope           string
	Reason          string
	LostInputTokens int
}

func TranscriptCommittedRowFactsFromEvent(evt Event) []TranscriptCommittedRowFact {
	switch evt.Kind {
	case EventConversationUpdated, EventAssistantMessage:
		return transcriptCommittedRowFactsFromMessage(evt.Message, evt.AssistantTranscriptStreamID, nil, nil)
	case EventUserMessageFlushed:
		if strings.TrimSpace(evt.UserMessage) == "" {
			return nil
		}
		return []TranscriptCommittedRowFact{{Kind: TranscriptCommittedRowFactUser, User: &TranscriptUserRowFact{Text: evt.UserMessage}}}
	case EventToolCallCompleted:
		if evt.ToolResult == nil {
			return nil
		}
		return []TranscriptCommittedRowFact{transcriptToolRowFactFromResult(*evt.ToolResult)}
	case EventCacheWarning:
		if evt.CacheWarning == nil {
			return nil
		}
		return []TranscriptCommittedRowFact{transcriptCacheWarningFact(*evt.CacheWarning)}
	case EventLocalEntryAdded:
		if evt.LocalEntry == nil {
			return nil
		}
		return []TranscriptCommittedRowFact{runtimeDiagnosticNoticeFact(strings.TrimSpace(evt.LocalEntry.Role), "info")}
	case EventBackgroundUpdated:
		return nil
	default:
		return nil
	}
}

func TranscriptToolStartFactsFromEvent(evt Event) []TranscriptLiveToolStart {
	switch evt.Kind {
	case EventToolCallStarted:
		if evt.ToolCall == nil {
			return nil
		}
		start := transcriptLiveToolStartFromCall(*evt.ToolCall)
		if strings.TrimSpace(start.ToolCallID) == "" {
			return nil
		}
		return []TranscriptLiveToolStart{start}
	case EventConversationUpdated, EventAssistantMessage:
		if len(evt.Message.ToolCalls) == 0 {
			return nil
		}
		out := make([]TranscriptLiveToolStart, 0, len(evt.Message.ToolCalls))
		for _, call := range evt.Message.ToolCalls {
			start := transcriptLiveToolStartFromCall(call)
			if strings.TrimSpace(start.ToolCallID) == "" {
				continue
			}
			out = append(out, start)
		}
		return out
	default:
		return nil
	}
}

func transcriptCommittedRowFactsFromMessage(msg llm.Message, streamID *uuid.UUID, completions map[string]tools.Result, materializedToolCalls map[string]struct{}) []TranscriptCommittedRowFact {
	switch msg.Role {
	case llm.RoleUser:
		if strings.TrimSpace(msg.Content) == "" {
			return nil
		}
		return []TranscriptCommittedRowFact{{Kind: TranscriptCommittedRowFactUser, User: &TranscriptUserRowFact{Text: msg.Content}}}
	case llm.RoleAssistant:
		out := make([]TranscriptCommittedRowFact, 0, 1+len(msg.ToolCalls))
		if strings.TrimSpace(msg.Content) != "" && !isNoopFinalAnswer(msg) {
			out = append(out, TranscriptCommittedRowFact{Kind: TranscriptCommittedRowFactAssistant, Assistant: &TranscriptAssistantRowFact{
				Text:     msg.Content,
				Phase:    msg.Phase,
				StreamID: cloneTranscriptStreamID(streamID),
			}})
		}
		for _, call := range msg.ToolCalls {
			if synthesized, ok := synthesizedTranscriptToolResultFact(call, completions, materializedToolCalls); ok {
				out = append(out, synthesized)
			}
		}
		return out
	case llm.RoleTool:
		callID := strings.TrimSpace(msg.ToolCallID)
		result := tools.Result{
			CallID: callID,
			Name:   toolspecIDFromString(msg.Name),
			Output: json.RawMessage(msg.Content),
		}
		if completion, ok := completions[callID]; ok {
			if result.Name == "" {
				result.Name = completion.Name
			}
			if strings.TrimSpace(msg.Content) == "" && len(completion.Output) > 0 {
				result.Output = completion.Output
			}
			result.IsError = completion.IsError
			result.Summary = completion.Summary
			result.CondensedText = completion.CondensedText
			result.Presentation = completion.Presentation
		}
		if result.Name == "" {
			result.Name = "tool"
		}
		return []TranscriptCommittedRowFact{transcriptToolRowFactFromResult(result)}
	case llm.RoleDeveloper:
		if strings.TrimSpace(msg.Content) == "" {
			return nil
		}
		return []TranscriptCommittedRowFact{runtimeDiagnosticNoticeFact(string(msg.MessageType), "info")}
	default:
		return nil
	}
}

func synthesizedTranscriptToolResultFact(call llm.ToolCall, completions map[string]tools.Result, materializedToolCalls map[string]struct{}) (TranscriptCommittedRowFact, bool) {
	callID := strings.TrimSpace(call.ID)
	if callID == "" {
		return TranscriptCommittedRowFact{}, false
	}
	if _, ok := materializedToolCalls[callID]; ok {
		return TranscriptCommittedRowFact{}, false
	}
	completion, ok := completions[callID]
	if !ok {
		return TranscriptCommittedRowFact{}, false
	}
	return transcriptToolRowFactFromResult(completion), true
}

func transcriptToolRowFactFromResult(result tools.Result) TranscriptCommittedRowFact {
	return TranscriptCommittedRowFact{Kind: TranscriptCommittedRowFactTool, Tool: &TranscriptToolRowFact{
		ToolCallID:    strings.TrimSpace(result.CallID),
		ToolName:      strings.TrimSpace(string(result.Name)),
		Text:          tools.FormatToolResultByName(string(result.Name), result.Output, result.IsError),
		IsError:       result.IsError,
		ResultSummary: strings.TrimSpace(result.Summary),
		CondensedText: strings.TrimSpace(result.CondensedText),
		Presentation:  cloneTranscriptToolCallMeta(result.Presentation),
	}}
}

func transcriptCacheWarningFact(warning transcript.CacheWarning) TranscriptCommittedRowFact {
	return TranscriptCommittedRowFact{Kind: TranscriptCommittedRowFactNotice, Notice: &TranscriptNoticeRowFact{
		Reason:   "cache_warning",
		Severity: "warning",
		CacheWarning: &TranscriptCacheWarningFact{
			Scope:           string(warning.Scope),
			Reason:          string(warning.Reason),
			LostInputTokens: warning.LostInputTokens,
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
	return TranscriptCommittedRowFact{Kind: TranscriptCommittedRowFactNotice, Notice: &TranscriptNoticeRowFact{
		Reason:     "legacy_untyped_notice",
		Severity:   legacyLocalEntrySeverity(entry),
		LegacyText: legacyTextPtr,
		NoticeID:   noticeIDPtr,
	}}
}

func legacyLocalEntrySeverity(entry ChatEntry) string {
	switch strings.TrimSpace(entry.Role) {
	case "error":
		return "error"
	case "warning", cacheWarningTranscriptRole:
		return "warning"
	default:
		return "info"
	}
}

func runtimeDiagnosticNoticeFact(code string, severity string) TranscriptCommittedRowFact {
	code = strings.TrimSpace(code)
	if code == "" {
		code = "runtime_notice"
	}
	severity = strings.TrimSpace(severity)
	if severity == "" {
		severity = "info"
	}
	return TranscriptCommittedRowFact{Kind: TranscriptCommittedRowFactNotice, Notice: &TranscriptNoticeRowFact{
		Reason:         "runtime_diagnostic",
		Severity:       severity,
		DiagnosticCode: code,
	}}
}

func toolspecIDFromString(value string) toolspec.ID {
	return toolspec.ID(strings.TrimSpace(value))
}
