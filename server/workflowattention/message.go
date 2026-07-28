package workflowattention

import (
	"encoding/json"
	"strings"
)

const ApprovalRequiredMessage = "Approval required"

func InterruptedCurrentNodeMessage(reason string, detailJSON string) string {
	message := "Workflow execution interrupted"
	if strings.TrimSpace(reason) != "" {
		message += ": " + strings.TrimSpace(reason)
	}
	if detail := interruptionErrorDetail(detailJSON); detail != nil {
		message += ": " + *detail
	}
	return message
}

func interruptionErrorDetail(detailJSON string) *string {
	trimmed := strings.TrimSpace(detailJSON)
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
