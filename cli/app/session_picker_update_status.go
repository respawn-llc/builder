package app

import (
	"errors"
	"fmt"

	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

type sessionPickerUpdateStatusMsg struct {
	response *serverapi.UpdateStatusResponse
	outcome  interactiveConnectionOutcome
}

var (
	errSessionPickerUpdateResponseRequired = errors.New("update status completion requires a response")
	errSessionPickerUpdateOutcomeRequired  = errors.New("update status completion requires a valid outcome")
)

func (m *sessionPickerModel) collectUpdateStatusCmd() tea.Cmd {
	if m == nil || m.header.updateStatus == nil {
		return nil
	}
	client := m.header.updateStatus
	ctx := m.requestContext
	return func() tea.Msg {
		response, err := client.GetUpdateStatus(ctx, serverapi.UpdateStatusRequest{})
		if err != nil {
			return sessionPickerUpdateStatusMsg{
				outcome: classifyInteractiveConnection(interactiveConnectionOperationUnary, err),
			}
		}
		if err := response.Validate(); err != nil {
			return sessionPickerUpdateStatusMsg{outcome: invalidInteractiveConnectionContract(err)}
		}
		return sessionPickerUpdateStatusMsg{
			response: &response,
			outcome:  classifyInteractiveConnection(interactiveConnectionOperationUnary, nil),
		}
	}
}

func (m *sessionPickerModel) applyUpdateStatus(message sessionPickerUpdateStatusMsg) tea.Cmd {
	outcome := message.outcome
	if m.connection != nil {
		m.connection.ObserveOutcome(outcome)
	}
	switch outcome.Kind() {
	case interactiveConnectionOutcomeSuccess:
		if message.response == nil {
			return m.handleUpdateStatusInvalidContract(errSessionPickerUpdateResponseRequired)
		}
		if err := message.response.Validate(); err != nil {
			return m.handleUpdateStatusInvalidContract(err)
		}
		result := message.response.Result
		m.updateStatus = &result
		m.ensureSelectedVisible(m.tab(m.activeTab))
		return nil
	case interactiveConnectionOutcomeSurfaceCanceled, interactiveConnectionOutcomeConnectionLoss:
		return nil
	case interactiveConnectionOutcomeReachableOperationFailure, interactiveConnectionOutcomeInconclusiveOperationFailure:
		if outcome.Err() == nil {
			return m.handleUpdateStatusInvalidContract(errSessionPickerUpdateOutcomeRequired)
		}
		m.recordPickerFailureForTab(
			m.tab(m.activeTab),
			sessionPickerOperationUpdateStatus,
			0,
			sessionPickerFailureUpdateRequest,
			outcome.Err(),
		)
		return nil
	case interactiveConnectionOutcomeInvalidContract:
		return m.handleUpdateStatusInvalidContract(outcome.Err())
	default:
		return m.handleUpdateStatusInvalidContract(errSessionPickerUpdateOutcomeRequired)
	}
}

func (m *sessionPickerModel) handleUpdateStatusInvalidContract(err error) tea.Cmd {
	if err == nil {
		err = errSessionPickerUpdateOutcomeRequired
	}
	if m.header.StatusRequest.Settings.Debug {
		panic(fmt.Sprintf("session picker update status contract violation: %v", err))
	}
	m.recordPickerFailureForTab(
		m.tab(m.activeTab),
		sessionPickerOperationUpdateStatus,
		0,
		sessionPickerFailureUpdateContract,
		err,
	)
	m.result = newSessionPickerCancelResult()
	return tea.Quit
}
