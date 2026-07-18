package app

import (
	"errors"

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
	if m == nil {
		return nil
	}
	if event.Kind == ongoingTranscriptEventReachable {
		m.observeRuntimeStreamResult(nil)
		return m.batchWithNativeOngoingRepaint(nil)
	}
	if m.ongoingTranscript == nil {
		return nil
	}
	switch event.Kind {
	case ongoingTranscriptEventMessage:
		result, stateCmd, err := m.ongoingTranscript.Accept(event.Message)
		if err != nil {
			var developerErr ongoing.DeveloperError
			if errors.As(err, &developerErr) {
				return m.handleOngoingDeveloperError(developerErr)
			}
			return m.handleOngoingSurfaceError(err)
		}
		stateCmd = sequenceCmds(stateCmd, m.inputController().resumeQueuedInputsAfterIdleRuntime())
		if m.ongoingTranscript.acceptedHydration(event.Message) {
			stateCmd = sequenceCmds(stateCmd, m.flushQueuedInputsAfterHydration())
		}
		m.layout().syncViewport()
		return tea.Batch(stateCmd, m.handleOngoingResult(result), m.reconcileSpinnerTicking(true))
	case ongoingTranscriptEventLoss:
		if m.turnQueueHook != nil {
			m.turnQueueHook.OnTurnQueueAborted()
		}
		m.observeRuntimeStreamResult(event.Err)
		result := m.ongoingTranscript.HandleSubscriptionLoss()
		return m.batchWithNativeOngoingRepaint(m.handleOngoingResult(result))
	default:
		if m.debugMode {
			panic("unknown ongoing transcript event kind")
		}
		return nil
	}
}
