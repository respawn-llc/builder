package app

import (
	"context"
	"errors"
	"io"
	"strings"

	"core/cli/app/internal/runtimeattach"
	"core/cli/app/internal/worktreeui"
	tuiinput "core/cli/tui/input"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	worktreeOverlayHeaderLines = 3
	worktreeOverlayFooterLines = 1
	worktreeOverlayRowLines    = 3
	worktreeCreateRowID        = worktreeui.CreateRowID
)

type uiWorktreeOverlayPhase string

const (
	uiWorktreeOverlayPhaseList          uiWorktreeOverlayPhase = "list"
	uiWorktreeOverlayPhaseCreate        uiWorktreeOverlayPhase = "create"
	uiWorktreeOverlayPhaseDeleteConfirm uiWorktreeOverlayPhase = "delete_confirm"
)

type uiWorktreeOpenIntent struct {
	OpenCreate          bool
	OpenDelete          bool
	ConfirmDeleteTarget string
	PreferDeleteBranch  bool
}

type uiWorktreeCreateField = worktreeui.Field

const (
	uiWorktreeCreateFieldBranchTarget = worktreeui.FieldBranchTarget
	uiWorktreeCreateFieldBaseRef      = worktreeui.FieldBaseRef
	uiWorktreeCreateFieldActions      = worktreeui.FieldActions
)

type uiWorktreeCreateAction = worktreeui.CreateFormAction

const (
	uiWorktreeCreateActionCreate = worktreeui.CreateFormActionCreate
	uiWorktreeCreateActionCancel = worktreeui.CreateFormActionCancel
)

type uiWorktreeCreateDialogState struct {
	baseRef       tuiinput.Editor
	branchTarget  tuiinput.Editor
	focus         uiWorktreeCreateField
	action        uiWorktreeCreateAction
	errorText     string
	submitting    bool
	resolving     bool
	submitPending bool
	resolveToken  uint64
	resolution    serverapi.WorktreeCreateTargetResolution
	setupProgress *uiWorktreeSetupProgressState
	setupEvent    *serverapi.WorktreeSetupEvent
}

type uiWorktreeSetupProgressState struct {
	cancel context.CancelFunc
}

type uiWorktreeDeleteAction = worktreeui.DeleteAction

const (
	uiWorktreeDeleteActionCancel       = worktreeui.DeleteActionCancel
	uiWorktreeDeleteActionDelete       = worktreeui.DeleteActionDelete
	uiWorktreeDeleteActionDeleteBranch = worktreeui.DeleteActionDeleteBranch
)

type uiWorktreeDeleteDialogState struct {
	target             serverapi.WorktreeView
	selectedAction     uiWorktreeDeleteAction
	preferDeleteBranch bool
	errorText          string
	submitting         bool
}

type uiWorktreeOverlayState struct {
	open          bool
	loading       bool
	phase         uiWorktreeOverlayPhase
	selection     int
	target        clientui.SessionExecutionTarget
	entries       []serverapi.WorktreeView
	errorText     string
	refreshToken  uint64
	mutationToken uint64
	switchToken   uint64
	switchPending bool
	queuedSwitch  uiWorktreeQueuedSwitch
	selectedID    string
	intent        uiWorktreeOpenIntent
	create        uiWorktreeCreateDialogState
	deleteConfirm uiWorktreeDeleteDialogState
	inputCursor   uiInputFieldCursor
}

type uiWorktreeQueuedSwitch struct {
	TargetToken string
	WorktreeID  string
}

type worktreeListDoneMsg struct {
	token uint64
	resp  serverapi.WorktreeListResponse
	err   error
}

type worktreeCreateDoneMsg struct {
	token uint64
	resp  serverapi.WorktreeCreateResponse
	err   error
}

type worktreeSetupEventMsg struct {
	token  uint64
	event  serverapi.WorktreeSetupEvent
	err    error
	events <-chan worktreeSetupEventMsg
}

type worktreeSwitchDoneMsg struct {
	token uint64
	resp  serverapi.WorktreeSwitchResponse
	err   error
}

type worktreeDeleteDoneMsg struct {
	token uint64
	resp  serverapi.WorktreeDeleteResponse
	err   error
}

func newWorktreeCreateDialog(suggestedBranch string) uiWorktreeCreateDialogState {
	dialog := uiWorktreeCreateDialogState{
		baseRef:      newSingleLineEditor(strings.TrimSpace("HEAD")),
		branchTarget: newSingleLineEditor(strings.TrimSpace(suggestedBranch)),
		focus:        uiWorktreeCreateFieldBranchTarget,
		action:       uiWorktreeCreateActionCreate,
	}
	dialog.syncFocus()
	return dialog
}

func (s uiWorktreeOverlayState) visibleErrorText() string {
	if !s.open {
		return ""
	}
	switch s.phase {
	case uiWorktreeOverlayPhaseCreate:
		return strings.TrimSpace(s.create.errorText)
	case uiWorktreeOverlayPhaseDeleteConfirm:
		return strings.TrimSpace(s.deleteConfirm.errorText)
	default:
		return strings.TrimSpace(s.errorText)
	}
}

func (m *uiModel) openWorktreeOverlay(intent uiWorktreeOpenIntent) {
	if m == nil {
		return
	}
	m.worktrees.open = true
	m.worktrees.phase = uiWorktreeOverlayPhaseList
	m.worktrees.loading = true
	m.worktrees.errorText = ""
	m.worktrees.intent = intent
	m.worktrees.create = uiWorktreeCreateDialogState{}
	m.worktrees.deleteConfirm = uiWorktreeDeleteDialogState{}
	m.setInputMode(uiInputModeWorktree)
	if len(m.worktrees.entries) == 0 {
		m.worktrees.selection = 0
	}
}

func (m *uiModel) closeWorktreeOverlay() {
	if m == nil {
		return
	}
	if m.worktrees.switchPending {
		return
	}
	if m.worktrees.create.setupProgress != nil && m.worktrees.create.setupProgress.cancel != nil {
		m.worktrees.create.setupProgress.cancel()
	}
	m.worktrees = uiWorktreeOverlayState{}
	m.restorePrimaryInputMode()
}

func (m *uiModel) requestWorktreeListCmd() tea.Cmd {
	if m == nil {
		return nil
	}
	m.worktrees.refreshToken++
	token := m.worktrees.refreshToken
	includeDirtyCount := m.worktrees.intent.OpenDelete || m.worktrees.phase == uiWorktreeOverlayPhaseDeleteConfirm
	m.worktrees.loading = true
	m.worktrees.errorText = ""
	service := m.worktreeMutationService()
	return func() tea.Msg {
		resp, err := service.List(includeDirtyCount)
		return worktreeListDoneMsg{token: token, resp: resp, err: err}
	}
}

func (m *uiModel) openCreateWorktreeDialog() tea.Cmd {
	if m == nil {
		return nil
	}
	m.worktrees.phase = uiWorktreeOverlayPhaseCreate
	m.worktrees.errorText = ""
	m.worktrees.create = newWorktreeCreateDialog(m.suggestedWorktreeBranchFromEntries())
	return m.scheduleWorktreeCreateTargetResolution()
}

func (m *uiModel) openDeleteWorktreeDialog(target serverapi.WorktreeView, preferDeleteBranch bool) {
	if m == nil {
		return
	}
	m.worktrees.phase = uiWorktreeOverlayPhaseDeleteConfirm
	m.worktrees.errorText = ""
	m.worktrees.deleteConfirm = uiWorktreeDeleteDialogState{target: target, preferDeleteBranch: preferDeleteBranch}
	m.worktrees.deleteConfirm.clampSelection()
}

func (m *uiModel) closeWorktreeDialog() {
	if m == nil {
		return
	}
	if m.worktrees.create.setupProgress != nil && m.worktrees.create.setupProgress.cancel != nil {
		m.worktrees.create.setupProgress.cancel()
	}
	m.worktrees.phase = uiWorktreeOverlayPhaseList
	m.worktrees.create = uiWorktreeCreateDialogState{}
	m.worktrees.deleteConfirm = uiWorktreeDeleteDialogState{}
	m.worktrees.errorText = ""
}

func (m *uiModel) applyWorktreeListResponse(resp serverapi.WorktreeListResponse) {
	if m == nil {
		return
	}
	m.recordWorktreeSelection()
	m.worktrees.target = resp.Target
	m.worktrees.entries = append([]serverapi.WorktreeView(nil), resp.Worktrees...)
	m.restoreWorktreeSelection()
	m.clampWorktreeSelection()
	if m.worktrees.phase == uiWorktreeOverlayPhaseDeleteConfirm {
		targetID := strings.TrimSpace(m.worktrees.deleteConfirm.target.WorktreeID)
		if targetID == "" {
			m.closeWorktreeDialog()
			return
		}
		for _, item := range m.worktrees.entries {
			if strings.TrimSpace(item.WorktreeID) == targetID {
				m.worktrees.deleteConfirm.target = item
				m.worktrees.deleteConfirm.clampSelection()
				return
			}
		}
		m.closeWorktreeDialog()
	}
}

func (m *uiModel) applyWorktreeIntent() tea.Cmd {
	if m == nil {
		return nil
	}
	intent := m.worktrees.intent
	m.worktrees.intent = uiWorktreeOpenIntent{}
	if intent.OpenCreate {
		return m.openCreateWorktreeDialog()
	}
	if !intent.OpenDelete {
		return nil
	}
	target, err := worktreeui.ResolveDeletionTarget(m.worktrees.entries, intent.ConfirmDeleteTarget)
	if err != nil {
		m.worktrees.errorText = runtimeattach.FormatSubmissionError(err)
		return nil
	}
	m.recordWorktreeSelection()
	for idx, item := range m.worktrees.entries {
		if strings.TrimSpace(item.WorktreeID) == strings.TrimSpace(target.WorktreeID) {
			m.worktrees.selection = idx + 1
			break
		}
	}
	m.openDeleteWorktreeDialog(target, intent.PreferDeleteBranch)
	return nil
}

func (m *uiModel) suggestedWorktreeBranchFromEntries() string {
	if m == nil {
		return ""
	}
	if sessionBranch := worktreeui.SanitizeBranchSuggestion(m.suggestedWorktreeSessionName()); sessionBranch != "" {
		return sessionBranch
	}
	return ""
}

func (m *uiModel) worktreeCreateCmd(req serverapi.WorktreeCreateRequest) tea.Cmd {
	if m == nil {
		return nil
	}
	m.worktrees.mutationToken++
	token := m.worktrees.mutationToken
	if err := req.SetupOperationID.Validate(); err != nil {
		req.SetupOperationID = serverapi.NewWorktreeSetupOperationID()
	}
	m.worktrees.create.errorText = ""
	m.worktrees.create.submitting = true
	m.worktrees.create.setupEvent = nil
	setupCtx, cancel := context.WithCancel(context.Background())
	m.worktrees.create.setupProgress = &uiWorktreeSetupProgressState{cancel: cancel}
	setupReady := make(chan error, 1)
	setupEvents := make(chan worktreeSetupEventMsg, 8)
	subscribeCmd := func() tea.Msg {
		go subscribeWorktreeSetupEvents(setupCtx, m.worktreeClient, token, req.SetupOperationID, setupReady, setupEvents)
		return nil
	}
	service := m.worktreeMutationService()
	createCmd := func() tea.Msg {
		if err := <-setupReady; err != nil {
			return worktreeCreateDoneMsg{token: token, err: err}
		}
		resp, err := service.Create(req)
		return worktreeCreateDoneMsg{token: token, resp: resp, err: err}
	}
	return tea.Batch(subscribeCmd, createCmd, worktreeSetupEventCmd(setupEvents))
}

func subscribeWorktreeSetupEvents(ctx context.Context, worktreeClient client.WorktreeClient, token uint64, setupOperationID serverapi.WorktreeSetupOperationID, ready chan<- error, events chan worktreeSetupEventMsg) {
	defer close(events)
	if worktreeClient == nil {
		ready <- worktreeui.ErrClientUnavailable
		return
	}
	subscription, err := worktreeClient.SubscribeWorktreeSetup(ctx, serverapi.WorktreeSetupSubscribeRequest{SetupOperationID: setupOperationID})
	if err != nil {
		ready <- err
		return
	}
	defer func() { _ = subscription.Close() }()
	ready <- nil
	for {
		event, err := subscription.Next(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
				return
			}
			events <- worktreeSetupEventMsg{token: token, err: err, events: events}
			return
		}
		events <- worktreeSetupEventMsg{token: token, event: event, events: events}
		if event.Phase == serverapi.WorktreeSetupPhaseCompleted || event.Phase == serverapi.WorktreeSetupPhaseFailed {
			return
		}
	}
}

func worktreeSetupEventCmd(events <-chan worktreeSetupEventMsg) tea.Cmd {
	if events == nil {
		return nil
	}
	return func() tea.Msg {
		msg, ok := <-events
		if !ok {
			return nil
		}
		return msg
	}
}

func (m *uiModel) worktreeSwitchCmd(target serverapi.WorktreeView) tea.Cmd {
	if m == nil {
		return nil
	}
	worktreeID := strings.TrimSpace(target.WorktreeID)
	if m.worktrees.switchPending {
		m.worktrees.queuedSwitch = uiWorktreeQueuedSwitch{WorktreeID: worktreeID}
		return nil
	}
	m.worktrees.errorText = ""
	return m.worktreeSwitchCommandForTarget("", worktreeID)
}

func (m *uiModel) takeQueuedWorktreeSwitchCmd() tea.Cmd {
	if m == nil {
		return nil
	}
	queued := m.worktrees.queuedSwitch
	m.worktrees.queuedSwitch = uiWorktreeQueuedSwitch{}
	if strings.TrimSpace(queued.WorktreeID) == "" && strings.TrimSpace(queued.TargetToken) == "" {
		return nil
	}
	m.worktrees.switchPending = false
	return m.worktreeSwitchCommandForTarget(queued.TargetToken, queued.WorktreeID)
}

func (m *uiModel) worktreeDeleteCmd(target serverapi.WorktreeView, deleteBranch bool) tea.Cmd {
	if m == nil {
		return nil
	}
	m.worktrees.mutationToken++
	token := m.worktrees.mutationToken
	m.worktrees.deleteConfirm.errorText = ""
	m.worktrees.deleteConfirm.submitting = true
	service := m.worktreeMutationService()
	return func() tea.Msg {
		resp, err := service.Delete(target.WorktreeID, deleteBranch)
		return worktreeDeleteDoneMsg{token: token, resp: resp, err: err}
	}
}

func (m *uiModel) worktreeMutationService() worktreeui.Service {
	if m == nil {
		return worktreeui.Service{}
	}
	service := worktreeui.Service{
		Client:    m.worktreeClient,
		SessionID: m.sessionID,
		ResolveContext: func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), uiRuntimeControlTimeout)
		},
	}
	if client, ok := m.runtimeClient().(*sessionRuntimeClient); ok && client != nil {
		service.Runtime = worktreeui.RuntimeControl{
			Context:                  service.ResolveContext,
			MutationContext:          worktreeui.DefaultMutationContext,
			RecoverRuntimeConnection: client.recoverRuntimeConnectionWithWarning,
			AppendRecoveryWarning:    true,
		}
	}
	return service
}
