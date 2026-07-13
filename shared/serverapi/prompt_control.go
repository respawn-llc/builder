package serverapi

import (
	"errors"
	"strings"

	"core/shared/clientui"
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
	Commentary      string                    `json:"commentary,omitempty"`
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
	if strings.TrimSpace(r.ErrorMessage) == "" {
		switch r.Decision {
		case clientui.ApprovalDecisionAllowOnce, clientui.ApprovalDecisionAllowSession, clientui.ApprovalDecisionDeny:
		default:
			return errors.New("decision is required when error_message is empty")
		}
	}
	return nil
}
