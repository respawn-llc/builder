package app

import (
	"io"
	"strings"

	"core/cli/app/internal/connectionstate"
	"core/shared/clientui"
)

const runtimeDisconnectedStatusMessage = "server disconnected"

func (m *uiModel) observeRuntimeRequestResult(err error) {
	if m == nil || !m.hasRuntimeClient() || m.connectionState == nil {
		return
	}
	m.connectionState.ObserveUnary(err)
}

func (m *uiModel) runtimeDisconnectStatusVisible() bool {
	return m != nil && m.hasRuntimeClient() && m.runtimeDisconnectedState()
}

func (m *uiModel) runtimeDisconnectStatusText() string {
	if !m.runtimeDisconnectStatusVisible() {
		return ""
	}
	return runtimeDisconnectedStatusMessage
}

func enqueueRuntimeConnectionStateChange(ch chan runtimeConnectionStateChangedMsg, err error) {
	if ch == nil {
		return
	}
	coalesceLatest(ch, runtimeConnectionStateChangedMsg{err: err})
}

func enqueueRuntimeReconnectWarning(ch chan runtimeReconnectWarningMsg, text string, visibility clientui.EntryVisibility) {
	if ch == nil || strings.TrimSpace(text) == "" {
		return
	}
	coalesceLatest(ch, runtimeReconnectWarningMsg{text: strings.TrimSpace(text), visibility: visibility})
}

func coalesceLatest[T any](ch chan T, msg T) {
	select {
	case ch <- msg:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- msg:
	default:
	}
}

func (m *uiModel) runtimeDisconnectedState() bool {
	if m == nil || m.connectionState == nil {
		return false
	}
	return m.connectionState.IsDisconnected()
}

func (m *uiModel) setRuntimeDisconnected(disconnected bool) {
	if m == nil || m.connectionState == nil {
		return
	}
	if disconnected {
		m.connectionState.ObserveUnary(io.EOF)
		return
	}
	m.connectionState.ObserveUnary(nil)
}

func (m *uiModel) observeRuntimeStreamResult(err error) connectionstate.Outcome {
	if m == nil || m.connectionState == nil {
		return connectionstate.Classify(connectionstate.OperationStream, err)
	}
	return m.connectionState.ObserveStream(err)
}
