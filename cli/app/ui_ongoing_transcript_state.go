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
	case clientui.TranscriptMessageReasoningUpdate:
		m.applyTranscriptReasoningUpdate(message.Payload().(clientui.TranscriptReasoningUpdate))
	case clientui.TranscriptMessageReasoningReset:
		// A reset replaces the live reasoning body. The typed status remains
		// current until another status arrives or the owning step finishes.
	case clientui.TranscriptMessageUserMessageFlushed:
		return m.applyTranscriptUserMessageFlushed(message.Payload().(clientui.TranscriptUserMessageFlushed))
	case clientui.TranscriptMessageQueuedMessageState:
		return m.applyTranscriptQueuedMessageState(message.Payload().(clientui.TranscriptQueuedMessageState))
	case clientui.TranscriptMessageStepState:
		m.applyTranscriptStepState(message.Payload().(clientui.TranscriptStepState))
	case clientui.TranscriptMessageReviewerState:
		m.applyTranscriptReviewerState(message.Payload().(clientui.TranscriptReviewerState))
	case clientui.TranscriptMessageRuntimeReadModelUpdate:
		return m.applyTranscriptRuntimeReadModelUpdate(admission)
	case clientui.TranscriptMessageSessionStatus:
		m.applyTranscriptSessionStatus(message.Payload().(clientui.TranscriptSessionStatus))
	case clientui.TranscriptMessageSessionIdentity:
		return m.applyTranscriptSessionIdentity(message.Payload().(clientui.TranscriptSessionIdentity))
	case clientui.TranscriptMessageCompactionStatus:
		m.applyTranscriptCompactionStatus(message.Payload().(clientui.TranscriptCompactionStatus))
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
	if hydration.ActiveReasoning != nil {
		m.applyTranscriptReasoningUpdate(*hydration.ActiveReasoning)
	}
	m.setCompacting(false)
	if hydration.ActiveCompaction != nil {
		m.applyTranscriptCompactionStatus(*hydration.ActiveCompaction)
	}
	m.clearReviewerState()
	if hydration.ActiveReviewer != nil {
		m.applyTranscriptReviewerState(*hydration.ActiveReviewer)
	}
	if hydration.ActiveStep != nil {
		m.applyTranscriptStepState(*hydration.ActiveStep)
	}

	m.reconcileTranscriptQueuedMessages(hydration.QueuedMessages)
	cmds = append(cmds, m.reconcileTranscriptPrompts(hydration.PendingPrompts))
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
	if view.Activity.ActiveForControl() {
		return nil
	}
	var cmd tea.Cmd
	if m.hasPendingInterrupt() {
		if m.pendingInterruptMissingInputReconciliation(m.cachedRuntimeMainView()) {
			cmd = m.requestInputReconciliationRefresh()
		} else {
			cmd = m.acknowledgePendingInterrupt()
		}
	}
	return tea.Batch(cmd, m.releaseDeferredRuntimeSyncs())
}

func (m *uiModel) applyTranscriptStepState(state clientui.TranscriptStepState) {
	if state.Lifecycle == clientui.StepLifecycleStarted {
		if m.activeSubmit.token != 0 && strings.TrimSpace(m.activeSubmit.stepID) == "" {
			m.activeSubmit.stepID = state.StepID.String()
		}
		return
	}
	m.reasoningStatusHeader = ""
	m.clearReviewerState()
}

func (m *uiModel) applyTranscriptReviewerState(state clientui.TranscriptReviewerState) {
	switch state.State {
	case clientui.ReviewerStateRunning:
		reviewer, err := clientui.NewReviewerLifecycle(true, true)
		if err != nil {
			panic(err)
		}
		m.runtimeLifecycle.Reviewer = reviewer
	case clientui.ReviewerStateCompleted:
		m.runtimeLifecycle.Reviewer = clientui.ReviewerLifecycleIdle
	default:
		panic(fmt.Sprintf("unsupported transcript reviewer state %q", state.State))
	}
}

func (m *uiModel) applyTranscriptCompactionStatus(status clientui.TranscriptCompactionStatus) {
	switch status.State {
	case clientui.CompactionStarted:
		m.setCompacting(true)
	case clientui.CompactionCompleted, clientui.CompactionFailed:
		m.setCompacting(false)
	default:
		panic(fmt.Sprintf("unsupported transcript compaction state %q", status.State))
	}
}

func (m *uiModel) applyTranscriptReasoningUpdate(update clientui.TranscriptReasoningUpdate) {
	if update.CurrentStatus != nil {
		m.reasoningStatusHeader = strings.TrimSpace(update.CurrentStatus.Text)
	}
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
	ids := runtimeOperationIdentityStrings(flushed.Operations)
	if m.activeSubmit.token != 0 && runtimeOperationRefsContain(flushed.Operations, m.activeSubmit.operationRef) {
		m.activeSubmit.stepID = flushed.StepID.String()
		m.activeSubmit.flushed = true
	}
	for _, id := range ids {
		m.removePendingInjectedByID(id)
	}
	var cmd tea.Cmd
	for _, answer := range m.removeInjectedQueueItemsByIDs(ids) {
		cmd = tea.Batch(cmd, m.answerQueuedApprovalCommentary(answer))
	}
	return cmd
}

func (m *uiModel) applyTranscriptQueuedMessageState(state clientui.TranscriptQueuedMessageState) tea.Cmd {
	ids := runtimeOperationIdentityStrings([]clientui.RuntimeOperationRef{{
		Kind:            clientui.RuntimeOperationKindQueuedMessage,
		ClientRequestID: state.ClientRequestID,
		QueueItemID:     &state.QueueItemID,
	}})
	if state.Status == clientui.QueuedUserMessageAccepted {
		m.registerSteeredQueuedUserMessage(clientui.QueuedUserMessage{
			ID:              state.QueueItemID.String(),
			Text:            dereferenceTranscriptText(state.Text),
			ClientRequestID: state.ClientRequestID.String(),
		})
		return nil
	}
	for _, id := range ids {
		m.removePendingInjectedByID(id)
	}
	var cmd tea.Cmd
	for _, answer := range m.removeInjectedQueueItemsByIDs(ids) {
		cmd = tea.Batch(cmd, m.answerQueuedApprovalCommentary(answer))
	}
	if state.Status != clientui.QueuedUserMessageFailed || state.Text == nil {
		return cmd
	}
	m.inputController().restoreInjectedTextIntoInput(*state.Text)
	return tea.Batch(cmd, m.sendTransientStatusWithNoticeID(
		"queued message was not submitted; restored to input",
		uiStatusNoticeError,
		transientStatusDuration,
		uiStatusNoticeReplace,
		"",
	))
}

func (m *uiModel) reconcileTranscriptQueuedMessages(states []clientui.TranscriptQueuedMessageState) {
	present := make(map[string]struct{}, len(states)*2)
	for _, state := range states {
		present[state.ClientRequestID.String()] = struct{}{}
		present[state.QueueItemID.String()] = struct{}{}
	}
	filtered := m.injectedQueue[:0]
	for _, item := range m.injectedQueue {
		switch item.State {
		case injectedRuntimeQueuePendingCreate, injectedRuntimeQueueCanceledBeforeCreate, injectedRuntimeQueueCreateFailed:
			filtered = append(filtered, item)
			continue
		}
		_, requestPresent := present[strings.TrimSpace(item.ClientRequestID)]
		_, itemPresent := present[strings.TrimSpace(item.ServerID)]
		if requestPresent || itemPresent {
			filtered = append(filtered, item)
			continue
		}
		m.removePendingInjectedByID(item.LocalID)
		m.removePendingInjectedByID(item.ServerID)
		m.removePendingInjectedByID(item.ClientRequestID)
	}
	m.injectedQueue = filtered
	for _, state := range states {
		m.registerSteeredQueuedUserMessage(clientui.QueuedUserMessage{
			ID:              state.QueueItemID.String(),
			Text:            dereferenceTranscriptText(state.Text),
			ClientRequestID: state.ClientRequestID.String(),
		})
	}
}

func runtimeOperationIdentityStrings(operations []clientui.RuntimeOperationRef) []string {
	ids := make([]string, 0, len(operations)*2)
	for _, operation := range operations {
		ids = append(ids, operation.ClientRequestID.String())
		if operation.QueueItemID != nil {
			ids = append(ids, operation.QueueItemID.String())
		}
	}
	return ids
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
	default:
		panic(fmt.Sprintf("unsupported transcript operational diagnostic %q", diagnostic.Code))
	}
}
