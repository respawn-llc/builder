package app

import (
	"context"
	"fmt"
	"time"

	"core/shared/client"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"

	tea "github.com/charmbracelet/bubbletea"
)

var runSessionPickerFlow = runSessionPicker

type sessionPageLoader interface {
	ProjectID() string
	ListSessionPage(context.Context, serverapi.SessionPageRequest) (serverapi.SessionPageResponse, error)
}

type sessionPickerResult interface {
	isSessionPickerResult()
}

type sessionPickerCreateResult struct{}
type sessionPickerCancelResult struct{}
type sessionPickerOpenResult struct {
	sessionID runtimeids.SessionID
}

func newSessionPickerCreateResult() sessionPickerResult {
	return sessionPickerCreateResult{}
}

func newSessionPickerCancelResult() sessionPickerResult {
	return sessionPickerCancelResult{}
}

func newSessionPickerOpenResult(sessionID runtimeids.SessionID) sessionPickerResult {
	if sessionID.IsZero() {
		panic("session picker open result requires a session ID")
	}
	return sessionPickerOpenResult{sessionID: sessionID}
}

func (sessionPickerCreateResult) isSessionPickerResult() {}
func (sessionPickerCancelResult) isSessionPickerResult() {}
func (sessionPickerOpenResult) isSessionPickerResult()   {}

type sessionPickerModel struct {
	loader                     sessionPageLoader
	requestContext             context.Context
	executionEnvironmentClient client.SessionViewClient
	header                     sessionPickerHeaderInfo
	activeTab                  sessioncontract.SessionCategory
	main                       sessionPickerTab
	subagents                  sessionPickerTab
	width                      int
	height                     int
	theme                      string
	styles                     sessionPickerStyles
	result                     sessionPickerResult

	spinnerFrame               int
	spinnerSequence            uint64
	scheduledSpinnerGeneration *uint64
	startupStatus              *startupPickerStatusModel
	clock                      func() time.Time
}

type sessionPickerPageLoadedMsg struct {
	category   sessioncontract.SessionCategory
	generation uint64
	position   serverapi.SessionPagePosition
	response   serverapi.SessionPageResponse
	err        error
}

type sessionPickerSpinnerTickMsg struct {
	generation uint64
}

func newSessionPickerModelWithExecutionEnvironmentClient(
	requestContext context.Context,
	loader sessionPageLoader,
	executionEnvironmentClient client.SessionViewClient,
	theme string,
	header sessionPickerHeaderInfo,
) *sessionPickerModel {
	if requestContext == nil {
		panic("session picker requires a request context")
	}
	if loader == nil {
		panic("session picker requires a page loader")
	}
	return &sessionPickerModel{
		loader:                     loader,
		requestContext:             requestContext,
		executionEnvironmentClient: executionEnvironmentClient,
		header:                     header,
		activeTab:                  sessioncontract.SessionCategoryMain,
		main:                       newSessionPickerTab(sessioncontract.SessionCategoryMain),
		subagents:                  newSessionPickerTab(sessioncontract.SessionCategorySubagent),
		width:                      defaultPickerWidth,
		height:                     defaultPickerHeight,
		theme:                      theme,
		styles:                     newSessionPickerStyles(theme),
		startupStatus:              newStartupPickerStatusModel(),
		clock:                      time.Now,
	}
}

func (m *sessionPickerModel) Init() tea.Cmd {
	commands := []tea.Cmd{
		m.startBodyRequest(sessioncontract.SessionCategoryMain, sessionPickerBodyRequestInitial),
		m.startBodyRequest(sessioncontract.SessionCategorySubagent, sessionPickerBodyRequestInitial),
		collectSessionPickerStatusCmd(m.header),
	}
	if tick := m.reconcileSpinnerTick(); tick != nil {
		commands = append(commands, tick)
	}
	return tea.Batch(commands...)
}

func (m *sessionPickerModel) tab(category sessioncontract.SessionCategory) *sessionPickerTab {
	switch category {
	case sessioncontract.SessionCategoryMain:
		return &m.main
	case sessioncontract.SessionCategorySubagent:
		return &m.subagents
	default:
		panic(fmt.Sprintf("unknown session picker category %q", category))
	}
}

func (m *sessionPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch message := msg.(type) {
	case sessionPickerPageLoadedMsg:
		cmd := m.applyPageLoaded(message)
		return m, tea.Batch(cmd, m.reconcileSpinnerTick())
	case sessionPickerSelectedDetailLoadedMsg:
		return m, m.applySelectedDetailLoaded(message)
	case sessionPickerSpinnerTickMsg:
		if m.scheduledSpinnerGeneration == nil || message.generation != *m.scheduledSpinnerGeneration {
			return m, nil
		}
		m.scheduledSpinnerGeneration = nil
		m.spinnerFrame++
		return m, m.reconcileSpinnerTick()
	case sessionPickerStatusMsg:
		m.header.CWD = sessionPickerStatusText(message.cwd)
		m.header.Branch = sessionPickerStatusText(message.branch)
		m.header.Auth = sessionPickerStatusText(message.auth)
		m.header.Model = sessionPickerStatusText(message.model)
		m.ensureSelectedVisible(m.tab(m.activeTab))
		return m, nil
	case tea.WindowSizeMsg:
		if message.Width > 0 {
			m.width = message.Width
		}
		if message.Height > 0 {
			m.height = message.Height
		}
		m.ensureSelectedVisible(m.tab(m.activeTab))
		return m, nil
	case tea.KeyMsg:
		return m, m.handleKey(message)
	case tea.KeyType:
		return m, m.handleKey(tea.KeyMsg{Type: message})
	}
	return m, nil
}

func sessionPickerStatusText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (m *sessionPickerModel) handleKey(key tea.KeyMsg) tea.Cmd {
	switch key.Type {
	case tea.KeyUp:
		return m.moveSelection(-1)
	case tea.KeyDown:
		return m.moveSelection(1)
	case tea.KeyTab, tea.KeyShiftTab, tea.KeyLeft, tea.KeyRight:
		return m.switchTab()
	case tea.KeyRunes:
		filtered, _ := stripMouseSGRRunes(key.Runes)
		if len(filtered) != 1 {
			return nil
		}
		switch filtered[0] {
		case 'k':
			return m.moveSelection(-1)
		case 'j':
			return m.moveSelection(1)
		case 'h', 'l':
			return m.switchTab()
		case 'n':
			m.cancelSelectedDetailRequests()
			m.result = newSessionPickerCreateResult()
			return tea.Quit
		}
	case tea.KeyEnter:
		tab := m.tab(m.activeTab)
		if tab.bodyPhase == sessionPickerBodyFailed {
			return m.startBodyRequest(m.activeTab, sessionPickerBodyRequestRetry)
		}
		switch selected := tab.selected.(type) {
		case sessionPickerCreateSelection:
			m.cancelSelectedDetailRequests()
			m.result = newSessionPickerCreateResult()
			return tea.Quit
		case sessionPickerSessionSelection:
			m.cancelSelectedDetailRequests()
			m.result = newSessionPickerOpenResult(selected.sessionID)
			return tea.Quit
		}
	case tea.KeyCtrlC:
		m.cancelSelectedDetailRequests()
		m.result = newSessionPickerCancelResult()
		return tea.Quit
	}
	return nil
}

func (m *sessionPickerModel) switchTab() tea.Cmd {
	if m.activeTab == sessioncontract.SessionCategoryMain {
		m.activeTab = sessioncontract.SessionCategorySubagent
	} else {
		m.activeTab = sessioncontract.SessionCategoryMain
	}
	tab := m.tab(m.activeTab)
	m.ensureSelectedVisible(tab)
	if tab.bodyPhase == sessionPickerBodyFailed && tab.bodyRequest == nil {
		return m.startBodyRequest(m.activeTab, sessionPickerBodyRequestRetry)
	}
	return nil
}

func (m *sessionPickerModel) moveSelection(delta int) tea.Cmd {
	tab := m.tab(m.activeTab)
	if tab.bodyPhase != sessionPickerBodyReady && tab.bodyPhase != sessionPickerBodyEmpty {
		return nil
	}
	index := tab.selectedIndex()
	if index == nil {
		if tab.itemCount() > 0 {
			tab.selectIndex(0)
			m.ensureSelectedVisible(tab)
			return m.startSelectedDetailForTab(tab)
		}
		return nil
	}
	next := *index + delta
	if next >= 0 && next < tab.itemCount() {
		tab.selectIndex(next)
		m.ensureSelectedVisible(tab)
		return m.startSelectedDetailForTab(tab)
	}
	if tab.directional != nil {
		return nil
	}
	if delta > 0 {
		if len(tab.segments) == 0 {
			return nil
		}
		continuation := tab.segments[len(tab.segments)-1].older
		if continuation == nil {
			return nil
		}
		return m.startDirectionalRequest(tab, serverapi.OlderSessionPagePosition(*continuation), delta)
	}
	if !tab.containsNewestEdge() {
		continuation := tab.segments[0].newer
		if continuation != nil {
			return m.startDirectionalRequest(tab, serverapi.NewerSessionPagePosition(*continuation), delta)
		}
	}
	return nil
}

func (m *sessionPickerModel) startBodyRequest(category sessioncontract.SessionCategory, kind sessionPickerBodyRequestKind) tea.Cmd {
	tab := m.tab(category)
	if tab.bodyRequest != nil || tab.directional != nil {
		return nil
	}
	if kind == sessionPickerBodyRequestRetry {
		m.discardSelectedDetailForTab(tab)
		m.clearPickerFailureForTab(tab, sessionPickerOperationDirectionalPage, tab.generation)
		tab.resetForFreshLoad()
	}
	tab.generation++
	request := &sessionPickerBodyRequest{
		kind:       kind,
		generation: tab.generation,
		position:   serverapi.NewestSessionPagePosition(),
	}
	tab.bodyRequest = request
	tab.bodyPhase = sessionPickerBodyInitialLoading
	return m.loadPageCmd(category, request.generation, request.position)
}

func (m *sessionPickerModel) startDirectionalRequest(tab *sessionPickerTab, position serverapi.SessionPagePosition, move int) tea.Cmd {
	if tab.bodyRequest != nil || tab.directional != nil {
		return nil
	}
	tab.generation++
	tab.directional = &sessionPickerDirectionalRequest{
		generation: tab.generation,
		position:   position,
		move:       move,
	}
	return tea.Batch(
		m.loadPageCmd(tab.category, tab.generation, position),
		m.reconcileSpinnerTick(),
	)
}

func (m *sessionPickerModel) loadPageCmd(category sessioncontract.SessionCategory, generation uint64, position serverapi.SessionPagePosition) tea.Cmd {
	request := serverapi.SessionPageRequest{
		ProjectID: m.loader.ProjectID(),
		Category:  category,
		PageSize:  serverapi.MaxSessionPageSize,
		Position:  position,
	}
	return func() tea.Msg {
		response, err := m.loader.ListSessionPage(m.requestContext, request)
		return sessionPickerPageLoadedMsg{
			category:   category,
			generation: generation,
			position:   position,
			response:   response,
			err:        err,
		}
	}
}

func (m *sessionPickerModel) applyPageLoaded(message sessionPickerPageLoadedMsg) tea.Cmd {
	tab := m.tab(message.category)
	if tab.bodyRequest != nil &&
		tab.bodyRequest.generation == message.generation &&
		sessionPagePositionsEqual(tab.bodyRequest.position, message.position) {
		requestKind := tab.bodyRequest.kind
		tab.bodyRequest = nil
		if message.err != nil {
			tab.resetForFreshLoad()
			tab.bodyPhase = sessionPickerBodyFailed
			m.recordPickerFailureForTab(tab, sessionPickerOperationBodyPage, message.generation, sessionPickerFailurePageRequest, message.err)
			return nil
		}
		if err := validateSessionPickerPage(message.response, m.loader.ProjectID(), tab.category); err != nil {
			tab.resetForFreshLoad()
			tab.bodyPhase = sessionPickerBodyFailed
			m.recordPickerFailureForTab(tab, sessionPickerOperationBodyPage, message.generation, sessionPickerFailurePageContract, err)
			return nil
		}
		tab.replaceSegments(message.response)
		m.clearPickerFailureForTab(tab, sessionPickerOperationBodyPage, message.generation)
		m.ensureSelectedVisible(tab)
		if requestKind == sessionPickerBodyRequestInitial || requestKind == sessionPickerBodyRequestRetry {
			return tea.Batch(
				m.startSelectedDetailForTab(tab),
				m.maybeCompleteAllEmpty(),
			)
		}
		return m.startSelectedDetailForTab(tab)
	}
	if tab.directional == nil ||
		tab.directional.generation != message.generation ||
		!sessionPagePositionsEqual(tab.directional.position, message.position) {
		return nil
	}
	directional := *tab.directional
	tab.directional = nil
	if message.err != nil {
		m.discardSelectedDetailForTab(tab)
		tab.resetForFreshLoad()
		tab.bodyPhase = sessionPickerBodyFailed
		m.recordPickerFailureForTab(tab, sessionPickerOperationDirectionalPage, message.generation, sessionPickerFailurePageRequest, message.err)
		return nil
	}
	if err := validateSessionPickerPage(message.response, m.loader.ProjectID(), tab.category); err != nil {
		m.discardSelectedDetailForTab(tab)
		tab.resetForFreshLoad()
		tab.bodyPhase = sessionPickerBodyFailed
		m.recordPickerFailureForTab(tab, sessionPickerOperationDirectionalPage, message.generation, sessionPickerFailurePageContract, err)
		return nil
	}
	segment := newSessionPickerPageSegment(message.response)
	switch message.position.Kind() {
	case serverapi.SessionPagePositionOlder:
		tab.segments = append(tab.segments, segment)
		if len(tab.segments) > 2 {
			tab.segments = tab.segments[len(tab.segments)-2:]
		}
		tab.rebuildResidentIDs()
		if directional.move > 0 && len(segment.sessions) > 0 {
			tab.selected = newSessionPickerSessionSelection(segment.sessions[0].SessionID)
			m.cancelSelectedDetailForTab(tab)
		}
	case serverapi.SessionPagePositionNewer:
		tab.segments = append([]sessionPickerPageSegment{segment}, tab.segments...)
		if len(tab.segments) > 2 {
			tab.segments = tab.segments[:2]
		}
		tab.rebuildResidentIDs()
		if directional.move < 0 && len(segment.sessions) > 0 {
			tab.selected = newSessionPickerSessionSelection(segment.sessions[len(segment.sessions)-1].SessionID)
			m.cancelSelectedDetailForTab(tab)
		}
	}
	tab.bodyPhase = sessionPickerBodyReady
	m.clearPickerFailureForTab(tab, sessionPickerOperationDirectionalPage, message.generation)
	m.ensureSelectedVisible(tab)
	return m.startSelectedDetailForTab(tab)
}

func (m *sessionPickerModel) maybeCompleteAllEmpty() tea.Cmd {
	if m.main.bodyPhase != sessionPickerBodyEmpty || m.subagents.bodyPhase != sessionPickerBodyEmpty {
		return nil
	}
	m.result = newSessionPickerCreateResult()
	return tea.Quit
}

func (m *sessionPickerModel) hasPendingPageRequest() bool {
	return m.main.bodyRequest != nil || m.main.directional != nil ||
		m.subagents.bodyRequest != nil || m.subagents.directional != nil
}

func (m *sessionPickerModel) reconcileSpinnerTick() tea.Cmd {
	if !m.hasPendingPageRequest() || m.scheduledSpinnerGeneration != nil {
		return nil
	}
	m.spinnerSequence++
	generation := m.spinnerSequence
	m.scheduledSpinnerGeneration = &generation
	return tea.Tick(spinnerTickInterval, func(time.Time) tea.Msg {
		return sessionPickerSpinnerTickMsg{generation: generation}
	})
}

func runSessionPicker(loader sessionPageLoader, theme string, header sessionPickerHeaderInfo) (sessionPickerResult, error) {
	var executionEnvironmentClient client.SessionViewClient
	if provider, ok := loader.(interface {
		SessionViewClient() client.SessionViewClient
	}); ok {
		executionEnvironmentClient = provider.SessionViewClient()
	}
	var lifecycle *sessionPickerLifecycle
	lifecycle = newSessionPickerLifecycle(sessionPickerLifecycleOptions{
		Loader:                     loader,
		ExecutionEnvironmentClient: executionEnvironmentClient,
		Theme:                      theme,
		Header:                     header,
		RunProgram: func(ctx context.Context, _ *sessionPickerModel) (sessionPickerResult, error) {
			finalModel, err := tea.NewProgram(lifecycle, tea.WithContext(ctx)).Run()
			if err != nil {
				return nil, err
			}
			if finalModel != lifecycle {
				return nil, fmt.Errorf("unexpected picker lifecycle model type %T", finalModel)
			}
			return lifecycle.Result(), nil
		},
	})
	return lifecycle.Run(context.Background())
}
