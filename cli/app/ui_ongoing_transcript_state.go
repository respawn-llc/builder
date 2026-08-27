package app

import (
	"fmt"
	"strings"

	"core/cli/tui"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *uiModel) applyAdmittedTranscriptMessageState(
	message clientui.TranscriptMessage,
	admission runtimeTupleMergeResult,
) tea.Cmd {
	if m == nil {
		return nil
	}
	if m.turnQueueHook != nil {
		if message.Kind() == clientui.TranscriptMessageHydration {
			m.turnQueueHook.OnTurnQueueAborted()
		} else {
			m.turnQueueHook.OnTranscriptMessage(message)
		}
	}
	switch message.Kind() {
	case clientui.TranscriptMessageHydration:
		return m.applyTranscriptHydration(message.Payload().(clientui.TranscriptHydration), admission)
	case clientui.TranscriptMessageThinkingStatusUpdate:
		m.applyTranscriptThinkingStatusUpdate(message.Payload().(clientui.TranscriptThinkingStatusUpdate))
	case clientui.TranscriptMessageReasoningTraceReset:
		// A reset replaces the live reasoning body. The typed status remains
		// current until another status arrives or the owning step finishes.
	case clientui.TranscriptMessageUserMessageFlushed:
		return m.applyTranscriptUserMessageFlushed(message.Payload().(clientui.TranscriptUserMessageFlushed))
	case clientui.TranscriptMessageQueuedMessageState:
		return m.applyTranscriptQueuedMessageState(message.Payload().(clientui.TranscriptQueuedMessageState))
	case clientui.TranscriptMessageHumanInputInterrupted:
		return m.applyTranscriptHumanInputInterrupted(message.Payload().(clientui.TranscriptHumanInputInterrupted))
	case clientui.TranscriptMessageStepState:
		m.applyTranscriptStepState(message.Payload().(clientui.TranscriptStepState))
	case clientui.TranscriptMessageRuntimeReadModelUpdate:
		return m.applyTranscriptRuntimeReadModelUpdate(admission)
	case clientui.TranscriptMessageSessionStatus:
		m.applyTranscriptSessionStatus(message.Payload().(clientui.TranscriptSessionStatus))
	case clientui.TranscriptMessageSessionIdentity:
		return m.applyTranscriptSessionIdentity(message.Payload().(clientui.TranscriptSessionIdentity))
	case clientui.TranscriptMessageCompactionStatus:
		// RuntimeActivity is the authoritative compaction lifecycle. This event
		// carries lifecycle notification facts, not live-session state.
		status := message.Payload().(clientui.TranscriptCompactionStatus)
		if status.Mode == clientui.CompactionModeManual && m.pendingManualCompaction {
			switch status.State {
			case clientui.CompactionCompleted:
				m.pendingManualCompaction = false
				if m.turnQueueHook != nil {
					m.turnQueueHook.OnUserCompactionCompleted(m.inputController().turnQueueDrained())
				}
			case clientui.CompactionFailed:
				m.pendingManualCompaction = false
			}
		}
	case clientui.TranscriptMessageContextUsage:
		m.applyTranscriptContextUsage(message.Payload().(clientui.TranscriptContextUsage))
	case clientui.TranscriptMessageGoalStatus:
		// The runtime-client main-view cache is the goal read model used by the
		// status line and goal flow.
	case clientui.TranscriptMessageBackgroundActivity:
		m.applyTranscriptBackgroundActivity(message.Payload().(clientui.TranscriptBackgroundActivity))
		if m.processList.open {
			return m.requestProcessListRefresh()
		}
	case clientui.TranscriptMessagePrompt:
		prompt := message.Payload().(clientui.TranscriptPrompt)
		if prompt.Status == clientui.TranscriptPromptStatusResolved {
			return m.askController().resolvePrompt(string(prompt.PromptID))
		}
		return m.askController().acceptEvent(m.transcriptPromptEvent(prompt))
	case clientui.TranscriptMessageWorktreeTransitionOutcome:
		return m.reconcileTranscriptWorktreeTransitionOutcome(message.Payload().(clientui.TranscriptWorktreeTransitionOutcome))
	case clientui.TranscriptMessageOperationalDiagnostic:
		return m.applyTranscriptOperationalDiagnostic(message.Payload().(clientui.TranscriptOperationalDiagnostic))
	}
	return nil
}

func (m *uiModel) applyTranscriptHydration(
	hydration clientui.TranscriptHydration,
	admission runtimeTupleMergeResult,
) tea.Cmd {
	var cmds []tea.Cmd
	cmds = append(cmds, m.applyTranscriptSessionIdentity(hydration.SessionIdentity))
	m.applyTranscriptSessionStatus(hydration.SessionStatus)
	cmds = append(cmds, m.applyTranscriptRuntimeReadModelUpdate(admission))

	m.reasoningStatusHeader = ""
	if hydration.ActiveThinkingStatus != nil {
		m.applyTranscriptThinkingStatusUpdate(*hydration.ActiveThinkingStatus)
	}
	if hydration.ActiveStep != nil {
		m.applyTranscriptStepState(*hydration.ActiveStep)
	}

	m.reconcileTranscriptQueuedMessages(hydration.QueuedMessages)
	cmds = append(cmds, m.reconcileTranscriptPrompts(hydration.PendingPrompts))
	cmds = append(cmds, m.releasePendingPromptCtrlCContinuation())
	currentSessionID := strings.TrimSpace(m.sessionID)
	preserved := m.processList.entries[:0]
	for _, entry := range m.processList.entries {
		if strings.TrimSpace(entry.OwnerSessionID) != currentSessionID {
			preserved = append(preserved, entry)
		}
	}
	m.processList.entries = preserved
	m.processList.selection = 0
	for _, background := range hydration.BackgroundActivities {
		m.applyTranscriptBackgroundActivity(background)
	}
	if hydration.ContextUsage == nil {
		m.setRuntimeContextUsage("", clientui.RuntimeContextUsage{})
	} else {
		m.applyTranscriptContextUsage(*hydration.ContextUsage)
	}
	if m.processList.open {
		cmds = append(cmds, m.requestProcessListRefresh())
	}
	return batchCmds(cmds...)
}

func (m *uiModel) applyTranscriptRuntimeReadModelUpdate(admission runtimeTupleMergeResult) tea.Cmd {
	switch admission.decision {
	case runtimeTupleRefresh:
		return m.startRuntimeMainViewRefreshRequest(runtimeReadModelResetMainViewRefreshRequest()).cmd
	}
	if !admission.project {
		return nil
	}
	view := admission.view
	if err := m.applyRuntimeActivityProjection(view.Activity); err != nil {
		m.activity = uiActivityError
		return m.sendTransientStatusWithNoticeID(
			"invalid runtime activity: "+err.Error(),
			uiStatusNoticeError,
			transientStatusDuration,
			uiStatusNoticeReplace,
			"",
		)
	}
	promptCtrlCCmd := m.releasePendingPromptCtrlCContinuation()
	if view.Activity.ActiveForControl() {
		return promptCtrlCCmd
	}
	var cmd tea.Cmd
	if m.hasPendingInterrupt() {
		cmd = m.acknowledgePendingInterrupt()
	}
	return tea.Batch(promptCtrlCCmd, cmd, m.releaseDeferredRuntimeSyncs())
}

func (m *uiModel) applyTranscriptStepState(state clientui.TranscriptStepState) {
	if state.Lifecycle == clientui.StepLifecycleStarted {
		return
	}
	m.reasoningStatusHeader = ""
}

func (m *uiModel) applyTranscriptThinkingStatusUpdate(update clientui.TranscriptThinkingStatusUpdate) {
	m.reasoningStatusHeader = strings.TrimSpace(update.Text)
}

func (m *uiModel) applyTranscriptSessionStatus(status clientui.TranscriptSessionStatus) {
	m.reviewerMode = status.ReviewerFrequency
	m.reviewerEnabled = status.ReviewerEnabled
	m.autoCompactionEnabled = status.AutoCompactionEnabled
	m.questionsEnabled = status.QuestionsEnabled
	m.fastModeAvailable = status.FastModeAvailable
	m.fastModeEnabled = status.FastModeEnabled
	m.thinkingLevel = status.ThinkingLevel
}

func (m *uiModel) applyTranscriptSessionIdentity(identity clientui.TranscriptSessionIdentity) tea.Cmd {
	previousSessionID := strings.TrimSpace(m.sessionID)
	nextSessionID := identity.SessionID.String()
	m.sessionID = nextSessionID
	m.sessionName = ""
	if identity.SessionName != nil {
		m.sessionName = strings.TrimSpace(*identity.SessionName)
	}
	m.conversationFreshness = identity.ConversationFreshness
	titleCmd := tea.SetWindowTitle(sessionTitle(m.sessionName))
	if previousSessionID == "" || previousSessionID == nextSessionID {
		return titleCmd
	}
	m.pendingManualCompaction = false
	m.askController().cancelActiveDelivery()
	m.ask.pendingCtrlCContinuation = nil
	promptCmd := m.reconcileTranscriptPrompts(nil)
	rollbackCmd := m.discardRollbackStateForSessionReplacement()
	cancelCmd := m.cancelPendingDetailTranscriptRequest()
	m.detailTranscript.reset()
	resetCmd := m.forwardToView(tui.ResetDetailTranscriptMsg{})
	loadCmd := m.loadDetailTranscriptPageCmd(m.detailTranscript.requestedPageForDetailEntry())
	return tea.Batch(
		promptCmd,
		sequenceCmds(titleCmd, rollbackCmd, cancelCmd, resetCmd, loadCmd),
	)
}

func (m *uiModel) applyTranscriptContextUsage(usage clientui.TranscriptContextUsage) {
	m.setRuntimeContextUsage(m.currentRuntimeSessionID(), runtimeContextUsageFromTranscript(usage))
}

func (m *uiModel) applyTranscriptUserMessageFlushed(flushed clientui.TranscriptUserMessageFlushed) tea.Cmd {
	m.conversationFreshness = clientui.ConversationFreshnessEstablished
	m.localConversationTurn = true
	return nil
}

func (m *uiModel) applyTranscriptQueuedMessageState(state clientui.TranscriptQueuedMessageState) tea.Cmd {
	if state.Status == clientui.QueuedUserMessageAccepted {
		m.registerSteeredQueuedUserMessage(clientui.QueuedUserMessage{
			ID:   state.QueueItemID.String(),
			Text: dereferenceTranscriptText(state.Text),
		})
		return nil
	}
	ids := []string{state.QueueItemID.String()}
	index := m.injectedQueueIndexByAnyID(state.QueueItemID.String())
	if index < 0 {
		m.retainUnownedQueuedTerminalState(state)
		return nil
	}
	localText := m.injectedQueue[index].Text
	var cmd tea.Cmd
	for _, answer := range m.removeInjectedQueueItemsByIDs(ids) {
		cmd = tea.Batch(cmd, m.answerQueuedApprovalCommentary(answer))
	}
	if state.Status != clientui.QueuedUserMessageFailed {
		return cmd
	}
	m.inputController().restoreInjectedTextIntoInput(localText)
	return tea.Batch(cmd, m.sendTransientStatusWithNoticeID(
		"queued message was not submitted; restored to input",
		uiStatusNoticeError,
		transientStatusDuration,
		uiStatusNoticeReplace,
		"",
	))
}

func (m *uiModel) applyTranscriptHumanInputInterrupted(event clientui.TranscriptHumanInputInterrupted) tea.Cmd {
	ids := make([]string, 0, len(event.Items))
	texts := make([]string, 0, len(event.Items))
	for _, item := range event.Items {
		id := item.QueueItemID.String()
		if m.injectedQueueIndexByAnyID(id) < 0 {
			m.retainUnownedQueuedTerminalState(clientui.TranscriptQueuedMessageState{
				QueueItemID: item.QueueItemID,
				Status:      clientui.QueuedUserMessageDiscarded,
			})
		} else {
			ids = append(ids, id)
		}
		texts = append(texts, item.Text)
	}
	var cmd tea.Cmd
	for _, answer := range m.removeInjectedQueueItemsByIDs(ids) {
		cmd = tea.Batch(cmd, m.answerQueuedApprovalCommentary(answer))
	}
	m.inputController().restoreServerOrderedTextBeforeComposer(strings.Join(texts, "\n\n"))
	if m.hasPendingInterrupt() {
		cmd = tea.Batch(cmd, m.acknowledgePendingInterrupt())
	}
	return tea.Batch(cmd, m.sendTransientStatusWithNoticeID(
		"interrupted input was restored",
		uiStatusNoticeError,
		transientStatusDuration,
		uiStatusNoticeReplace,
		"",
	))
}

func (m *uiModel) reconcileTranscriptQueuedMessages(states []clientui.TranscriptQueuedMessageState) {
	for _, state := range states {
		if state.Status != clientui.QueuedUserMessageAccepted {
			continue
		}
		m.registerSteeredQueuedUserMessage(clientui.QueuedUserMessage{
			ID:   state.QueueItemID.String(),
			Text: dereferenceTranscriptText(state.Text),
		})
	}
}

func dereferenceTranscriptText(text *string) string {
	if text == nil {
		return ""
	}
	return *text
}

func (m *uiModel) transcriptPromptEvent(prompt clientui.TranscriptPrompt) askEvent {
	if m.promptAnswers != nil {
		return m.promptAnswers.event(prompt)
	}
	return askEvent{
		prompt: cloneTranscriptPromptForAsk(prompt),
	}
}

func (m *uiModel) reconcileTranscriptPrompts(prompts []clientui.TranscriptPrompt) tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(prompts)+1)
	present := make(map[string]struct{}, len(prompts))
	for _, prompt := range prompts {
		present[string(prompt.PromptID)] = struct{}{}
	}
	var stale []string
	if m.ask.hasCurrent() {
		id := m.ask.current.promptID()
		if _, exists := present[id]; !exists {
			stale = append(stale, id)
		}
	}
	for _, queued := range m.ask.queue {
		id := queued.promptID()
		if _, exists := present[id]; !exists {
			stale = append(stale, id)
		}
	}
	for _, id := range stale {
		cmds = append(cmds, m.askController().resolvePrompt(id))
	}
	for _, prompt := range prompts {
		cmds = append(cmds, m.askController().acceptEvent(m.transcriptPromptEvent(prompt)))
	}
	return batchCmds(cmds...)
}

func (m *uiModel) applyTranscriptOperationalDiagnostic(diagnostic clientui.TranscriptOperationalDiagnostic) tea.Cmd {
	switch diagnostic.Code {
	case clientui.OperationalDiagnosticSleepGuardFailed:
		return m.sendTransientStatusWithNoticeID(
			"sleep prevention failed: "+diagnostic.Detail,
			uiStatusNoticeError,
			transientStatusDuration,
			uiStatusNoticeReplace,
			"",
		)
	case clientui.OperationalDiagnosticPromptHistoryPersistFailed:
		return m.sendTransientStatusWithNoticeID(
			"prompt history persistence failed: "+diagnostic.Detail,
			uiStatusNoticeError,
			transientStatusDuration,
			uiStatusNoticeReplace,
			"",
		)
	case clientui.OperationalDiagnosticInFlightClearFailed:
		return m.sendTransientStatusWithNoticeID(
			"run cleanup failed: "+diagnostic.Detail,
			uiStatusNoticeError,
			transientStatusDuration,
			uiStatusNoticeReplace,
			"",
		)
	default:
		panic(fmt.Sprintf("unsupported transcript operational diagnostic %q", diagnostic.Code))
	}
}
