package app

import (
	"context"
	"errors"
	"fmt"

	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

type sessionPickerUpdateStatusMsg struct {
	response serverapi.UpdateStatusResponse
	err      error
}

func (m *sessionPickerModel) collectUpdateStatusCmd() tea.Cmd {
	if m == nil || m.header.updateStatus == nil {
		return nil
	}
	client := m.header.updateStatus
	ctx := m.requestContext
	return func() tea.Msg {
		response, err := client.GetUpdateStatus(ctx, serverapi.UpdateStatusRequest{})
		if err != nil {
			return sessionPickerUpdateStatusMsg{err: err}
		}
		return sessionPickerUpdateStatusMsg{response: response}
	}
}

func (m *sessionPickerModel) applyUpdateStatus(message sessionPickerUpdateStatusMsg) tea.Cmd {
	if errors.Is(message.err, context.Canceled) {
		return nil
	}
	if message.err != nil {
		m.setUpdateStatusFailure(fmt.Errorf("request update status: %w", message.err))
		return nil
	}
	if err := message.response.Validate(); err != nil {
		m.handleUpdateStatusInvalidContract(err)
		return nil
	}
	result := message.response.Result
	m.updateStatus = &result
	m.ensureSelectedVisible(m.tab(m.activeTab))
	return nil
}

func (m *sessionPickerModel) handleUpdateStatusInvalidContract(err error) {
	if err == nil {
		err = errors.New("update status contract violation")
	}
	if m.header.StatusRequest.Settings.Debug {
		panic(fmt.Sprintf("session picker update status contract violation: %v", err))
	}
	m.setUpdateStatusFailure(fmt.Errorf("invalid update status response: %w", err))
}

func (m *sessionPickerModel) setUpdateStatusFailure(err error) {
	cause := "unknown update status failure"
	if err != nil {
		cause = err.Error()
	}
	result := serverapi.FailedUpdateStatusResult(cause)
	m.updateStatus = &result
	m.ensureSelectedVisible(m.tab(m.activeTab))
}
