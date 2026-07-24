package workflowexecution

import (
	"errors"
	"strings"

	"core/server/workflow"
	"core/shared/runtimeids"
)

type InterruptSelector struct {
	TaskID    workflow.TaskID
	SessionID *runtimeids.SessionID
}

func (s InterruptSelector) Validate() error {
	if strings.TrimSpace(string(s.TaskID)) == "" {
		return errors.New("workflow interrupt task id is required")
	}
	if s.SessionID != nil && s.SessionID.IsZero() {
		return errors.New("workflow interrupt session id is invalid")
	}
	return nil
}
