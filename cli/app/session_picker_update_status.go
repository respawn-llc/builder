package app

import (
	"context"
	"errors"
	"fmt"

	serverpb "core/shared/protoapi/gen/kent/api/server"

	tea "github.com/charmbracelet/bubbletea"
	"google.golang.org/protobuf/types/known/emptypb"
)

type sessionPickerUpdateStatusMsg struct {
	response *serverpb.GetUpdateStatusSuccess
	err      error
}

func (m *sessionPickerModel) collectUpdateStatusCmd() tea.Cmd {
	if m == nil || m.header.updateStatus == nil {
		return nil
	}
	client := m.header.updateStatus
	ctx := m.requestContext
	return func() tea.Msg {
		response, err := client.GetUpdateStatus(ctx, &emptypb.Empty{})
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
	m.updateStatus = message.response.GetStatus()
	m.ensureSelectedVisible(m.tab(m.activeTab))
	return nil
}

func (m *sessionPickerModel) setUpdateStatusFailure(err error) {
	cause := "unknown update status failure"
	if err != nil {
		cause = err.Error()
	}
	m.updateStatus = &serverpb.UpdateStatus{
		Status: &serverpb.UpdateStatus_CheckFailed{
			CheckFailed: &serverpb.UpdateCheckFailed{Cause: cause},
		},
	}
	m.ensureSelectedVisible(m.tab(m.activeTab))
}
