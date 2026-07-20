package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"core/shared/boundedio"
	"core/shared/clientui"
	"core/shared/lifecyclecontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

const (
	clientLifecycleEventCapacity = 64
	clientLifecycleActiveLimit   = 64
	clientLifecycleTimeout       = 30 * time.Second
	clientLifecycleStderrLimit   = 4 * 1024
)

type lifecycleHookIssue struct {
	category lifecyclecontract.Category
	err      error
	stderr   string
}

type clientLifecycleProxy struct {
	command             []string
	ctx                 context.Context
	cancel              context.CancelFunc
	events              chan lifecyclecontract.Event
	active              chan struct{}
	issues              chan lifecycleHookIssue
	done                chan struct{}
	focused             func() bool
	transcriptAttention bool
	contextMu           sync.RWMutex
	eventContext        lifecyclecontract.Context
	closed              atomic.Bool
}

func newClientLifecycleProxy(
	parent context.Context,
	command []string,
	initialContext lifecyclecontract.Context,
	focused func() bool,
	transcriptAttention bool,
) *clientLifecycleProxy {
	if len(command) == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	proxy := &clientLifecycleProxy{
		command:             append([]string(nil), command...),
		ctx:                 ctx,
		cancel:              cancel,
		events:              make(chan lifecyclecontract.Event, clientLifecycleEventCapacity),
		active:              make(chan struct{}, clientLifecycleActiveLimit),
		issues:              make(chan lifecycleHookIssue, clientLifecycleEventCapacity),
		done:                make(chan struct{}),
		focused:             focused,
		transcriptAttention: transcriptAttention,
		eventContext:        initialContext,
	}
	go proxy.drain()
	return proxy
}

func (p *clientLifecycleProxy) Close() {
	if p == nil || !p.closed.CompareAndSwap(false, true) {
		return
	}
	p.cancel()
	close(p.done)
}

func (p *clientLifecycleProxy) Issues() <-chan lifecycleHookIssue {
	if p == nil {
		return nil
	}
	return p.issues
}

func (p *clientLifecycleProxy) Done() <-chan struct{} {
	if p == nil {
		return nil
	}
	return p.done
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
	if taskID := strings.TrimSpace(notification.Target.TaskID); taskID != "" {
		typed := lifecyclecontract.WorkflowTaskID(taskID)
		context.WorkflowTaskID = &typed
	}
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
	case result.ResultKind == clientui.LiveRunResultAssistantFinalAnswer && result.FinalAnswer != nil:
		p.enqueue(lifecyclecontract.NewTaskComplete(
			result.FinishedAt,
			p.isFocused(),
			p.context(),
			*result.FinalAnswer,
			result.WorkPerformed,
		))
	case result.Status == clientui.LiveRunStatusFailed && result.Failure != nil:
		p.enqueue(lifecyclecontract.NewTaskError(
			result.FinishedAt,
			p.isFocused(),
			p.context(),
			*result.Failure,
		))
	}
}

func (p *clientLifecycleProxy) acceptTranscriptPrompt(prompt clientui.TranscriptPrompt) {
	if prompt.State != clientui.TranscriptPromptStatePending {
		return
	}
	kind := lifecyclecontract.InputKindQuestion
	if prompt.Kind == clientui.TranscriptPromptKindApproval {
		kind = lifecyclecontract.InputKindApproval
	}
	p.enqueue(lifecyclecontract.NewInputRequired(
		prompt.CreatedAt,
		p.isFocused(),
		p.context(),
		kind,
		prompt.Question,
	))
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
	} else if taskID := strings.TrimSpace(status.Workflow.TaskID); taskID != "" {
		typed := lifecyclecontract.WorkflowTaskID(taskID)
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
	if p == nil || p.closed.Load() {
		return
	}
	select {
	case p.events <- event:
	default:
	}
}

func (p *clientLifecycleProxy) drain() {
	for {
		select {
		case <-p.ctx.Done():
			return
		case event := <-p.events:
			select {
			case p.active <- struct{}{}:
				go p.invoke(event)
			default:
			}
		}
	}
}

func (p *clientLifecycleProxy) invoke(event lifecyclecontract.Event) {
	defer func() { <-p.active }()
	payload, err := lifecyclecontract.Encode(event)
	if err != nil {
		p.report(lifecycleHookIssue{category: event.Category, err: fmt.Errorf("encode lifecycle hook payload: %w", err)})
		return
	}
	ctx, cancel := context.WithTimeout(p.ctx, clientLifecycleTimeout)
	defer cancel()
	stderr, writerErr := boundedio.NewWriter(clientLifecycleStderrLimit)
	if writerErr != nil {
		p.report(lifecycleHookIssue{category: event.Category, err: writerErr})
		return
	}
	command := exec.CommandContext(ctx, p.command[0], p.command[1:]...)
	command.Stdin = strings.NewReader(string(payload))
	command.Stdout = io.Discard
	command.Stderr = stderr
	err = command.Run()
	if err == nil || (p.ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled)) {
		return
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("lifecycle hook timed out after %s", clientLifecycleTimeout)
	}
	p.report(lifecycleHookIssue{
		category: event.Category,
		err:      err,
		stderr:   stderr.String(),
	})
}

func (p *clientLifecycleProxy) report(issue lifecycleHookIssue) {
	select {
	case <-p.ctx.Done():
	case p.issues <- issue:
	default:
	}
}

func startClientLifecycleAttention(
	parent context.Context,
	sessionID string,
	service serverapiAttentionService,
	proxy *clientLifecycleProxy,
) context.CancelFunc {
	ctx, cancel := context.WithCancel(parent)
	if service == nil || proxy == nil {
		return cancel
	}
	go func() {
		for {
			subscription, err := service.SubscribeSessionAttentionNotifications(ctx, serverapi.AttentionSessionNotificationSubscribeRequest{
				SessionID:                    sessionID,
				IncludePendingPromptSnapshot: true,
			})
			if err != nil {
				if ctx.Err() != nil || !waitSubscriptionRetry(ctx) {
					return
				}
				continue
			}
			for {
				event, nextErr := subscription.Next(ctx)
				if nextErr != nil {
					_ = subscription.Close()
					break
				}
				proxy.AcceptAttention(event)
			}
			if ctx.Err() != nil || !waitSubscriptionRetry(ctx) {
				return
			}
		}
	}()
	return cancel
}

type serverapiAttentionService interface {
	SubscribeSessionAttentionNotifications(context.Context, serverapi.AttentionSessionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error)
}

func lifecycleInitialContext(sessionID string, title string) lifecyclecontract.Context {
	context := lifecyclecontract.Context{}
	if parsed, err := runtimeids.ParseSessionID(strings.TrimSpace(sessionID)); err == nil {
		context.SessionID = &parsed
	}
	if trimmed := strings.TrimSpace(title); trimmed != "" {
		context.SessionTitle = &trimmed
	}
	return context
}

func cloneOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
