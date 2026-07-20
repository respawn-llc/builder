package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"core/cli/app/internal/lifecyclehook"
	"core/shared/clientui"
	"core/shared/lifecyclecontract"
	"core/shared/runtimeids"
)

type lifecycleHookIssue = lifecyclehook.Issue

type clientLifecycleProxy struct {
	dispatcher   *lifecyclehook.Dispatcher
	focused      func() bool
	contextMu    sync.RWMutex
	eventContext lifecyclecontract.Context
}

func newClientLifecycleProxy(
	parent context.Context,
	command []string,
	initialContext lifecyclecontract.Context,
	focused func() bool,
) *clientLifecycleProxy {
	dispatcher := lifecyclehook.New(parent, command)
	if dispatcher == nil {
		return nil
	}
	return &clientLifecycleProxy{
		dispatcher:   dispatcher,
		focused:      focused,
		eventContext: initialContext,
	}
}

func (p *clientLifecycleProxy) Close() {
	if p != nil {
		p.dispatcher.Close()
	}
}

func (p *clientLifecycleProxy) Issues() <-chan lifecycleHookIssue {
	if p == nil {
		return nil
	}
	return p.dispatcher.Issues()
}

func (p *clientLifecycleProxy) Done() <-chan struct{} {
	if p == nil {
		return nil
	}
	return p.dispatcher.Done()
}

func (p *clientLifecycleProxy) AcceptSessionStart(kind lifecyclecontract.OpeningKind) {
	if p == nil {
		return
	}
	p.enqueue(lifecyclecontract.NewSessionStart(time.Now().UTC(), p.isFocused(), p.context(), kind))
}

func (p *clientLifecycleProxy) AcceptAttention(event clientui.AttentionNotificationEvent) {
	if p == nil || event.Type != clientui.AttentionNotificationEventPending || event.Pending == nil {
		return
	}
	notification := event.Pending
	context := p.context()
	switch notification.Kind {
	case clientui.AttentionNotificationKindQuestion:
		if notification.Question != nil {
			p.enqueue(lifecyclecontract.NewInputRequired(
				notification.OccurredAt,
				p.isFocused(),
				context,
				lifecyclecontract.InputKindQuestion,
				notification.Question.Preview,
			))
		}
	case clientui.AttentionNotificationKindApproval:
		if notification.Approval != nil {
			p.enqueue(lifecyclecontract.NewInputRequired(
				notification.OccurredAt,
				p.isFocused(),
				context,
				lifecyclecontract.InputKindApproval,
				notification.Approval.Message,
			))
		}
	}
}

func (p *clientLifecycleProxy) acceptLiveRunFinished(result clientui.TranscriptLiveRunResult) {
	switch {
	case result.Status == clientui.LiveRunStatusFailed && result.Failure != nil:
		p.enqueue(lifecyclecontract.NewTaskError(
			result.FinishedAt,
			p.isFocused(),
			p.context(),
			*result.Failure,
		))
	case result.ResultKind == clientui.LiveRunResultAssistantFinalAnswer && result.FinalAnswer != nil:
		p.enqueue(lifecyclecontract.NewTaskComplete(
			result.FinishedAt,
			p.isFocused(),
			p.context(),
			*result.FinalAnswer,
			result.WorkPerformed,
		))
	}
}

func (p *clientLifecycleProxy) acceptSessionIdentity(identity clientui.TranscriptSessionIdentity) {
	p.contextMu.Lock()
	sessionID := identity.SessionID
	p.eventContext.SessionID = &sessionID
	p.eventContext.SessionTitle = cloneOptionalString(identity.SessionName)
	p.contextMu.Unlock()
}

func (p *clientLifecycleProxy) acceptSessionStatus(status clientui.TranscriptSessionStatus) {
	p.contextMu.Lock()
	if status.Workflow == nil {
		p.eventContext.WorkflowTaskID = nil
	} else {
		typed := lifecyclecontract.WorkflowTaskID(status.Workflow.TaskID)
		p.eventContext.WorkflowTaskID = &typed
	}
	p.contextMu.Unlock()
}

func (p *clientLifecycleProxy) context() lifecyclecontract.Context {
	p.contextMu.RLock()
	defer p.contextMu.RUnlock()
	context := p.eventContext
	context.SessionTitle = cloneOptionalString(context.SessionTitle)
	if context.SessionID != nil {
		sessionID := *context.SessionID
		context.SessionID = &sessionID
	}
	if context.WorkflowTaskID != nil {
		taskID := *context.WorkflowTaskID
		context.WorkflowTaskID = &taskID
	}
	return context
}

func (p *clientLifecycleProxy) isFocused() bool {
	return p.focused != nil && p.focused()
}

func (p *clientLifecycleProxy) enqueue(event lifecyclecontract.Event) {
	if p != nil {
		p.dispatcher.Submit(event)
	}
}

func lifecycleInitialContext(sessionID string) (lifecyclecontract.Context, error) {
	parsed, err := runtimeids.ParseSessionID(strings.TrimSpace(sessionID))
	if err != nil {
		return lifecyclecontract.Context{}, fmt.Errorf("parse lifecycle session id: %w", err)
	}
	context := lifecyclecontract.Context{SessionID: &parsed}
	if err := context.Validate(); err != nil {
		return lifecyclecontract.Context{}, err
	}
	return context, nil
}

func cloneOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
