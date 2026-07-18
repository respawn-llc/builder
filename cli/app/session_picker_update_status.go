package app

import (
	"errors"
	"fmt"

	"core/cli/app/internal/connectionstate"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

type sessionPickerUpdateKind uint8

const (
	sessionPickerUpdatePending sessionPickerUpdateKind = iota + 1
	sessionPickerUpdateCurrent
	sessionPickerUpdateAvailable
	sessionPickerUpdateCheckUnavailable
	sessionPickerUpdateCheckFailed
)

type sessionPickerUpdateState struct {
	kind   sessionPickerUpdateKind
	result *serverapi.UpdateStatusResult
}

func pendingSessionPickerUpdateState() sessionPickerUpdateState {
	return sessionPickerUpdateState{kind: sessionPickerUpdatePending}
}

func sessionPickerUpdateStateFor(result serverapi.UpdateStatusResult) (sessionPickerUpdateState, error) {
	if err := result.Validate(); err != nil {
		return sessionPickerUpdateState{}, err
	}
	state := sessionPickerUpdateState{}
	switch result.Kind() {
	case serverapi.UpdateStatusCurrent:
		state.kind = sessionPickerUpdateCurrent
	case serverapi.UpdateStatusAvailable:
		state.kind = sessionPickerUpdateAvailable
	case serverapi.UpdateStatusCheckUnavailable:
		state.kind = sessionPickerUpdateCheckUnavailable
	case serverapi.UpdateStatusCheckFailed:
		state.kind = sessionPickerUpdateCheckFailed
	default:
		return sessionPickerUpdateState{}, fmt.Errorf("unknown update status result kind %q", result.Kind())
	}
	state.result = &result
	return state, nil
}

type sessionPickerUpdateStatusMsg struct {
	response *serverapi.UpdateStatusResponse
	outcome  connectionstate.Outcome
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
				outcome: connectionstate.Classify(connectionstate.OperationUnary, err),
			}
		}
		if err := response.Validate(); err != nil {
			return sessionPickerUpdateStatusMsg{outcome: connectionstate.InvalidContract(err)}
		}
		return sessionPickerUpdateStatusMsg{
			response: &response,
			outcome:  connectionstate.Classify(connectionstate.OperationUnary, nil),
		}
	}
}

func (m *sessionPickerModel) applyUpdateStatus(message sessionPickerUpdateStatusMsg) tea.Cmd {
	outcome := message.outcome
	if m.connection != nil {
		m.connection.ObserveOutcome(outcome)
	}
	switch outcome.Kind() {
	case connectionstate.OutcomeSuccess:
		if message.response == nil {
			return m.handleUpdateStatusInvalidContract(errSessionPickerUpdateResponseRequired)
		}
		if err := message.response.Validate(); err != nil {
			return m.handleUpdateStatusInvalidContract(err)
		}
		state, err := sessionPickerUpdateStateFor(message.response.Result)
		if err != nil {
			return m.handleUpdateStatusInvalidContract(err)
		}
		m.updateStatus = state
		m.ensureSelectedVisible(m.tab(m.activeTab))
		return nil
	case connectionstate.OutcomeSurfaceCanceled, connectionstate.OutcomeConnectionLoss:
		return nil
	case connectionstate.OutcomeReachableOperationFailure, connectionstate.OutcomeInconclusiveOperationFailure:
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
	case connectionstate.OutcomeInvalidContract:
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
