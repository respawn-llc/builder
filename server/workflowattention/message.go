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
	if detail := interruptionErrorDetail(detailJSON); detail != "" {
		message += ": " + detail
	}
	return message
}

func interruptionErrorDetail(detailJSON *string) string {
	if detailJSON == nil || strings.TrimSpace(*detailJSON) == "" {
		return ""
	}
	var detail struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(*detailJSON), &detail); err != nil {
		return ""
	}
	return strings.TrimSpace(detail.Error)
}
