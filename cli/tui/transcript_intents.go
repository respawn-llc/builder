package tui

// Deprecated emergency path: this file survives the tui-redesign demolition
// only to keep the branch's remaining transcript role mapping symbols
// available until the replacement surface lands. Do not extend this file.

import (
	"strings"

	"core/shared/clientui"
	"core/shared/transcript"
)

type TranscriptRole string

const (
	TranscriptRoleUnknown                   TranscriptRole = "unknown"
	TranscriptRoleUser                      TranscriptRole = "user"
	TranscriptRoleAssistant                 TranscriptRole = "assistant"
	TranscriptRoleSystem                    TranscriptRole = "system"
	TranscriptRoleToolCall                  TranscriptRole = "tool_call"
	TranscriptRoleToolResult                TranscriptRole = "tool_result"
	TranscriptRoleToolResultOK              TranscriptRole = "tool_result_ok"
	TranscriptRoleToolResultError           TranscriptRole = "tool_result_error"
	TranscriptRoleReviewerStatus            TranscriptRole = "reviewer_status"
	TranscriptRoleReviewerSuggestions       TranscriptRole = "reviewer_suggestions"
	TranscriptRoleWarning                   TranscriptRole = "warning"
	TranscriptRoleCacheWarning              TranscriptRole = "cache_warning"
	TranscriptRoleError                     TranscriptRole = "error"
	TranscriptRoleThinking                  TranscriptRole = "thinking"
	TranscriptRoleThinkingTrace             TranscriptRole = "thinking_trace"
	TranscriptRoleReasoning                 TranscriptRole = "reasoning"
	TranscriptRoleCompactionSummary         TranscriptRole = TranscriptRole(transcript.EntryRoleCompactionSummary)
	TranscriptRoleManualCompactionCarryover TranscriptRole = TranscriptRole(transcript.EntryRoleManualCompactionCarryover)
	TranscriptRoleDeveloperContext          TranscriptRole = TranscriptRole(transcript.EntryRoleDeveloperContext)
	TranscriptRoleDeveloperFeedback         TranscriptRole = TranscriptRole(transcript.EntryRoleDeveloperFeedback)
	TranscriptRoleDeveloperErrorFeedback    TranscriptRole = TranscriptRole(transcript.EntryRoleDeveloperErrorFeedback)
	TranscriptRoleInterruption              TranscriptRole = TranscriptRole(transcript.EntryRoleInterruption)
	TranscriptRoleGoalFeedback              TranscriptRole = TranscriptRole(transcript.EntryRoleGoalFeedback)
)

type RenderIntent string

const (
	RenderIntentUnknown                   RenderIntent = "unknown"
	RenderIntentUser                      RenderIntent = "user"
	RenderIntentAssistant                 RenderIntent = "assistant"
	RenderIntentAssistantCommentary       RenderIntent = "assistant_commentary"
	RenderIntentSystem                    RenderIntent = "system"
	RenderIntentTool                      RenderIntent = "tool"
	RenderIntentToolSuccess               RenderIntent = "tool_success"
	RenderIntentToolError                 RenderIntent = "tool_error"
	RenderIntentToolShell                 RenderIntent = "tool_shell"
	RenderIntentToolShellSuccess          RenderIntent = "tool_shell_success"
	RenderIntentToolShellError            RenderIntent = "tool_shell_error"
	RenderIntentToolPatch                 RenderIntent = "tool_patch"
	RenderIntentToolPatchSuccess          RenderIntent = "tool_patch_success"
	RenderIntentToolPatchError            RenderIntent = "tool_patch_error"
	RenderIntentToolQuestion              RenderIntent = "tool_question"
	RenderIntentToolQuestionError         RenderIntent = "tool_question_error"
	RenderIntentToolWebSearch             RenderIntent = "tool_web_search"
	RenderIntentToolWebSearchSuccess      RenderIntent = "tool_web_search_success"
	RenderIntentToolWebSearchError        RenderIntent = "tool_web_search_error"
	RenderIntentReviewerStatus            RenderIntent = "reviewer_status"
	RenderIntentReviewerSuggestions       RenderIntent = "reviewer_suggestions"
	RenderIntentWarning                   RenderIntent = "warning"
	RenderIntentCacheWarning              RenderIntent = "cache_warning"
	RenderIntentError                     RenderIntent = "error"
	RenderIntentReasoning                 RenderIntent = "reasoning"
	RenderIntentThinking                  RenderIntent = "thinking"
	RenderIntentThinkingTrace             RenderIntent = "thinking_trace"
	RenderIntentCompactionSummary         RenderIntent = RenderIntent(TranscriptRoleCompactionSummary)
	RenderIntentManualCompactionCarryover RenderIntent = RenderIntent(TranscriptRoleManualCompactionCarryover)
	RenderIntentDeveloperContext          RenderIntent = RenderIntent(TranscriptRoleDeveloperContext)
	RenderIntentDeveloperFeedback         RenderIntent = RenderIntent(TranscriptRoleDeveloperFeedback)
	RenderIntentDeveloperErrorFeedback    RenderIntent = RenderIntent(TranscriptRoleDeveloperErrorFeedback)
	RenderIntentInterruption              RenderIntent = RenderIntent(TranscriptRoleInterruption)
	RenderIntentGoalFeedback              RenderIntent = RenderIntent(TranscriptRoleGoalFeedback)
)

func TranscriptRoleFromWire(role string) TranscriptRole {
	normalized := strings.ToLower(strings.TrimSpace(role))
	if normalized == "" {
		return TranscriptRoleUnknown
	}
	return TranscriptRole(normalized)
}

func (r TranscriptRole) String() string {
	return string(r)
}

func (r TranscriptRole) IsToolResult() bool {
	switch r {
	case TranscriptRoleToolResult, TranscriptRoleToolResultOK, TranscriptRoleToolResultError:
		return true
	default:
		return false
	}
}

func (r TranscriptRole) IsThinking() bool {
	switch r {
	case TranscriptRoleThinking, TranscriptRoleThinkingTrace, TranscriptRoleReasoning:
		return true
	default:
		return false
	}
}

func (r RenderIntent) IsThinking() bool {
	switch r {
	case RenderIntentThinking, RenderIntentThinkingTrace, RenderIntentReasoning:
		return true
	default:
		return false
	}
}

func (r TranscriptRole) IsCompaction() bool {
	switch r {
	case TranscriptRoleCompactionSummary, TranscriptRoleManualCompactionCarryover:
		return true
	default:
		return false
	}
}

func (r TranscriptRole) DisplayIntent(phase clientui.MessagePhase) RenderIntent {
	switch r {
	case TranscriptRoleUser:
		return RenderIntentUser
	case TranscriptRoleAssistant:
		if phase == clientui.MessagePhaseCommentary {
			return RenderIntentAssistantCommentary
		}
		return RenderIntentAssistant
	case TranscriptRoleSystem:
		return RenderIntentSystem
	case TranscriptRoleReviewerStatus:
		return RenderIntentReviewerStatus
	case TranscriptRoleReviewerSuggestions:
		return RenderIntentReviewerSuggestions
	case TranscriptRoleWarning:
		return RenderIntentWarning
	case TranscriptRoleCacheWarning:
		return RenderIntentCacheWarning
	case TranscriptRoleError:
		return RenderIntentError
	case TranscriptRoleThinking:
		return RenderIntentThinking
	case TranscriptRoleThinkingTrace:
		return RenderIntentThinkingTrace
	case TranscriptRoleReasoning:
		return RenderIntentReasoning
	case TranscriptRoleCompactionSummary:
		return RenderIntentCompactionSummary
	case TranscriptRoleManualCompactionCarryover:
		return RenderIntentManualCompactionCarryover
	case TranscriptRoleDeveloperContext:
		return RenderIntentDeveloperContext
	case TranscriptRoleDeveloperFeedback:
		return RenderIntentDeveloperFeedback
	case TranscriptRoleDeveloperErrorFeedback:
		return RenderIntentDeveloperErrorFeedback
	case TranscriptRoleInterruption:
		return RenderIntentInterruption
	case TranscriptRoleGoalFeedback:
		return RenderIntentGoalFeedback
	case TranscriptRoleToolResult, TranscriptRoleToolResultOK:
		return RenderIntentToolSuccess
	case TranscriptRoleToolResultError:
		return RenderIntentToolError
	default:
		if strings.TrimSpace(string(r)) == "" {
			return RenderIntentUnknown
		}
		return RenderIntent(r)
	}
}

func (i RenderIntent) String() string {
	return string(i)
}

func (i RenderIntent) IsToolHeadline() bool {
	switch i {
	case RenderIntentTool, RenderIntentToolSuccess, RenderIntentToolError,
		RenderIntentToolShell, RenderIntentToolShellSuccess, RenderIntentToolShellError,
		RenderIntentToolPatch, RenderIntentToolPatchSuccess, RenderIntentToolPatchError,
		RenderIntentToolQuestion, RenderIntentToolQuestionError,
		RenderIntentToolWebSearch, RenderIntentToolWebSearchSuccess, RenderIntentToolWebSearchError:
		return true
	default:
		return false
	}
}

func (i RenderIntent) IsToolErrorHeadline() bool {
	switch i {
	case RenderIntentToolError, RenderIntentToolShellError, RenderIntentToolPatchError, RenderIntentToolQuestionError, RenderIntentToolWebSearchError:
		return true
	default:
		return false
	}
}

func (i RenderIntent) IsShellPreview() bool {
	switch i {
	case RenderIntentToolShell, RenderIntentToolShellSuccess, RenderIntentToolShellError:
		return true
	default:
		return false
	}
}

func (i RenderIntent) BaseToolResultIntent(resultRole TranscriptRole) RenderIntent {
	if resultRole == TranscriptRoleToolResultError {
		switch i {
		case RenderIntentToolQuestion:
			return RenderIntentToolQuestionError
		case RenderIntentToolWebSearch:
			return RenderIntentToolWebSearchError
		case RenderIntentToolPatch:
			return RenderIntentToolPatchError
		case RenderIntentToolShell:
			return RenderIntentToolShellError
		default:
			return RenderIntentToolError
		}
	}
	if resultRole.IsToolResult() {
		switch i {
		case RenderIntentToolQuestion:
			return RenderIntentToolQuestion
		case RenderIntentToolWebSearch:
			return RenderIntentToolWebSearchSuccess
		case RenderIntentToolPatch:
			return RenderIntentToolPatchSuccess
		case RenderIntentToolShell:
			return RenderIntentToolShellSuccess
		default:
			return RenderIntentToolSuccess
		}
	}
	switch i {
	case RenderIntentToolShell, RenderIntentToolWebSearch, RenderIntentToolPatch:
		return i
	default:
		return RenderIntentTool
	}
}
