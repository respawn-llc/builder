package workflowexecution

import (
	"context"
	"errors"
	"strings"

	"core/server/workflow"
	"core/shared/clientui"
	"core/shared/runtimeids"
)

type InterruptSelector struct {
	TaskID      workflow.TaskID
	SessionID   *runtimeids.SessionID
	CurrentNode *workflow.CurrentNodeReference

	expectedRunID       *currentNodeRunID
	expectedOperation   *clientui.RuntimeOperationRef
	revalidateOwnership func(context.Context) error
}

func (s InterruptSelector) Validate() error {
	if strings.TrimSpace(string(s.TaskID)) == "" {
		return errors.New("workflow interrupt task id is required")
	}
	if s.SessionID != nil && s.SessionID.IsZero() {
		return errors.New("workflow interrupt session id is invalid")
	}
	if s.CurrentNode != nil && s.CurrentNode.TaskID != s.TaskID {
		return errors.New("workflow interrupt Current Node belongs to a different Task")
	}
	if s.CurrentNode != nil && s.SessionID == nil {
		return errors.New("workflow interrupt Current Node requires a Session selector")
	}
	return nil
}
