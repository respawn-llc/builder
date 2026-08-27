package app

import (
	"context"
	"errors"
	"strings"

	"core/cli/app/internal/runtimeattach"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

type runtimeInterruptCandidateClient interface {
	interruptRuntimeCandidate() (runtimeTupleCandidate, error)
}

type chatSettingsRuntimeClient interface {
	ReadChatSettings() (serverapi.ChatSettings, error)
	MutateChatSettings(serverapi.ChatSettingsMutationOperation) (chatSettingsMutationResult, error)
}

func (m *uiModel) runtimeClient() clientui.RuntimeClient {
	if m == nil {
		return nil
	}
	return m.engine
}

func (m *uiModel) hasRuntimeClient() bool {
	return m.runtimeClient() != nil
}

func (m *uiModel) chatSettingsMutationCommand(operation serverapi.ChatSettingsMutationOperation) tea.Cmd {
	if m == nil {
		return nil
	}
	client := m.runtimeClient().(chatSettingsRuntimeClient)
	return func() tea.Msg {
		result, err := client.MutateChatSettings(operation)
		return chatSettingsDoneMsg{
			operation:  operation.Kind,
			response:   result.response,
			projection: result.projection,
			err:        err,
		}
	}
}

func (m *uiModel) chatSettingsToggleCommand(
	kind serverapi.ChatSettingsMutationOperationKind,
	requested string,
) tea.Cmd {
	if m == nil {
		return nil
	}
	client := m.runtimeClient().(chatSettingsRuntimeClient)
	return func() tea.Msg {
		settings, err := client.ReadChatSettings()
		if err != nil {
			return chatSettingsDoneMsg{operation: kind, err: err}
		}
		operation, err := resolveChatSettingsToggle(kind, requested, settings)
		if err != nil {
			return chatSettingsDoneMsg{operation: kind, err: err}
		}
		result, err := client.MutateChatSettings(operation)
		return chatSettingsDoneMsg{
			operation:  kind,
			response:   result.response,
			projection: result.projection,
			err:        err,
		}
	}
}

func resolveChatSettingsToggle(
	kind serverapi.ChatSettingsMutationOperationKind,
	requested string,
	settings serverapi.ChatSettings,
) (serverapi.ChatSettingsMutationOperation, error) {
	requested = strings.ToLower(strings.TrimSpace(requested))
	switch kind {
	case serverapi.ChatSettingsMutationSupervisor:
		switch requested {
		case "on":
			value := settings.Supervisor.Baseline
			if value == serverapi.ChatSettingsSupervisorOff {
				value = serverapi.ChatSettingsSupervisorAfterEdits
			}
			encoded := string(value)
			return serverapi.ChatSettingsMutationOperation{
				Kind:  kind,
				Value: &encoded,
			}, nil
		case "off":
			value := string(serverapi.ChatSettingsSupervisorOff)
			return serverapi.ChatSettingsMutationOperation{Kind: kind, Value: &value}, nil
		case "":
			value := string(serverapi.ChatSettingsSupervisorOff)
			if settings.Supervisor.Value == serverapi.ChatSettingsSupervisorOff {
				value = string(settings.Supervisor.Baseline)
				if value == string(serverapi.ChatSettingsSupervisorOff) {
					value = string(serverapi.ChatSettingsSupervisorAfterEdits)
				}
			}
			return serverapi.ChatSettingsMutationOperation{Kind: kind, Value: &value}, nil
		}
	case serverapi.ChatSettingsMutationFast:
		value := settings.Fast != nil && settings.Fast.Value
		return enabledChatSettingsOperation(kind, requested, value)
	case serverapi.ChatSettingsMutationQuestions:
		return enabledChatSettingsOperation(kind, requested, settings.Questions.Enabled)
	case serverapi.ChatSettingsMutationAutoCompaction:
		return enabledChatSettingsOperation(kind, requested, settings.AutoCompaction.Stored)
	default:
		return serverapi.ChatSettingsMutationOperation{}, errors.New("unsupported Chat settings toggle")
	}
	return serverapi.ChatSettingsMutationOperation{}, errors.New("invalid Chat settings toggle")
}

func enabledChatSettingsOperation(
	kind serverapi.ChatSettingsMutationOperationKind,
	requested string,
	current bool,
) (serverapi.ChatSettingsMutationOperation, error) {
	target := current
	switch requested {
	case "":
		target = !current
	case "on":
		target = true
	case "off":
		target = false
	default:
		return serverapi.ChatSettingsMutationOperation{}, errors.New("invalid Chat settings toggle")
	}
	return serverapi.ChatSettingsMutationOperation{
		Kind:    kind,
		Enabled: &target,
	}, nil
}

func (m *uiModel) applyChatSettingsDone(msg chatSettingsDoneMsg) tea.Cmd {
	if m == nil {
		return nil
	}
	if msg.err != nil {
		errText := runtimeattach.FormatSubmissionError(msg.err)
		return m.sendTransientStatusWithNoticeID(errText, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
	}
	response := msg.response
	msg.projection.applyToUIModel(m)
	if response.Result.Kind != serverapi.ChatSettingsMutationApplied {
		reason := "Chat settings mutation rejected"
		if response.Result.Rejected != nil {
			reason = chatSettingsRejectionNotice(response.Result.Rejected.Reason)
		}
		return m.sendTransientStatusWithNoticeID(reason, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
	}
	if response.Result.Applied == nil || !response.Result.Applied.Changed {
		return nil
	}
	name, value := chatSettingsResultValue(msg.operation, response.Settings)
	return m.sendTransientStatusWithNoticeID(
		name+": "+value,
		uiStatusNoticeSuccess,
		transientStatusDuration,
		uiStatusNoticeReplace,
		"",
	)
}

func chatSettingsResultValue(
	operation serverapi.ChatSettingsMutationOperationKind,
	settings serverapi.ChatSettings,
) (string, string) {
	switch operation {
	case serverapi.ChatSettingsMutationAgent:
		return "Agent", settings.SelectedAgent.Role
	case serverapi.ChatSettingsMutationSupervisor:
		return "Supervisor", chatSettingsSupervisorNotice(settings.Supervisor.Value)
	case serverapi.ChatSettingsMutationThinking:
		return "Thinking", settings.SelectedAgent.Thinking
	case serverapi.ChatSettingsMutationFast:
		if settings.Fast != nil && settings.Fast.Value {
			return "Fast", "on"
		}
		return "Fast", "off"
	case serverapi.ChatSettingsMutationQuestions:
		if settings.Questions.Enabled {
			return "Questions", "on"
		}
		return "Questions", "off"
	case serverapi.ChatSettingsMutationAutoCompaction:
		if settings.AutoCompaction.Stored {
			return "Auto-compaction", "on"
		}
		return "Auto-compaction", "off"
	default:
		return "Chat settings", "updated"
	}
}

func chatSettingsRejectionNotice(reason serverapi.ChatSettingsMutationRejectionReason) string {
	switch reason {
	case serverapi.ChatSettingsMutationAgentLocked:
		return "Agent is locked"
	case serverapi.ChatSettingsMutationAgentUnavailable:
		return "Agent is unavailable"
	case serverapi.ChatSettingsMutationThinkingUnavailable:
		return "Thinking is unavailable"
	case serverapi.ChatSettingsMutationFastUnavailable:
		return "Fast mode is unavailable"
	case serverapi.ChatSettingsMutationAutoCompactionPolicyLock:
		return "Auto-compaction is unavailable"
	default:
		return "Chat settings mutation rejected"
	}
}

func chatSettingsSupervisorNotice(value serverapi.ChatSettingsSupervisorValue) string {
	switch value {
	case serverapi.ChatSettingsSupervisorOff:
		return "Off"
	case serverapi.ChatSettingsSupervisorAfterEdits:
		return "After edits"
	case serverapi.ChatSettingsSupervisorAlways:
		return "Always"
	default:
		return "Unknown"
	}
}

func (m *uiModel) setRuntimeSessionName(name string) error {
	m.checkTUIBlockingOperation("runtime control mutation", "set session name")
	if client := m.runtimeClient(); client != nil {
		err := client.SetSessionName(name)
		m.observeRuntimeRequestResult(err)
		return err
	}
	return nil
}

func (m *uiModel) showRuntimeGoal() (*clientui.RuntimeGoal, error) {
	m.checkTUIBlockingOperation("runtime control read", "show goal")
	if client := m.runtimeClient(); client != nil {
		goal, err := client.ShowGoal()
		m.observeRuntimeRequestResult(err)
		return goal, err
	}
	return nil, nil
}

func (m *uiModel) setRuntimeGoal(objective string) (clientui.GoalMutationResult, error) {
	m.checkTUIBlockingOperation("runtime control mutation", "set goal")
	if client := m.runtimeClient(); client != nil {
		result, err := client.SetGoal(objective)
		m.observeRuntimeRequestResult(err)
		return result, err
	}
	return clientui.GoalMutationResult{}, nil
}

func (m *uiModel) pauseRuntimeGoal() (clientui.GoalMutationResult, error) {
	m.checkTUIBlockingOperation("runtime control mutation", "pause goal")
	if client := m.runtimeClient(); client != nil {
		result, err := client.PauseGoal()
		m.observeRuntimeRequestResult(err)
		return result, err
	}
	return clientui.GoalMutationResult{}, nil
}

func (m *uiModel) resumeRuntimeGoal() (clientui.GoalMutationResult, error) {
	m.checkTUIBlockingOperation("runtime control mutation", "resume goal")
	if client := m.runtimeClient(); client != nil {
		result, err := client.ResumeGoal()
		m.observeRuntimeRequestResult(err)
		return result, err
	}
	return clientui.GoalMutationResult{}, nil
}

func (m *uiModel) clearRuntimeGoal() (clientui.GoalMutationResult, error) {
	m.checkTUIBlockingOperation("runtime control mutation", "clear goal")
	if client := m.runtimeClient(); client != nil {
		result, err := client.ClearGoal()
		m.observeRuntimeRequestResult(err)
		return result, err
	}
	return clientui.GoalMutationResult{}, nil
}

func (m *uiModel) submitRuntimeUserMessage(ctx context.Context, text string) (clientui.UserTurnSubmission, error) {
	return m.submitRuntimeInput(ctx, clientui.RuntimeSubmitRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID(),
		Input:           runtimeinput.Text(text),
	})
}

func (m *uiModel) submitRuntimeInput(ctx context.Context, req clientui.RuntimeSubmitRequest) (clientui.UserTurnSubmission, error) {
	if client := m.runtimeClient(); client != nil {
		submission, err := client.SubmitRuntimeInput(ctx, req)
		m.observeRuntimeRequestResult(err)
		return submission, err
	}
	return clientui.UserTurnSubmission{}, nil
}

func (m *uiModel) submitRuntimeUserShellCommand(ctx context.Context, command string) error {
	return m.submitRuntimeShell(ctx, clientui.RuntimeShellRequest{Command: command})
}

func (m *uiModel) submitRuntimeShell(ctx context.Context, req clientui.RuntimeShellRequest) error {
	if client := m.runtimeClient(); client != nil {
		err := client.RunUserShell(ctx, req)
		m.observeRuntimeRequestResult(err)
		return err
	}
	return nil
}

func (m *uiModel) interruptRuntime() error {
	m.checkTUIBlockingOperation("runtime control mutation", "interrupt")
	candidate, err := executeRuntimeInterrupt(runtimeInterruptRequestFromModel(m))
	if err == nil && candidate != nil {
		if client, ok := m.runtimeClient().(*sessionRuntimeClient); ok {
			client.mergeRuntimeTuple(*candidate, runtimeTupleIngressIncremental)
		}
	}
	m.observeRuntimeRequestResult(err)
	return err
}

type runtimeInterruptRequest struct {
	client clientui.RuntimeClient
}

func runtimeInterruptRequestFromModel(m *uiModel) runtimeInterruptRequest {
	if m == nil {
		return runtimeInterruptRequest{}
	}
	return runtimeInterruptRequest{client: m.runtimeClient()}
}

func executeRuntimeInterrupt(req runtimeInterruptRequest) (*runtimeTupleCandidate, error) {
	if req.client == nil {
		return nil, nil
	}
	if candidateClient, ok := req.client.(runtimeInterruptCandidateClient); ok {
		candidate, err := candidateClient.interruptRuntimeCandidate()
		if err != nil {
			return nil, err
		}
		return &candidate, nil
	}
	return nil, req.client.Interrupt()
}

func (m *uiModel) discardQueuedRuntimeUserMessage(queueItemID string) bool {
	m.checkTUIBlockingOperation("runtime queue mutation", "discard queued user message")
	if client := m.runtimeClient(); client != nil {
		return client.DiscardQueuedUserMessage(queueItemID)
	}
	return false
}

func (m *uiModel) recordRuntimePromptHistory(text string) error {
	m.checkTUIBlockingOperation("runtime control mutation", "record prompt history")
	if client := m.runtimeClient(); client != nil {
		err := client.RecordPromptHistory(text)
		m.observeRuntimeRequestResult(err)
		return err
	}
	return nil
}

type runtimeControlPendingState struct {
	sessionID    string
	inFlight     bool
	inFlightText string
	desiredText  string
}

func (m *uiModel) nextRuntimeControlToken(operation runtimeControlOperation) uint64 {
	m.runtimeControlToken++
	if m.runtimeControlToken == 0 {
		m.runtimeControlToken++
	}
	if m.runtimeControlTokens == nil {
		m.runtimeControlTokens = make(map[runtimeControlOperation]uint64)
	}
	m.runtimeControlTokens[operation] = m.runtimeControlToken
	return m.runtimeControlToken
}

func (m *uiModel) runtimeControlTokenFor(operation runtimeControlOperation) uint64 {
	if m == nil || m.runtimeControlTokens == nil {
		return 0
	}
	return m.runtimeControlTokens[operation]
}

func (m *uiModel) beginRuntimeControlMutation(operation runtimeControlOperation, sessionID, text string, enabled bool, compactionMode string) (uint64, bool) {
	if m == nil {
		return 0, false
	}
	sessionID = strings.TrimSpace(sessionID)
	text = strings.TrimSpace(text)
	if operation != runtimeControlSetSessionName {
		return m.nextRuntimeControlToken(operation), true
	}
	if m.runtimeControlPending == nil {
		m.runtimeControlPending = make(map[runtimeControlOperation]runtimeControlPendingState)
	}
	if pending, ok := m.runtimeControlPending[operation]; ok && pending.inFlight && pending.sessionID == sessionID {
		pending.desiredText = text
		m.runtimeControlPending[operation] = pending
		return 0, false
	}
	token := m.nextRuntimeControlToken(operation)
	m.runtimeControlPending[operation] = runtimeControlPendingState{
		sessionID:    sessionID,
		inFlight:     true,
		inFlightText: text,
		desiredText:  text,
	}
	return token, true
}

func (m *uiModel) clearRuntimeControlPending(operation runtimeControlOperation) {
	if m == nil || m.runtimeControlPending == nil {
		return
	}
	delete(m.runtimeControlPending, operation)
}

func runtimeControlOperationUsesTextTarget(operation runtimeControlOperation) bool {
	switch operation {
	case runtimeControlSetSessionName:
		return true
	default:
		return false
	}
}

func (m *uiModel) runtimeControlCommand(operation runtimeControlOperation, text string, enabled bool, compactionMode string) tea.Cmd {
	if m == nil {
		return nil
	}
	client := m.runtimeClient()
	if client == nil {
		return nil
	}
	interruptReq := runtimeInterruptRequest{}
	if operation == runtimeControlInterrupt {
		interruptReq = runtimeInterruptRequestFromModel(m)
	}
	sessionID := strings.TrimSpace(m.sessionID)
	text = strings.TrimSpace(text)
	token, shouldStart := m.beginRuntimeControlMutation(operation, sessionID, text, enabled, compactionMode)
	if !shouldStart {
		return nil
	}
	return func() tea.Msg {
		msg := runtimeControlDoneMsg{token: token, sessionID: sessionID, operation: operation, text: text}
		switch operation {
		case runtimeControlSetSessionName:
			msg.err = client.SetSessionName(text)
		case runtimeControlInterrupt:
			msg.runtimeTuple, msg.err = executeRuntimeInterrupt(interruptReq)
		}
		return msg
	}
}

func (m *uiModel) applyRuntimeControlDone(msg runtimeControlDoneMsg) tea.Cmd {
	if m == nil || msg.token != m.runtimeControlTokenFor(msg.operation) {
		return nil
	}
	if msg.sessionID != "" && strings.TrimSpace(m.sessionID) != "" && msg.sessionID != strings.TrimSpace(m.sessionID) {
		m.clearRuntimeControlPending(msg.operation)
		return nil
	}
	m.observeRuntimeRequestResult(msg.err)
	if msg.err != nil {
		m.clearRuntimeControlPending(msg.operation)
		if msg.operation == runtimeControlInterrupt {
			m.setPendingInterrupt(false)
		}
		errText := runtimeattach.FormatSubmissionError(msg.err)
		return sequenceCmds(
			m.appendLocalEntryWithNoticeID("error", errText, ""),
			m.sendTransientStatusWithNoticeID(errText, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""),
		)
	}
	var followUpCmd tea.Cmd
	if runtimeControlOperationUsesTextTarget(msg.operation) {
		pending := m.runtimeControlPending[msg.operation]
		if pending.inFlight && pending.desiredText != pending.inFlightText {
			pending.inFlight = false
			m.runtimeControlPending[msg.operation] = pending
			followUpCmd = m.runtimeControlCommand(msg.operation, pending.desiredText, false, "")
		} else {
			m.clearRuntimeControlPending(msg.operation)
		}
	}
	switch msg.operation {
	case runtimeControlSetSessionName:
		m.sessionName = strings.TrimSpace(msg.text)
		return sequenceCmds(tea.SetWindowTitle(sessionTitle(m.sessionName)), followUpCmd)
	case runtimeControlInterrupt:
		var merge runtimeTupleMergeResult
		if msg.runtimeTuple != nil {
			if client, ok := m.runtimeClient().(*sessionRuntimeClient); ok {
				merge = client.mergeRuntimeTuple(*msg.runtimeTuple, runtimeTupleIngressIncremental)
			}
		}
		if merge.decision == runtimeTupleRefresh {
			decision := m.startRuntimeMainViewRefreshRequest(runtimeReadModelResetMainViewRefreshRequest())
			return tea.Batch(followUpCmd, decision.cmd)
		}
		if view := m.cachedRuntimeMainView(); view.Activity.State != "" && !view.Activity.ActiveForControl() && m.hasPendingInterrupt() {
			if err := m.applyRuntimeActivityProjection(view.Activity); err != nil {
				m.activity = uiActivityError
				return tea.Batch(followUpCmd, m.sendTransientStatusWithNoticeID("invalid runtime activity: "+err.Error(), uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""))
			}
			return tea.Batch(followUpCmd, m.acknowledgePendingInterrupt())
		}
		return followUpCmd
	default:
		return followUpCmd
	}
}
