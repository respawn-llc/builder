package app

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *uiModel) AcceptLifecycleHookIssue(issue lifecycleHookIssue) tea.Cmd {
	if m == nil {
		return nil
	}
	m.logf(
		"lifecycle_hook.issue kind=%d cause=%q launch_failure=%v exit_code=%v stderr=%q stderr_overflow_bytes=%v dropped_count=%v",
		issue.Kind,
		lifecycleHookIssueCause(issue),
		lifecycleHookOptionalValue(issue.LaunchFailure),
		lifecycleHookOptionalValue(issue.ExitCode),
		lifecycleHookIssueStderr(issue),
		lifecycleHookOptionalValue(issue.StderrOverflowBytes),
		lifecycleHookOptionalValue(issue.DroppedCount),
	)
	return m.sendTransientStatusWithNoticeID(
		lifecycleHookIssueNotice(issue),
		uiStatusNoticeError,
		transientStatusDuration,
		uiStatusNoticeQueue,
		"",
	)
}

func lifecycleHookIssueNotice(issue lifecycleHookIssue) string {
	switch issue.Kind {
	case lifecycleHookIssueEncoding:
		return "Lifecycle hook event could not be encoded"
	case lifecycleHookIssueLaunchDisabled:
		return "Lifecycle hook is unavailable and was disabled for this session"
	case lifecycleHookIssueLaunchFailed:
		return "Lifecycle hook could not be started"
	case lifecycleHookIssueNonzeroExit:
		return "Lifecycle hook exited with an error"
	case lifecycleHookIssueTimeout:
		return "Lifecycle hook timed out"
	case lifecycleHookIssueQueueOverload:
		if issue.DroppedCount != nil {
			return fmt.Sprintf("Lifecycle hook queue overflowed; %d events were dropped", *issue.DroppedCount)
		}
		return "Lifecycle hook queue overflowed and events were dropped"
	default:
		return "Lifecycle hook failed"
	}
}

func lifecycleHookIssueCause(issue lifecycleHookIssue) string {
	if issue.Cause == nil {
		return ""
	}
	return issue.Cause.Error()
}

func lifecycleHookIssueStderr(issue lifecycleHookIssue) string {
	if issue.Stderr == nil {
		return ""
	}
	return *issue.Stderr
}

func lifecycleHookOptionalValue[T any](value *T) any {
	if value == nil {
		return nil
	}
	return *value
}
