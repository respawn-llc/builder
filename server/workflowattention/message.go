package workflowattention

import (
	"encoding/json"
	"strings"
)

const ApprovalRequiredMessage = "Approval required"

func InterruptedRunMessage(reason *string, detailJSON *string) string {
	message := "Run interrupted"
	if reason != nil && strings.TrimSpace(*reason) != "" {
		message += ": " + strings.TrimSpace(*reason)
	}
	if detail := interruptionErrorDetail(detailJSON); detail != nil {
		message += ": " + *detail
	}
	return message
}

func interruptionErrorDetail(detailJSON *string) *string {
	if detailJSON == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*detailJSON)
	if trimmed == "" {
		return nil
	}
	var detail struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(trimmed), &detail); err != nil {
		return nil
	}
	errorDetail := strings.TrimSpace(detail.Error)
	if errorDetail == "" {
		return nil
	}
	return &errorDetail
}
