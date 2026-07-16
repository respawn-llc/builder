package app

import (
	"core/cli/tui/ongoing"
	"core/shared/clientui"

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
		var stateCmd tea.Cmd
		result, stateCmd, err = m.ongoingTranscript.Accept(event.Message)
		if err == nil {
			stateCmd = sequenceCmds(stateCmd, m.inputController().resumeQueuedInputsAfterIdleRuntime())
		}
		if event.Message.Kind == clientui.TranscriptMessageHydration {
			stateCmd = sequenceCmds(stateCmd, m.flushQueuedInputsAfterHydration())
		}
		m.layout().syncViewport()
		if err != nil {
			return m.handleOngoingSurfaceError(err)
		}
		return tea.Batch(stateCmd, m.handleOngoingResult(result), m.reconcileSpinnerTicking(true))
	case ongoingTranscriptEventLoss:
		result = m.ongoingTranscript.HandleSubscriptionLoss()
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
