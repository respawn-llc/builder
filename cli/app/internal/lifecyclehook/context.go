package lifecyclehook

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"core/shared/clientui"
	"core/shared/lifecyclecontract"
	"core/shared/runtimeids"
	"core/shared/textutil"
)

type EventContext struct {
	mu      sync.RWMutex
	context lifecyclecontract.Context
}

func InitialContext(sessionID string, sessionTitle *string) (lifecyclecontract.Context, error) {
	parsed, err := runtimeids.ParseSessionID(strings.TrimSpace(sessionID))
	if err != nil {
		return lifecyclecontract.Context{}, fmt.Errorf("parse lifecycle session id: %w", err)
	}
	context := lifecyclecontract.Context{
		SessionID:    &parsed,
		SessionTitle: textutil.Pointer(sessionTitle),
	}
	if sessionTitle != nil && *sessionTitle == "" {
		return lifecyclecontract.Context{}, errors.New("lifecycle session title cannot be empty")
	}
	if err := context.Validate(); err != nil {
		return lifecyclecontract.Context{}, err
	}
	return context, nil
}

func NewEventContext(initial lifecyclecontract.Context) *EventContext {
	return &EventContext{context: cloneContext(initial)}
}

func (c *EventContext) AcceptSessionIdentity(identity clientui.TranscriptSessionIdentity) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	c.mu.Lock()
	sessionID := identity.SessionID
	c.context.SessionID = &sessionID
	c.context.SessionTitle = textutil.Pointer(identity.SessionName)
	c.mu.Unlock()
	return nil
}

func (c *EventContext) AcceptSessionStatus(status clientui.TranscriptSessionStatus) error {
	if err := status.Validate(); err != nil {
		return err
	}
	c.mu.Lock()
	if status.Workflow == nil {
		c.context.WorkflowTaskID = nil
	} else {
		taskID := lifecyclecontract.WorkflowTaskID(status.Workflow.TaskID)
		c.context.WorkflowTaskID = &taskID
	}
	c.mu.Unlock()
	return nil
}

func (c *EventContext) Snapshot() lifecyclecontract.Context {
	if c == nil {
		return lifecyclecontract.Context{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cloneContext(c.context)
}

func cloneContext(context lifecyclecontract.Context) lifecyclecontract.Context {
	cloned := context
	cloned.SessionID = textutil.Pointer(context.SessionID)
	cloned.SessionTitle = textutil.Pointer(context.SessionTitle)
	cloned.WorkflowTaskID = textutil.Pointer(context.WorkflowTaskID)
	return cloned
}
