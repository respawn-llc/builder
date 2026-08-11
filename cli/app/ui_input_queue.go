package app

import (
	"context"
	"sort"
	"strings"

	"core/cli/app/commands"
	"core/cli/app/internal/runtimeattach"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
)

type queueDrainMode uint8

const (
	queueDrainOne queueDrainMode = iota
	queueDrainAuto
)

type queuedInputItem struct {
	ID              string
	Text            string
	submissionOrder inputSubmissionOrder
}

type inputSubmissionOrder struct {
	sequence uint64
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
	submissionOrder          inputSubmissionOrder
}

func (m *uiModel) queueInput(text string) {
	m.queued = append(m.queued, queuedInputItem{
		ID:              uuid.NewString(),
		Text:            text,
		submissionOrder: m.nextPendingInputSubmissionOrder(),
	})
	m.clearInput()
}

func (m *uiModel) nextPendingInputSubmissionOrder() inputSubmissionOrder {
	m.pendingInputSubmissionOrder++
	if m.pendingInputSubmissionOrder == 0 {
		panic("pending input submission order overflow")
	}
	return inputSubmissionOrder{sequence: m.pendingInputSubmissionOrder}
}

func (m *uiModel) registerSteeredQueuedUserMessage(queued clientui.QueuedUserMessage) bool {
	serverID := strings.TrimSpace(queued.ID)
	if serverID == "" {
		return false
	}
	index := m.injectedQueueIndexByAnyID(serverID)
	if index < 0 {
		index = m.injectedQueueIndexByAnyID(queued.ClientRequestID)
	}
	if index < 0 && m.activeSubmit.clientRequestID.String() == strings.TrimSpace(queued.ClientRequestID) {
		m.injectedQueue = append(m.injectedQueue, injectedRuntimeQueueItem{
			LocalID:         queued.ClientRequestID,
			ServerID:        serverID,
			Text:            m.activeSubmit.text,
			ClientRequestID: queued.ClientRequestID,
			State:           injectedRuntimeQueueEnqueued,
			submissionOrder: m.activeSubmit.submissionOrder,
		})
		return true
	}
	if index < 0 {
		return false
	}
	item := m.injectedQueue[index]
	item.ServerID = serverID
	item.State = injectedRuntimeQueueEnqueued
	m.injectedQueue[index] = item
	return true
}

func (m *uiModel) enqueueInjectedInputWithApprovalAnswer(text string, answer *clientui.PromptAnswer) tea.Cmd {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	localID := uuid.NewString()
	submissionOrder := m.nextPendingInputSubmissionOrder()
	if !m.hasRuntimeClient() {
		m.injectedQueue = append(m.injectedQueue, injectedRuntimeQueueItem{
			LocalID:         localID,
			ServerID:        localID,
			Text:            text,
			State:           injectedRuntimeQueueEnqueued,
			submissionOrder: submissionOrder,
		})
		return nil
	}
	token := m.nextInjectedQueueToken()
	clientRequestID := runtimeids.NewRuntimeClientRequestID()
	var approvalCommentaryAnswer *clientui.PromptAnswer
	if answer != nil {
		snap := *answer
		approvalCommentaryAnswer = &snap
	}
	m.injectedQueue = append(m.injectedQueue, injectedRuntimeQueueItem{
		LocalID:                  localID,
		Text:                     text,
		ClientRequestID:          clientRequestID.String(),
		State:                    injectedRuntimeQueuePendingCreate,
		CreateToken:              token,
		ApprovalCommentaryAnswer: approvalCommentaryAnswer,
		submissionOrder:          submissionOrder,
	})
	client := m.runtimeClient()
	return func() tea.Msg {
		item, completed, err := submitRuntimeSteering(client, text, clientRequestID)
		return injectedQueueCreateDoneMsg{
			token:                    token,
			localID:                  localID,
			item:                     item,
			completed:                completed,
			approvalCommentaryAnswer: answer,
			err:                      err,
		}
	}
}

func submitRuntimeSteering(client clientui.RuntimeClient, text string, clientRequestID runtimeids.RuntimeClientRequestID) (clientui.QueuedUserMessage, bool, error) {
	submission, err := client.SubmitRuntimeInput(context.Background(), clientui.RuntimeSubmitRequest{
		ClientRequestID: clientRequestID,
		Input:           runtimeinput.Text(text),
	})
	if err != nil {
		return clientui.QueuedUserMessage{}, false, err
	}
	if strings.TrimSpace(submission.Queued.ID) == "" {
		return clientui.QueuedUserMessage{}, true, nil
	}
	return submission.Queued, false, nil
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
	if blocked, blockCmd := c.blockInjectedQueueSubmission(); blocked {
		return m, blockCmd
	}
	if blocked, disconnectCmd := c.blockDisconnectedSubmission(false, ""); blocked {
		return m, disconnectCmd
	}
	draft := m.capturePromptHistoryDraftForReuse()
	m.queueInput(text)
	if c.preservePromptHistoryDraftForQueuedText(text) {
		m.restoreCapturedPromptHistoryDraft(draft)
	} else {
		m.resetPromptHistoryNavigation()
	}
	if m.blocksRuntimeInput() {
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
		restoreCmd := c.restoreInjectedInputsIntoComposer()
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

func (c uiInputController) blockDisconnectedQueuedSubmission() (bool, tea.Cmd) {
	m := c.model
	if !m.runtimeDisconnectStatusVisible() {
		return false, nil
	}
	c.restoreQueuedMessagesIntoInput()
	m.layout().syncViewport()
	statusCmd := m.sendTransientStatusWithNoticeID(runtimeDisconnectedStatusMessage, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
	return true, statusCmd
}

func (c uiInputController) restoreQueuedMessagesIntoInput() {
	m := c.model
	if len(m.queued) == 0 {
		return
	}
	joined := strings.Join(queuedInputTexts(m.queued), "\n\n")
	m.queued = nil
	c.appendRestoredInputToComposer(joined)
}

func (c uiInputController) restoreSubmittedTextIntoInput(text string) {
	c.appendRestoredInputToComposer(text)
}

func (c uiInputController) appendRestoredInputToComposer(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	m := c.model
	current := m.mainEditor.Text()
	if strings.TrimSpace(current) != "" {
		text = strings.TrimRight(current, "\n") + "\n\n" + text
	}
	m.replaceMainInputAtEnd(text)
}

func (c uiInputController) restoreInjectedInputsIntoComposer() tea.Cmd {
	m := c.model
	pending := m.restorableInjectedQueueItems()
	if len(pending) == 0 {
		return nil
	}
	cmds := make([]tea.Cmd, 0, len(pending))
	for _, item := range pending {
		if cmd := m.markInjectedQueueDiscardRequested(item.LocalID); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	texts := make([]string, 0, len(pending))
	for _, item := range pending {
		texts = append(texts, item.Text)
	}
	c.appendRestoredInputToComposer(strings.Join(texts, "\n\n"))
	return tea.Batch(cmds...)
}

func (m *uiModel) restorableInjectedQueueItems() []injectedRuntimeQueueItem {
	if m == nil {
		return nil
	}
	items := make([]injectedRuntimeQueueItem, 0, len(m.injectedQueue))
	for _, item := range m.injectedQueue {
		switch item.State {
		case injectedRuntimeQueuePendingCreate, injectedRuntimeQueueEnqueued:
			items = append(items, item)
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].submissionOrder.sequence < items[j].submissionOrder.sequence
	})
	return items
}

type interruptedInputDraftPart struct {
	submissionOrder inputSubmissionOrder
	text            string
}

func (c uiInputController) restoreInterruptedInputsIntoComposer() tea.Cmd {
	m := c.model
	if m == nil {
		return nil
	}
	draft := m.mainEditor.Text()
	parts := make([]interruptedInputDraftPart, 0, len(m.injectedQueue)+len(m.queued))
	var approvalCmd tea.Cmd
	for _, item := range m.restorableInjectedQueueItems() {
		parts = append(parts, interruptedInputDraftPart{submissionOrder: item.submissionOrder, text: item.Text})
		if item.ApprovalCommentaryAnswer != nil {
			approvalCmd = tea.Batch(approvalCmd, m.answerQueuedApprovalCommentary(*item.ApprovalCommentaryAnswer))
		}
	}
	for _, queued := range m.queued {
		parts = append(parts, interruptedInputDraftPart{submissionOrder: queued.submissionOrder, text: queued.Text})
	}
	sort.SliceStable(parts, func(i, j int) bool {
		return parts[i].submissionOrder.sequence < parts[j].submissionOrder.sequence
	})
	texts := make([]string, 0, len(parts)+1)
	for _, part := range parts {
		texts = append(texts, part.text)
	}
	if draft != "" {
		texts = append(texts, draft)
	}
	m.injectedQueue = nil
	m.queued = nil
	if len(texts) == 0 {
		return approvalCmd
	}
	m.replaceMainInputAtEnd(strings.Join(texts, "\n\n"))
	m.logf("interrupt.restore pending_inputs=%d draft=%t", len(parts), draft != "")
	return approvalCmd
}

func (c uiInputController) flushQueuedInputs(mode queueDrainMode) (tea.Model, tea.Cmd) {
	m := c.model
	if len(m.queued) == 0 {
		return m, nil
	}
	if blocked, disconnectCmd := c.blockDisconnectedQueuedSubmission(); blocked {
		return m, disconnectCmd
	}
	if m.injectedQueueBlocksDrain() || m.hasEnqueuedInjectedRuntimeWork() {
		return m, nil
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
	if m == nil || m.blocksRuntimeInput() || m.ask.hasCurrent() ||
		m.processList.actionInFlight {
		return nil
	}
	if len(m.queued) == 0 {
		return nil
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
				_, cmd := c.applyCommandResultWithPreSubmitQueuePositionAndOriginAndOrder(
					commandResult,
					preSubmitQueueFront,
					activeSubmitOriginQueued,
					&item.submissionOrder,
				)
				var recordCmd tea.Cmd
				if commandResult.PromptCommand == nil {
					recordCmd = m.recordPromptHistory(text)
				}
				return finalizeSlashCommandCmd(commandResult.Action, cmd, recordCmd)
			}
		}
	}
	if cmd, rejected := c.rejectUnavailablePromptCommand(text); rejected {
		return cmd
	}
	m.rememberPromptHistoryLocally(item.Text)
	return c.startSubmissionWithPreSubmitQueuePositionAndOriginAndOrder(
		item.Text,
		preSubmitQueueFront,
		item.ID,
		activeSubmitOriginQueued,
		&item.submissionOrder,
	)
}

func (m *uiModel) shouldContinueQueuedInputAutoDrain() bool {
	if len(m.queued) == 0 || m.blocksRuntimeInput() ||
		m.exitAction != UIActionNone || m.ask.hasCurrent() || m.processList.actionInFlight {
		return false
	}
	if m.inputMode() != uiInputModeMain {
		return false
	}
	return strings.TrimSpace(m.mainEditor.Text()) == ""
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
		if item.LocalID == id || item.ServerID == id || item.ClientRequestID == id {
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
		if item.State == injectedRuntimeQueuePendingCreate {
			c.restoreInjectedTextIntoInput(item.Text)
			detailErr := runtimeattach.FormatSubmissionError(msg.err)
			statusCmd := m.sendTransientStatusWithNoticeID(detailErr, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
			m.logf("queue_create.error err=%q", detailErr)
			m.removeInjectedQueueItemAt(index)
			m.layout().syncViewport()
			if approvalCommentaryAnswer != nil {
				return m, batchCmds(statusCmd, m.answerQueuedApprovalCommentary(*approvalCommentaryAnswer))
			}
			return m, statusCmd
		}
		m.removeInjectedQueueItemAt(index)
		return m, nil
	}
	if msg.completed {
		m.removeInjectedQueueItemAt(index)
		if item.State != injectedRuntimeQueuePendingCreate {
			return m, nil
		}
		m.rememberPromptHistoryLocally(item.Text)
		if approvalCommentaryAnswer != nil {
			return m, m.answerQueuedApprovalCommentary(*approvalCommentaryAnswer)
		}
		return m, nil
	}
	serverID := strings.TrimSpace(msg.item.ID)
	if serverID == "" {
		serverID = item.LocalID
	}
	item.ServerID = serverID
	item.ApprovalCommentaryAnswer = nil
	switch item.State {
	case injectedRuntimeQueuePendingCreate:
		item.State = injectedRuntimeQueueEnqueued
		m.injectedQueue[index] = item
		m.rememberPromptHistoryLocally(item.Text)
		if approvalCommentaryAnswer != nil {
			return m, m.answerQueuedApprovalCommentary(*approvalCommentaryAnswer)
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
		m.removeInjectedQueueItemAt(index)
		return m, c.resumeQueuedInputsAfterIdleRuntime()
	}
	item.State = injectedRuntimeQueueDiscardFailed
	m.injectedQueue[index] = item
	detailErr := "failed to discard queued runtime user message"
	m.activity = uiActivityError
	appendCmd := m.appendLocalEntryWithNoticeID(operatorErrorFeedbackRole, detailErr, "")
	m.logf("queue_discard.error queue_item_id=%q", item.ServerID)
	return m, appendCmd
}

func (c uiInputController) restoreInjectedTextIntoInput(text string) {
	c.appendRestoredInputToComposer(text)
}

func (m *uiModel) removeInjectedQueueItemAt(index int) {
	if index < 0 || index >= len(m.injectedQueue) {
		return
	}
	m.injectedQueue = append(m.injectedQueue[:index], m.injectedQueue[index+1:]...)
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
