package app

import (
	tea "github.com/charmbracelet/bubbletea"
)

type uiAcceptedExternalEvent interface {
	uiAcceptedExternalEvent()
}

type uiAcceptedTranscriptEvent struct {
	event ongoingTranscriptEvent
}

func (uiAcceptedTranscriptEvent) uiAcceptedExternalEvent() {}

type uiAcceptedAttentionEvent struct {
	outcome attentionStreamOutcome
}

func (uiAcceptedAttentionEvent) uiAcceptedExternalEvent() {}

type uiAcceptedLifecycleHookIssue struct {
	issue lifecycleHookIssue
}

func (uiAcceptedLifecycleHookIssue) uiAcceptedExternalEvent() {}

type uiDispatchedEventMsg struct {
	event uiAcceptedExternalEvent
}

type uiEventDispatcher struct {
	transcriptEvents       <-chan ongoingTranscriptEvent
	attentionEvents        <-chan attentionStreamOutcome
	lifecycleHookIssues    *lifecycleHookIssueMailbox
	requestAttentionReopen func()
}

func newUIEventDispatcher(transcriptEvents <-chan ongoingTranscriptEvent) *uiEventDispatcher {
	return &uiEventDispatcher{transcriptEvents: transcriptEvents}
}

func WithUIAttentionEvents(events <-chan attentionStreamOutcome, requestReopen func()) UIOption {
	return func(m *uiModelConstruction) {
		m.eventDispatcher.attentionEvents = events
		m.eventDispatcher.requestAttentionReopen = requestReopen
	}
}

type lifecycleHookIssueSink interface {
	AcceptLifecycleHookIssue(lifecycleHookIssue)
}

func WithUILifecycleHookIssues(
	issues *lifecycleHookIssueMailbox,
	sink lifecycleHookIssueSink,
) UIOption {
	return func(m *uiModelConstruction) {
		m.eventDispatcher.lifecycleHookIssues = issues
		m.lifecycleHookIssueSink = sink
	}
}

func (d *uiEventDispatcher) wait() tea.Cmd {
	if d == nil ||
		(d.transcriptEvents == nil && d.attentionEvents == nil && d.lifecycleHookIssues == nil) {
		return nil
	}
	return func() tea.Msg {
		transcriptEvents := d.transcriptEvents
		attentionEvents := d.attentionEvents
		lifecycleHookIssues := d.lifecycleHookIssues
		for transcriptEvents != nil || attentionEvents != nil || lifecycleHookIssues != nil {
			var lifecycleHookIssueSignal <-chan struct{}
			if lifecycleHookIssues != nil {
				lifecycleHookIssueSignal = lifecycleHookIssues.Signal()
			}
			select {
			case event, ok := <-transcriptEvents:
				if !ok {
					transcriptEvents = nil
					continue
				}
				return uiDispatchedEventMsg{event: uiAcceptedTranscriptEvent{event: event}}
			case outcome, ok := <-attentionEvents:
				if !ok {
					attentionEvents = nil
					continue
				}
				return uiDispatchedEventMsg{event: uiAcceptedAttentionEvent{outcome: outcome}}
			case <-lifecycleHookIssueSignal:
				issue, ok := lifecycleHookIssues.Take()
				if ok {
					return uiDispatchedEventMsg{
						event: uiAcceptedLifecycleHookIssue{issue: issue},
					}
				}
				if lifecycleHookIssues.ClosedAndEmpty() {
					lifecycleHookIssues = nil
				}
			}
		}
		return nil
	}
}

func (m *uiModel) reduceDispatchedEvent(message tea.Msg) uiFeatureUpdateResult {
	dispatched, ok := message.(uiDispatchedEventMsg)
	if !ok {
		return uiFeatureUpdateResult{}
	}
	switch event := dispatched.event.(type) {
	case uiAcceptedTranscriptEvent:
		result := m.reduceOngoingMessage(event.event)
		return handledUIFeatureUpdate(result.model, tea.Batch(result.cmd, result.model.eventDispatcher.wait()))
	case uiAcceptedAttentionEvent:
		m.reduceAcceptedAttentionEvent(event.outcome)
		return handledUIFeatureUpdate(m, m.eventDispatcher.wait())
	case uiAcceptedLifecycleHookIssue:
		if m.lifecycleHookIssueSink != nil {
			m.lifecycleHookIssueSink.AcceptLifecycleHookIssue(event.issue)
		}
		return handledUIFeatureUpdate(m, m.eventDispatcher.wait())
	default:
		if m.debugMode {
			panic("unknown accepted external event")
		}
		return handledUIFeatureUpdate(m, m.eventDispatcher.wait())
	}
}

func (m *uiModel) reduceAcceptedAttentionEvent(outcome attentionStreamOutcome) {
	m.reduceAcceptedAttentionEventWithNative(outcome, m.nativeTurnNotifications)
}

func (m *uiModel) reduceAcceptedAttentionEventWithNative(outcome attentionStreamOutcome, native *nativeTurnNotificationObserver) {
	if m == nil {
		return
	}
	switch outcome := outcome.(type) {
	case *attentionFact:
		m.fanOutAcceptedAttentionFact(*outcome, native)
	case attentionStreamDiscontinuity:
		if reopen := m.eventDispatcher.requestAttentionReopen; reopen != nil {
			reopen()
		}
	}
}

func (m *uiModel) fanOutAcceptedAttentionFact(fact attentionFact, native *nativeTurnNotificationObserver) {
	if native != nil {
		native.OnAttentionFact(fact)
	}
	if m.lifecycleCoordinator != nil {
		m.lifecycleCoordinator.AcceptAttentionFact(fact)
	}
}
