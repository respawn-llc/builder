package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"core/shared/clientui"
	serverpb "core/shared/protoapi/gen/kent/api/server"
	"core/shared/runtimeids"
	"core/shared/sessioncontract"

	tea "github.com/charmbracelet/bubbletea"
)

var runSessionPickerFlow = runSessionPicker

const sessionPickerPageSize = 50

type sessionPageLoader interface {
	ProjectID() string
	ListSessionPage(context.Context, sessionPageRequest) (sessionPageResponse, error)
}

type sessionPageRequest struct {
	ProjectID string
	Category  sessioncontract.SessionCategory
	Offset    *int
	Limit     *int
}

type sessionPageResponse struct {
	ProjectID  string
	Category   sessioncontract.SessionCategory
	Sessions   []clientui.SessionSummary
	NextOffset *int
}

func (r sessionPageResponse) Validate() error {
	if strings.TrimSpace(r.ProjectID) == "" || strings.TrimSpace(r.ProjectID) != r.ProjectID {
		return errors.New("project_id is invalid")
	}
	if _, err := sessioncontract.ParseSessionCategory(string(r.Category)); err != nil {
		return err
	}
	if len(r.Sessions) > sessionPickerPageSize {
		return fmt.Errorf("session page exceeds maximum size %d", sessionPickerPageSize)
	}
	for index, summary := range r.Sessions {
		if summary.SessionID.IsZero() {
			return fmt.Errorf("sessions[%d].session_id is required", index)
		}
		if summary.Category != r.Category {
			return fmt.Errorf("sessions[%d].category does not match page category", index)
		}
		if !summary.UpdatedAt.After(time.Unix(0, 0).UTC()) {
			return fmt.Errorf("sessions[%d].updated_at is invalid", index)
		}
	}
	if r.NextOffset != nil && *r.NextOffset <= 0 {
		return errors.New("next_offset must be positive when present")
	}
	return nil
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
	loader         sessionPageLoader
	requestContext context.Context
	header         sessionPickerHeaderInfo
	activeTab      sessioncontract.SessionCategory
	main           sessionPickerTab
	subagents      sessionPickerTab
	width          int
	height         int
	theme          string
	styles         sessionPickerStyles
	result         sessionPickerResult

	spinnerFrame               int
	spinnerSequence            uint64
	scheduledSpinnerGeneration *uint64
	startupStatus              *startupPickerStatusModel
	updateStatus               *serverpb.UpdateStatus
	clock                      func() time.Time
}

type sessionPickerPageLoadedMsg struct {
	category        sessioncontract.SessionCategory
	generation      uint64
	requestedOffset int
	pageRequest     *startupPickerPageRequest
	response        sessionPageResponse
	err             error
}

type sessionPickerSpinnerTickMsg struct {
	generation uint64
}

func newSessionPickerModel(
	requestContext context.Context,
	loader sessionPageLoader,
	theme string,
	header sessionPickerHeaderInfo,
) *sessionPickerModel {
	if requestContext == nil {
		panic("session picker requires a request context")
	}
	if loader == nil {
		panic("session picker requires a page loader")
	}
	startupStatus := newStartupPickerStatusModel()
	if header.Notice != nil {
		startupStatus.notice = *header.Notice
	}
	return &sessionPickerModel{
		loader:         loader,
		requestContext: requestContext,
		header:         header,
		activeTab:      sessioncontract.SessionCategoryMain,
		main:           newSessionPickerTab(sessioncontract.SessionCategoryMain),
		subagents:      newSessionPickerTab(sessioncontract.SessionCategorySubagent),
		width:          defaultPickerWidth,
		height:         defaultPickerHeight,
		theme:          theme,
		styles:         newSessionPickerStyles(theme),
		startupStatus:  startupStatus,
		clock:          time.Now,
	}
}

func (m *sessionPickerModel) Init() tea.Cmd {
	commands := []tea.Cmd{
		m.startBodyRequest(sessioncontract.SessionCategoryMain, sessionPickerBodyRequestInitial),
		m.startBodyRequest(sessioncontract.SessionCategorySubagent, sessionPickerBodyRequestInitial),
		collectSessionPickerStatusCmd(m.header),
	}
	if updateStatus := m.collectUpdateStatusCmd(); updateStatus != nil {
		commands = append(commands, updateStatus)
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
	case sessionPickerUpdateStatusMsg:
		return m, m.applyUpdateStatus(message)
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
	case tea.KeyPgUp:
		return m.moveSelectionPage(-1)
	case tea.KeyPgDown:
		return m.moveSelectionPage(1)
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
			m.result = newSessionPickerCreateResult()
			return tea.Quit
		case sessionPickerSessionSelection:
			m.result = newSessionPickerOpenResult(selected.sessionID)
			return tea.Quit
		}
	case tea.KeyCtrlC:
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
			return m.selectTabIndex(tab, 0)
		}
		return nil
	}
	next := *index + delta
	if next >= 0 && next < tab.itemCount() {
		return m.selectTabIndex(tab, next)
	}
	if (delta > 0 && tab.nextEdge.request != nil) ||
		(delta < 0 && tab.previousEdge.request != nil) {
		return nil
	}
	if delta > 0 {
		if len(tab.segments) == 0 {
			return nil
		}
		nextOffset := tab.segments[len(tab.segments)-1].nextOffset
		if nextOffset == nil {
			return nil
		}
		return m.startDirectionalRequest(tab, *nextOffset, delta)
	}
	if !tab.containsNewestEdge() {
		requestedOffset := tab.segments[0].requestedOffset - sessionPickerPageSize
		if requestedOffset < 0 {
			requestedOffset = 0
		}
		return m.startDirectionalRequest(tab, requestedOffset, delta)
	}
	return nil
}

func (m *sessionPickerModel) moveSelectionPage(direction int) tea.Cmd {
	if direction != -1 && direction != 1 {
		panic("session picker page direction must be -1 or 1")
	}
	tab := m.tab(m.activeTab)
	if tab.bodyPhase != sessionPickerBodyReady && tab.bodyPhase != sessionPickerBodyEmpty {
		return nil
	}
	index := tab.selectedIndex()
	if index == nil {
		if tab.itemCount() == 0 {
			return nil
		}
		if direction > 0 {
			return m.selectTabIndex(tab, 0)
		}
		return m.selectTabIndex(tab, tab.itemCount()-1)
	}
	pageSize := len(m.visibleRowsFromOffset(tab, tab.offset))
	if pageSize < 1 {
		pageSize = 1
	}
	next := *index + direction*pageSize
	if next >= 0 && next < tab.itemCount() {
		return m.selectTabIndex(tab, next)
	}
	if (direction > 0 && tab.nextEdge.request != nil) ||
		(direction < 0 && tab.previousEdge.request != nil) {
		return nil
	}
	if direction > 0 {
		if len(tab.segments) > 0 {
			nextOffset := tab.segments[len(tab.segments)-1].nextOffset
			if nextOffset != nil {
				return m.startDirectionalRequest(tab, *nextOffset, direction)
			}
		}
		if tab.itemCount() > 0 {
			return m.selectTabIndex(tab, tab.itemCount()-1)
		}
		return nil
	}
	if !tab.containsNewestEdge() {
		requestedOffset := tab.segments[0].requestedOffset - sessionPickerPageSize
		if requestedOffset < 0 {
			requestedOffset = 0
		}
		return m.startDirectionalRequest(tab, requestedOffset, direction)
	}
	if tab.itemCount() > 0 {
		return m.selectTabIndex(tab, 0)
	}
	return nil
}

func (m *sessionPickerModel) selectTabIndex(tab *sessionPickerTab, index int) tea.Cmd {
	tab.selectIndex(index)
	m.ensureSelectedVisible(tab)
	return nil
}

func (m *sessionPickerModel) startBodyRequest(category sessioncontract.SessionCategory, kind sessionPickerBodyRequestKind) tea.Cmd {
	tab := m.tab(category)
	if tab.bodyRequest != nil || tab.hasPendingRequest() {
		return nil
	}
	if kind == sessionPickerBodyRequestRetry {
		m.clearPickerFailureForTab(tab, sessionPickerOperationDirectionalPage, tab.generation)
		tab.resetForFreshLoad()
	}
	tab.generation++
	request := &sessionPickerBodyRequest{
		kind:            kind,
		generation:      tab.generation,
		requestedOffset: 0,
	}
	tab.bodyRequest = request
	tab.bodyPhase = sessionPickerBodyInitialLoading
	load := m.loadPageCmd(category, request.generation, request.requestedOffset, nil)
	if kind == sessionPickerBodyRequestRetry {
		return tea.Batch(load, m.reconcileSpinnerTick())
	}
	return load
}

func (m *sessionPickerModel) startDirectionalRequest(tab *sessionPickerTab, requestedOffset int, move int) tea.Cmd {
	if tab.bodyRequest != nil || tab.hasPendingRequest() {
		return nil
	}
	direction := startupPickerPageNext
	if move < 0 {
		direction = startupPickerPagePrevious
	}
	request, ok := tab.begin(direction, requestedOffset, nil, false, false, 0, 0, move)
	if !ok {
		return nil
	}
	return tea.Batch(
		m.loadPageCmd(tab.category, request.generation, requestedOffset, &request),
		m.reconcileSpinnerTick(),
	)
}

func (m *sessionPickerModel) loadPageCmd(category sessioncontract.SessionCategory, generation uint64, requestedOffset int, pageRequest *startupPickerPageRequest) tea.Cmd {
	offset := requestedOffset
	limit := sessionPickerPageSize
	request := sessionPageRequest{
		ProjectID: m.loader.ProjectID(),
		Category:  category,
		Offset:    &offset,
		Limit:     &limit,
	}
	return func() tea.Msg {
		response, err := m.loader.ListSessionPage(m.requestContext, request)
		return sessionPickerPageLoadedMsg{
			category:        category,
			generation:      generation,
			requestedOffset: requestedOffset,
			pageRequest:     pageRequest,
			response:        response,
			err:             err,
		}
	}
}

func validateSessionPickerPage(
	response sessionPageResponse,
	expectedProjectID string,
	expectedCategory sessioncontract.SessionCategory,
) error {
	if err := response.Validate(); err != nil {
		return fmt.Errorf("session picker page is invalid: %w", err)
	}
	if response.ProjectID != expectedProjectID {
		return fmt.Errorf(
			"session picker page project %q does not match requested project %q",
			response.ProjectID,
			expectedProjectID,
		)
	}
	if response.Category != expectedCategory {
		return fmt.Errorf(
			"session picker page category %q does not match requested category %q",
			response.Category,
			expectedCategory,
		)
	}
	if len(response.Sessions) > sessionPickerPageSize {
		return fmt.Errorf(
			"session picker page contains %d sessions, exceeding requested bound %d",
			len(response.Sessions),
			sessionPickerPageSize,
		)
	}
	return nil
}

func (m *sessionPickerModel) applyPageLoaded(message sessionPickerPageLoadedMsg) tea.Cmd {
	tab := m.tab(message.category)
	if tab.bodyRequest != nil &&
		tab.bodyRequest.generation == message.generation &&
		tab.bodyRequest.requestedOffset == message.requestedOffset {
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
		tab.replaceSegments(message.requestedOffset, message.response)
		m.clearPickerFailureForTab(tab, sessionPickerOperationBodyPage, message.generation)
		m.ensureSelectedVisible(tab)
		return m.maybeCompleteAllEmpty()
	}
	if message.pageRequest == nil {
		return nil
	}
	active := tab.requestFor(message.pageRequest.direction)
	if active == nil || *active != *message.pageRequest {
		return nil
	}
	directional := *active
	if message.err != nil {
		tab.resetForFreshLoad()
		tab.bodyPhase = sessionPickerBodyFailed
		m.recordPickerFailureForTab(tab, sessionPickerOperationDirectionalPage, message.generation, sessionPickerFailurePageRequest, message.err)
		return nil
	}
	if err := validateSessionPickerPage(message.response, m.loader.ProjectID(), tab.category); err != nil {
		tab.resetForFreshLoad()
		tab.bodyPhase = sessionPickerBodyFailed
		m.recordPickerFailureForTab(tab, sessionPickerOperationDirectionalPage, message.generation, sessionPickerFailurePageContract, err)
		return nil
	}
	tab.complete(directional)
	segment := newSessionPickerPageSegment(message.requestedOffset, message.response)
	switch {
	case directional.move > 0:
		tab.segments = appendBoundedPickerPage(tab.segments, segment)
		appended := tab.segments[len(tab.segments)-1]
		if directional.move > 0 && len(appended.sessions) > 0 {
			tab.selected = newSessionPickerSessionSelection(appended.sessions[0].SessionID)
		}
	case directional.move < 0:
		tab.segments = prependBoundedPickerPage(tab.segments, segment)
		prepended := tab.segments[0]
		if directional.move < 0 && len(prepended.sessions) > 0 {
			tab.selected = newSessionPickerSessionSelection(prepended.sessions[len(prepended.sessions)-1].SessionID)
		}
	default:
		err := fmt.Errorf(
			"session picker directional request requires movement: category=%q generation=%d requested_offset=%d",
			tab.category,
			directional.generation,
			directional.offset,
		)
		if m.header.StatusRequest.Settings.Debug {
			panic(err)
		}
		tab.resetForFreshLoad()
		tab.bodyPhase = sessionPickerBodyFailed
		m.recordPickerFailureForTab(
			tab,
			sessionPickerOperationDirectionalPage,
			message.generation,
			sessionPickerFailurePageContract,
			err,
		)
		return nil
	}
	tab.bodyPhase = sessionPickerBodyReady
	m.clearPickerFailureForTab(tab, sessionPickerOperationDirectionalPage, message.generation)
	m.ensureSelectedVisible(tab)
	return nil
}

func (m *sessionPickerModel) maybeCompleteAllEmpty() tea.Cmd {
	if m.main.bodyPhase != sessionPickerBodyEmpty || m.subagents.bodyPhase != sessionPickerBodyEmpty {
		return nil
	}
	m.result = newSessionPickerCreateResult()
	return tea.Quit
}

func (m *sessionPickerModel) hasPendingPageRequest() bool {
	return m.main.bodyRequest != nil || m.main.hasPendingRequest() ||
		m.subagents.bodyRequest != nil || m.subagents.hasPendingRequest()
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

func runSessionPicker(ctx context.Context, loader sessionPageLoader, theme string, header sessionPickerHeaderInfo) (sessionPickerResult, error) {
	lifecycle := newSessionPickerLifecycle(sessionPickerLifecycleOptions{
		Loader: loader,
		Theme:  theme,
		Header: header,
	})
	defer lifecycle.Close()
	finalModel, err := runContextualStartupPicker(ctx, lifecycle)
	if err != nil {
		return nil, err
	}
	if finalModel != lifecycle {
		return nil, fmt.Errorf("unexpected picker lifecycle model type %T", finalModel)
	}
	result := lifecycle.Result()
	if err := validateSessionPickerLifecycleResult(result); err != nil {
		return nil, err
	}
	return result, nil
}
