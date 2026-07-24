package app

import (
	"context"
	"time"

	"core/cli/app/internal/lifecyclehook"
	"core/shared/clientui"
	"core/shared/lifecyclecontract"
)

type lifecycleHookIssue = lifecyclehook.Issue

type clientLifecycleProxy struct {
	dispatcher   *lifecyclehook.Dispatcher
	focused      func() bool
	eventContext *lifecyclehook.EventContext
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
		eventContext: lifecyclehook.NewEventContext(initialContext),
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
	if err := p.eventContext.AcceptSessionIdentity(identity); err != nil {
		p.dispatcher.Report(lifecyclehook.NewObservationIssue(
			lifecyclehook.ObservationFactSessionIdentity,
			err,
		))
		return
	}
}

func (p *clientLifecycleProxy) acceptSessionStatus(status clientui.TranscriptSessionStatus) {
	if err := p.eventContext.AcceptSessionStatus(status); err != nil {
		p.dispatcher.Report(lifecyclehook.NewObservationIssue(
			lifecyclehook.ObservationFactSessionStatus,
			err,
		))
		return
	}
}

func (p *clientLifecycleProxy) context() lifecyclecontract.Context {
	return p.eventContext.Snapshot()
}

func (p *clientLifecycleProxy) isFocused() bool {
	return p.focused != nil && p.focused()
}

func (p *clientLifecycleProxy) enqueue(event lifecyclecontract.Event) {
	if p != nil {
		p.dispatcher.Submit(event)
	}
}
