package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"core/cli/app/internal/projectbinding"
	"core/cli/tui"
	"core/shared/client"
	projectpb "core/shared/protoapi/gen/kent/api/project"

	tea "github.com/charmbracelet/bubbletea"
	"google.golang.org/protobuf/proto"
)

type projectWorkspacePickerModel struct {
	requestContext context.Context
	loader         projectbinding.WorkspacePageLoader
	projectID      string
	phase          projectWorkspacePickerPhase
	startupPickerPageWindow[projectWorkspacePickerPageSegment]
	selected        *projectWorkspacePickerOccurrence
	viewport        *projectWorkspacePickerOccurrence
	cursor          int
	offset          int
	width           int
	height          int
	theme           string
	styles          sessionPickerStyles
	headerMD        *startupMarkdownRenderer
	failure         error
	startupStatus   *startupPickerStatusModel
	result          projectWorkspacePickerResult
	spinnerFrame    int
	spinnerSequence uint64
	spinnerPending  *uint64
}

const projectWorkspacePickerPageSize = 50

type projectWorkspacePickerPhase uint8

const (
	projectWorkspacePickerInitialLoading projectWorkspacePickerPhase = iota + 1
	projectWorkspacePickerReady
	projectWorkspacePickerEmpty
	projectWorkspacePickerFirstPageFailed
	projectWorkspacePickerReadyWithEdgeFailure
)

type projectWorkspacePickerPageDirection = startupPickerPageDirection

const (
	projectWorkspacePickerPageInitial  = startupPickerPageInitial
	projectWorkspacePickerPageNext     = startupPickerPageNext
	projectWorkspacePickerPagePrevious = startupPickerPagePrevious
)

type projectWorkspacePickerEdgeState = startupPickerEdgeState

const (
	projectWorkspacePickerEdgeUnknown   = startupPickerEdgeUnknown
	projectWorkspacePickerEdgeLoading   = startupPickerEdgeLoading
	projectWorkspacePickerEdgeExhausted = startupPickerEdgeExhausted
	projectWorkspacePickerEdgeFailed    = startupPickerEdgeFailed
)

type projectWorkspacePickerEdge = startupPickerPageEdge

type projectWorkspacePickerPageRequest = startupPickerPageRequest

type projectWorkspacePickerPageSegment struct {
	generation uint64
	offset     int
	workspaces []*projectpb.ProjectWorkspaceCatalogSummary
	nextOffset *int32
}

type projectWorkspacePickerOccurrence struct {
	generation uint64
	offset     int
	index      int
	workspace  string
}

type projectWorkspacePickerPageLoadedMsg struct {
	request  projectWorkspacePickerPageRequest
	response *projectpb.ListProjectWorkspacesSuccess
	err      error
}

type projectWorkspacePickerSpinnerTickMsg struct {
	generation uint64
}

func newProjectWorkspacePickerModel(
	requestContext context.Context,
	loader projectbinding.WorkspacePageLoader,
	projectID string,
	theme string,
) *projectWorkspacePickerModel {
	if requestContext == nil {
		panic("project Workspace picker requires a request context")
	}
	if loader == nil {
		panic("project Workspace picker requires a page loader")
	}
	if strings.TrimSpace(projectID) == "" {
		panic("project Workspace picker requires a Project ID")
	}
	return &projectWorkspacePickerModel{
		requestContext: requestContext,
		loader:         loader,
		projectID:      strings.TrimSpace(projectID),
		phase:          projectWorkspacePickerInitialLoading,
		width:          defaultPickerWidth,
		height:         defaultPickerHeight,
		theme:          theme,
		styles:         newSessionPickerStyles(theme),
		headerMD:       newStartupMarkdownRendererWithWordWrap(theme),
		startupStatus:  newStartupPickerStatusModel(),
	}
}

func (m *projectWorkspacePickerModel) Init() tea.Cmd {
	return tea.Batch(m.startPageRequest(0, projectWorkspacePickerPageInitial), m.reconcileSpinner())
}

func (m *projectWorkspacePickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch key := msg.(type) {
	case projectWorkspacePickerPageLoadedMsg:
		command := m.applyPageLoaded(key)
		return m, tea.Batch(command, m.reconcileSpinner())
	case projectWorkspacePickerSpinnerTickMsg:
		if m.spinnerPending == nil || *m.spinnerPending != key.generation {
			return m, nil
		}
		m.spinnerPending = nil
		m.spinnerFrame++
		return m, m.reconcileSpinner()
	case tea.WindowSizeMsg:
		if key.Width > 0 {
			m.width = key.Width
		}
		if key.Height > 0 {
			m.height = key.Height
		}
		m.ensureCursorVisible()
		return m, nil
	case tea.KeyMsg:
		switch key.Type {
		case tea.KeyUp:
			return m, m.moveCursor(-1)
		case tea.KeyDown:
			return m, m.moveCursor(1)
		case tea.KeyPgUp:
			return m, m.moveCursorPage(-1)
		case tea.KeyPgDown:
			return m, m.moveCursorPage(1)
		case tea.KeyRunes:
			filtered, _ := stripMouseSGRRunes(key.Runes)
			if len(filtered) == 1 {
				switch filtered[0] {
				case 'k':
					return m, m.moveCursor(-1)
				case 'j':
					return m, m.moveCursor(1)
				}
			}
		case tea.KeyEnter:
			switch m.phase {
			case projectWorkspacePickerInitialLoading:
				return m, nil
			case projectWorkspacePickerFirstPageFailed:
				return m, m.startPageRequest(0, projectWorkspacePickerPageInitial)
			case projectWorkspacePickerEmpty:
				return m, m.startPageRequest(0, projectWorkspacePickerPageInitial)
			}
			if m.phase != projectWorkspacePickerReady && m.phase != projectWorkspacePickerReadyWithEdgeFailure {
				return m, nil
			}
			workspaces := m.workspaces()
			if len(workspaces) == 0 || m.cursor < 0 || m.cursor >= len(workspaces) {
				return m, nil
			}
			m.result = projectbinding.WorkspacePickerSelected{Workspace: workspaces[m.cursor]}
			return m, tea.Quit
		case tea.KeyEsc, tea.KeyCtrlC:
			if key.Type == tea.KeyEsc {
				m.result = projectbinding.WorkspacePickerBack{}
			} else {
				m.result = projectbinding.WorkspacePickerExit{}
			}
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *projectWorkspacePickerModel) View() string {
	var out strings.Builder
	out.WriteString(m.renderHeader())
	out.WriteString("\n\n")
	out.WriteString(tui.ApplyThemeStyleIntents(truncateQueuedMessageLine(projectWorkspacePickerNoticeText, m.width), m.theme, tui.ThemeForeground))
	out.WriteString("\n\n")
	if m.phase == projectWorkspacePickerReady || m.phase == projectWorkspacePickerReadyWithEdgeFailure {
		if status := renderStartupPickerStatus(projectStartupPickerStatus(m.startupStatus), m.width); status != "" {
			out.WriteString(status)
			out.WriteString("\n\n")
		}
	}
	switch m.phase {
	case projectWorkspacePickerInitialLoading:
		out.WriteString(m.styles.row.Render(pendingToolSpinnerFrame(m.spinnerFrame)))
		return out.String()
	case projectWorkspacePickerFirstPageFailed:
		if status := renderStartupPickerStatus(projectStartupPickerStatus(m.startupStatus), m.width); status != "" {
			out.WriteString(status)
			out.WriteString("\n\n")
		}
		out.WriteString(m.styles.headerWarning.Render("Workspaces unavailable. Press Enter to retry."))
		return out.String()
	case projectWorkspacePickerEmpty:
		out.WriteString(m.styles.row.Render("no workspace is attached to this project."))
		out.WriteString("\n")
		out.WriteString(m.styles.row.Render("Please attach workspace before continuing."))
		return out.String()
	}
	if status := m.renderEdgeStatus(&m.previousEdge); status != "" {
		out.WriteString(status)
		out.WriteString("\n")
	}
	for idx, row := range projectbinding.VisibleRows(projectbinding.VisibleRowsRequest{
		Offset:     m.offset,
		ItemCount:  len(m.workspaces()),
		LineBudget: m.visibleLineBudget(),
		HasPreview: m.hasPreview,
	}) {
		if idx > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(m.renderRow(row.Index, row.ShowPreview))
	}
	if status := m.renderEdgeStatus(&m.nextEdge); status != "" {
		if len(m.workspaces()) > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(status)
	}
	return out.String()
}

func (m *projectWorkspacePickerModel) renderEdgeStatus(edge *projectWorkspacePickerEdge) string {
	switch edge.state {
	case projectWorkspacePickerEdgeLoading:
		return m.styles.row.Render(pendingToolSpinnerFrame(m.spinnerFrame))
	case projectWorkspacePickerEdgeFailed:
		return m.styles.headerWarning.Render("Move toward this edge to retry.")
	default:
		return ""
	}
}

func (m *projectWorkspacePickerModel) visibleLineBudget() int {
	rows := m.height - 4
	if m.phase == projectWorkspacePickerReady || m.phase == projectWorkspacePickerReadyWithEdgeFailure {
		if renderStartupPickerStatus(projectStartupPickerStatus(m.startupStatus), m.width) != "" {
			rows -= 2
		}
	}
	if m.renderEdgeStatus(&m.previousEdge) != "" {
		rows--
	}
	if m.renderEdgeStatus(&m.nextEdge) != "" {
		rows--
	}
	if rows < 1 {
		return 1
	}
	return rows
}

func (m *projectWorkspacePickerModel) moveCursor(delta int) tea.Cmd {
	if m.phase != projectWorkspacePickerReady && m.phase != projectWorkspacePickerReadyWithEdgeFailure {
		return nil
	}
	workspaces := m.workspaces()
	if len(workspaces) == 0 {
		return nil
	}
	next := m.cursor + delta
	if next >= 0 && next < len(workspaces) {
		m.selectIndex(next)
		return m.prefetchAtSelection(delta)
	}
	if delta > 0 {
		return m.requestEdge(projectWorkspacePickerPageNext, true, false, 0)
	}
	return m.requestEdge(projectWorkspacePickerPagePrevious, true, false, 0)
}

func (m *projectWorkspacePickerModel) moveCursorPage(direction int) tea.Cmd {
	if direction != -1 && direction != 1 {
		panic("workspace picker page direction must be -1 or 1")
	}
	if m.phase != projectWorkspacePickerReady && m.phase != projectWorkspacePickerReadyWithEdgeFailure {
		return nil
	}
	workspaces := m.workspaces()
	if len(workspaces) == 0 {
		return nil
	}
	visible := m.visiblePageDistance()
	next := m.cursor + direction*visible
	if next >= 0 && next < len(workspaces) {
		m.selectIndex(next)
		return m.prefetchAtSelection(direction)
	}
	if direction > 0 {
		return m.requestEdge(projectWorkspacePickerPageNext, true, true, visible)
	}
	return m.requestEdge(projectWorkspacePickerPagePrevious, true, true, visible)
}

func (m *projectWorkspacePickerModel) workspaces() []*projectpb.ProjectWorkspaceCatalogSummary {
	return flattenBoundedPickerPages(m.segments, func(segment projectWorkspacePickerPageSegment) []*projectpb.ProjectWorkspaceCatalogSummary {
		return segment.workspaces
	})
}

func (m *projectWorkspacePickerModel) selectIndex(index int) {
	workspaces := m.workspaces()
	if index < 0 || index >= len(workspaces) {
		return
	}
	m.cursor = index
	var count int
	for _, segment := range m.segments {
		if index < count+len(segment.workspaces) {
			local := index - count
			m.selected = &projectWorkspacePickerOccurrence{
				generation: segment.generation, offset: segment.offset, index: local,
				workspace: segment.workspaces[local].WorkspaceId,
			}
			break
		}
		count += len(segment.workspaces)
	}
	m.ensureCursorVisible()
}

func (m *projectWorkspacePickerModel) ensureCursorVisible() {
	workspaces := m.workspaces()
	if len(workspaces) == 0 {
		m.cursor, m.offset, m.viewport = 0, 0, nil
		return
	}
	m.cursor = projectbinding.MoveCursor(m.cursor, 0, len(workspaces))
	m.offset = projectbinding.EnsureCursorVisible(m.cursor, m.offset, projectbinding.VisibleRowsRequest{
		ItemCount: len(workspaces), LineBudget: m.visibleLineBudget(), HasPreview: m.hasPreview,
	})
	visible := projectbinding.VisibleRows(projectbinding.VisibleRowsRequest{
		Offset: m.offset, ItemCount: len(workspaces), LineBudget: m.visibleLineBudget(), HasPreview: m.hasPreview,
	})
	if len(visible) == 0 {
		return
	}
	top := visible[0].Index
	count := 0
	for _, segment := range m.segments {
		if top < count+len(segment.workspaces) {
			local := top - count
			m.viewport = &projectWorkspacePickerOccurrence{
				generation: segment.generation, offset: segment.offset, index: local,
				workspace: segment.workspaces[local].WorkspaceId,
			}
			return
		}
		count += len(segment.workspaces)
	}
}

func (m *projectWorkspacePickerModel) prefetchAtSelection(direction int) tea.Cmd {
	if len(m.segments) == 0 {
		return nil
	}
	visible := m.visiblePageDistance()
	if direction < 0 && m.cursor < visible {
		if cmd := m.requestEdge(projectWorkspacePickerPagePrevious, false, false, 0); cmd != nil {
			return cmd
		}
	}
	if direction > 0 && len(m.workspaces())-1-m.cursor < visible {
		return m.requestEdge(projectWorkspacePickerPageNext, false, false, 0)
	}
	return nil
}

func (m *projectWorkspacePickerModel) visiblePageDistance() int {
	visible := len(projectbinding.VisibleRows(projectbinding.VisibleRowsRequest{
		Offset: m.offset, ItemCount: len(m.workspaces()),
		LineBudget: m.visibleLineBudget(), HasPreview: m.hasPreview,
	}))
	if visible < 1 {
		return 1
	}
	return visible
}

func (m *projectWorkspacePickerModel) reconcileSpinner() tea.Cmd {
	if !m.hasPendingRequest() || m.spinnerPending != nil {
		return nil
	}
	m.spinnerSequence++
	generation := m.spinnerSequence
	m.spinnerPending = &generation
	return tea.Tick(spinnerTickInterval, func(time.Time) tea.Msg {
		return projectWorkspacePickerSpinnerTickMsg{generation: generation}
	})
}

func (m *projectWorkspacePickerModel) requestEdge(direction projectWorkspacePickerPageDirection, crossing bool, pageMove bool, visibleDistance int) tea.Cmd {
	if len(m.segments) == 0 {
		return nil
	}
	edge := m.edge(direction)
	if edge.request != nil {
		return nil
	}
	if edge.state == projectWorkspacePickerEdgeExhausted {
		return nil
	}
	pageMoveOverflow := m.pageMoveOverflow(direction, pageMove, visibleDistance)
	if edge.state == projectWorkspacePickerEdgeFailed {
		request, ok := m.retryEdge(direction, crossing, pageMove, visibleDistance, pageMoveOverflow)
		if !ok {
			return nil
		}
		return m.loadPageRequest(request)
	}
	offset, ok := m.edgeOffset(direction)
	if !ok {
		edge.state = projectWorkspacePickerEdgeExhausted
		return nil
	}
	return m.startPageRequestWithIntent(offset, direction, crossing, pageMove, visibleDistance, pageMoveOverflow)
}

func (m *projectWorkspacePickerModel) retryEdge(
	direction projectWorkspacePickerPageDirection,
	crossing bool,
	pageMove bool,
	visibleDistance int,
	pageMoveOverflow int,
) (projectWorkspacePickerPageRequest, bool) {
	edge := m.edge(direction)
	if edge.failedRequest == nil {
		return projectWorkspacePickerPageRequest{}, false
	}
	offset, ok := m.edgeOffset(direction)
	if !ok {
		return projectWorkspacePickerPageRequest{}, false
	}
	if visibleDistance < 1 {
		visibleDistance = edge.failedRequest.visibleDistance
	}
	return m.begin(
		direction,
		offset,
		m.pageBoundary(direction),
		crossing,
		pageMove,
		visibleDistance,
		pageMoveOverflow,
		edge.failedRequest.move,
	)
}

func (m *projectWorkspacePickerModel) pageMoveOverflow(
	direction projectWorkspacePickerPageDirection,
	pageMove bool,
	visibleDistance int,
) int {
	if !pageMove {
		return 0
	}
	if visibleDistance < 1 {
		visibleDistance = 1
	}
	if direction == projectWorkspacePickerPagePrevious {
		return m.cursor - visibleDistance
	}
	return m.cursor + visibleDistance - len(m.workspaces())
}

func (m *projectWorkspacePickerModel) edgeOffset(direction projectWorkspacePickerPageDirection) (int, bool) {
	switch direction {
	case projectWorkspacePickerPagePrevious:
		first := m.segments[0]
		if first.offset == 0 {
			return 0, false
		}
		offset := first.offset - projectWorkspacePickerPageSize
		if offset < 0 {
			offset = 0
		}
		return offset, true
	case projectWorkspacePickerPageNext:
		last := m.segments[len(m.segments)-1]
		if last.nextOffset == nil {
			return 0, false
		}
		return int(*last.nextOffset), true
	default:
		return 0, false
	}
}

func (m *projectWorkspacePickerModel) startPageRequest(offset int, direction projectWorkspacePickerPageDirection) tea.Cmd {
	return m.startPageRequestWithIntent(offset, direction, false, false, 0, 0)
}

func (m *projectWorkspacePickerModel) startPageRequestWithIntent(
	offset int,
	direction projectWorkspacePickerPageDirection,
	crossing bool,
	pageMove bool,
	visibleDistance int,
	pageMoveOverflow int,
) tea.Cmd {
	request, ok := m.begin(
		direction,
		offset,
		m.pageBoundary(direction),
		crossing,
		pageMove,
		visibleDistance,
		pageMoveOverflow,
		0,
	)
	if !ok {
		return nil
	}
	if direction == projectWorkspacePickerPageInitial {
		m.phase = projectWorkspacePickerInitialLoading
		m.segments = nil
		m.selected = nil
		m.viewport = nil
		m.cursor, m.offset = 0, 0
		m.failure = nil
	}
	return m.loadPageRequest(request)
}

func (m *projectWorkspacePickerModel) loadPageRequest(request projectWorkspacePickerPageRequest) tea.Cmd {
	return func() tea.Msg {
		response, err := m.loader.ListProjectWorkspaces(m.requestContext, &projectpb.ProjectWorkspaceListRequest{
			ProjectId: m.projectID, Offset: int32(request.offset), Limit: projectWorkspacePickerPageSize,
		})
		return projectWorkspacePickerPageLoadedMsg{request: request, response: response, err: err}
	}
}

func (m *projectWorkspacePickerModel) applyPageLoaded(message projectWorkspacePickerPageLoadedMsg) tea.Cmd {
	active := m.requestFor(message.request.direction)
	if active == nil || *active != message.request {
		return nil
	}
	request := *active
	if request.direction != projectWorkspacePickerPageInitial && !m.pageBoundaryMatches(request) {
		m.completeStalePage(request)
		return nil
	}
	if message.err != nil {
		m.failPage(request, sessionPickerFailurePageRequest, message.err)
		return nil
	}
	if err := client.ValidateProjectWorkspacePage(message.response, &projectpb.ProjectWorkspaceListRequest{
		ProjectId: m.projectID,
		Offset:    int32(request.offset),
		Limit:     projectWorkspacePickerPageSize,
	}); err != nil {
		m.failPage(request, sessionPickerFailurePageContract, err)
		return nil
	}
	segment := projectWorkspacePickerPageSegment{
		generation: request.generation, offset: request.offset,
		workspaces: cloneProjectWorkspaceCatalogRows(message.response.Workspaces),
		nextOffset: cloneInt32(message.response.NextOffset),
	}
	if request.direction == projectWorkspacePickerPageInitial {
		if len(segment.workspaces) == 0 {
			m.complete(request)
			m.clearFailure(startupPickerWorkspaceOperationFirstPage, request.generation)
			m.phase = projectWorkspacePickerEmpty
			m.previousEdge.state, m.nextEdge.state = projectWorkspacePickerEdgeExhausted, projectWorkspacePickerEdgeExhausted
			return nil
		}
		if len(segment.workspaces) == 1 && segment.nextOffset == nil {
			m.complete(request)
			m.clearFailure(startupPickerWorkspaceOperationFirstPage, request.generation)
			m.result = projectbinding.WorkspacePickerSelected{Workspace: segment.workspaces[0]}
			return tea.Quit
		}
		m.complete(request)
		m.segments = []projectWorkspacePickerPageSegment{segment}
		m.selectIndex(0)
		m.clearFailure(startupPickerWorkspaceOperationFirstPage, request.generation)
		m.previousEdge.state = projectWorkspacePickerEdgeExhausted
		if segment.nextOffset == nil {
			m.nextEdge.state = projectWorkspacePickerEdgeExhausted
		} else {
			m.nextEdge.state = projectWorkspacePickerEdgeUnknown
		}
		m.phase = projectWorkspacePickerReady
		return nil
	}
	if len(segment.workspaces) == 0 {
		m.complete(request)
		edge := m.edge(request.direction)
		edge.state = projectWorkspacePickerEdgeExhausted
		edge.requestedOffset = request.offset
		edge.generation = request.generation
		edge.diagnostic = nil
		m.phase, m.failure = m.edgeFailurePhase(), nil
		m.clearFailure(m.operationID(request.direction), request.generation)
		return nil
	}
	m.complete(request)
	candidate := append([]projectWorkspacePickerPageSegment(nil), m.segments...)
	if request.direction == projectWorkspacePickerPageNext {
		candidate = appendBoundedPickerPage(candidate, segment)
	} else {
		candidate = prependBoundedPickerPage(candidate, segment)
	}
	if !occurrenceInSegments(m.selected, candidate) || !occurrenceInSegments(m.viewport, candidate) {
		edge := m.edge(request.direction)
		edge.state = projectWorkspacePickerEdgeUnknown
		m.clearFailure(m.operationID(request.direction), request.generation)
		m.phase = m.edgeFailurePhase()
		return nil
	}
	m.segments = candidate
	edge := m.edge(request.direction)
	edge.state = projectWorkspacePickerEdgeUnknown
	if request.direction == projectWorkspacePickerPageNext && segment.nextOffset == nil {
		edge.state = projectWorkspacePickerEdgeExhausted
	}
	if request.direction == projectWorkspacePickerPagePrevious && segment.offset == 0 {
		edge.state = projectWorkspacePickerEdgeExhausted
	}
	m.syncBoundaryEdges()
	m.phase, m.failure = m.edgeFailurePhase(), nil
	m.clearFailure(m.operationID(request.direction), request.generation)
	if request.crossing && len(segment.workspaces) > 0 {
		local := len(segment.workspaces) - 1
		if request.direction == projectWorkspacePickerPageNext {
			local = 0
			if request.pageMove {
				local = request.pageMoveOverflow
				if local < 0 {
					local = 0
				}
				if local >= len(segment.workspaces) {
					local = len(segment.workspaces) - 1
				}
			}
		} else if request.pageMove {
			local = len(segment.workspaces) + request.pageMoveOverflow
			if local < 0 {
				local = 0
			}
		}
		m.selected = &projectWorkspacePickerOccurrence{
			generation: segment.generation, offset: segment.offset,
			index: local, workspace: segment.workspaces[local].WorkspaceId,
		}
	}
	m.remapCursor()
	if request.crossing {
		m.ensureCursorVisible()
	}
	return nil
}

func (m *projectWorkspacePickerModel) pageBoundary(direction projectWorkspacePickerPageDirection) *startupPickerPageBoundary {
	if len(m.segments) == 0 {
		return nil
	}
	switch direction {
	case projectWorkspacePickerPagePrevious:
		first := m.segments[0]
		if len(first.workspaces) == 0 {
			return nil
		}
		return &startupPickerPageBoundary{
			generation: first.generation,
			offset:     first.offset,
			index:      0,
		}
	case projectWorkspacePickerPageNext:
		last := m.segments[len(m.segments)-1]
		if len(last.workspaces) == 0 {
			return nil
		}
		return &startupPickerPageBoundary{
			generation: last.generation,
			offset:     last.offset,
			index:      len(last.workspaces) - 1,
		}
	default:
		return nil
	}
}

func (m *projectWorkspacePickerModel) pageBoundaryMatches(request projectWorkspacePickerPageRequest) bool {
	boundary := request.boundary
	if boundary == nil || len(m.segments) == 0 {
		return false
	}
	switch request.direction {
	case projectWorkspacePickerPagePrevious:
		first := m.segments[0]
		if len(first.workspaces) == 0 ||
			boundary.generation != first.generation ||
			boundary.offset != first.offset ||
			boundary.index != 0 ||
			first.offset == 0 {
			return false
		}
		expectedOffset := first.offset - projectWorkspacePickerPageSize
		if expectedOffset < 0 {
			expectedOffset = 0
		}
		return request.offset == expectedOffset
	case projectWorkspacePickerPageNext:
		last := m.segments[len(m.segments)-1]
		if len(last.workspaces) == 0 ||
			boundary.generation != last.generation ||
			boundary.offset != last.offset ||
			boundary.index != len(last.workspaces)-1 ||
			last.nextOffset == nil {
			return false
		}
		return request.offset == int(*last.nextOffset)
	default:
		return false
	}
}

func (m *projectWorkspacePickerModel) completeStalePage(request projectWorkspacePickerPageRequest) {
	if !m.complete(request) {
		return
	}
	edge := m.edge(request.direction)
	edge.state = projectWorkspacePickerEdgeUnknown
	m.clearFailure(m.operationID(request.direction), request.generation)
	m.syncBoundaryEdges()
	m.phase, m.failure = m.edgeFailurePhase(), nil
}

func (m *projectWorkspacePickerModel) syncBoundaryEdges() {
	if len(m.segments) == 0 {
		return
	}
	m.invalidateStaleFailedEdge(projectWorkspacePickerPagePrevious)
	m.invalidateStaleFailedEdge(projectWorkspacePickerPageNext)
	first := m.segments[0]
	if m.previousEdge.state != projectWorkspacePickerEdgeFailed && m.previousEdge.state != projectWorkspacePickerEdgeLoading {
		m.previousEdge.state = projectWorkspacePickerEdgeUnknown
		if first.offset == 0 {
			m.previousEdge.state = projectWorkspacePickerEdgeExhausted
		}
	}
	last := m.segments[len(m.segments)-1]
	if m.nextEdge.state != projectWorkspacePickerEdgeFailed && m.nextEdge.state != projectWorkspacePickerEdgeLoading {
		m.nextEdge.state = projectWorkspacePickerEdgeUnknown
		if last.nextOffset == nil {
			m.nextEdge.state = projectWorkspacePickerEdgeExhausted
		}
	}
}

func (m *projectWorkspacePickerModel) invalidateStaleFailedEdge(direction projectWorkspacePickerPageDirection) {
	edge := m.edge(direction)
	if edge.state != projectWorkspacePickerEdgeFailed || edge.failedRequest == nil {
		return
	}
	failedRequest := *edge.failedRequest
	if m.pageBoundaryMatches(failedRequest) {
		return
	}
	edge.state = projectWorkspacePickerEdgeUnknown
	edge.failedRequest = nil
	edge.diagnostic = nil
	m.clearFailure(m.operationID(direction), failedRequest.generation)
}

func (m *projectWorkspacePickerModel) edgeFailurePhase() projectWorkspacePickerPhase {
	if m.previousEdge.state == projectWorkspacePickerEdgeFailed ||
		m.nextEdge.state == projectWorkspacePickerEdgeFailed {
		return projectWorkspacePickerReadyWithEdgeFailure
	}
	return projectWorkspacePickerReady
}

func occurrenceInSegments(occurrence *projectWorkspacePickerOccurrence, segments []projectWorkspacePickerPageSegment) bool {
	if occurrence == nil {
		return true
	}
	for _, segment := range segments {
		if segment.generation == occurrence.generation && segment.offset == occurrence.offset &&
			occurrence.index >= 0 && occurrence.index < len(segment.workspaces) &&
			segment.workspaces[occurrence.index].WorkspaceId == occurrence.workspace {
			return true
		}
	}
	return false
}

func (m *projectWorkspacePickerModel) remapCursor() {
	if m.selected == nil {
		m.selectIndex(0)
		return
	}
	if index, ok := occurrenceIndex(m.selected, m.segments); ok {
		m.cursor = index
		if viewport, ok := occurrenceIndex(m.viewport, m.segments); ok {
			m.offset = viewport
		} else {
			m.offset = projectbinding.EnsureCursorVisible(m.cursor, m.offset, projectbinding.VisibleRowsRequest{
				ItemCount: len(m.workspaces()), LineBudget: m.visibleLineBudget(), HasPreview: m.hasPreview,
			})
		}
		return
	}
	m.selectIndex(0)
}

func occurrenceIndex(occurrence *projectWorkspacePickerOccurrence, segments []projectWorkspacePickerPageSegment) (int, bool) {
	if occurrence == nil {
		return 0, false
	}
	index := 0
	for _, segment := range segments {
		if segment.generation == occurrence.generation && segment.offset == occurrence.offset &&
			occurrence.index >= 0 && occurrence.index < len(segment.workspaces) &&
			segment.workspaces[occurrence.index].WorkspaceId == occurrence.workspace {
			return index + occurrence.index, true
		}
		index += len(segment.workspaces)
	}
	return 0, false
}

func cloneInt32(value *int32) *int32 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneProjectWorkspaceCatalogRows(rows []*projectpb.ProjectWorkspaceCatalogSummary) []*projectpb.ProjectWorkspaceCatalogSummary {
	cloned := make([]*projectpb.ProjectWorkspaceCatalogSummary, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		cloned = append(cloned, proto.Clone(row).(*projectpb.ProjectWorkspaceCatalogSummary))
	}
	return cloned
}

func (m *projectWorkspacePickerModel) failPage(request projectWorkspacePickerPageRequest, kind sessionPickerFailureKind, diagnostic error) {
	if request.direction == projectWorkspacePickerPageInitial {
		m.complete(request)
		m.phase = projectWorkspacePickerFirstPageFailed
		m.segments = nil
		m.selected, m.viewport = nil, nil
		m.cursor, m.offset = 0, 0
		m.failure = diagnostic
		m.recordFailure(startupPickerWorkspaceOperationFirstPage, request, kind, diagnostic)
		return
	}
	m.fail(request, diagnostic)
	m.phase = projectWorkspacePickerReadyWithEdgeFailure
	m.recordFailure(m.operationID(request.direction), request, kind, diagnostic)
}

func (m *projectWorkspacePickerModel) operationID(direction projectWorkspacePickerPageDirection) startupPickerWorkspaceOperationKind {
	switch direction {
	case projectWorkspacePickerPagePrevious:
		return startupPickerWorkspaceOperationPreviousEdge
	case projectWorkspacePickerPageNext:
		return startupPickerWorkspaceOperationNextEdge
	default:
		return startupPickerWorkspaceOperationFirstPage
	}
}

func (m *projectWorkspacePickerModel) recordFailure(operation startupPickerWorkspaceOperationKind, request projectWorkspacePickerPageRequest, kind sessionPickerFailureKind, diagnostic error) {
	m.startupStatus.Record(startupPickerStatusFailure{
		Operation:  startupPickerWorkspaceOperation{kind: operation},
		Generation: request.generation,
		Kind:       kind,
		Diagnostic: diagnostic,
	})
}

func (m *projectWorkspacePickerModel) clearFailure(operation startupPickerWorkspaceOperationKind, generation uint64) {
	m.startupStatus.ClearWorkspace(operation, generation)
}
func (m *projectWorkspacePickerModel) renderHeader() string {
	if m.headerMD != nil {
		rendered := m.headerMD.Render(projectWorkspacePickerHeaderMarkdown, m.width)
		return tui.ApplyThemeStyleIntents(trimRenderedHeaderInset(rendered), m.theme, tui.ThemeForeground)
	}
	return m.styles.headerFallback.Render(projectWorkspacePickerHeaderFallback)
}

func (m *projectWorkspacePickerModel) renderRow(index int, showPreview bool) string {
	selected := index == m.cursor
	workspace := m.workspaces()[index]
	row := projectbinding.WorkspaceRowText(workspace.DisplayName, workspace.RootPath, projectBindingHomeDir())
	markerStyle := m.styles.marker
	rowStyle := m.styles.row
	marker := "◈"
	if selected {
		markerStyle = m.styles.markerSelected
		rowStyle = m.styles.rowSelected
	}
	left := markerStyle.Render(marker) + " " + rowStyle.Render(row.Title)
	if row.Preview == "" || !showPreview {
		return left
	}
	previewWidth := m.width - 2
	if previewWidth < 1 {
		previewWidth = 1
	}
	previewLine := "  " + m.styles.preview.Render(truncateQueuedMessageLine(row.Preview, previewWidth))
	return left + "\n" + previewLine
}

func projectBindingTimestamp(ts time.Time) string {
	if ts.IsZero() {
		return "unknown"
	}
	return ts.Local().Format("2006-01-02 15:04")
}

func (m *projectWorkspacePickerModel) hasPreview(index int) bool {
	workspaces := m.workspaces()
	if index < 0 || index >= len(workspaces) {
		return false
	}
	return strings.TrimSpace(workspaces[index].RootPath) != ""
}

func runProjectWorkspacePicker(ctx context.Context, loader projectbinding.WorkspacePageLoader, projectID string, theme string) (projectWorkspacePickerResult, error) {
	requestContext, cancel := context.WithCancel(ctx)
	defer cancel()
	model := newProjectWorkspacePickerModel(requestContext, loader, projectID, theme)
	finalModel, err := runContextualStartupPicker(ctx, model)
	if err != nil {
		return nil, err
	}
	picked, ok := finalModel.(*projectWorkspacePickerModel)
	if !ok {
		return nil, fmt.Errorf("unexpected workspace picker model type %T", finalModel)
	}
	if picked.result == nil {
		return nil, errors.New("workspace picker exited without a result")
	}
	return picked.result, nil
}
