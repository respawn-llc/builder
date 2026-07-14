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
)

type uiWorktreeOverlayPhase string

const (
	uiWorktreeOverlayPhaseList          uiWorktreeOverlayPhase = "list"
	uiWorktreeOverlayPhaseCreate        uiWorktreeOverlayPhase = "create"
	uiWorktreeOverlayPhaseDeleteConfirm uiWorktreeOverlayPhase = "delete_confirm"
)

type uiWorktreeOpenIntent struct {
	OpenCreate         bool
	OpenDelete         bool
	DeleteTarget       uiWorktreeDeleteIntentTarget
	PreferDeleteBranch bool
}

type uiWorktreeDeleteIntentTargetKind uint8

const (
	uiWorktreeDeleteIntentTargetCurrent uiWorktreeDeleteIntentTargetKind = iota
	uiWorktreeDeleteIntentTargetSelector
	uiWorktreeDeleteIntentTargetIdentity
)

type uiWorktreeDeleteIntentTarget struct {
	kind     uiWorktreeDeleteIntentTargetKind
	selector string
	identity worktreeui.SelectionIdentity
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

type uiWorktreeDeleteTargetAuthority uint8

const (
	uiWorktreeDeleteTargetAuthorityListed uiWorktreeDeleteTargetAuthority = iota
	uiWorktreeDeleteTargetAuthorityResolvedSelector
)

type uiWorktreeDeleteDialogState struct {
	target             worktreeui.Item
	targetAuthority    uiWorktreeDeleteTargetAuthority
	selectedAction     uiWorktreeDeleteAction
	preferDeleteBranch bool
	errorText          string
	submitting         bool
	forceFolderRemoval bool
}

type uiWorktreeOverlayState struct {
	open                          bool
	listPending                   bool
	deleteTargetResolutionPending bool
	phase                         uiWorktreeOverlayPhase
	selection                     int
	target                        clientui.SessionExecutionTarget
	entries                       []worktreeui.Item
	errorText                     string
	mutationToken                 uint64
	switchToken                   uint64
	switchPending                 bool
	queuedSwitch                  uiWorktreeQueuedSwitch
	selectedIdentity              worktreeui.SelectionIdentity
	intent                        uiWorktreeOpenIntent
	create                        uiWorktreeCreateDialogState
	deleteConfirm                 uiWorktreeDeleteDialogState
	inputCursor                   uiInputFieldCursor
}

type uiWorktreeQueuedSwitch struct {
	TargetToken string
}

type worktreeListDoneMsg struct {
	token uint64
	resp  serverapi.WorktreeListResponse
	err   error
}

type worktreeDeleteTargetResolvedMsg struct {
	generation         uint64
	resp               serverapi.WorktreeSelectorPreviewResponse
	preferDeleteBranch bool
	err                error
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
	token  uint64
	target string
	ack    serverapi.WorktreeScheduledAcknowledgement
	err    error
}

type worktreeDeleteDoneMsg struct {
	token  uint64
	target string
	resp   serverapi.WorktreeDeleteResult
	err    error
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

func (s uiWorktreeOverlayState) isLoading() bool {
	return s.listPending || s.deleteTargetResolutionPending
}

func (m *uiModel) openWorktreeOverlay(intent uiWorktreeOpenIntent) {
	if m == nil {
		return
	}
	m.invalidateWorktreeListRequest()
	m.invalidateWorktreeDeleteTargetResolution()
	m.worktrees.open = true
	m.worktrees.phase = uiWorktreeOverlayPhaseList
	m.worktrees.listPending = true
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
	m.invalidateWorktreeListRequest()
	m.invalidateWorktreeDeleteTargetResolution()
	m.worktrees = uiWorktreeOverlayState{}
	m.restorePrimaryInputMode()
}

func (m *uiModel) requestWorktreeListCmd() tea.Cmd {
	if m == nil {
		return nil
	}
	m.worktreeListGeneration = nextNonZeroToken(m.worktreeListGeneration)
	token := m.worktreeListGeneration
	m.worktrees.listPending = true
	m.worktrees.errorText = ""
	service := m.worktreeMutationService()
	return func() tea.Msg {
		resp, err := service.List()
		return worktreeListDoneMsg{token: token, resp: resp, err: err}
	}
}

func (m *uiModel) openCreateWorktreeDialog() tea.Cmd {
	if m == nil {
		return nil
	}
	m.invalidateWorktreeDeleteTargetResolution()
	m.worktrees.phase = uiWorktreeOverlayPhaseCreate
	m.worktrees.errorText = ""
	m.worktrees.create = newWorktreeCreateDialog(m.suggestedWorktreeBranchFromEntries())
	return m.scheduleWorktreeCreateTargetResolution()
}

func (m *uiModel) openDeleteWorktreeDialog(
	target worktreeui.Item,
	preferDeleteBranch bool,
	targetAuthority uiWorktreeDeleteTargetAuthority,
) {
	if m == nil {
		return
	}
	m.invalidateWorktreeDeleteTargetResolution()
	m.worktrees.phase = uiWorktreeOverlayPhaseDeleteConfirm
	m.worktrees.errorText = ""
	m.worktrees.deleteConfirm = uiWorktreeDeleteDialogState{
		target:             target,
		targetAuthority:    targetAuthority,
		preferDeleteBranch: preferDeleteBranch,
	}
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

func (m *uiModel) applyWorktreeListResponse(resp serverapi.WorktreeListResponse) error {
	if m == nil {
		return nil
	}
	if err := m.recordWorktreeSelection(); err != nil {
		return err
	}
	m.worktrees.target = resp.Target
	entries, err := worktreeui.ProjectItems(resp.Worktrees)
	if err != nil {
		m.worktrees.entries = nil
		return err
	}
	m.worktrees.entries = entries
	if err := m.restoreWorktreeSelection(); err != nil {
		return err
	}
	m.clampWorktreeSelection()
	if m.worktrees.phase == uiWorktreeOverlayPhaseDeleteConfirm {
		targetIdentity, err := worktreeui.SelectionIdentityForItem(m.worktrees.deleteConfirm.target)
		if err != nil {
			return err
		}
		item, idx, ok, err := worktreeui.FindByIdentity(m.worktrees.entries, targetIdentity)
		if err != nil {
			return err
		}
		if ok {
			if m.worktrees.deleteConfirm.targetAuthority == uiWorktreeDeleteTargetAuthorityResolvedSelector {
				target := m.worktrees.deleteConfirm.target
				target.IsCurrent = item.IsCurrent
				target.Entry.Projection.IsCurrent = item.Entry.Projection.IsCurrent
				m.worktrees.entries[idx] = target
				m.worktrees.deleteConfirm.target = target
			} else {
				m.worktrees.deleteConfirm.target = item
			}
			m.worktrees.deleteConfirm.clampSelection()
			return nil
		}
		if m.worktrees.deleteConfirm.targetAuthority == uiWorktreeDeleteTargetAuthorityResolvedSelector {
			return nil
		}
		m.closeWorktreeDialog()
	}
	return nil
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
	if intent.DeleteTarget.kind == uiWorktreeDeleteIntentTargetSelector {
		return m.worktreeDeleteTargetResolveCmd(intent.DeleteTarget.selector, intent.PreferDeleteBranch)
	}
	target, err := resolveWorktreeDeleteIntentTarget(m.worktrees.entries, intent.DeleteTarget)
	if err != nil {
		m.worktrees.errorText = runtimeattach.FormatSubmissionError(err)
		return nil
	}
	targetIdentity, err := worktreeui.SelectionIdentityForItem(target)
	if err != nil {
		m.worktrees.errorText = runtimeattach.FormatSubmissionError(err)
		return nil
	}
	_, idx, ok, err := worktreeui.FindByIdentity(m.worktrees.entries, targetIdentity)
	if err != nil {
		m.worktrees.errorText = runtimeattach.FormatSubmissionError(err)
		return nil
	}
	if ok {
		m.worktrees.selection = idx + 1
	}
	if err := m.recordWorktreeSelection(); err != nil {
		m.worktrees.errorText = runtimeattach.FormatSubmissionError(err)
		return nil
	}
	m.openDeleteWorktreeDialog(
		target,
		intent.PreferDeleteBranch,
		uiWorktreeDeleteTargetAuthorityListed,
	)
	return nil
}

func resolveWorktreeDeleteIntentTarget(
	entries []worktreeui.Item,
	target uiWorktreeDeleteIntentTarget,
) (worktreeui.Item, error) {
	switch target.kind {
	case uiWorktreeDeleteIntentTargetCurrent:
		return worktreeui.ResolveCurrentDeletionTarget(entries)
	case uiWorktreeDeleteIntentTargetIdentity:
		item, _, ok, err := worktreeui.FindByIdentity(entries, target.identity)
		if err != nil {
			return worktreeui.Item{}, err
		}
		if !ok {
			return worktreeui.Item{}, serverapi.ErrWorktreeNotFound
		}
		return item, nil
	default:
		return worktreeui.Item{}, errors.New("worktree delete intent target is invalid")
	}
}

func (m *uiModel) worktreeDeleteTargetResolveCmd(selector string, preferDeleteBranch bool) tea.Cmd {
	if m == nil {
		return nil
	}
	m.deleteTargetResolutionGeneration = nextNonZeroToken(m.deleteTargetResolutionGeneration)
	generation := m.deleteTargetResolutionGeneration
	m.worktrees.deleteTargetResolutionPending = true
	m.worktrees.errorText = ""
	service := m.worktreeMutationService()
	return func() tea.Msg {
		resp, err := service.ResolveSelector(selector)
		return worktreeDeleteTargetResolvedMsg{
			generation:         generation,
			resp:               resp,
			preferDeleteBranch: preferDeleteBranch,
			err:                err,
		}
	}
}

func (m *uiModel) invalidateWorktreeDeleteTargetResolution() {
	if m == nil {
		return
	}
	m.deleteTargetResolutionGeneration = nextNonZeroToken(m.deleteTargetResolutionGeneration)
	m.worktrees.deleteTargetResolutionPending = false
}

func (m *uiModel) invalidateWorktreeListRequest() {
	if m == nil {
		return
	}
	m.worktreeListGeneration = nextNonZeroToken(m.worktreeListGeneration)
	m.worktrees.listPending = false
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

func (m *uiModel) worktreeSwitchCmd(target worktreeui.Item) tea.Cmd {
	if m == nil {
		return nil
	}
	selector, err := worktreeui.StableMutationSelector(target)
	if err != nil {
		m.worktrees.errorText = runtimeattach.FormatSubmissionError(err)
		return nil
	}
	if m.worktrees.switchPending {
		m.worktrees.queuedSwitch = uiWorktreeQueuedSwitch{TargetToken: selector}
		return nil
	}
	m.invalidateWorktreeDeleteTargetResolution()
	m.worktrees.errorText = ""
	return m.worktreeSwitchCommandForTarget(selector)
}

func (m *uiModel) takeQueuedWorktreeSwitchCmd() tea.Cmd {
	if m == nil {
		return nil
	}
	queued := m.worktrees.queuedSwitch
	m.worktrees.queuedSwitch = uiWorktreeQueuedSwitch{}
	if strings.TrimSpace(queued.TargetToken) == "" {
		return nil
	}
	m.worktrees.switchPending = false
	return m.worktreeSwitchCommandForTarget(queued.TargetToken)
}

func (m *uiModel) worktreeDeleteCmd(target worktreeui.Item, deleteBranch bool) tea.Cmd {
	if m == nil {
		return nil
	}
	m.worktrees.mutationToken++
	token := m.worktrees.mutationToken
	m.worktrees.deleteConfirm.errorText = ""
	m.worktrees.deleteConfirm.submitting = true
	service := m.worktreeMutationService()
	selector, selectorErr := worktreeui.StableMutationSelector(target)
	cleanupPolicy := serverapi.WorktreeBranchCleanupModeAutoIfKentCreated
	if deleteBranch {
		cleanupPolicy = serverapi.WorktreeBranchCleanupModeDeleteSafe
	}
	return func() tea.Msg {
		if selectorErr != nil {
			return worktreeDeleteDoneMsg{token: token, target: worktreeui.DisplayName(target), err: selectorErr}
		}
		resp, err := service.Delete(selector, m.worktrees.deleteConfirm.forceFolderRemoval, cleanupPolicy)
		return worktreeDeleteDoneMsg{token: token, target: worktreeui.DisplayName(target), resp: resp, err: err}
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
