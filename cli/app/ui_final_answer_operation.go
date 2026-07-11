package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

const finalAnswerLookupTimeout = time.Second

type uiFinalAnswerOperationPurpose uint8

const (
	uiFinalAnswerOperationCopy uiFinalAnswerOperationPurpose = iota
	uiFinalAnswerOperationBack
)

type uiFinalAnswerOperationPhase uint8

const (
	uiFinalAnswerOperationLookup uiFinalAnswerOperationPhase = iota
	uiFinalAnswerOperationClipboard
)

type uiFinalAnswerOperation struct {
	token           uint64
	purpose         uiFinalAnswerOperationPurpose
	sessionID       string
	parentSessionID string
	phase           uiFinalAnswerOperationPhase
}

type latestFinalAnswerDoneMsg struct {
	token           uint64
	purpose         uiFinalAnswerOperationPurpose
	sessionID       string
	parentSessionID string
	answer          *string
	err             error
}

type latestFinalAnswerTimeoutMsg struct {
	token uint64
}

func (m *uiModel) startFinalAnswerOperation(purpose uiFinalAnswerOperationPurpose, parentSessionID string) tea.Cmd {
	if m == nil || m.finalAnswerOperation != nil {
		return nil
	}
	sessionID := strings.TrimSpace(m.sessionID)
	if sessionID == "" {
		return m.sendTransientStatusWithNoticeID("session id is required", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
	}
	m.finalAnswerOperationToken = nextNonZeroToken(m.finalAnswerOperationToken)
	op := &uiFinalAnswerOperation{
		token:           m.finalAnswerOperationToken,
		purpose:         purpose,
		sessionID:       sessionID,
		parentSessionID: strings.TrimSpace(parentSessionID),
		phase:           uiFinalAnswerOperationLookup,
	}
	m.finalAnswerOperation = op
	reads := m.statusConfig.SessionViews
	lookup := func() tea.Msg {
		if reads == nil {
			return latestFinalAnswerDoneMsg{token: op.token, purpose: op.purpose, sessionID: op.sessionID, parentSessionID: op.parentSessionID, err: errors.New("session view client is unavailable")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), finalAnswerLookupTimeout)
		defer cancel()
		resp, err := reads.GetLatestCommittedAssistantFinalAnswer(ctx, serverapi.SessionLatestCommittedAssistantFinalAnswerRequest{SessionID: op.sessionID})
		return latestFinalAnswerDoneMsg{token: op.token, purpose: op.purpose, sessionID: op.sessionID, parentSessionID: op.parentSessionID, answer: resp.Answer, err: err}
	}
	timeout := tea.Tick(finalAnswerLookupTimeout, func(time.Time) tea.Msg { return latestFinalAnswerTimeoutMsg{token: op.token} })
	return tea.Batch(lookup, timeout)
}

func (m *uiModel) finalAnswerOperationMatches(token uint64, purpose uiFinalAnswerOperationPurpose, sessionID, parentSessionID string, phase uiFinalAnswerOperationPhase) bool {
	op := m.finalAnswerOperation
	return op != nil && op.token == token && op.purpose == purpose && op.sessionID == sessionID && op.parentSessionID == parentSessionID && op.phase == phase && m.exitAction == UIActionNone && m.sessionID == sessionID
}

func (m *uiModel) handleLatestFinalAnswerDone(msg latestFinalAnswerDoneMsg) tea.Cmd {
	if !m.finalAnswerOperationMatches(msg.token, msg.purpose, msg.sessionID, msg.parentSessionID, uiFinalAnswerOperationLookup) {
		return nil
	}
	if msg.err != nil {
		m.finalAnswerOperation = nil
		return m.sendTransientStatusWithNoticeID(msg.err.Error(), uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
	}
	switch msg.purpose {
	case uiFinalAnswerOperationBack:
		m.finalAnswerOperation = nil
		m.nextSessionInitialInput = ""
		if msg.answer != nil {
			m.nextSessionInitialInput = *msg.answer
		}
		m.nextSessionID = msg.parentSessionID
		m.exitAction = UIActionOpenSession
		return tea.Quit
	case uiFinalAnswerOperationCopy:
		if msg.answer == nil {
			m.finalAnswerOperation = nil
			return m.sendTransientStatusWithNoticeID("No final answer available to copy", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
		}
		m.finalAnswerOperation.phase = uiFinalAnswerOperationClipboard
		return m.copyClipboardTextCmdForOperation(msg.token, *msg.answer)
	default:
		return nil
	}
}

func (m *uiModel) handleLatestFinalAnswerTimeout(msg latestFinalAnswerTimeoutMsg) tea.Cmd {
	if m.finalAnswerOperation == nil || m.finalAnswerOperation.token != msg.token || m.finalAnswerOperation.phase != uiFinalAnswerOperationLookup {
		return nil
	}
	m.finalAnswerOperation = nil
	return m.sendTransientStatusWithNoticeID("final answer lookup timed out", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
}
