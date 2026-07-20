package app

import (
	"fmt"

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
		m.logf(
			"lifecycle_hook.issue count=%d category=%q err=%q stderr=%q",
			issue.Count,
			issue.Category,
			issue.Err,
			issue.Stderr,
		)
		message := fmt.Sprintf("Lifecycle hook failed: %v", issue.Err)
		if issue.Count > 1 {
			message = fmt.Sprintf("%d lifecycle hooks failed; latest: %v", issue.Count, issue.Err)
		}
		cmd := m.sendTransientStatusWithNoticeID(
			message,
			uiStatusNoticeError,
			transientStatusDuration,
			uiStatusNoticeReplace,
			"",
		)
		return handledUIFeatureUpdate(m, tea.Batch(cmd, m.eventDispatcher.wait()))
	}
	result := m.reduceOngoingMessage(dispatched.event)
	return handledUIFeatureUpdate(result.model, tea.Batch(result.cmd, result.model.eventDispatcher.wait()))
}
