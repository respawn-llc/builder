package app

import (
	"context"
	"errors"
	"fmt"

	"core/shared/protocol"
	"core/shared/runtimeids"

	tea "github.com/charmbracelet/bubbletea"
)

type sessionPickerOpenInspection struct {
	workspaceChange *sessionPickerWorkspaceChange
}

type sessionPickerWorkspaceChange struct {
	selectedRoot string
	currentRoot  string
}

type sessionPickerOpenController interface {
	Inspect(context.Context, runtimeids.SessionID) (sessionPickerOpenInspection, error)
	Retarget(context.Context, runtimeids.SessionID, string) error
	Plan(context.Context, runtimeids.SessionID) (sessionLaunchPlan, error)
}

type sessionPickerPendingOpen struct {
	sessionID  runtimeids.SessionID
	generation uint64
}

type sessionPickerOpenInspectedMsg struct {
	sessionID  runtimeids.SessionID
	generation uint64
	inspection sessionPickerOpenInspection
	err        error
}

type sessionPickerOpenPlannedMsg struct {
	sessionID  runtimeids.SessionID
	generation uint64
	plan       sessionLaunchPlan
	err        error
}

type sessionPickerOpenRetargetedMsg struct {
	sessionID  runtimeids.SessionID
	generation uint64
	err        error
}

type sessionPickerOpenFailure struct {
	sessionID  runtimeids.SessionID
	kind       sessionPickerOpenFailureKind
	diagnostic error
}

type sessionPickerOpenFailureKind uint8

const (
	sessionPickerOpenFailureGeneric sessionPickerOpenFailureKind = iota + 1
	sessionPickerOpenFailureUpgradeRequired
	sessionPickerOpenFailureInsufficientSpace
	sessionPickerOpenFailureStructural
	sessionPickerOpenFailureReconciliationPending
)

func (m *sessionPickerModel) cancelPendingOpen() tea.Cmd {
	m.openSequence++
	m.pendingOpen = nil
	m.workspacePrompt = nil
	m.openFailure = nil
	if m.startupStatus != nil {
		m.startupStatus.notice = startupPickerNotice{}
	}
	m.result = newSessionPickerCancelResult()
	return tea.Quit
}

func (m *sessionPickerModel) startOpenSession(
	sessionID runtimeids.SessionID,
) tea.Cmd {
	if m.openController == nil {
		return nil
	}
	m.openSequence++
	generation := m.openSequence
	m.pendingOpen = &sessionPickerPendingOpen{
		sessionID:  sessionID,
		generation: generation,
	}
	m.openFailure = nil
	if m.startupStatus == nil {
		m.startupStatus = newStartupPickerStatusModel()
	}
	m.startupStatus.notice = startupPickerNotice{
		Text: "Opening session…",
		Kind: startupPickerNoticeNeutral,
	}
	controller := m.openController
	requestContext := m.requestContext
	openCommand := func() tea.Msg {
		inspection, err := controller.Inspect(requestContext, sessionID)
		return sessionPickerOpenInspectedMsg{
			sessionID:  sessionID,
			generation: generation,
			inspection: inspection,
			err:        err,
		}
	}
	return tea.Batch(openCommand, m.reconcileSpinnerTick())
}

func (m *sessionPickerModel) applyOpenInspection(
	message sessionPickerOpenInspectedMsg,
) tea.Cmd {
	if !m.matchesPendingOpen(message.sessionID, message.generation) {
		return nil
	}
	if message.err != nil {
		if m.openRequestCanceled(message.err) {
			return m.cancelPendingOpen()
		}
		return m.failOpenSession(message.sessionID, message.err)
	}
	if message.inspection.workspaceChange != nil {
		change := message.inspection.workspaceChange
		m.workspacePrompt = newWorkspaceChangePromptModel(
			change.selectedRoot,
			change.currentRoot,
			m.theme,
		)
		m.workspacePrompt.width = m.width
		m.workspacePrompt.height = m.height
		if m.startupStatus != nil {
			m.startupStatus.notice = startupPickerNotice{}
		}
		return nil
	}
	return m.startOpenPlan(message.sessionID, message.generation)
}

func (m *sessionPickerModel) startOpenPlan(
	sessionID runtimeids.SessionID,
	generation uint64,
) tea.Cmd {
	controller := m.openController
	requestContext := m.requestContext
	return func() tea.Msg {
		plan, err := controller.Plan(requestContext, sessionID)
		return sessionPickerOpenPlannedMsg{
			sessionID:  sessionID,
			generation: generation,
			plan:       plan,
			err:        err,
		}
	}
}

func (m *sessionPickerModel) updateWorkspacePrompt(message tea.Msg) tea.Cmd {
	next, command := m.workspacePrompt.Update(message)
	updated, ok := next.(*workspaceChangePromptModel)
	if !ok {
		panic(fmt.Sprintf(
			"session picker workspace prompt replaced model with %T",
			next,
		))
	}
	m.workspacePrompt = updated
	if !updated.done {
		return command
	}
	rebind := updated.result.Rebind
	currentRoot := updated.currentRoot
	m.workspacePrompt = nil
	if !rebind {
		m.pendingOpen = nil
		if m.startupStatus != nil {
			m.startupStatus.notice = startupPickerNotice{}
		}
		return m.reconcileSpinnerTick()
	}
	pending := m.pendingOpen
	if pending == nil {
		panic("session picker workspace prompt completed without a pending open")
	}
	controller := m.openController
	requestContext := m.requestContext
	return func() tea.Msg {
		err := controller.Retarget(
			requestContext,
			pending.sessionID,
			currentRoot,
		)
		return sessionPickerOpenRetargetedMsg{
			sessionID:  pending.sessionID,
			generation: pending.generation,
			err:        err,
		}
	}
}

func (m *sessionPickerModel) applyOpenRetarget(
	message sessionPickerOpenRetargetedMsg,
) tea.Cmd {
	if !m.matchesPendingOpen(message.sessionID, message.generation) {
		return nil
	}
	if message.err != nil {
		if m.openRequestCanceled(message.err) {
			return m.cancelPendingOpen()
		}
		return m.failOpenSession(message.sessionID, message.err)
	}
	if m.startupStatus != nil {
		m.startupStatus.notice = startupPickerNotice{
			Text: "Opening session…",
			Kind: startupPickerNoticeNeutral,
		}
	}
	return m.startOpenPlan(message.sessionID, message.generation)
}

func (m *sessionPickerModel) applyOpenPlan(
	message sessionPickerOpenPlannedMsg,
) tea.Cmd {
	if !m.matchesPendingOpen(message.sessionID, message.generation) {
		return nil
	}
	if message.err != nil {
		if m.openRequestCanceled(message.err) {
			return m.cancelPendingOpen()
		}
		return m.failOpenSession(message.sessionID, message.err)
	}
	if message.plan.SessionID != message.sessionID.String() {
		return m.failOpenSession(
			message.sessionID,
			fmt.Errorf(
				"opened session plan %q does not match selected session %q",
				message.plan.SessionID,
				message.sessionID,
			),
		)
	}
	m.pendingOpen = nil
	m.openFailure = nil
	if m.startupStatus != nil {
		m.startupStatus.notice = startupPickerNotice{}
	}
	m.result = sessionPickerOpenResult{
		sessionID: message.sessionID,
		plan:      message.plan,
	}
	return tea.Quit
}

func (m *sessionPickerModel) failOpenSession(
	sessionID runtimeids.SessionID,
	err error,
) tea.Cmd {
	m.pendingOpen = nil
	m.openFailure = &sessionPickerOpenFailure{
		sessionID:  sessionID,
		kind:       classifySessionPickerOpenFailure(err),
		diagnostic: err,
	}
	if m.startupStatus == nil {
		m.startupStatus = newStartupPickerStatusModel()
	}
	m.startupStatus.notice = sessionPickerOpenFailureNotice(*m.openFailure)
	return m.reconcileSpinnerTick()
}

func (m *sessionPickerModel) matchesPendingOpen(
	sessionID runtimeids.SessionID,
	generation uint64,
) bool {
	return m.pendingOpen != nil &&
		m.pendingOpen.sessionID == sessionID &&
		m.pendingOpen.generation == generation
}

func (m *sessionPickerModel) openRequestCanceled(err error) bool {
	return errors.Is(err, context.Canceled) &&
		m.requestContext != nil &&
		m.requestContext.Err() != nil
}

func classifySessionPickerOpenFailure(err error) sessionPickerOpenFailureKind {
	var materialization *protocol.SessionEventLogMaterializationError
	if !errors.As(err, &materialization) {
		return sessionPickerOpenFailureGeneric
	}
	switch materialization.Reason {
	case protocol.SessionEventLogMaterializationUnsupportedVersion:
		return sessionPickerOpenFailureUpgradeRequired
	case protocol.SessionEventLogMaterializationInsufficientSpace:
		return sessionPickerOpenFailureInsufficientSpace
	case protocol.SessionEventLogMaterializationStructuralFailure:
		return sessionPickerOpenFailureStructural
	case protocol.SessionEventLogMaterializationReconciliationPending:
		return sessionPickerOpenFailureReconciliationPending
	case protocol.SessionEventLogMaterializationFailure:
		return sessionPickerOpenFailureGeneric
	default:
		return sessionPickerOpenFailureGeneric
	}
}

func sessionPickerOpenFailureNotice(
	failure sessionPickerOpenFailure,
) startupPickerNotice {
	notice := startupPickerNotice{
		Text:       "Could not open this session. Press Enter to retry.",
		Kind:       startupPickerNoticeError,
		Diagnostic: failure.diagnostic,
	}
	switch failure.kind {
	case sessionPickerOpenFailureUpgradeRequired:
		notice.Text = "This session requires a newer Kent version. Update Kent, then press Enter to retry."
	case sessionPickerOpenFailureInsufficientSpace:
		notice.Text = "Not enough disk space to upgrade this session. Free space, then press Enter to retry."
	case sessionPickerOpenFailureStructural:
		notice.Text = "This session history is invalid and could not be upgraded. Press Enter to retry."
	case sessionPickerOpenFailureReconciliationPending:
		notice.Text = "Session history was upgraded, but metadata repair is pending. Press Enter to retry."
	case sessionPickerOpenFailureGeneric:
	default:
		panic(fmt.Sprintf(
			"unknown session picker open failure kind %d",
			failure.kind,
		))
	}
	return notice
}
