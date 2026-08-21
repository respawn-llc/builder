package app

import (
	"context"
	"strings"
	"time"

	"core/cli/app/internal/status"

	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/config"
	authpb "core/shared/protoapi/gen/kent/api/auth"
	"core/shared/textutil"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	statusRefreshTimeout = 10 * time.Second
	statusGitTimeout     = 4 * time.Second
)

type uiStatusConfig struct {
	WorkspaceRoot   string
	PersistenceRoot string
	ExecutionTarget clientui.SessionExecutionTarget
	SessionViews    apicontract.SessionViewService
	Settings        config.Settings
	AuthSelection   *authpb.ProviderSelection
	Source          config.SourceReport
	AuthStatus      apicontract.AuthStatusService
}

type uiStatusCollector interface {
	Collect(ctx context.Context, req uiStatusRequest) (uiStatusSnapshot, error)
}

type uiStatusProgressiveCollector interface {
	CollectBase(req uiStatusRequest) uiStatusSnapshot
	CollectAuth(ctx context.Context, req uiStatusRequest, base uiStatusSnapshot) uiStatusAuthStageResult
	CollectGit(ctx context.Context, req uiStatusRequest, base uiStatusSnapshot) uiStatusGitStageResult
	CollectEnvironment(ctx context.Context, req uiStatusRequest, base uiStatusSnapshot) uiStatusEnvironmentStageResult
}

type uiStatusSection = status.Section

const (
	uiStatusSectionBase        = status.SectionBase
	uiStatusSectionAuth        = status.SectionAuth
	uiStatusSectionGit         = status.SectionGit
	uiStatusSectionEnvironment = status.SectionEnvironment
)

type uiStatusRequest = status.Request
type uiStatusSnapshot = status.Snapshot
type uiStatusAuthInfo = status.AuthInfo
type uiStatusGitInfo = status.GitInfo
type uiStatusContextInfo = status.ContextInfo
type uiStatusModelInfo = status.ModelInfo
type uiStatusConfigInfo = status.ConfigInfo
type uiStatusSubscriptionInfo = status.SubscriptionInfo
type uiStatusSubscriptionWindow = status.SubscriptionWindow
type uiStatusSkillInspection = status.SkillInspection
type uiStatusRepository = status.Repository
type uiStatusSeedResult = status.SeedResult
type uiStatusAuthStageResult = status.AuthStageResult
type uiStatusGitStageResult = status.GitStageResult
type uiStatusEnvironmentStageResult = status.EnvironmentStageResult

type statusRefreshDoneMsg struct {
	token    uint64
	snapshot uiStatusSnapshot
	err      error
}

type statusBaseRefreshDoneMsg struct {
	token    uint64
	snapshot uiStatusSnapshot
}

type statusAuthRefreshDoneMsg struct {
	token  uint64
	result uiStatusAuthStageResult
}

type statusGitRefreshDoneMsg struct {
	token      uint64
	cacheKey   string
	result     uiStatusGitStageResult
	background bool
}

type statusEnvironmentRefreshDoneMsg struct {
	token    uint64
	cacheKey string
	result   uiStatusEnvironmentStageResult
}

func WithUIStatusConfig(statusConfig uiStatusConfig) UIOption {
	return func(m *uiModelConstruction) {
		m.statusConfig = statusConfig
		if statusConfig.Settings.Debug {
			m.debugMode = true
		}
		if m.statusCollector == nil {
			m.statusCollector = defaultUIStatusCollector()
		}
	}
}

func WithUIStatusCollector(collector uiStatusCollector) UIOption {
	return func(m *uiModelConstruction) {
		if collector != nil {
			m.statusCollector = collector
		}
	}
}

func WithUIStatusRepository(repository uiStatusRepository) UIOption {
	return func(m *uiModelConstruction) {
		if repository != nil {
			m.statusRepository = repository
		}
	}
}

func (m *uiModel) newStatusRequest(now time.Time) uiStatusRequest {
	executionTarget := m.currentExecutionTarget()
	request := uiStatusRequest{
		WorkspaceRoot:         m.currentExecutionWorkdir(executionTarget),
		PersistenceRoot:       strings.TrimSpace(m.statusConfig.PersistenceRoot),
		ExecutionTarget:       executionTarget,
		SessionViews:          m.statusConfig.SessionViews,
		Settings:              m.statusConfig.Settings,
		AuthSelection:         m.statusConfig.AuthSelection,
		Source:                m.statusConfig.Source,
		AuthStatus:            m.statusConfig.AuthStatus,
		SessionName:           strings.TrimSpace(m.sessionName),
		SessionID:             strings.TrimSpace(m.sessionID),
		AgentRole:             textutil.Pointer(m.cachedRuntimeMainView().Session.AgentRole),
		ConfiguredModelName:   textutil.Pointer(m.configuredModelName),
		ModelName:             strings.TrimSpace(m.modelName),
		ThinkingLevel:         strings.TrimSpace(m.thinkingLevel),
		FastModeAvailable:     m.fastModeAvailable,
		FastModeEnabled:       m.fastModeEnabled,
		ReviewerEnabled:       m.reviewerEnabled,
		ReviewerMode:          strings.TrimSpace(m.reviewerMode),
		AutoCompactionEnabled: m.autoCompactionEnabled,
		QuestionsEnabled:      m.questionsEnabled,
		CurrentTime:           now,
	}
	return populateStatusRequestCacheKeys(request)
}

func (m *uiModel) currentExecutionTarget() clientui.SessionExecutionTarget {
	if m == nil {
		return clientui.SessionExecutionTarget{}
	}
	target := m.cachedRuntimeMainView().Session.ExecutionTarget
	if !clientui.SessionExecutionTargetIsZero(target) {
		return target
	}
	return m.statusConfig.ExecutionTarget
}

func (m *uiModel) currentExecutionWorkdir(target clientui.SessionExecutionTarget) string {
	if workdir := strings.TrimSpace(target.EffectiveWorkdir); workdir != "" {
		return workdir
	}
	return strings.TrimSpace(m.statusConfig.WorkspaceRoot)
}

func populateStatusRequestCacheKeys(req uiStatusRequest) uiStatusRequest {
	if strings.TrimSpace(req.CacheKeys.Git) == "" {
		req.CacheKeys.Git = status.GitCacheKey(status.GitRoot(req))
	}
	if strings.TrimSpace(req.CacheKeys.Environment) == "" {
		req.CacheKeys.Environment = strings.TrimSpace(req.WorkspaceRoot)
	}
	return req
}

func defaultUIStatusCollector() status.Collector {
	return status.Collector{
		RequestTimeout:         statusRefreshTimeout,
		GitTimeout:             statusGitTimeout,
		SessionNameReadTimeout: uiRuntimeReadTimeout,
		EnvSanitizer:           sanitizedGitEnv,
	}
}

func (m *uiModel) openStatusOverlay() {
	m.status.open = true
	m.status.scroll = 0
	m.status.error = ""
	m.status.loading = false
	m.status.pendingSections = nil
	m.status.sectionWarnings = nil
	m.setInputMode(uiInputModeStatus)
}

func (m *uiModel) closeStatusOverlay() {
	m.status.open = false
	m.status.scroll = 0
	m.status.loading = false
	m.status.pendingSections = nil
	m.status.sectionWarnings = nil
	m.restorePrimaryInputMode()
}

func (m *uiModel) startStatusSectionRefresh(sections ...uiStatusSection) {
	if len(sections) == 0 {
		m.status.loading = false
		return
	}
	if m.status.pendingSections == nil {
		m.status.pendingSections = map[uiStatusSection]bool{}
	}
	if m.status.sectionWarnings == nil {
		m.status.sectionWarnings = map[uiStatusSection]string{}
	}
	for _, section := range sections {
		m.status.pendingSections[section] = true
		delete(m.status.sectionWarnings, section)
	}
	m.status.loading = len(m.status.pendingSections) > 0
}

func (m *uiModel) finishStatusSectionRefresh(section uiStatusSection, warning string) {
	if m.status.pendingSections != nil {
		delete(m.status.pendingSections, section)
	}
	if m.status.sectionWarnings == nil {
		m.status.sectionWarnings = map[uiStatusSection]string{}
	}
	if strings.TrimSpace(warning) == "" {
		delete(m.status.sectionWarnings, section)
	} else {
		m.status.sectionWarnings[section] = strings.TrimSpace(warning)
	}
	m.status.loading = len(m.status.pendingSections) > 0
	m.status.snapshot.CollectorWarning = m.statusCombinedWarnings()
}

func (m *uiModel) statusCombinedWarnings() string {
	warnings := ""
	for _, section := range []uiStatusSection{uiStatusSectionBase, uiStatusSectionEnvironment, uiStatusSectionGit, uiStatusSectionAuth} {
		warnings = status.JoinWarnings(warnings, m.status.sectionWarnings[section])
	}
	return warnings
}

func (m *uiModel) moveStatusScroll(delta int) {
	m.status.scroll += delta
	if m.status.scroll < 0 {
		m.status.scroll = 0
	}
}

func (m *uiModel) moveStatusScrollPage(deltaPages int) {
	rowsPerPage := m.statusRowsPerPage()
	m.moveStatusScroll(deltaPages * rowsPerPage)
}

func (m *uiModel) statusRowsPerPage() int {
	available := m.layout().effectiveHeight() - 1
	if available < 1 {
		return 1
	}
	return available
}

func (m *uiModel) statusRefreshCmd() tea.Cmd {
	m.status.refreshToken++
	token := m.status.refreshToken
	request := m.newStatusRequest(time.Now())
	collector := m.statusCollector
	if collector == nil {
		collector = defaultUIStatusCollector()
	}
	collectorRequest := request
	collectorRequest.Runtime = m.engine
	if progressive, ok := collector.(uiStatusProgressiveCollector); ok {
		seedBase := defaultUIStatusCollector().CollectBase(request)
		seed := uiStatusSeedResult{Snapshot: seedBase}
		if m.statusRepository != nil {
			seed = m.statusRepository.SeedSnapshot(request, seedBase, request.CurrentTime)
		}
		m.status.snapshot = seed.Snapshot
		m.status.error = ""
		m.status.pendingSections = nil
		m.status.sectionWarnings = seed.Warnings
		m.startStatusSectionRefresh(append([]uiStatusSection{uiStatusSectionBase}, seed.PendingSections...)...)
		cmds := make([]tea.Cmd, 0, len(seed.PendingSections)+1)
		cmds = append(cmds, m.statusBaseRefreshCmd(token, collectorRequest, progressive))
		for _, section := range seed.PendingSections {
			switch section {
			case uiStatusSectionAuth:
				cmds = append(cmds, m.statusAuthRefreshCmd(token, collectorRequest, progressive, seedBase))
			case uiStatusSectionGit:
				cmds = append(cmds, m.statusGitRefreshCmd(token, request.CacheKeys.Git, collectorRequest, progressive, seedBase, false))
			case uiStatusSectionEnvironment:
				cmds = append(cmds, m.statusEnvironmentRefreshCmd(token, request.CacheKeys.Environment, collectorRequest, progressive, seedBase))
			}
		}
		return tea.Batch(cmds...)
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), statusRefreshTimeout)
		defer cancel()
		snapshot, err := collector.Collect(ctx, collectorRequest)
		return statusRefreshDoneMsg{token: token, snapshot: snapshot, err: err}
	}
}

func (m *uiModel) statusLineGitRefreshCmd() tea.Cmd {
	request := m.newStatusRequest(time.Now())
	token := m.status.refreshToken
	collector := m.statusCollector
	if collector == nil {
		collector = defaultUIStatusCollector()
	}
	progressive, ok := collector.(uiStatusProgressiveCollector)
	if !ok {
		progressive = defaultUIStatusCollector()
	}
	gitRoot := status.GitRoot(request)
	if strings.TrimSpace(gitRoot) == "" {
		return nil
	}
	base := defaultUIStatusCollector().CollectBase(request)
	cacheKey := status.GitCacheKey(gitRoot)
	m.statusGitBackgroundInFlight = true
	request.Runtime = m.engine
	return m.statusGitRefreshCmd(token, cacheKey, request, progressive, base, true)
}

func (m *uiModel) statusBaseRefreshCmd(token uint64, request uiStatusRequest, collector uiStatusProgressiveCollector) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), statusRefreshTimeout)
		defer cancel()
		base := collector.CollectBase(request)
		return statusBaseRefreshDoneMsg{token: token, snapshot: defaultUIStatusCollector().EnrichBase(ctx, request, base)}
	}
}

func (m *uiModel) statusAuthRefreshCmd(token uint64, request uiStatusRequest, collector uiStatusProgressiveCollector, base uiStatusSnapshot) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), statusRefreshTimeout)
		defer cancel()
		return statusAuthRefreshDoneMsg{token: token, result: collector.CollectAuth(ctx, request, base)}
	}
}

func (m *uiModel) statusGitRefreshCmd(token uint64, cacheKey string, request uiStatusRequest, collector uiStatusProgressiveCollector, base uiStatusSnapshot, background bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), statusRefreshTimeout)
		defer cancel()
		return statusGitRefreshDoneMsg{token: token, cacheKey: cacheKey, result: collector.CollectGit(ctx, request, base), background: background}
	}
}

func (m *uiModel) statusEnvironmentRefreshCmd(token uint64, cacheKey string, request uiStatusRequest, collector uiStatusProgressiveCollector, base uiStatusSnapshot) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), statusRefreshTimeout)
		defer cancel()
		return statusEnvironmentRefreshDoneMsg{token: token, cacheKey: cacheKey, result: collector.CollectEnvironment(ctx, request, base)}
	}
}

func (c uiInputController) startStatusFlowCmd() tea.Cmd {
	m := c.model
	m.openStatusOverlay()
	refreshCmd := m.statusRefreshCmd()
	if overlayCmd := m.activateSurface(uiSurfaceStatus); overlayCmd != nil {
		return tea.Batch(overlayCmd, refreshCmd)
	}
	return refreshCmd
}

func (c uiInputController) stopStatusFlowCmd() tea.Cmd {
	m := c.model
	overlayCmd := m.restoreTranscriptSurface()
	m.closeStatusOverlay()
	return overlayCmd
}

func (c uiInputController) handleStatusOverlayKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m := c.model
	switch strings.ToLower(msg.String()) {
	case "ctrl+c":
		return c.handleRuntimeCtrlC(c.closeTranscriptSurfaceForRuntimeCtrlC(m.closeStatusOverlay))
	case "esc", "q":
		return m, c.stopStatusFlowCmd()
	case "up":
		m.moveStatusScroll(-1)
		return m, nil
	case "down":
		m.moveStatusScroll(1)
		return m, nil
	case "pgup":
		m.moveStatusScrollPage(-1)
		return m, nil
	case "pgdown":
		m.moveStatusScrollPage(1)
		return m, nil
	case "home":
		m.status.scroll = 0
		return m, nil
	case "end":
		m.status.scroll = 1 << 30
		return m, nil
	default:
		return m, nil
	}
}
