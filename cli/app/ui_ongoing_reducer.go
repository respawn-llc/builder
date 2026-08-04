package app

import (
	"errors"
	"fmt"

	"core/cli/app/internal/runtimeattach"
	"core/cli/tui/ongoing"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *uiModel) reduceOngoingMessage(msg tea.Msg) uiFeatureUpdateResult {
	switch msg := msg.(type) {
	case ongoingTranscriptEvent:
		cmd := m.handleOngoingTranscriptEvent(msg)
		return handledUIFeatureUpdate(m, cmd)
	case ongoingNormalBufferOwnedMsg:
		_ = msg
		return handledUIFeatureUpdate(m, m.reconcileOngoingOwnership())
	case ongoingOwnershipReconcileMsg:
		return handledUIFeatureUpdate(m, m.reconcileOngoingOwnership())
	case ongoingWidthRehydrationDebounceMsg:
		if msg.token != m.ongoingWidthToken {
			return handledUIFeatureUpdate(m, nil)
		}
		result := ongoing.Result{Action: ongoing.ResultRequestScratchRehydration, Reason: ongoing.RehydrateReasonWidthChange}
		return handledUIFeatureUpdate(m, m.handleOngoingResult(result))
	}
	return uiFeatureUpdateResult{}
}

func (m *uiModel) handleOngoingTranscriptEvent(event ongoingTranscriptEvent) tea.Cmd {
	if m == nil || m.ongoingTranscript == nil {
		return nil
	}
	var (
		result ongoing.Result
		err    error
	)
	switch event.Kind {
	case ongoingTranscriptEventMessage:
		wasRuntimeDisconnected := m.runtimeDisconnectStatusVisible()
		var stateCmd tea.Cmd
		result, stateCmd, err = m.ongoingTranscript.Accept(event.Message)
		if err != nil {
			var developerErr ongoing.DeveloperError
			if errors.As(err, &developerErr) {
				return m.handleOngoingDeveloperError(developerErr)
			}
			return m.handleOngoingSurfaceError(err)
		}
		acceptedHydration := m.ongoingTranscript.acceptedHydration(event.Message)
		if acceptedHydration {
			m.observeRuntimeRequestResult(nil)
		}
		stateCmd = sequenceCmds(stateCmd, m.inputController().resumeQueuedInputsAfterIdleRuntime())
		if acceptedHydration {
			stateCmd = sequenceCmds(stateCmd, m.flushQueuedInputsAfterHydration())
		}
		m.inputController().notifyTurnQueueDrainedIfIdle()
		m.layout().syncViewport()
		var connectionStatusCmd tea.Cmd
		if wasRuntimeDisconnected && acceptedHydration {
			connectionStatusCmd = m.renderNativeOngoingSurface()
		}
		return tea.Batch(stateCmd, m.handleOngoingResult(result), m.reconcileSpinnerTicking(true), connectionStatusCmd)
	case ongoingTranscriptEventLoss:
		if runtimeattach.IsRuntimeConnectionError(event.Err) {
			m.observeRuntimeRequestResult(event.Err)
		}
		if m.turnQueueHook != nil {
			m.turnQueueHook.OnTurnQueueAborted()
		}
		result = m.ongoingTranscript.HandleSubscriptionLoss()
	case ongoingTranscriptEventFailure:
		err = fmt.Errorf("open transcript subscription: %w", event.Err)
		m.logf("ongoing.transcript.open.error err=%q", err.Error())
		if runtimeattach.IsRuntimeConnectionError(event.Err) {
			m.observeRuntimeRequestResult(event.Err)
			m.layout().syncViewport()
			return m.renderNativeOngoingSurface()
		}
		return m.handleFatalUIError(fmt.Sprintf("ongoing transcript failed: %v", err), err)
	default:
		if m.debugMode {
			panic("unknown ongoing transcript event kind")
		}
		return nil
	}
	if err != nil {
		return m.handleOngoingSurfaceError(err)
	}
	return m.handleOngoingResult(result)
}
