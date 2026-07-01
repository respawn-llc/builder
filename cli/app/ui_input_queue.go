package app

import (
	"strings"

	"core/cli/app/commands"
	"core/cli/app/internal/runtimeattach"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
)

type queueDrainMode uint8

const (
	queueDrainOne queueDrainMode = iota
	queueDrainAuto
)

type queuedInputItem struct {
	ID   string
	Text string
}

type injectedRuntimeQueueState string

const (
	injectedRuntimeQueuePendingCreate        injectedRuntimeQueueState = "pendingCreate"
	injectedRuntimeQueueEnqueued             injectedRuntimeQueueState = "enqueued"
	injectedRuntimeQueueDiscardPending       injectedRuntimeQueueState = "discardPending"
	injectedRuntimeQueueCanceledBeforeCreate injectedRuntimeQueueState = "canceledBeforeCreate"
	injectedRuntimeQueueCreateFailed         injectedRuntimeQueueState = "createFailed"
	injectedRuntimeQueueDiscardFailed        injectedRuntimeQueueState = "discardFailed"
)

type injectedRuntimeQueueItem struct {
	LocalID                  string
	ServerID                 string
	Text                     string
	ClientRequestID          string
	State                    injectedRuntimeQueueState
	CreateToken              uint64
	DiscardToken             uint64
	ApprovalCommentaryAnswer *clientui.PromptAnswer
}

func (m *uiModel) queueInput(text string) {
	m.queued = append(m.queued, newQueuedInputItem(text))
	m.clearInput()
}

func newQueuedInputItem(text string) queuedInputItem {
	return queuedInputItem{ID: uuid.NewString(), Text: text}
}

func (m *uiModel) registerSteeredQueuedUserMessage(queued clientui.QueuedUserMessage) {
	serverID := strings.TrimSpace(queued.ID)
	if serverID == "" {
		return
	}
	if m.injectedQueueIndexByAnyID(serverID) >= 0 {
		return
	}
	m.pendingInjected = append(m.pendingInjected, clientui.QueuedUserMessage{ID: serverID, Text: queued.Text, ClientRequestID: queued.ClientRequestID})
	m.injectedQueue = append(m.injectedQueue, injectedRuntimeQueueItem{LocalID: serverID, ServerID: serverID, Text: queued.Text, ClientRequestID: queued.ClientRequestID, State: injectedRuntimeQueueEnqueued})
}

func (m *uiModel) enqueueInjectedInputWithApprovalAnswer(text string, answer *clientui.PromptAnswer) tea.Cmd {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	localID := uuid.NewString()
	if !m.hasRuntimeClient() {
		item := clientui.QueuedUserMessage{ID: localID, Text: trimmed}
		m.pendingInjected = append(m.pendingInjected, item)
		m.injectedQueue = append(m.injectedQueue, injectedRuntimeQueueItem{LocalID: localID, ServerID: localID, Text: trimmed, State: injectedRuntimeQueueEnqueued})
		return nil
	}
	token := m.nextInjectedQueueToken()
	clientRequestID := uuid.NewString()
	var approvalCommentaryAnswer *clientui.PromptAnswer
	if answer != nil {
		snap := *answer
		approvalCommentaryAnswer = &snap
	}
	m.pendingInjected = append(m.pendingInjected, clientui.QueuedUserMessage{ID: localID, Text: trimmed, ClientRequestID: clientRequestID})
	m.injectedQueue = append(m.injectedQueue, injectedRuntimeQueueItem{
		LocalID:                  localID,
		Text:                     trimmed,
		ClientRequestID:          clientRequestID,
		State:                    injectedRuntimeQueuePendingCreate,
		CreateToken:              token,
		ApprovalCommentaryAnswer: approvalCommentaryAnswer,
	})
	client := m.runtimeClient()
	return func() tea.Msg {
		item, err := queueRuntimeUserMessage(client, trimmed, clientRequestID)
		return injectedQueueCreateDoneMsg{token: token, localID: localID, item: item, approvalCommentaryAnswer: answer, err: err}
	}
}

type runtimeQueueUserMessageWithClientRequestID interface {
	QueueUserMessageWithClientRequestID(text string, clientRequestID string) (clientui.QueuedUserMessage, error)
}

func queueRuntimeUserMessage(client clientui.RuntimeClient, text string, clientRequestID string) (clientui.QueuedUserMessage, error) {
	if queueClient, ok := client.(runtimeQueueUserMessageWithClientRequestID); ok {
		return queueClient.QueueUserMessageWithClientRequestID(text, clientRequestID)
	}
	return client.QueueUserMessage(text)
}

func (m *uiModel) queueInjectedInput(text string) tea.Cmd {
	cmd := m.enqueueInjectedInputWithApprovalAnswer(text, nil)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	m.clearInput()
	return cmd
}

func (c uiInputController) queueOrStartSubmission(text string) (tea.Model, tea.Cmd) {
	m := c.model
	if m.isInputSubmitLocked() {
		return m, nil
	}
	if blocked, blockCmd := c.blockInjectedQueueSubmission(); blocked {
		return m, blockCmd
	}
	if blocked, disconnectCmd := c.blockDisconnectedSubmission(false, ""); blocked {
		return m, disconnectCmd
	}
	draftText, draftCursor, restoreDraft := m.capturePromptHistoryDraftForReuse()
	m.queueInput(text)
	if c.preservePromptHistoryDraftForQueuedText(text) {
		m.restoreCapturedPromptHistoryDraft(draftText, draftCursor, restoreDraft)
	} else {
		m.resetPromptHistoryNavigation()
	}
	if m.isBusy() {
		return m, nil
	}
	return c.flushQueuedInputs(queueDrainOne)
}

func (c uiInputController) preservePromptHistoryDraftForQueuedText(text string) bool {
	if c.model == nil || c.model.commandRegistry == nil {
		return true
	}
	command, knownCommand := c.model.commandRegistry.Command(text)
	if !knownCommand {
		return true
	}
	return command.PreservePromptHistoryDraft
}

func (c uiInputController) blockInjectedQueueSubmission() (bool, tea.Cmd) {
	m := c.model
	if m == nil || !m.injectedQueueBlocksDrain() {
		return false, nil
	}
	detailErr := "queued runtime message is still pending; retry or discard it before submitting"
	m.activity = uiActivityError
	m.layout().syncViewport()
	return true, m.sendTransientStatusWithNoticeID(detailErr, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
}

func (c uiInputController) blockDisconnectedSubmission(restoreHidden bool, submittedText string) (bool, tea.Cmd) {
	m := c.model
	if !m.runtimeDisconnectStatusVisible() {
		return false, nil
	}
	if restoreHidden {
		restoreCmd := c.restorePendingInjectedIntoInput()
		c.restoreSubmittedTextIntoInput(submittedText)
		c.restoreQueuedMessagesIntoInput()
		m.activity = uiActivityError
		m.layout().syncViewport()
		return true, tea.Batch(restoreCmd, m.appendLocalEntryWithNoticeID(operatorErrorFeedbackRole, runtimeDisconnectedStatusMessage, ""))
	}
	m.activity = uiActivityError
	m.layout().syncViewport()
	return true, m.appendLocalEntryWithNoticeID(operatorErrorFeedbackRole, runtimeDisconnectedStatusMessage, "")
}

func (c uiInputController) restoreQueuedMessagesIntoInput() {
	m := c.model
	if len(m.queued) == 0 {
		return
	}
	joined := strings.Join(queuedInputTexts(m.queued), "\n\n")
	m.queued = nil
	newInput := joined
	if strings.TrimSpace(m.input) == "" {
		newInput = joined
	} else {
		newInput = strings.TrimRight(m.input, "\n") + "\n\n" + joined
	}
	m.replaceMainInput(newInput, -1)
}

func (c uiInputController) restoreSubmittedTextIntoInput(text string) {
	m := c.model
	submitted := strings.TrimSpace(text)
	if submitted == "" {
		return
	}
	newInput := submitted
	if strings.TrimSpace(m.input) != "" {
		newInput = strings.TrimRight(m.input, "\n") + "\n\n" + submitted
	}
	m.replaceMainInput(newInput, -1)
}

func (c uiInputController) restorePendingInjectedIntoInput() tea.Cmd {
	m := c.model
	if len(m.pendingInjected) == 0 {
		return nil
	}
	pending := append([]clientui.QueuedUserMessage(nil), m.pendingInjected...)
	cmds := make([]tea.Cmd, 0, len(pending))
	for _, item := range pending {
		if cmd := m.markInjectedQueueDiscardRequested(item.ID); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	joined := strings.Join(queuedUserMessageTexts(pending), "\n\n")
	m.pendingInjected = nil
	newInput := joined
	if strings.TrimSpace(m.input) == "" {
		newInput = joined
	} else {
		newInput = strings.TrimRight(m.input, "\n") + "\n\n" + joined
	}
	m.replaceMainInput(newInput, -1)
	return tea.Batch(cmds...)
}

func (c uiInputController) releaseLockedInjectedInput(discardEngineQueue bool) tea.Cmd {
	m := c.model
	if !m.isInputSubmitLocked() {
		return nil
	}
	lockedID := strings.TrimSpace(m.lockedInjectID)
	var discardCmd tea.Cmd
	if lockedID != "" {
		filtered := m.pendingInjected[:0]
		for _, pending := range m.pendingInjected {
			if pending.ID == lockedID {
				continue
			}
			filtered = append(filtered, pending)
		}
		m.pendingInjected = filtered
		if discardEngineQueue {
			discardCmd = m.markInjectedQueueDiscardRequested(lockedID)
		}
	}
	m.setInputSubmitLocked(false)
	m.lockedInjectText = ""
	m.lockedInjectID = ""
	return discardCmd
}

func (c uiInputController) flushQueuedInputs(mode queueDrainMode) (tea.Model, tea.Cmd) {
	m := c.model
	if len(m.queued) == 0 {
		return m, nil
	}
	if blocked, disconnectCmd := c.blockDisconnectedSubmission(true, ""); blocked {
		return m, disconnectCmd
	}
	cmds := make([]tea.Cmd, 0, 2)
	for len(m.queued) > 0 {
		next := m.popQueued()
		if cmd := c.dispatchQueuedInput(next); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if mode == queueDrainOne || !m.shouldContinueQueuedInputAutoDrain() {
			break
		}
	}
	return m, tea.Batch(cmds...)
}

func (c uiInputController) resumeQueuedInputsAfterIdleRuntime() tea.Cmd {
	m := c.model
	if m == nil || m.isBusy() || m.ask.hasCurrent() ||
		m.isInputSubmitLocked() ||
		m.pendingQueuedDrainAfterHydration || m.processList.actionInFlight {
		return nil
	}
	hasQueuedInputs := len(m.queued) > 0
	hasInjectedWork := m.hasRuntimeClient() && m.hasEnqueuedInjectedRuntimeWork()
	if !hasQueuedInputs && !hasInjectedWork {
		return nil
	}
	if !hasQueuedInputs {
		return c.startQueuedInjectionSubmission()
	}
	if m.hasRuntimeClient() && c.queuedDrainRequiresHydration() {
		m.pendingQueuedDrainAfterHydration = true
		m.queuedDrainReadyAfterHydration = false
		m.layout().syncViewport()
		return m.requestRuntimeQueuedDrainTranscriptSync()
	}
	_, cmd := c.flushQueuedInputs(queueDrainAuto)
	c.notifyTurnQueueDrainedIfIdle()
	return cmd
}

func (c uiInputController) dispatchQueuedInput(item queuedInputItem) tea.Cmd {
	m := c.model
	text := item.Text
	if m.commandRegistry != nil {
		if _, knownCommand := m.commandRegistry.Command(text); knownCommand {
			if commandResult := m.commandRegistry.Execute(text); commandResult.Handled {
				if commandResult.Action == commands.ActionCompact {
					return finalizeSlashCommandCmd(commandResult.Action, c.startCompactionWithOrigin(commandResult.Args, uiCompactionOriginQueued), m.recordPromptHistory(text))
				}
				_, cmd := c.applyCommandResultWithPreSubmitQueuePosition(commandResult, preSubmitQueueFront)
				return finalizeSlashCommandCmd(commandResult.Action, cmd, m.recordPromptHistory(text))
			}
		}
	}
	return c.startSubmissionWithPromptHistoryAndQueuePositionAndID(item.Text, preSubmitQueueFront, item.ID)
}

func (m *uiModel) shouldContinueQueuedInputAutoDrain() bool {
	if len(m.queued) == 0 || m.isBusy() ||
		m.isInputSubmitLocked() ||
		m.exitAction != UIActionNone || m.ask.hasCurrent() || m.processList.actionInFlight {
		return false
	}
	if m.inputMode() != uiInputModeMain {
		return false
	}
	return strings.TrimSpace(m.input) == ""
}

func (m *uiModel) popQueued() queuedInputItem {
	if len(m.queued) == 0 {
		return queuedInputItem{}
	}
	next := m.queued[0]
	m.queued = m.queued[1:]
	return next
}

func (m *uiModel) discardQueuedInput(id string) bool {
	if strings.TrimSpace(id) == "" {
		return false
	}
	for i := 0; i < len(m.queued); i++ {
		if m.queued[i].ID != id {
			continue
		}
		m.queued = append(m.queued[:i], m.queued[i+1:]...)
		return true
	}
	return false
}

func queuedInputTexts(messages []queuedInputItem) []string {
	texts := make([]string, 0, len(messages))
	for _, message := range messages {
		texts = append(texts, message.Text)
	}
	return texts
}

func queuedUserMessageTexts(messages []clientui.QueuedUserMessage) []string {
	texts := make([]string, 0, len(messages))
	for _, message := range messages {
		texts = append(texts, message.Text)
	}
	return texts
}

func (m *uiModel) nextInjectedQueueToken() uint64 {
	m.injectedQueueToken++
	if m.injectedQueueToken == 0 {
		m.injectedQueueToken++
	}
	return m.injectedQueueToken
}

func (m *uiModel) markInjectedQueueDiscardRequested(id string) tea.Cmd {
	if m == nil || strings.TrimSpace(id) == "" {
		return nil
	}
	index := m.injectedQueueIndexByAnyID(id)
	if index < 0 {
		if !m.hasRuntimeClient() {
			return nil
		}
		return m.discardInjectedRuntimeQueueCommand("", id, m.nextInjectedQueueToken(), m.runtimeClient())
	}
	item := m.injectedQueue[index]
	switch item.State {
	case injectedRuntimeQueuePendingCreate:
		item.State = injectedRuntimeQueueCanceledBeforeCreate
		m.injectedQueue[index] = item
		return nil
	case injectedRuntimeQueueCanceledBeforeCreate, injectedRuntimeQueueDiscardPending:
		return nil
	case injectedRuntimeQueueEnqueued, injectedRuntimeQueueDiscardFailed:
		serverID := strings.TrimSpace(item.ServerID)
		if serverID == "" {
			serverID = strings.TrimSpace(item.LocalID)
		}
		token := m.nextInjectedQueueToken()
		item.State = injectedRuntimeQueueDiscardPending
		item.DiscardToken = token
		m.injectedQueue[index] = item
		return m.discardInjectedRuntimeQueueCommand(item.LocalID, serverID, token, m.runtimeClient())
	default:
		return nil
	}
}

func (m *uiModel) discardInjectedRuntimeQueueCommand(localID, serverID string, token uint64, client clientui.RuntimeClient) tea.Cmd {
	if client == nil || strings.TrimSpace(serverID) == "" {
		return nil
	}
	return func() tea.Msg {
		return injectedQueueDiscardDoneMsg{
			token:     token,
			localID:   localID,
			serverID:  serverID,
			discarded: client.DiscardQueuedUserMessage(serverID),
		}
	}
}

func (m *uiModel) injectedQueueIndexByAnyID(id string) int {
	id = strings.TrimSpace(id)
	if id == "" {
		return -1
	}
	for index, item := range m.injectedQueue {
		if item.LocalID == id || item.ServerID == id {
			return index
		}
	}
	return -1
}

func (m *uiModel) injectedQueueBlocksDrain() bool {
	if m == nil {
		return false
	}
	for _, item := range m.injectedQueue {
		switch item.State {
		case injectedRuntimeQueuePendingCreate, injectedRuntimeQueueCanceledBeforeCreate, injectedRuntimeQueueDiscardPending, injectedRuntimeQueueDiscardFailed:
			return true
		}
	}
	return false
}

func (m *uiModel) hasEnqueuedInjectedRuntimeWork() bool {
	if m == nil {
		return false
	}
	for _, item := range m.injectedQueue {
		if item.State == injectedRuntimeQueueEnqueued {
			return true
		}
	}
	return false
}

func (c uiInputController) handleInjectedQueueCreateDone(msg injectedQueueCreateDoneMsg) (tea.Model, tea.Cmd) {
	m := c.model
	index := m.injectedQueueIndexByAnyID(msg.localID)
	if index < 0 {
		return m, nil
	}
	item := m.injectedQueue[index]
	if item.CreateToken != msg.token {
		return m, nil
	}
	approvalCommentaryAnswer := item.ApprovalCommentaryAnswer
	if approvalCommentaryAnswer == nil {
		approvalCommentaryAnswer = msg.approvalCommentaryAnswer
	}
	m.observeRuntimeRequestResult(msg.err)
	if msg.err != nil {
		m.injectedQueue[index].State = injectedRuntimeQueueCreateFailed
		m.removePendingInjectedByID(item.LocalID)
		if item.State == injectedRuntimeQueuePendingCreate {
			c.restoreInjectedTextIntoInput(item.Text)
			detailErr := runtimeattach.FormatSubmissionError(msg.err)
			m.activity = uiActivityError
			appendCmd := m.appendLocalEntryWithNoticeID(operatorErrorFeedbackRole, detailErr, "")
			m.logf("queue_create.error err=%q", detailErr)
			m.removeInjectedQueueItemAt(index)
			m.layout().syncViewport()
			if approvalCommentaryAnswer != nil {
				return m, sequenceCmds(appendCmd, m.answerQueuedApprovalCommentary(*approvalCommentaryAnswer))
			}
			return m, appendCmd
		}
		m.removeInjectedQueueItemAt(index)
		return m, nil
	}
	serverID := strings.TrimSpace(msg.item.ID)
	if serverID == "" {
		serverID = item.LocalID
	}
	serverText := strings.TrimSpace(msg.item.Text)
	if serverText == "" {
		serverText = item.Text
	}
	item.ServerID = serverID
	item.Text = serverText
	item.ClientRequestID = strings.TrimSpace(msg.item.ClientRequestID)
	item.ApprovalCommentaryAnswer = nil
	switch item.State {
	case injectedRuntimeQueuePendingCreate:
		item.State = injectedRuntimeQueueEnqueued
		m.injectedQueue[index] = item
		m.replacePendingInjectedID(item.LocalID, clientui.QueuedUserMessage{ID: serverID, Text: serverText, ClientRequestID: item.ClientRequestID})
		m.rememberPromptHistoryLocally(serverText)
		if approvalCommentaryAnswer != nil {
			return m, m.answerQueuedApprovalCommentary(*approvalCommentaryAnswer)
		}
		if !m.isBusy() && !m.isInputSubmitLocked() &&
			!m.injectedQueueBlocksDrain() {
			return m, c.startQueuedInjectionSubmission()
		}
	case injectedRuntimeQueueCanceledBeforeCreate:
		token := m.nextInjectedQueueToken()
		item.State = injectedRuntimeQueueDiscardPending
		item.DiscardToken = token
		m.injectedQueue[index] = item
		return m, m.discardInjectedRuntimeQueueCommand(item.LocalID, serverID, token, m.runtimeClient())
	default:
		m.injectedQueue[index] = item
	}
	return m, nil
}

func (c uiInputController) handleInjectedQueueDiscardDone(msg injectedQueueDiscardDoneMsg) (tea.Model, tea.Cmd) {
	m := c.model
	id := strings.TrimSpace(msg.localID)
	if id == "" {
		id = strings.TrimSpace(msg.serverID)
	}
	index := m.injectedQueueIndexByAnyID(id)
	if index < 0 {
		return m, nil
	}
	item := m.injectedQueue[index]
	if item.DiscardToken != msg.token {
		return m, nil
	}
	if msg.discarded {
		m.removePendingInjectedByID(item.LocalID)
		m.removePendingInjectedByID(item.ServerID)
		m.removeInjectedQueueItemAt(index)
		if !m.isBusy() && !m.isInputSubmitLocked() &&
			!m.injectedQueueBlocksDrain() {
			return m, c.startQueuedInjectionSubmission()
		}
		return m, nil
	}
	item.State = injectedRuntimeQueueDiscardFailed
	m.injectedQueue[index] = item
	m.ensurePendingInjectedVisible(item)
	detailErr := "failed to discard queued runtime user message"
	m.activity = uiActivityError
	appendCmd := m.appendLocalEntryWithNoticeID(operatorErrorFeedbackRole, detailErr, "")
	m.logf("queue_discard.error queue_item_id=%q", item.ServerID)
	return m, appendCmd
}

func (c uiInputController) restoreInjectedTextIntoInput(text string) {
	m := c.model
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return
	}
	if strings.TrimSpace(m.input) == "" {
		m.replaceMainInput(trimmed, -1)
		return
	}
	m.replaceMainInput(strings.TrimRight(m.input, "\n")+"\n\n"+trimmed, -1)
}

func (m *uiModel) replacePendingInjectedID(oldID string, next clientui.QueuedUserMessage) {
	oldID = strings.TrimSpace(oldID)
	for index, item := range m.pendingInjected {
		if item.ID != oldID {
			continue
		}
		m.pendingInjected[index] = next
		return
	}
	m.pendingInjected = append(m.pendingInjected, next)
}

func (m *uiModel) removePendingInjectedByID(id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	filtered := m.pendingInjected[:0]
	for _, item := range m.pendingInjected {
		if item.ID == id || item.ClientRequestID == id {
			continue
		}
		filtered = append(filtered, item)
	}
	m.pendingInjected = filtered
}

func (m *uiModel) removeInjectedQueueItemAt(index int) {
	if index < 0 || index >= len(m.injectedQueue) {
		return
	}
	m.injectedQueue = append(m.injectedQueue[:index], m.injectedQueue[index+1:]...)
}

func (m *uiModel) ensurePendingInjectedVisible(item injectedRuntimeQueueItem) {
	id := strings.TrimSpace(item.ServerID)
	if id == "" {
		id = strings.TrimSpace(item.LocalID)
	}
	if id == "" {
		return
	}
	for _, pending := range m.pendingInjected {
		if pending.ID == id || pending.ClientRequestID == id {
			return
		}
	}
	m.pendingInjected = append(m.pendingInjected, clientui.QueuedUserMessage{ID: id, Text: item.Text, ClientRequestID: item.ClientRequestID})
}

func (m *uiModel) removeInjectedQueueItemsByIDs(ids []string) []clientui.PromptAnswer {
	if len(ids) == 0 || len(m.injectedQueue) == 0 {
		return nil
	}
	var approvalAnswers []clientui.PromptAnswer
	filtered := m.injectedQueue[:0]
	for _, item := range m.injectedQueue {
		if containsInjectedQueueID(ids, item.ServerID) || containsInjectedQueueID(ids, item.LocalID) || containsInjectedQueueID(ids, item.ClientRequestID) {
			if item.ApprovalCommentaryAnswer != nil {
				approvalAnswers = append(approvalAnswers, *item.ApprovalCommentaryAnswer)
			}
			continue
		}
		filtered = append(filtered, item)
	}
	m.injectedQueue = filtered
	return approvalAnswers
}

func containsInjectedQueueID(values []string, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}
