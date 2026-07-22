package app

import (
	"fmt"

	"core/cli/app/internal/lifecyclehook"

	tea "github.com/charmbracelet/bubbletea"
)

type uiDispatchedEventMsg struct {
	event ongoingTranscriptEvent
	issue *lifecycleHookIssue
}

type uiEventDispatcher struct {
	transcriptEvents    <-chan ongoingTranscriptEvent
	lifecycleHookIssues <-chan lifecycleHookIssue
	lifecycleHookDone   <-chan struct{}
}

func newUIEventDispatcher(transcriptEvents <-chan ongoingTranscriptEvent) *uiEventDispatcher {
	return &uiEventDispatcher{transcriptEvents: transcriptEvents}
}

func (d *uiEventDispatcher) wait() tea.Cmd {
	if d == nil || (d.transcriptEvents == nil && d.lifecycleHookIssues == nil) {
		return nil
	}
	return func() tea.Msg {
		transcriptEvents := d.transcriptEvents
		issues := d.lifecycleHookIssues
		for transcriptEvents != nil || issues != nil {
			select {
			case <-d.lifecycleHookDone:
				return nil
			case event, ok := <-transcriptEvents:
				if !ok {
					transcriptEvents = nil
					continue
				}
				return uiDispatchedEventMsg{event: event}
			case issue := <-issues:
				return uiDispatchedEventMsg{issue: &issue}
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
	if dispatched.issue != nil {
		issue := *dispatched.issue
		var message string
		switch detail := issue.Detail.(type) {
		case lifecyclehook.ProcessIssue:
			m.logf(
				"lifecycle_hook.issue count=%d category=%q err=%q stderr=%q",
				issue.Count,
				detail.Category,
				detail.Cause,
				detail.Stderr,
			)
			message = fmt.Sprintf("Lifecycle hook failed: %v", detail.Cause)
			if issue.Count > 1 {
				message = fmt.Sprintf("%d lifecycle hooks failed; latest: %v", issue.Count, detail.Cause)
			}
		case lifecyclehook.ObservationIssue:
			m.logf(
				"lifecycle_hook.observation_issue count=%d fact=%q err=%q",
				issue.Count,
				detail.Fact,
				detail.Cause,
			)
			message = fmt.Sprintf("Lifecycle hook context rejected: %v", detail.Cause)
			if issue.Count > 1 {
				message = fmt.Sprintf("%d lifecycle issues; latest context rejection: %v", issue.Count, detail.Cause)
			}
		default:
			panic(fmt.Sprintf("unknown lifecycle issue detail %T", issue.Detail))
		}
		cmd := m.sendTransientStatusWithNoticeID(
			message,
			uiStatusNoticeError,
			transientStatusDuration,
			uiStatusNoticeReplace,
			"",
		)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(
			m,
			tea.Batch(m.batchWithNativeOngoingRepaint(cmd), m.eventDispatcher.wait()),
		)
	}
	result := m.reduceOngoingMessage(dispatched.event)
	return handledUIFeatureUpdate(result.model, tea.Batch(result.cmd, result.model.eventDispatcher.wait()))
}
