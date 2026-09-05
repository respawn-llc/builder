package serverapi

import (
	"errors"
	"fmt"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/sessioncontract"
)

type PromptAnswerBatchRequest struct {
	SessionID runtimeids.SessionID     `json:"session_id"`
	StepID    runtimeids.StepID        `json:"step_id"`
	Entries   []PromptAnswerBatchEntry `json:"entries"`
}

type PromptAnswerBatchEntry struct {
	ToolCallID     clientui.ToolCallID   `json:"tool_call_id"`
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
	ToolCallID clientui.ToolCallID      `json:"tool_call_id"`
	Outcome    PromptAnswerBatchOutcome `json:"outcome"`
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
	seen := make(map[clientui.ToolCallID]struct{}, len(r.Entries))
	for index, entry := range r.Entries {
		if err := entry.Validate(); err != nil {
			return fmt.Errorf("prompt answer batch entry %d: %w", index, err)
		}
		if _, exists := seen[entry.ToolCallID]; exists {
			return fmt.Errorf("prompt answer batch tool call id %q is duplicated", entry.ToolCallID)
		}
		seen[entry.ToolCallID] = struct{}{}
	}
	return nil
}

func (e PromptAnswerBatchEntry) Validate() error {
	if err := e.ToolCallID.Validate(); err != nil {
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
	return sessioncontract.ValidatePromptQuestionAnswerShape(a.SelectedOptionNumber, a.Freeform)
}

func (a PromptApprovalAnswer) Validate() error {
	return sessioncontract.ValidatePromptApprovalAnswerShape(a.Decision, a.Commentary)
}

func (r PromptAnswerBatchResponse) Validate() error {
	if len(r.Results) == 0 {
		return errors.New("prompt answer batch results are required")
	}
	seen := make(map[clientui.ToolCallID]struct{}, len(r.Results))
	for index, result := range r.Results {
		if err := result.ToolCallID.Validate(); err != nil {
			return fmt.Errorf("prompt answer batch result %d: %w", index, err)
		}
		switch result.Outcome {
		case PromptAnswerBatchOutcomeResolved, PromptAnswerBatchOutcomeSkipped:
		default:
			return fmt.Errorf("prompt answer batch result %d outcome is invalid", index)
		}
		if _, exists := seen[result.ToolCallID]; exists {
			return fmt.Errorf("prompt answer batch result tool call id %q is duplicated", result.ToolCallID)
		}
		seen[result.ToolCallID] = struct{}{}
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
	requestIDs := make(map[clientui.ToolCallID]struct{}, len(request.Entries))
	for _, entry := range request.Entries {
		requestIDs[entry.ToolCallID] = struct{}{}
	}
	for _, result := range response.Results {
		if _, exists := requestIDs[result.ToolCallID]; !exists {
			return fmt.Errorf("prompt answer batch result contains foreign tool call id %q", result.ToolCallID)
		}
	}
	return nil
}
