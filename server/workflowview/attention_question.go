package workflowview

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
)

type SessionActiveTranscriptProvider interface {
	SessionNewestActiveSegmentQuestions(ctx context.Context, sessionID string) ([]PendingQuestionTranscriptEntry, error)
}

type PendingPromptSnapshot struct {
	PromptID               clientui.PromptID
	SessionID              runtimeids.SessionID
	StepID                 runtimeids.StepID
	ID                     string
	CreatedAt              time.Time
	Question               string
	Suggestions            []string
	RecommendedOptionIndex *int
	Approval               bool
	ApprovalDecisions      []clientui.ApprovalDecision
}

type PendingQuestionTranscriptEntry struct {
	AskID                  string
	Question               string
	Suggestions            []string
	RecommendedOptionIndex *int
}

type PendingPromptSource interface {
	ListPendingPrompts(sessionID string) ([]PendingPromptSnapshot, error)
}

// ErrPendingQuestionNotFound is returned when the newest active transcript
// segment has no pending question matching the requested ask ID.
var ErrPendingQuestionNotFound = errors.New("pending question was not found")

const pendingQuestionFallbackMessage = "Question pending; open the task to answer."

type pendingQuestionResolver struct {
	transcripts SessionActiveTranscriptProvider
	prompts     PendingPromptSource
}

type pendingQuestion struct {
	message                string
	suggestions            []string
	recommendedOptionIndex *int
	prompt                 *serverapi.WorkflowAttentionQuestionPrompt
}

func newPendingQuestionResolver(transcripts SessionActiveTranscriptProvider, prompts PendingPromptSource) *pendingQuestionResolver {
	return &pendingQuestionResolver{transcripts: transcripts, prompts: prompts}
}

func (r *pendingQuestionResolver) Question(ctx context.Context, sessionID *string, askID string) (pendingQuestion, error) {
	askID = strings.TrimSpace(askID)
	if askID == "" {
		return pendingQuestion{}, errors.New("ask_id is required to resolve pending question")
	}
	if sessionID == nil {
		return pendingQuestion{}, ErrPendingQuestionNotFound
	}
	resolvedSessionID := strings.TrimSpace(*sessionID)
	if resolvedSessionID == "" {
		return pendingQuestion{}, errors.New("session_id must be non-blank when present")
	}
	if question, ok, err := r.questionFromPendingPrompt(resolvedSessionID, askID); ok || err != nil {
		return question, err
	}
	if r == nil || r.transcripts == nil {
		return pendingQuestion{}, errors.New("session active transcript provider is required to resolve pending question")
	}
	entries, err := r.transcripts.SessionNewestActiveSegmentQuestions(ctx, resolvedSessionID)
	if err != nil {
		return pendingQuestion{}, fmt.Errorf("load session %q newest active transcript segment for pending question %q: %w", resolvedSessionID, askID, err)
	}
	question, err := pendingQuestionFromTranscriptEntries(entries, askID)
	if err != nil {
		return pendingQuestion{}, err
	}
	if strings.TrimSpace(question.message) == "" {
		return pendingQuestion{}, fmt.Errorf("pending question %q in session %q newest active transcript segment: %w", askID, resolvedSessionID, ErrPendingQuestionNotFound)
	}
	return question, nil
}

func (r *pendingQuestionResolver) questionFromPendingPrompt(sessionID string, askID string) (pendingQuestion, bool, error) {
	if r == nil || r.prompts == nil {
		return pendingQuestion{}, false, nil
	}
	snapshots, err := r.prompts.ListPendingPrompts(sessionID)
	if err != nil {
		return pendingQuestion{}, false, fmt.Errorf("load pending prompts for session %q: %w", sessionID, err)
	}
	for _, snapshot := range snapshots {
		if strings.TrimSpace(snapshot.ID) != askID {
			continue
		}
		return pendingQuestionFromPrompt(snapshot)
	}
	return pendingQuestion{}, false, nil
}

func pendingQuestionFromPrompt(snapshot PendingPromptSnapshot) (pendingQuestion, bool, error) {
	if err := snapshot.PromptID.Validate(); err != nil {
		return pendingQuestion{}, true, fmt.Errorf("pending prompt identity: %w", err)
	}
	if snapshot.SessionID.IsZero() {
		return pendingQuestion{}, true, fmt.Errorf("pending prompt %q has no session identity", snapshot.PromptID)
	}
	if snapshot.StepID.IsZero() {
		return pendingQuestion{}, true, fmt.Errorf("pending prompt %q has no step identity", snapshot.PromptID)
	}
	if string(snapshot.PromptID) != strings.TrimSpace(snapshot.ID) {
		return pendingQuestion{}, true, fmt.Errorf("pending prompt identity %q does not match request identity %q", snapshot.PromptID, snapshot.ID)
	}
	if snapshot.Approval {
		decisions := append([]clientui.ApprovalDecision(nil), snapshot.ApprovalDecisions...)
		for _, decision := range decisions {
			switch decision {
			case clientui.ApprovalDecisionAllowOnce, clientui.ApprovalDecisionAllowSession, clientui.ApprovalDecisionDeny:
			default:
				return pendingQuestion{}, true, fmt.Errorf("pending approval question %q has invalid decision %q", snapshot.ID, decision)
			}
		}
		if len(decisions) == 0 {
			return pendingQuestion{}, true, fmt.Errorf("pending approval question %q has no approval decisions", snapshot.ID)
		}
		return pendingQuestion{
			message: strings.TrimSpace(snapshot.Question),
			prompt: &serverapi.WorkflowAttentionQuestionPrompt{
				SessionID:         snapshot.SessionID,
				StepID:            snapshot.StepID,
				PromptID:          snapshot.PromptID,
				Kind:              serverapi.WorkflowAttentionQuestionKindApproval,
				ApprovalDecisions: decisions,
			},
		}, true, nil
	}
	suggestions := normalizedPendingQuestionSuggestions(snapshot.Suggestions)
	recommended, err := validatePendingQuestionRecommendation(snapshot.RecommendedOptionIndex, len(suggestions))
	if err != nil {
		return pendingQuestion{}, true, fmt.Errorf("pending question %q: %w", snapshot.ID, err)
	}
	return pendingQuestion{
		message:                strings.TrimSpace(snapshot.Question),
		suggestions:            suggestions,
		recommendedOptionIndex: recommended,
		prompt: &serverapi.WorkflowAttentionQuestionPrompt{
			SessionID:              snapshot.SessionID,
			StepID:                 snapshot.StepID,
			PromptID:               snapshot.PromptID,
			Kind:                   serverapi.WorkflowAttentionQuestionKindOrdinary,
			Suggestions:            suggestions,
			RecommendedOptionIndex: textutil.Pointer(recommended),
		},
	}, true, nil
}

func normalizedPendingQuestionSuggestions(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return append([]string(nil), in...)
}

func pendingQuestionFromTranscriptEntries(entries []PendingQuestionTranscriptEntry, askID string) (pendingQuestion, error) {
	for _, entry := range entries {
		if strings.TrimSpace(entry.AskID) != askID {
			continue
		}
		if question := strings.TrimSpace(entry.Question); question != "" {
			recommended, err := validatePendingQuestionRecommendation(entry.RecommendedOptionIndex, len(entry.Suggestions))
			if err != nil {
				return pendingQuestion{}, fmt.Errorf("pending question %q: %w", entry.AskID, err)
			}
			return pendingQuestion{
				message:                question,
				suggestions:            append([]string(nil), entry.Suggestions...),
				recommendedOptionIndex: recommended,
				prompt: &serverapi.WorkflowAttentionQuestionPrompt{
					Kind:                   serverapi.WorkflowAttentionQuestionKindOrdinary,
					Suggestions:            append([]string(nil), entry.Suggestions...),
					RecommendedOptionIndex: textutil.Pointer(recommended),
				},
			}, nil
		}
	}
	return pendingQuestion{}, nil
}

func validatePendingQuestionRecommendation(index *int, suggestionCount int) (*int, error) {
	if index == nil {
		return nil, nil
	}
	if *index < 1 || *index > suggestionCount {
		return nil, serverapi.WorkflowRequestValidationError{
			Code:    serverapi.WorkflowRequestErrorInvalidValue,
			Field:   "recommended_option_index",
			Message: fmt.Sprintf("recommended option %d is invalid for %d suggestions", *index, suggestionCount),
		}
	}
	return textutil.Pointer(index), nil
}
