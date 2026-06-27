package app

import (
	"context"
	"strings"

	"core/cli/app/internal/runtimeattach"
	"core/shared/clientui"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
)

func (m *uiModel) runtimeClient() clientui.RuntimeClient {
	if m == nil {
		return nil
	}
	return m.engine
}

func (m *uiModel) hasRuntimeClient() bool {
	return m.runtimeClient() != nil
}

func (m *uiModel) setRuntimeSessionName(name string) error {
	m.checkTUIBlockingOperation("runtime control mutation", "set session name")
	if client := m.runtimeClient(); client != nil {
		err := client.SetSessionName(name)
		m.observeRuntimeRequestResult(err)
		return err
	}
	return nil
}

func (m *uiModel) setRuntimeThinkingLevel(level string) error {
	m.checkTUIBlockingOperation("runtime control mutation", "set thinking level")
	if client := m.runtimeClient(); client != nil {
		err := client.SetThinkingLevel(level)
		m.observeRuntimeRequestResult(err)
		return err
	}
	return nil
}

func (m *uiModel) setRuntimeFastModeEnabled(enabled bool) (bool, error) {
	m.checkTUIBlockingOperation("runtime control mutation", "set fast mode")
	if client := m.runtimeClient(); client != nil {
		changed, err := client.SetFastModeEnabled(enabled)
		m.observeRuntimeRequestResult(err)
		return changed, err
	}
	return false, nil
}

func (m *uiModel) setRuntimeReviewerEnabled(enabled bool) (bool, string, error) {
	m.checkTUIBlockingOperation("runtime control mutation", "set reviewer")
	if client := m.runtimeClient(); client != nil {
		changed, mode, err := client.SetReviewerEnabled(enabled)
		m.observeRuntimeRequestResult(err)
		return changed, mode, err
	}
	return false, "", nil
}

func (m *uiModel) setRuntimeAutoCompactionEnabled(enabled bool) (bool, bool, error) {
	m.checkTUIBlockingOperation("runtime control mutation", "set auto compaction")
	if client := m.runtimeClient(); client != nil {
		changed, nextEnabled, err := client.SetAutoCompactionEnabled(enabled)
		m.observeRuntimeRequestResult(err)
		return changed, nextEnabled, err
	}
	return false, false, nil
}

func (m *uiModel) showRuntimeGoal() (*clientui.RuntimeGoal, error) {
	m.checkTUIBlockingOperation("runtime control read", "show goal")
	if client := m.runtimeClient(); client != nil {
		goal, err := client.ShowGoal()
		m.observeRuntimeRequestResult(err)
		return goal, err
	}
	return nil, nil
}

func (m *uiModel) setRuntimeGoal(objective string) (*clientui.RuntimeGoal, error) {
	m.checkTUIBlockingOperation("runtime control mutation", "set goal")
	if client := m.runtimeClient(); client != nil {
		goal, err := client.SetGoal(objective)
		m.observeRuntimeRequestResult(err)
		return goal, err
	}
	return nil, nil
}

func (m *uiModel) pauseRuntimeGoal() (*clientui.RuntimeGoal, error) {
	m.checkTUIBlockingOperation("runtime control mutation", "pause goal")
	if client := m.runtimeClient(); client != nil {
		goal, err := client.PauseGoal()
		m.observeRuntimeRequestResult(err)
		return goal, err
	}
	return nil, nil
}

func (m *uiModel) resumeRuntimeGoal() (*clientui.RuntimeGoal, error) {
	m.checkTUIBlockingOperation("runtime control mutation", "resume goal")
	if client := m.runtimeClient(); client != nil {
		goal, err := client.ResumeGoal()
		m.observeRuntimeRequestResult(err)
		return goal, err
	}
	return nil, nil
}

func (m *uiModel) clearRuntimeGoal() (*clientui.RuntimeGoal, error) {
	m.checkTUIBlockingOperation("runtime control mutation", "clear goal")
	if client := m.runtimeClient(); client != nil {
		goal, err := client.ClearGoal()
		m.observeRuntimeRequestResult(err)
		return goal, err
	}
	return nil, nil
}

func (m *uiModel) appendRuntimeLocalEntryWithNoticeID(role, text, noticeID string) error {
	if client := m.runtimeClient(); client != nil {
		err := client.AppendCommittedEntryWithNoticeID(role, text, noticeID)
		m.observeRuntimeRequestResult(err)
		return err
	}
	return nil
}

type promptHistoryRecordedUserMessageSubmitter interface {
	SubmitUserMessageWithPromptHistoryRecorded(ctx context.Context, text string) (clientui.UserTurnSubmission, error)
}

func (m *uiModel) submitRuntimeUserMessage(ctx context.Context, text string, promptHistoryRecorded bool) (clientui.UserTurnSubmission, error) {
	if client := m.runtimeClient(); client != nil {
		var (
			submission clientui.UserTurnSubmission
			err        error
		)
		if promptHistoryRecorded {
			if submitter, ok := client.(promptHistoryRecordedUserMessageSubmitter); ok {
				submission, err = submitter.SubmitUserMessageWithPromptHistoryRecorded(ctx, text)
			} else {
				submission, err = client.SubmitUserMessage(ctx, text)
			}
		} else {
			submission, err = client.SubmitUserMessage(ctx, text)
		}
		m.observeRuntimeRequestResult(err)
		return submission, err
	}
	return clientui.UserTurnSubmission{}, nil
}

func (m *uiModel) submitRuntimeUserShellCommand(ctx context.Context, command string) error {
	if client := m.runtimeClient(); client != nil {
		err := client.SubmitUserShellCommand(ctx, command)
		m.observeRuntimeRequestResult(err)
		return err
	}
	return nil
}

func (m *uiModel) compactRuntimeContext(ctx context.Context, args string) error {
	m.checkTUIBlockingOperation("runtime control mutation", "compact")
	if client := m.runtimeClient(); client != nil {
		err := client.CompactContext(ctx, args)
		m.observeRuntimeRequestResult(err)
		return err
	}
	return nil
}

func (m *uiModel) hasQueuedRuntimeUserWork() (bool, error) {
	m.checkTUIBlockingOperation("runtime queue read", "has queued user work")
	if client := m.runtimeClient(); client != nil {
		hasWork, err := client.HasQueuedUserWork()
		m.observeRuntimeRequestResult(err)
		return hasWork, err
	}
	return false, nil
}

func (m *uiModel) submitQueuedRuntimeUserMessages(ctx context.Context) (string, error) {
	m.checkTUIBlockingOperation("runtime queue mutation", "submit queued user messages")
	if client := m.runtimeClient(); client != nil {
		message, err := client.SubmitQueuedUserMessages(ctx)
		m.observeRuntimeRequestResult(err)
		return message, err
	}
	return "", nil
}

func (m *uiModel) interruptRuntime() error {
	m.checkTUIBlockingOperation("runtime control mutation", "interrupt")
	if client := m.runtimeClient(); client != nil {
		err := client.Interrupt()
		m.observeRuntimeRequestResult(err)
		return err
	}
	return nil
}

func (m *uiModel) queueRuntimeUserMessage(text string) (clientui.QueuedUserMessage, error) {
	m.checkTUIBlockingOperation("runtime queue mutation", "queue user message")
	if client := m.runtimeClient(); client != nil {
		return client.QueueUserMessage(text)
	}
	return clientui.QueuedUserMessage{ID: uuid.NewString(), Text: text}, nil
}

func (m *uiModel) discardQueuedRuntimeUserMessage(queueItemID string) bool {
	m.checkTUIBlockingOperation("runtime queue mutation", "discard queued user message")
	if client := m.runtimeClient(); client != nil {
		return client.DiscardQueuedUserMessage(queueItemID)
	}
	return false
}

func (m *uiModel) recordRuntimePromptHistory(text string) error {
	m.checkTUIBlockingOperation("runtime control mutation", "record prompt history")
	if client := m.runtimeClient(); client != nil {
		err := client.RecordPromptHistory(text)
		m.observeRuntimeRequestResult(err)
		return err
	}
	return nil
}

type runtimeControlPendingState struct {
	sessionID       string
	inFlight        bool
	inFlightText    string
	inFlightEnabled bool
	desiredText     string
	desiredEnabled  bool
	compactionMode  string
}

func (m *uiModel) nextRuntimeControlToken(operation runtimeControlOperation) uint64 {
	m.runtimeControlToken++
	if m.runtimeControlToken == 0 {
		m.runtimeControlToken++
	}
	if m.runtimeControlTokens == nil {
		m.runtimeControlTokens = make(map[runtimeControlOperation]uint64)
	}
	m.runtimeControlTokens[operation] = m.runtimeControlToken
	return m.runtimeControlToken
}

func (m *uiModel) runtimeControlTokenFor(operation runtimeControlOperation) uint64 {
	if m == nil || m.runtimeControlTokens == nil {
		return 0
	}
	return m.runtimeControlTokens[operation]
}

func (m *uiModel) beginRuntimeControlMutation(operation runtimeControlOperation, sessionID, text string, enabled bool, compactionMode string) (uint64, bool) {
	if m == nil {
		return 0, false
	}
	sessionID = strings.TrimSpace(sessionID)
	text = strings.TrimSpace(text)
	if !runtimeControlOperationUsesEnabledTarget(operation) && !runtimeControlOperationUsesTextTarget(operation) {
		return m.nextRuntimeControlToken(operation), true
	}
	if m.runtimeControlPending == nil {
		m.runtimeControlPending = make(map[runtimeControlOperation]runtimeControlPendingState)
	}
	if pending, ok := m.runtimeControlPending[operation]; ok && pending.inFlight && pending.sessionID == sessionID {
		if runtimeControlOperationUsesTextTarget(operation) {
			pending.desiredText = text
		} else {
			pending.desiredEnabled = enabled
			pending.compactionMode = strings.TrimSpace(compactionMode)
		}
		m.runtimeControlPending[operation] = pending
		return 0, false
	}
	token := m.nextRuntimeControlToken(operation)
	m.runtimeControlPending[operation] = runtimeControlPendingState{
		sessionID:       sessionID,
		inFlight:        true,
		inFlightText:    text,
		inFlightEnabled: enabled,
		desiredText:     text,
		desiredEnabled:  enabled,
		compactionMode:  strings.TrimSpace(compactionMode),
	}
	return token, true
}

func (m *uiModel) clearRuntimeControlPending(operation runtimeControlOperation) {
	if m == nil || m.runtimeControlPending == nil {
		return
	}
	delete(m.runtimeControlPending, operation)
}

func (m *uiModel) runtimeControlPendingEnabled(operation runtimeControlOperation, sessionID string, fallback bool) bool {
	if m == nil || m.runtimeControlPending == nil {
		return fallback
	}
	pending, ok := m.runtimeControlPending[operation]
	if !ok {
		return fallback
	}
	if pending.sessionID != strings.TrimSpace(sessionID) {
		return fallback
	}
	return pending.desiredEnabled
}

func runtimeControlOperationUsesEnabledTarget(operation runtimeControlOperation) bool {
	switch operation {
	case runtimeControlSetFastMode, runtimeControlSetReviewer, runtimeControlSetAutoCompaction, runtimeControlSetQuestions:
		return true
	default:
		return false
	}
}

func runtimeControlOperationUsesTextTarget(operation runtimeControlOperation) bool {
	switch operation {
	case runtimeControlSetSessionName, runtimeControlSetThinkingLevel:
		return true
	default:
		return false
	}
}

func (m *uiModel) runtimeControlCommand(operation runtimeControlOperation, text string, enabled bool, compactionMode string) tea.Cmd {
	if m == nil {
		return nil
	}
	client := m.runtimeClient()
	if client == nil {
		return nil
	}
	sessionID := strings.TrimSpace(m.sessionID)
	text = strings.TrimSpace(text)
	token, shouldStart := m.beginRuntimeControlMutation(operation, sessionID, text, enabled, compactionMode)
	if !shouldStart {
		return nil
	}
	return func() tea.Msg {
		msg := runtimeControlDoneMsg{token: token, sessionID: sessionID, operation: operation, text: text, enabled: enabled, compactionMode: compactionMode}
		switch operation {
		case runtimeControlSetSessionName:
			msg.err = client.SetSessionName(text)
		case runtimeControlSetThinkingLevel:
			msg.err = client.SetThinkingLevel(text)
		case runtimeControlSetFastMode:
			msg.changed, msg.err = client.SetFastModeEnabled(enabled)
		case runtimeControlSetReviewer:
			msg.changed, msg.mode, msg.err = client.SetReviewerEnabled(enabled)
		case runtimeControlSetAutoCompaction:
			msg.changed, msg.enabled, msg.err = client.SetAutoCompactionEnabled(enabled)
		case runtimeControlSetQuestions:
			msg.changed, msg.err = client.SetQuestionsEnabled(enabled)
		case runtimeControlInterrupt:
			msg.err = client.Interrupt()
		}
		return msg
	}
}

func (m *uiModel) applyRuntimeControlDone(msg runtimeControlDoneMsg) tea.Cmd {
	if m == nil || msg.token != m.runtimeControlTokenFor(msg.operation) {
		return nil
	}
	if msg.sessionID != "" && strings.TrimSpace(m.sessionID) != "" && msg.sessionID != strings.TrimSpace(m.sessionID) {
		m.clearRuntimeControlPending(msg.operation)
		return nil
	}
	m.observeRuntimeRequestResult(msg.err)
	if msg.err != nil {
		m.clearRuntimeControlPending(msg.operation)
		errText := runtimeattach.FormatSubmissionError(msg.err)
		return sequenceCmds(
			m.appendLocalEntryWithNoticeID("error", errText, ""),
			m.sendTransientStatusWithNoticeID(errText, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""),
		)
	}
	var followUpCmd tea.Cmd
	if runtimeControlOperationUsesEnabledTarget(msg.operation) {
		pending := m.runtimeControlPending[msg.operation]
		if pending.inFlight && pending.desiredEnabled != pending.inFlightEnabled {
			pending.inFlight = false
			m.runtimeControlPending[msg.operation] = pending
			followUpCmd = m.runtimeControlCommand(msg.operation, "", pending.desiredEnabled, pending.compactionMode)
		} else {
			m.clearRuntimeControlPending(msg.operation)
		}
	}
	if runtimeControlOperationUsesTextTarget(msg.operation) {
		pending := m.runtimeControlPending[msg.operation]
		if pending.inFlight && pending.desiredText != pending.inFlightText {
			pending.inFlight = false
			m.runtimeControlPending[msg.operation] = pending
			followUpCmd = m.runtimeControlCommand(msg.operation, pending.desiredText, false, "")
		} else {
			m.clearRuntimeControlPending(msg.operation)
		}
	}
	switch msg.operation {
	case runtimeControlSetSessionName:
		m.sessionName = strings.TrimSpace(msg.text)
		return sequenceCmds(tea.SetWindowTitle(sessionTitle(m.sessionName)), followUpCmd)
	case runtimeControlSetThinkingLevel:
		m.thinkingLevel = strings.TrimSpace(msg.text)
		return sequenceCmds(m.appendLocalEntryWithNoticeID("system", "Thinking level set to "+m.thinkingLevel, ""), followUpCmd)
	case runtimeControlSetFastMode:
		m.fastModeEnabled = msg.enabled
		status := serverapi.FastModeToggleStatusMessage(m.fastModeEnabled, msg.changed)
		return sequenceCmds(m.sendTransientStatusWithNoticeID(status, uiStatusNoticeSuccess, transientStatusDuration, uiStatusNoticeReplace, ""), followUpCmd)
	case runtimeControlSetReviewer:
		nextMode := strings.TrimSpace(msg.mode)
		if nextMode == "" {
			nextMode = "off"
		}
		m.reviewerMode = nextMode
		m.reviewerEnabled = nextMode != "off"
		status := serverapi.ReviewerToggleStatusMessage(m.reviewerEnabled, nextMode, msg.changed)
		return sequenceCmds(m.sendTransientStatusWithNoticeID(status, uiStatusNoticeNeutral, transientStatusDuration, uiStatusNoticeReplace, ""), followUpCmd)
	case runtimeControlSetAutoCompaction:
		m.autoCompactionEnabled = msg.enabled
		status := serverapi.AutoCompactionToggleStatusMessage(msg.enabled, msg.changed, msg.compactionMode)
		return sequenceCmds(m.inputController().appendSystemFeedbackWithMirroredStatus(status, uiStatusNoticeNeutral), followUpCmd)
	case runtimeControlSetQuestions:
		m.questionsEnabled = msg.enabled
		status := serverapi.QuestionsToggleStatusMessage(msg.enabled, msg.changed)
		return sequenceCmds(m.sendTransientStatusWithNoticeID(status, uiStatusNoticeNeutral, transientStatusDuration, uiStatusNoticeReplace, ""), followUpCmd)
	case runtimeControlInterrupt:
		return followUpCmd
	default:
		return followUpCmd
	}
}
