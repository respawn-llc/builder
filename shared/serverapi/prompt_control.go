package serverapi

import (
	"errors"
	"fmt"
	"strings"

	"core/shared/clientui"
	"core/shared/runtimeids"
)

type AskAnswerRequest struct {
	ClientRequestID      string `json:"client_request_id"`
	SessionID            string `json:"session_id"`
	AskID                string `json:"ask_id"`
	ErrorMessage         string `json:"error_message,omitempty"`
	Answer               string `json:"answer,omitempty"`
	SelectedOptionNumber *int   `json:"selected_option_number"`
	FreeformAnswer       string `json:"freeform_answer,omitempty"`
}

type ApprovalAnswerRequest struct {
	ClientRequestID string                    `json:"client_request_id"`
	SessionID       string                    `json:"session_id"`
	ApprovalID      string                    `json:"approval_id"`
	ErrorMessage    string                    `json:"error_message,omitempty"`
	Decision        clientui.ApprovalDecision `json:"decision"`
	Commentary      *string                   `json:"commentary,omitempty"`
}

type PromptAnswerBatchRequest struct {
	SessionID runtimeids.SessionID     `json:"session_id"`
	StepID    runtimeids.StepID        `json:"step_id"`
	Entries   []PromptAnswerBatchEntry `json:"entries"`
}

type PromptAnswerBatchEntry struct {
	PromptID       clientui.PromptID     `json:"prompt_id"`
	QuestionAnswer *PromptQuestionAnswer `json:"question_answer,omitempty"`
	ApprovalAnswer *PromptApprovalAnswer `json:"approval_answer,omitempty"`
	Declined       *PromptDeclined       `json:"declined,omitempty"`
}

type PromptQuestionAnswer struct {
	SelectedOptionNumber *int    `json:"selected_option_number,omitempty"`
	Freeform             *string `json:"freeform,omitempty"`
}

type PromptApprovalAnswer struct {
	Decision   clientui.ApprovalDecision `json:"decision"`
	Commentary *string                   `json:"commentary,omitempty"`
}

type PromptDeclined struct{}

type PromptAnswerBatchOutcome string

const (
	PromptAnswerBatchOutcomeResolved PromptAnswerBatchOutcome = "resolved"
	PromptAnswerBatchOutcomeSkipped  PromptAnswerBatchOutcome = "skipped"
)

type PromptAnswerBatchResponse struct {
	Results []PromptAnswerBatchResult `json:"results"`
}

type PromptAnswerBatchResult struct {
	PromptID clientui.PromptID        `json:"prompt_id"`
	Outcome  PromptAnswerBatchOutcome `json:"outcome"`
}

func (r PromptAnswerBatchRequest) Validate() error {
	if r.SessionID.IsZero() {
		return errors.New("session_id is required")
	}
	if r.StepID.IsZero() {
		return errors.New("step_id is required")
	}
	if len(r.Entries) == 0 {
		return errors.New("prompt answer batch entries are required")
	}
	seen := make(map[clientui.PromptID]struct{}, len(r.Entries))
	for index, entry := range r.Entries {
		if err := entry.Validate(); err != nil {
			return fmt.Errorf("prompt answer batch entry %d: %w", index, err)
		}
		if _, exists := seen[entry.PromptID]; exists {
			return fmt.Errorf("prompt answer batch prompt id %q is duplicated", entry.PromptID)
		}
		seen[entry.PromptID] = struct{}{}
	}
	return nil
}

func (e PromptAnswerBatchEntry) Validate() error {
	if err := e.PromptID.Validate(); err != nil {
		return err
	}
	memberCount := 0
	if e.QuestionAnswer != nil {
		memberCount++
	}
	if e.ApprovalAnswer != nil {
		memberCount++
	}
	if e.Declined != nil {
		memberCount++
	}
	if memberCount != 1 {
		return errors.New("exactly one question_answer, approval_answer, or declined payload is required")
	}
	switch {
	case e.QuestionAnswer != nil:
		return e.QuestionAnswer.Validate()
	case e.ApprovalAnswer != nil:
		return e.ApprovalAnswer.Validate()
	default:
		return nil
	}
}

func (a PromptQuestionAnswer) Validate() error {
	if a.SelectedOptionNumber == nil && a.Freeform == nil {
		return errors.New("question answer requires selected_option_number or freeform")
	}
	if a.SelectedOptionNumber != nil && *a.SelectedOptionNumber <= 0 {
		return errors.New("selected_option_number must be positive when present")
	}
	if a.Freeform != nil && strings.TrimSpace(*a.Freeform) == "" {
		return errors.New("freeform must be non-blank when present")
	}
	return nil
}

func (a PromptApprovalAnswer) Validate() error {
	switch a.Decision {
	case clientui.ApprovalDecisionAllowOnce,
		clientui.ApprovalDecisionAllowSession,
		clientui.ApprovalDecisionDeny:
	default:
		return errors.New("approval decision is invalid")
	}
	if a.Commentary != nil && strings.TrimSpace(*a.Commentary) == "" {
		return errors.New("commentary must be non-blank when present")
	}
	return nil
}

func (r PromptAnswerBatchResponse) Validate() error {
	if len(r.Results) == 0 {
		return errors.New("prompt answer batch results are required")
	}
	seen := make(map[clientui.PromptID]struct{}, len(r.Results))
	for index, result := range r.Results {
		if err := result.PromptID.Validate(); err != nil {
			return fmt.Errorf("prompt answer batch result %d: %w", index, err)
		}
		switch result.Outcome {
		case PromptAnswerBatchOutcomeResolved, PromptAnswerBatchOutcomeSkipped:
		default:
			return fmt.Errorf("prompt answer batch result %d outcome is invalid", index)
		}
		if _, exists := seen[result.PromptID]; exists {
			return fmt.Errorf("prompt answer batch result prompt id %q is duplicated", result.PromptID)
		}
		seen[result.PromptID] = struct{}{}
	}
	return nil
}

func ValidatePromptAnswerBatchResponse(request PromptAnswerBatchRequest, response PromptAnswerBatchResponse) error {
	if err := request.Validate(); err != nil {
		return fmt.Errorf("validate prompt answer batch request: %w", err)
	}
	if err := response.Validate(); err != nil {
		return fmt.Errorf("validate prompt answer batch response: %w", err)
	}
	if len(request.Entries) != len(response.Results) {
		return fmt.Errorf(
			"prompt answer batch result count %d does not match request entry count %d",
			len(response.Results),
			len(request.Entries),
		)
	}
	requestIDs := make(map[clientui.PromptID]struct{}, len(request.Entries))
	for _, entry := range request.Entries {
		requestIDs[entry.PromptID] = struct{}{}
	}
	for _, result := range response.Results {
		if _, exists := requestIDs[result.PromptID]; !exists {
			return fmt.Errorf("prompt answer batch result contains foreign prompt id %q", result.PromptID)
		}
	}
	return nil
}

func (r AskAnswerRequest) Validate() error {
	if strings.TrimSpace(r.ClientRequestID) == "" {
		return errors.New("client_request_id is required")
	}
	if err := validateRequiredSessionID(r.SessionID); err != nil {
		return err
	}
	if strings.TrimSpace(r.AskID) == "" {
		return errors.New("ask_id is required")
	}
	if r.SelectedOptionNumber != nil && *r.SelectedOptionNumber <= 0 {
		return errors.New("selected_option_number must be positive when present")
	}
	hasAnswer := strings.TrimSpace(r.Answer) != "" || strings.TrimSpace(r.FreeformAnswer) != "" || r.SelectedOptionNumber != nil
	if hasAnswer && strings.TrimSpace(r.ErrorMessage) != "" {
		return errors.New("error_message cannot be combined with answer fields")
	}
	if !hasAnswer && strings.TrimSpace(r.ErrorMessage) == "" {
		return errors.New("answer is required")
	}
	return nil
}

func (r ApprovalAnswerRequest) Validate() error {
	if strings.TrimSpace(r.ClientRequestID) == "" {
		return errors.New("client_request_id is required")
	}
	if err := validateRequiredSessionID(r.SessionID); err != nil {
		return err
	}
	if strings.TrimSpace(r.ApprovalID) == "" {
		return errors.New("approval_id is required")
	}
	if r.Commentary != nil && strings.TrimSpace(*r.Commentary) == "" {
		return errors.New("commentary must be non-blank when present")
	}
	if strings.TrimSpace(r.ErrorMessage) == "" {
		switch r.Decision {
		case clientui.ApprovalDecisionAllowOnce, clientui.ApprovalDecisionAllowSession, clientui.ApprovalDecisionDeny:
		default:
			return errors.New("decision is required when error_message is empty")
		}
	}
	return nil
}
