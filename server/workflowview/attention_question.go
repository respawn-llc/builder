package workflowview

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/runtime"
	askquestion "core/server/tools"
	"core/shared/clientui"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

type SessionActiveTranscriptProvider interface {
	SessionNewestActiveSegmentEntries(ctx context.Context, sessionID string) ([]runtime.ChatEntry, error)
}

type PendingPromptSnapshot struct {
	Request askquestion.AskQuestionRequest
}

type PendingPromptSource interface {
	ListPendingPrompts(sessionID string) []PendingPromptSnapshot
}

// ErrPendingQuestionNotFound is returned when the newest active transcript
// segment has no pending question matching the requested ask ID.
var ErrPendingQuestionNotFound = errors.New("pending question was not found")

func workflowQuestionAttentionItem(id string, projectID string, workflowID string, taskID string, shortID string, title string, runID string, sessionID string, askID string, question pendingQuestion, occurredAtUnixMs int64) serverapi.WorkflowAttentionItem {
	workflowIDValue := workflowID
	return serverapi.WorkflowAttentionItem{ID: id, Kind: "question", ProjectID: projectID, WorkflowID: &workflowIDValue, TaskID: taskID, TaskShortID: shortID, TaskTitle: title, RunID: runID, SessionID: sessionID, AskID: askID, Message: question.message, Suggestions: question.suggestions, RecommendedOptionIndex: question.recommendedOptionIndex, Question: question.prompt, OccurredAtUnixMs: occurredAtUnixMs}
}

const pendingQuestionFallbackMessage = "Question pending; open the task to answer."

type pendingQuestionResolver struct {
	transcripts SessionActiveTranscriptProvider
	prompts     PendingPromptSource
}

type pendingQuestion struct {
	message                string
	suggestions            []string
	recommendedOptionIndex int
	prompt                 *serverapi.WorkflowAttentionQuestionPrompt
}

func newPendingQuestionResolver(transcripts SessionActiveTranscriptProvider, prompts PendingPromptSource) *pendingQuestionResolver {
	return &pendingQuestionResolver{transcripts: transcripts, prompts: prompts}
}

func (r *pendingQuestionResolver) Question(ctx context.Context, sessionID string, askID string) (pendingQuestion, error) {
	sessionID = strings.TrimSpace(sessionID)
	askID = strings.TrimSpace(askID)
	if question, ok, err := r.questionFromPendingPrompt(sessionID, askID); ok || err != nil {
		return question, err
	}
	if r == nil || r.transcripts == nil {
		return pendingQuestion{}, errors.New("session active transcript provider is required to resolve pending question")
	}
	if sessionID == "" || askID == "" {
		return pendingQuestion{}, errors.New("session_id and ask_id are required to resolve pending question")
	}
	entries, err := r.transcripts.SessionNewestActiveSegmentEntries(ctx, sessionID)
	if err != nil {
		return pendingQuestion{}, fmt.Errorf("load session %q newest active transcript segment for pending question %q: %w", sessionID, askID, err)
	}
	question := askQuestionFromActiveTranscriptEntries(entries, askID)
	if strings.TrimSpace(question.message) == "" {
		return pendingQuestion{}, fmt.Errorf("pending question %q in session %q newest active transcript segment: %w", askID, sessionID, ErrPendingQuestionNotFound)
	}
	return question, nil
}

func (r *pendingQuestionResolver) questionFromPendingPrompt(sessionID string, askID string) (pendingQuestion, bool, error) {
	if r == nil || r.prompts == nil || sessionID == "" || askID == "" {
		return pendingQuestion{}, false, nil
	}
	for _, snapshot := range r.prompts.ListPendingPrompts(sessionID) {
		req := snapshot.Request
		if strings.TrimSpace(req.ID) != askID {
			continue
		}
		return pendingQuestionFromRequest(req)
	}
	return pendingQuestion{}, false, nil
}

func pendingQuestionFromRequest(req askquestion.AskQuestionRequest) (pendingQuestion, bool, error) {
	if req.Approval {
		decisions := make([]clientui.ApprovalDecision, 0, len(req.ApprovalOptions))
		for _, option := range req.ApprovalOptions {
			decision := clientui.ApprovalDecision(option.Decision)
			switch decision {
			case clientui.ApprovalDecisionAllowOnce, clientui.ApprovalDecisionAllowSession, clientui.ApprovalDecisionDeny:
				decisions = append(decisions, decision)
			default:
				return pendingQuestion{}, true, fmt.Errorf("pending approval question %q has invalid decision %q", req.ID, option.Decision)
			}
		}
		if len(decisions) == 0 {
			return pendingQuestion{}, true, fmt.Errorf("pending approval question %q has no approval decisions", req.ID)
		}
		return pendingQuestion{
			message: strings.TrimSpace(req.Question),
			prompt: &serverapi.WorkflowAttentionQuestionPrompt{
				Kind:              serverapi.WorkflowAttentionQuestionKindApproval,
				ApprovalDecisions: decisions,
			},
		}, true, nil
	}
	suggestions := normalizedPendingQuestionSuggestions(req.Suggestions)
	return pendingQuestion{
		message:                strings.TrimSpace(req.Question),
		suggestions:            suggestions,
		recommendedOptionIndex: req.RecommendedOptionIndex,
		prompt: &serverapi.WorkflowAttentionQuestionPrompt{
			Kind:                   serverapi.WorkflowAttentionQuestionKindOrdinary,
			Suggestions:            suggestions,
			RecommendedOptionIndex: req.RecommendedOptionIndex,
		},
	}, true, nil
}

func normalizedPendingQuestionSuggestions(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return append([]string(nil), in...)
}

func askQuestionFromActiveTranscriptEntries(entries []runtime.ChatEntry, askID string) pendingQuestion {
	for _, entry := range entries {
		entryAskID := strings.TrimSpace(entry.ToolCallID)
		if strings.TrimSpace(entry.Role) != "tool_call" || entryAskID != askID || entry.ToolCall == nil {
			continue
		}
		if strings.TrimSpace(entry.ToolCall.ToolName) != string(toolspec.ToolAskQuestion) {
			continue
		}
		if question := strings.TrimSpace(entry.ToolCall.Question); question != "" {
			return pendingQuestion{
				message:                question,
				suggestions:            append([]string(nil), entry.ToolCall.Suggestions...),
				recommendedOptionIndex: entry.ToolCall.RecommendedOptionIndex,
				prompt: &serverapi.WorkflowAttentionQuestionPrompt{
					Kind:                   serverapi.WorkflowAttentionQuestionKindOrdinary,
					Suggestions:            append([]string(nil), entry.ToolCall.Suggestions...),
					RecommendedOptionIndex: entry.ToolCall.RecommendedOptionIndex,
				},
			}
		}
	}
	return pendingQuestion{}
}
