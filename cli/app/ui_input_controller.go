package app

import (
	"strconv"
	"strings"
	"time"

	"core/shared/transcript"

	bubblespinner "github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

const operatorErrorFeedbackRole = string(transcript.EntryRoleDeveloperErrorFeedback)

func (c uiInputController) appendSystemFeedbackWithMirroredStatus(text string, kind uiStatusNoticeKind) tea.Cmd {
	noticeID := c.model.nextLocalNoticeID()
	return sequenceCmds(
		c.model.appendLocalEntryWithNoticeID("system", text, noticeID),
		c.model.sendTransientStatusWithNoticeID(text, kind, transientStatusDuration, uiStatusNoticeReplace, noticeID),
	)
}

type uiInputController struct {
	model *uiModel
}

var pendingToolSpinner = bubblespinner.Spinner{
	Frames: []string{"⢎ ", "⠎⠁", "⠊⠑", "⠈⠱", " ⡱", "⢀⡰", "⢄⡠", "⢆⡀"},
	FPS:    80 * time.Millisecond,
}
var spinnerTickInterval = pendingToolSpinner.FPS
var transientStatusDuration = 8 * time.Second
var updateNoticeDuration = 5 * time.Second
var spinnerTickRearmGrace = 3 * time.Second
var scheduleTransientStatusClear = func(duration time.Duration, token uint64) tea.Cmd {
	if duration <= 0 {
		duration = transientStatusDuration
	}
	return tea.Tick(duration, func(time.Time) tea.Msg {
		return clearTransientStatusMsg{token: token}
	})
}
var processListRefreshInterval = 1500 * time.Millisecond
var rollbackDoubleEscWindow = 500 * time.Millisecond
var csiShiftEnterDedupWindow = 120 * time.Millisecond

func (m *uiModel) nextLocalNoticeID() string {
	if m == nil {
		return ""
	}
	m.localNoticeSequence++
	return "local-notice-" + strconv.FormatUint(m.localNoticeSequence, 10)
}

func tickSpinner(token uint64, delay time.Duration) tea.Cmd {
	if delay <= 0 {
		delay = spinnerTickInterval
	}
	return tea.Tick(delay, func(now time.Time) tea.Msg {
		return spinnerTickMsg{token: token, at: now}
	})
}

func (m *uiModel) shouldAnimateSpinner() bool {
	if m == nil {
		return false
	}
	return m.isBusy() || m.isReviewerRunning() || m.processListHasRunningEntries() || m.worktrees.loading || m.worktrees.create.submitting || m.worktrees.deleteConfirm.submitting
}

func (m *uiModel) reconcileSpinnerTicking(force bool) tea.Cmd {
	if m == nil {
		return nil
	}
	if !m.shouldAnimateSpinner() {
		m.stopSpinnerTicking()
		return nil
	}
	now := uiAnimationNow()
	if m.spinnerTickToken != 0 && !m.spinnerClock.anchor.IsZero() && !m.spinnerTickDue.IsZero() {
		rearmAfter := m.spinnerTickDue.Add(spinnerTickRearmGrace)
		if force {
			rearmAfter = m.spinnerTickDue
		}
		if !now.After(rearmAfter) {
			return nil
		}
	}
	if m.spinnerClock.anchor.IsZero() {
		m.spinnerClock.Start(now)
		m.spinnerFrame = 0
	} else {
		frameCount := len(pendingToolSpinner.Frames)
		if frameCount <= 0 {
			frameCount = 1
		}
		m.spinnerFrame = m.spinnerClock.Frame(now, frameCount, spinnerTickInterval)
	}
	m.spinnerGeneration++
	m.spinnerTickToken = m.spinnerGeneration
	if m.spinnerTickToken == 0 {
		m.spinnerGeneration++
		m.spinnerTickToken = m.spinnerGeneration
	}
	return m.scheduleSpinnerTick(m.spinnerTickToken, now)
}

func (m *uiModel) stopSpinnerTicking() {
	if m == nil {
		return
	}
	m.spinnerTickToken = 0
	m.spinnerTickDue = time.Time{}
	m.spinnerClock.Stop()
}

func (m *uiModel) scheduleSpinnerTick(token uint64, now time.Time) tea.Cmd {
	if m == nil || token == 0 {
		return nil
	}
	if now.IsZero() {
		now = uiAnimationNow()
	}
	delay := m.spinnerClock.NextDelay(now, spinnerTickInterval)
	m.spinnerTickDue = now.Add(delay)
	return tickSpinner(token, delay)
}

func (c uiInputController) interruptBusyRuntime() tea.Cmd {
	m := c.model
	m.setPendingInterrupt(true)
	return m.runtimeControlCommand(runtimeControlInterrupt, "", false, "")
}

func parseUserShellCommand(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "$") {
		return "", false
	}
	command := strings.TrimSpace(strings.TrimPrefix(trimmed, "$"))
	if command == "" {
		return "", false
	}
	return command, true
}

func (m *uiModel) appendLocalEntryWithNoticeID(role, text, noticeID string) tea.Cmd {
	role = strings.TrimSpace(role)
	text = strings.TrimSpace(text)
	noticeID = strings.TrimSpace(noticeID)
	if role == "" || text == "" {
		return nil
	}
	if noticeID == "" {
		noticeID = m.nextLocalNoticeID()
	}
	if !m.hasRuntimeClient() {
		return m.sendTransientStatusWithNoticeID(text, statusKindForLocalEntryRole(role), transientStatusDuration, uiStatusNoticeReplace, noticeID)
	}
	return m.persistLocalEntryCmd(role, text, noticeID)
}

func (m *uiModel) persistLocalEntryCmd(role, text, noticeID string) tea.Cmd {
	client := m.runtimeClient()
	if client == nil {
		return nil
	}
	role = strings.TrimSpace(role)
	text = strings.TrimSpace(text)
	noticeID = strings.TrimSpace(noticeID)
	return func() tea.Msg {
		err := client.AppendCommittedEntryWithNoticeID(role, text, noticeID)
		return committedEntryPersistDoneMsg{noticeID: noticeID, role: role, text: text, err: err}
	}
}

func statusKindForLocalEntryRole(role string) uiStatusNoticeKind {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case string(transcript.EntryRoleDeveloperErrorFeedback), "error":
		return uiStatusNoticeError
	case "warning":
		return uiStatusNoticeNeutral
	default:
		return uiStatusNoticeNeutral
	}
}
