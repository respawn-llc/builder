package tui

import (
	"core/shared/clientui"
	"core/shared/theme"
	"core/shared/transcript"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type Mode string

const (
	ModeOngoing Mode = "ongoing"
	ModeDetail  Mode = "detail"

	DefaultPreviewLines = 8
	TranscriptDivider   = "────────────────────────"
	detailItemSeparator = ""
)

type TranscriptEntry struct {
	Visibility        transcript.EntryVisibility
	RollbackTargetID  string
	Transient         bool
	Committed         bool
	Role              TranscriptRole
	Text              string
	CondensedText     string
	Phase             clientui.MessagePhase
	MessageType       clientui.MessageType
	SourcePath        string
	CompactLabel      string
	ToolResultSummary string
	ToolCallID        string
	NoticeID          string
	ToolCall          *transcript.ToolCallMeta
}

type VisibleLineKind uint8

const (
	VisibleLineContent VisibleLineKind = iota
	VisibleLineDivider
)

type StreamingReasoningEntry struct {
	Key  string
	Role TranscriptRole
	Text string
}

type ToggleModeMsg struct {
	SkipDetailWarmup bool
}

type SetModeMsg struct {
	Mode             Mode
	SkipDetailWarmup bool
}

type SetViewportLinesMsg struct {
	Lines int
}

type SetViewportSizeMsg struct {
	Lines int
	Width int
}

type AppendTranscriptMsg struct {
	Visibility        transcript.EntryVisibility
	Transient         bool
	Committed         bool
	Role              TranscriptRole
	Text              string
	CondensedText     string
	Phase             clientui.MessagePhase
	MessageType       clientui.MessageType
	SourcePath        string
	CompactLabel      string
	ToolResultSummary string
	ToolCallID        string
	NoticeID          string
	ToolCall          *transcript.ToolCallMeta
}

type SetConversationMsg struct {
	BaseOffset   int
	TotalEntries int
	Entries      []TranscriptEntry
	Ongoing      string
	OngoingError string
}

type SetSelectedTranscriptEntryMsg struct {
	EntryIndex            int
	Active                bool
	RefreshDetailSnapshot bool
}

type FocusTranscriptEntryMsg struct {
	EntryIndex int
	Center     bool
	Bottom     bool
}

type StreamAssistantMsg struct {
	Delta string
}

type ClearOngoingAssistantMsg struct{}

type UpsertStreamingReasoningMsg struct {
	Key  string
	Role string
	Text string
}

type ClearStreamingReasoningMsg struct{}

type CommitAssistantMsg struct{}

type SetOngoingErrorMsg struct {
	Err error
}

type ClearOngoingErrorMsg struct{}

type Option func(*Model)

type RenderDiagnosticSeverity string

const (
	RenderDiagnosticSeverityInfo  RenderDiagnosticSeverity = "info"
	RenderDiagnosticSeverityWarn  RenderDiagnosticSeverity = "warn"
	RenderDiagnosticSeverityError RenderDiagnosticSeverity = "error"
	RenderDiagnosticSeverityFatal RenderDiagnosticSeverity = "fatal"
)

type RenderDiagnostic struct {
	Component string
	Message   string
	Err       error
	Severity  RenderDiagnosticSeverity
}

type RenderDiagnosticHandler func(RenderDiagnostic)

func WithPreviewLines(lines int) Option {
	return func(m *Model) {
		if lines > 0 {
			m.viewportLines = lines
		}
	}
}

func WithTheme(themeName string) Option {
	return func(m *Model) {
		m.theme = theme.Resolve(themeName)
	}
}

func WithRenderDiagnosticHandler(handler RenderDiagnosticHandler) Option {
	return func(m *Model) {
		m.renderDiagnosticHandler = handler
	}
}

func WithCompactDetail() Option {
	return func(m *Model) {
		m.compactDetail = true
	}
}

type Model struct {
	mode Mode

	compactDetail bool
	viewportLines int
	viewportWidth int
	detailScroll  int
	toolSymbolGap int

	transcriptInput TranscriptProjectionInput

	selectedTranscriptEntry  int
	selectedTranscriptActive bool
	detailSelectedEntry      int
	detailSelectedActive     bool
	detailExpandedEntries    map[int]struct{}

	detailBottomAnchor      bool
	detailBottomOffset      int
	ongoingError            string
	theme                   string
	md                      *markdownRenderer
	code                    *codeRenderer
	viewProjector           *TranscriptViewProjector
	renderDiagnosticHandler RenderDiagnosticHandler
}

func (m Model) DetailScroll() int {
	if m.detailBottomAnchor {
		return m.detailBottomOffset
	}
	return m.detailScroll
}

func (m Model) DetailMaxScroll() int {
	return m.maxDetailScroll()
}

func (m Model) DetailMetricsResolved() bool {
	return !m.detailBottomAnchor
}

func (m Model) DetailSelectedEntry() (int, bool) {
	selectedEntry, ok := m.resolveDetailSelection()
	if !ok {
		return 0, false
	}
	return selectedEntry, true
}

func (m Model) DetailSelectedExpansionAction() (string, bool) {
	state, ok := m.detailSelectedExpansionState()
	if !ok {
		return "", false
	}
	if state.expanded {
		return "collapse", true
	}
	return "expand", true
}

func (m Model) TranscriptBaseOffset() int {
	return m.transcriptInput.BaseOffset
}

func (m Model) TranscriptTotalEntries() int {
	if m.transcriptInput.TotalEntries > 0 {
		return m.transcriptInput.TotalEntries
	}
	return m.transcriptInput.BaseOffset + len(m.transcriptInput.Entries)
}

func (m Model) LoadedTranscriptEntryCount() int {
	return len(m.transcriptInput.Entries)
}

func (m Model) LoadedTranscriptEntries() []TranscriptEntry {
	if len(m.transcriptInput.Entries) == 0 {
		return nil
	}
	entries := make([]TranscriptEntry, 0, len(m.transcriptInput.Entries))
	for _, entry := range m.transcriptInput.Entries {
		copyEntry := entry
		copyEntry.ToolCall = cloneToolCallMeta(entry.ToolCall)
		entries = append(entries, copyEntry)
	}
	return entries
}

func (m Model) DetailVisibleEntryRange() (int, int, bool) {
	first := -1
	last := -1
	for _, entryIndex := range m.detailViewProjection().DetailViewport(m.currentDetailViewportState()).Owners {
		if entryIndex < 0 {
			continue
		}
		if first < 0 {
			first = entryIndex
		}
		last = entryIndex
	}
	if first < 0 || last < 0 {
		return 0, 0, false
	}
	return first, last, true
}

func (m Model) absoluteTranscriptIndex(localIndex int) int {
	if localIndex < 0 {
		return -1
	}
	return m.transcriptInput.BaseOffset + localIndex
}

func (m Model) localTranscriptIndex(absoluteIndex int) (int, bool) {
	local := absoluteIndex - m.transcriptInput.BaseOffset
	if local < 0 || local >= len(m.transcriptInput.Entries) {
		return 0, false
	}
	return local, true
}

type ongoingBlock struct {
	role       RenderIntent
	lines      []string
	entryIndex int
	entryEnd   int
}

type lineRange struct {
	Start int
	End   int
}

type detailBlockSpec struct {
	role       RenderIntent
	entryIndex int
	entryEnd   int
	selectable bool
	expanded   bool
	expandable bool
	render     func(Model, string) []string
}

type detailProjectionLookup struct {
	projection             TranscriptViewProjection
	blocks                 []TranscriptProjectionBlock
	selectableBlockIndexes map[int]int
}

func newDetailProjectionLookup(projection TranscriptViewProjection) detailProjectionLookup {
	blocks := projection.Detail.Blocks
	indexes := projection.DetailSelectableBlocks
	if indexes == nil && len(blocks) > 0 {
		indexes = projection.Detail.SelectableBlockIndexes()
	}
	return detailProjectionLookup{
		projection:             projection,
		blocks:                 blocks,
		selectableBlockIndexes: indexes,
	}
}

func (l detailProjectionLookup) blockIndexForEntry(entryIndex int) int {
	if entryIndex < 0 {
		return -1
	}
	if idx, ok := l.selectableBlockIndexes[entryIndex]; ok {
		return idx
	}
	return -1
}

func (l detailProjectionLookup) ownsSelectableEntry(lineIndex int, owners []int) bool {
	if lineIndex < 0 || lineIndex >= len(owners) {
		return false
	}
	return l.blockIndexForEntry(owners[lineIndex]) >= 0
}

type ongoingLineParts struct {
	base             []TranscriptProjectionLine
	streaming        []TranscriptProjectionLine
	streamingDivider bool
}

func NewModel(opts ...Option) Model {
	m := Model{
		mode:          ModeOngoing,
		viewportLines: DefaultPreviewLines,
		viewportWidth: 120,
		toolSymbolGap: 1,
		theme:         theme.Resolve(""),
		viewProjector: &TranscriptViewProjector{},
	}
	for _, opt := range opts {
		opt(&m)
	}
	m.md = newMarkdownRenderer(m.theme, m.reportRenderDiagnostic)
	m.code = newCodeRenderer(m.theme)
	return m
}

func (m Model) reportRenderDiagnostic(diag RenderDiagnostic) {
	if strings.TrimSpace(diag.Message) == "" && diag.Err != nil {
		diag.Message = diag.Err.Error()
	}
	if strings.TrimSpace(diag.Component) == "" {
		diag.Component = "render"
	}
	if strings.TrimSpace(string(diag.Severity)) == "" {
		diag.Severity = RenderDiagnosticSeverityWarn
	}
	if m.renderDiagnosticHandler != nil {
		m.renderDiagnosticHandler(diag)
	}
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.reduce(msg)
	return m, nil
}

func (m Model) View() string {
	if m.mode == ModeDetail {
		return m.renderDetailSnapshot()
	}
	return m.renderOngoing()
}

func (m *Model) VisibleLineKinds() []VisibleLineKind {
	if m == nil {
		return nil
	}
	if m.mode == ModeDetail {
		return visibleKindsForViewport(m.detailViewProjection().DetailViewport(m.currentDetailViewportState()).Kinds, m.viewportLines)
	}
	return m.visibleOngoingLineKinds()
}

func visibleKindsForViewport(kinds []VisibleLineKind, viewportLines int) []VisibleLineKind {
	if viewportLines <= 0 {
		return nil
	}
	if len(kinds) == 0 {
		return append(make([]VisibleLineKind, 0, viewportLines), VisibleLineContent)
	}
	out := append([]VisibleLineKind(nil), kinds...)
	for len(out) < viewportLines {
		out = append(out, VisibleLineContent)
	}
	if len(out) > viewportLines {
		out = out[:viewportLines]
	}
	return out
}

func (m Model) Mode() Mode {
	return m.mode
}

func (m Model) OngoingScroll() int {
	return m.maxOngoingScroll()
}

func (m Model) OngoingSnapshot() string {
	return strings.Join(m.ongoingLines(), "\n")
}

func (m Model) LiveOngoingLines() []TranscriptProjectionLine {
	return m.liveOngoingLines(uniformPendingSpinnerFrame(""))
}

func (m Model) LiveOngoingLinesWithPendingSpinnerFrame(spinner string) []TranscriptProjectionLine {
	return m.liveOngoingLines(uniformPendingSpinnerFrame(spinner))
}

func (m Model) PendingOngoingLines() []TranscriptProjectionLine {
	return m.pendingLiveOngoingLines(uniformPendingSpinnerFrame("")).lines
}

func (m Model) PendingOngoingLinesWithPendingSpinnerFrame(spinner string) []TranscriptProjectionLine {
	return m.pendingLiveOngoingLines(uniformPendingSpinnerFrame(spinner)).lines
}

func (m Model) StreamingOngoingLines() []TranscriptProjectionLine {
	return m.streamingOngoingProjectionLines()
}

func (m Model) OngoingStreamingText() string {
	return m.transcriptInput.Ongoing
}

func (m Model) OngoingErrorText() string {
	return m.ongoingError
}

func FormatOngoingError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return "error"
	}
	return fmt.Sprintf("error: %s", msg)
}

func (m Model) transitionMode(target Mode, skipDetailWarmup bool) Model {
	if target == "" || target == m.mode {
		return m
	}
	switch target {
	case ModeDetail:
		m.mode = ModeDetail
		m.detailBottomAnchor = true
		m.detailBottomOffset = 0
		if skipDetailWarmup {
			return m
		}
		m.refreshDetailViewport()
		if m.compactDetail {
			m.focusVisibleDetailEntry(len(m.detailViewProjection().DetailViewport(m.currentDetailViewportState()).Owners) - 1)
		}
	case ModeOngoing:
		m.mode = ModeOngoing
	}
	return m
}

func (m Model) scrollDetail(delta int) Model {
	if m.moveDetailSelectionTowardCenterAtScrollEdge(delta) {
		return m
	}
	if moved := m.scrollDetailLine(delta); moved {
		m.focusVisibleDetailEntry(m.viewportLines / 2)
		return m
	}
	m.moveDetailSelectionWithinViewport(delta)
	return m
}

func (m *Model) scrollDetailLine(delta int) bool {
	if m.detailBottomAnchor {
		before := m.detailBottomOffset
		nextOffset := m.detailBottomOffset - delta
		if nextOffset < 0 {
			nextOffset = 0
		}
		m.detailBottomOffset = nextOffset
		m.refreshDetailViewport()
		return m.detailBottomOffset != before
	}
	m.ensureDetailScrollResolved()
	before := m.detailScroll
	m.detailScroll = clamp(m.detailScroll+delta, 0, m.maxDetailScroll())
	m.refreshDetailViewport()
	return m.detailScroll != before
}

func (m *Model) ensureDetailSelection() {
	if m == nil {
		return
	}
	lookup := newDetailProjectionLookup(m.detailViewProjection())
	if m.detailSelectedActive && lookup.blockIndexForEntry(m.detailSelectedEntry) >= 0 {
		return
	}
	blocks := lookup.blocks
	for idx := len(blocks) - 1; idx >= 0; idx-- {
		if !blocks[idx].Selectable {
			continue
		}
		m.detailSelectedEntry = blocks[idx].EntryIndex
		m.detailSelectedActive = true
		return
	}
	m.detailSelectedEntry = -1
	m.detailSelectedActive = false
}

func (m *Model) focusVisibleDetailEntry(anchor int) {
	if m == nil || !m.compactDetail {
		return
	}
	lookup := newDetailProjectionLookup(m.detailViewProjection())
	owners := lookup.projection.DetailViewport(m.currentDetailViewportState()).Owners
	if len(owners) == 0 {
		m.ensureDetailSelection()
		return
	}
	if anchor >= len(owners) {
		anchor = len(owners) - 1
	}
	if anchor < 0 {
		m.ensureDetailSelection()
		return
	}
	bestEntry := -1
	bestDistance := len(owners) + 1
	for lineIndex, entryIndex := range owners {
		if lookup.blockIndexForEntry(entryIndex) < 0 {
			continue
		}
		distance := detailLineDistance(lineIndex, anchor)
		if distance >= bestDistance {
			continue
		}
		bestEntry = entryIndex
		bestDistance = distance
	}
	if bestEntry < 0 {
		m.ensureDetailSelection()
		return
	}
	previousEntry := m.detailSelectedEntry
	previousActive := m.detailSelectedActive
	m.detailSelectedEntry = bestEntry
	m.detailSelectedActive = true
	if previousEntry != m.detailSelectedEntry || previousActive != m.detailSelectedActive {
		m.refreshDetailViewport()
	}
}

func (m *Model) moveDetailSelectionWithinViewport(delta int) bool {
	if m == nil || !m.compactDetail {
		return false
	}
	if len(m.visibleSelectableDetailEntries()) == 0 {
		m.ensureDetailSelection()
		return false
	}
	first, last, ok := m.visibleDetailEntryLineRange(m.detailSelectedEntry)
	if !m.detailSelectedActive || !ok {
		m.focusVisibleDetailEntry(m.viewportLines / 2)
		return false
	}
	startLine := first - 1
	if delta > 0 {
		startLine = last + 1
	}
	return m.selectVisibleDetailEntryInLineDirection(startLine, delta)
}

func (m *Model) moveDetailSelectionTowardCenterAtScrollEdge(delta int) bool {
	if m == nil || !m.compactDetail || (delta != -1 && delta != 1) {
		return false
	}
	first, last, ok := m.visibleDetailEntryLineRange(m.detailSelectedEntry)
	if !m.detailSelectedActive || !ok {
		return false
	}
	centerEntry := m.centerVisibleSelectableDetailEntry()
	centerFirst, centerLast, ok := m.visibleDetailEntryLineRange(centerEntry)
	if !ok {
		return false
	}
	centerLine := (centerFirst + centerLast) / 2
	if delta < 0 && first <= centerLine {
		return false
	}
	if delta > 0 && last >= centerLine {
		return false
	}
	startLine := first - 1
	if delta > 0 {
		startLine = last + 1
	}
	return m.selectVisibleDetailEntryInLineDirection(startLine, delta)
}

func (m *Model) selectVisibleDetailEntry(entryIndex int) bool {
	if m == nil || entryIndex < 0 {
		return false
	}
	if detailVisibleEntryIndex(m.visibleSelectableDetailEntries(), entryIndex) < 0 {
		return false
	}
	previousEntry := m.detailSelectedEntry
	previousActive := m.detailSelectedActive
	m.detailSelectedEntry = entryIndex
	m.detailSelectedActive = true
	if previousEntry != m.detailSelectedEntry || previousActive != m.detailSelectedActive {
		m.refreshDetailViewport()
	}
	return true
}

func (m *Model) selectVisibleDetailEntryInLineDirection(startLine int, delta int) bool {
	if m == nil || delta == 0 {
		return false
	}
	lookup := newDetailProjectionLookup(m.detailViewProjection())
	owners := lookup.projection.DetailViewport(m.currentDetailViewportState()).Owners
	for lineIndex := startLine; lineIndex >= 0 && lineIndex < len(owners); lineIndex += delta {
		entryIndex := owners[lineIndex]
		if entryIndex == m.detailSelectedEntry || lookup.blockIndexForEntry(entryIndex) < 0 {
			continue
		}
		return m.selectVisibleDetailEntry(entryIndex)
	}
	return false
}

func (m Model) visibleDetailEntryLineRange(entryIndex int) (int, int, bool) {
	if entryIndex < 0 {
		return -1, -1, false
	}
	first := -1
	last := -1
	for lineIndex, owner := range m.detailViewProjection().DetailViewport(m.currentDetailViewportState()).Owners {
		if owner != entryIndex {
			continue
		}
		if first < 0 {
			first = lineIndex
		}
		last = lineIndex
	}
	return first, last, first >= 0
}

func (m *Model) visibleSelectableDetailEntries() []int {
	if m == nil {
		return nil
	}
	lookup := newDetailProjectionLookup(m.detailViewProjection())
	owners := lookup.projection.DetailViewport(m.currentDetailViewportState()).Owners
	entries := make([]int, 0, len(owners))
	seen := make(map[int]struct{}, len(owners))
	for _, entryIndex := range owners {
		if lookup.blockIndexForEntry(entryIndex) < 0 {
			continue
		}
		if _, ok := seen[entryIndex]; ok {
			continue
		}
		seen[entryIndex] = struct{}{}
		entries = append(entries, entryIndex)
	}
	return entries
}

func (m *Model) centerVisibleSelectableDetailEntry() int {
	if m == nil {
		return -1
	}
	lookup := newDetailProjectionLookup(m.detailViewProjection())
	owners := lookup.projection.DetailViewport(m.currentDetailViewportState()).Owners
	if len(owners) == 0 {
		return -1
	}
	anchor := m.viewportLines / 2
	if anchor >= len(owners) {
		anchor = len(owners) - 1
	}
	if anchor < 0 {
		return -1
	}
	bestEntry := -1
	bestDistance := len(owners) + 1
	for lineIndex, entryIndex := range owners {
		if lookup.blockIndexForEntry(entryIndex) < 0 {
			continue
		}
		distance := detailLineDistance(lineIndex, anchor)
		if distance >= bestDistance {
			continue
		}
		bestEntry = entryIndex
		bestDistance = distance
	}
	return bestEntry
}

func detailLineDistance(left int, right int) int {
	distance := left - right
	if distance < 0 {
		return -distance
	}
	return distance
}

func detailVisibleEntryIndex(entries []int, entryIndex int) int {
	for idx, candidate := range entries {
		if candidate == entryIndex {
			return idx
		}
	}
	return -1
}

func (m Model) maxOngoingScroll() int {
	lineCount := m.ongoingLineParts().lineCount()
	if lineCount <= m.viewportLines {
		return 0
	}
	return lineCount - m.viewportLines
}

func (m *Model) maxDetailScroll() int {
	if m.detailBottomAnchor {
		return m.detailBottomOffset
	}
	return m.detailViewProjection().DetailViewport(ProjectionViewportState{ViewportLines: m.viewportLines}).MaxScroll
}

func (m Model) renderOngoing() string {
	parts := m.ongoingLineParts()
	lineCount := parts.lineCount()
	start := m.maxOngoingScroll()
	end := start + m.viewportLines
	if end > lineCount {
		end = lineCount
	}

	out := make([]string, 0, m.viewportLines+1)
	for i := start; i < end; i++ {
		out = append(out, parts.lineAt(i).Text)
	}
	for len(out) < m.viewportLines {
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}

func (m Model) ongoingLines() []string {
	parts := m.ongoingLineParts()
	lines := make([]string, 0, parts.lineCount())
	for idx := 0; idx < parts.lineCount(); idx++ {
		lines = append(lines, parts.lineAt(idx).Text)
	}
	return lines
}

func (m Model) transcriptViewProjection() TranscriptViewProjection {
	if m.viewProjector == nil {
		return ProjectTranscriptViews(m.TranscriptProjectionInput(), m.TranscriptProjectionViewState())
	}
	return m.viewProjector.project(m.transcriptProjectionInput(), m.TranscriptProjectionViewState(), false)
}

func (m Model) currentDetailViewportState() ProjectionViewportState {
	return ProjectionViewportState{
		ViewportLines: m.viewportLines,
		Scroll:        m.detailScroll,
		BottomAnchor:  m.detailBottomAnchor,
		BottomOffset:  m.detailBottomOffset,
	}
}

func (m Model) detailViewProjection() TranscriptViewProjection {
	if m.viewProjector == nil {
		return transcriptViewProjectionForDetail(m.transcriptInput.Revision, ProjectTranscriptViews(m.TranscriptProjectionInput(), m.TranscriptProjectionViewState()).Detail)
	}
	return m.viewProjector.ProjectDetailShared(m.transcriptProjectionInput(), m.TranscriptProjectionViewState())
}

func (m Model) ongoingLineParts() ongoingLineParts {
	input := m.transcriptProjectionInput()
	state := m.TranscriptProjectionViewState()
	var base []TranscriptProjectionLine
	lastGroup := ""
	if m.viewProjector != nil {
		base, lastGroup = m.viewProjector.CommittedOngoingLines(input, state)
	} else {
		projection := projectOngoingTranscriptWithRenderer(
			transcriptProjectionRenderer(state.Theme, state.ViewportWidth, input.BaseOffset),
			CommittedOngoingEntries(input.Entries),
		)
		base = projection.Lines(TranscriptDivider)
		if blockCount := len(projection.Blocks); blockCount > 0 {
			lastGroup = projection.Blocks[blockCount-1].DividerGroup
		}
	}
	if pending := m.pendingOngoingProjectionLines(input, state, lastGroup, uniformPendingSpinnerFrame("")); len(pending.lines) > 0 {
		base = append(base, pending.lines...)
		lastGroup = pending.lastGroup
	}
	streamingLines := m.streamingOngoingProjectionLines()
	parts := ongoingLineParts{base: base, streaming: streamingLines}
	parts.streamingDivider = len(base) > 0 && len(streamingLines) > 0 && lastGroup != ongoingDividerGroup(RenderIntentAssistant)
	return parts
}

func (m Model) liveOngoingLines(spinnerForEntry PendingSpinnerFrameFunc) []TranscriptProjectionLine {
	pending := m.pendingLiveOngoingLines(spinnerForEntry)
	lines := append([]TranscriptProjectionLine(nil), pending.lines...)
	lastGroup := pending.lastGroup
	hasStableBefore := pending.previousGroup != ""
	streamingLines := m.streamingOngoingProjectionLines()
	if len(streamingLines) == 0 {
		return lines
	}
	if (len(lines) > 0 || hasStableBefore) && lastGroup != ongoingDividerGroup(RenderIntentAssistant) {
		lines = append(lines, TranscriptProjectionLine{Kind: VisibleLineDivider, Text: TranscriptDivider})
	}
	lines = append(lines, streamingLines...)
	return lines
}

func (m Model) pendingLiveOngoingLines(spinnerForEntry PendingSpinnerFrameFunc) pendingOngoingLines {
	input := m.transcriptProjectionInput()
	state := m.TranscriptProjectionViewState()
	lastGroup := ""
	if m.viewProjector != nil {
		_, lastGroup = m.viewProjector.CommittedOngoingLines(input, state)
	} else {
		projection := projectOngoingTranscriptWithRenderer(
			transcriptProjectionRenderer(state.Theme, state.ViewportWidth, input.BaseOffset),
			CommittedOngoingEntries(input.Entries),
		)
		if blockCount := len(projection.Blocks); blockCount > 0 {
			lastGroup = projection.Blocks[blockCount-1].DividerGroup
		}
	}
	pending := m.pendingOngoingProjectionLines(input, state, lastGroup, spinnerForEntry)
	pending.previousGroup = lastGroup
	if len(pending.lines) == 0 {
		pending.lastGroup = lastGroup
	}
	return pending
}

type pendingOngoingLines struct {
	lines         []TranscriptProjectionLine
	lastGroup     string
	previousGroup string
}

func (m Model) pendingOngoingProjectionLines(input TranscriptProjectionInput, state TranscriptProjectionViewState, previousGroup string, spinnerForEntry PendingSpinnerFrameFunc) pendingOngoingLines {
	pendingEntries := PendingOngoingEntries(input.Entries)
	if len(pendingEntries) == 0 {
		return pendingOngoingLines{}
	}
	projection := renderPendingOngoingSnapshotProjection(pendingEntries, state.Theme, state.ViewportWidth, spinnerForEntry)
	if len(projection.Blocks) == 0 {
		return pendingOngoingLines{}
	}
	lines := projection.Lines(TranscriptDivider)
	if previousGroup != "" && previousGroup != projection.Blocks[0].DividerGroup {
		lines = append([]TranscriptProjectionLine{{Kind: VisibleLineDivider, Text: TranscriptDivider}}, lines...)
	}
	return pendingOngoingLines{lines: lines, lastGroup: projection.Blocks[len(projection.Blocks)-1].DividerGroup}
}

func (m Model) streamingOngoingProjectionLines() []TranscriptProjectionLine {
	if strings.TrimSpace(m.transcriptInput.Ongoing) == "" {
		return nil
	}
	if m.viewProjector != nil {
		return m.viewProjector.StreamingOngoingLines(m.transcriptInput.Ongoing, m.TranscriptProjectionViewState())
	}
	lines := m.flattenEntryPlain(RenderIntentAssistant, m.transcriptInput.Ongoing)
	if len(lines) == 0 {
		return nil
	}
	out := make([]TranscriptProjectionLine, 0, len(lines))
	for _, line := range lines {
		out = append(out, TranscriptProjectionLine{Kind: VisibleLineContent, Text: line})
	}
	return out
}

func (p ongoingLineParts) lineCount() int {
	total := len(p.base) + len(p.streaming)
	if p.streamingDivider {
		total++
	}
	if total == 0 {
		return 1
	}
	return total
}

func (p ongoingLineParts) lineAt(index int) TranscriptProjectionLine {
	if index < 0 || index >= p.lineCount() {
		return TranscriptProjectionLine{Kind: VisibleLineContent}
	}
	if len(p.base) == 0 && !p.streamingDivider && len(p.streaming) == 0 {
		return TranscriptProjectionLine{Kind: VisibleLineContent}
	}
	if index < len(p.base) {
		return p.base[index]
	}
	index -= len(p.base)
	if p.streamingDivider {
		if index == 0 {
			return TranscriptProjectionLine{Kind: VisibleLineDivider, Text: TranscriptDivider}
		}
		index--
	}
	if index >= 0 && index < len(p.streaming) {
		return p.streaming[index]
	}
	return TranscriptProjectionLine{Kind: VisibleLineContent}
}

func (m Model) visibleOngoingLineKinds() []VisibleLineKind {
	if m.viewportLines <= 0 {
		return nil
	}
	parts := m.ongoingLineParts()
	start := m.maxOngoingScroll()
	end := start + m.viewportLines
	if end > parts.lineCount() {
		end = parts.lineCount()
	}
	out := make([]VisibleLineKind, 0, m.viewportLines)
	for idx := start; idx < end; idx++ {
		out = append(out, parts.lineAt(idx).Kind)
	}
	for len(out) < m.viewportLines {
		out = append(out, VisibleLineContent)
	}
	return out
}

func (m Model) renderDetailSnapshot() string {
	lookup := newDetailProjectionLookup(m.detailViewProjection())
	viewport := lookup.projection.DetailViewport(m.currentDetailViewportState())
	lines := viewport.Lines
	if len(lines) == 0 {
		lines = []string{""}
	}
	owners := viewport.Owners

	out := make([]string, 0, m.viewportLines)
	selectedEntry, highlightSelected := m.resolveDetailSelection()
	firstSelectedLine := -1
	lastSelectedLine := -1
	if highlightSelected && m.compactDetail {
		for i, entryIndex := range owners {
			if entryIndex != selectedEntry {
				continue
			}
			if firstSelectedLine < 0 {
				firstSelectedLine = i
			}
			lastSelectedLine = i
		}
	}
	for i, line := range lines {
		selected := highlightSelected && i < len(owners) && owners[i] == selectedEntry
		if m.compactDetail && highlightSelected && m.shouldInsertDetailSelectionSpacerBefore(i, firstSelectedLine, owners, lookup) {
			out = append(out, m.renderDetailSelectionSpacerLine())
		}
		if m.compactDetail && highlightSelected && m.shouldRenderDetailSelectionSpacer(i, firstSelectedLine, lastSelectedLine, owners, lookup) {
			out = append(out, m.renderDetailSelectionSpacerLine())
			continue
		}
		line = m.renderDetailViewportLine(line, selected)
		out = append(out, line)
		if m.compactDetail && highlightSelected && m.shouldInsertDetailSelectionSpacerAfter(i, lastSelectedLine, owners, lookup) {
			out = append(out, m.renderDetailSelectionSpacerLine())
		}
	}
	if len(out) > m.viewportLines {
		out = out[:m.viewportLines]
	}
	for len(out) < m.viewportLines {
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}

func (m Model) shouldRenderDetailSelectionSpacer(lineIndex int, firstSelectedLine int, lastSelectedLine int, owners []int, lookup detailProjectionLookup) bool {
	if firstSelectedLine < 0 || lastSelectedLine < 0 {
		return false
	}
	if lineIndex == firstSelectedLine-1 {
		return !m.shouldInsertDetailSelectionSpacerBefore(firstSelectedLine, firstSelectedLine, owners, lookup)
	}
	if lineIndex == lastSelectedLine+1 {
		return !m.shouldInsertDetailSelectionSpacerAfter(lastSelectedLine, lastSelectedLine, owners, lookup)
	}
	return false
}

func (m Model) shouldInsertDetailSelectionSpacerBefore(lineIndex int, firstSelectedLine int, owners []int, lookup detailProjectionLookup) bool {
	return lineIndex == firstSelectedLine && !m.detailBottomAnchor && m.detailScroll == 0 && lookup.ownsSelectableEntry(firstSelectedLine-1, owners)
}

func (m Model) shouldInsertDetailSelectionSpacerAfter(lineIndex int, lastSelectedLine int, owners []int, lookup detailProjectionLookup) bool {
	return lineIndex == lastSelectedLine && !m.detailBottomAnchor && m.detailScroll == m.maxDetailScroll() && lookup.ownsSelectableEntry(lastSelectedLine+1, owners)
}

type detailExpansionSymbolState struct {
	role     RenderIntent
	expanded bool
}

func (m Model) detailSelectedExpansionState() (detailExpansionSymbolState, bool) {
	if !m.compactDetail || m.mode != ModeDetail {
		return detailExpansionSymbolState{}, false
	}
	selectedEntry, ok := m.resolveDetailSelection()
	if !ok {
		return detailExpansionSymbolState{}, false
	}
	lookup := newDetailProjectionLookup(m.detailViewProjection())
	blockIndex := lookup.blockIndexForEntry(selectedEntry)
	blocks := lookup.blocks
	if blockIndex < 0 || blockIndex >= len(blocks) {
		return detailExpansionSymbolState{}, false
	}
	block := blocks[blockIndex]
	if !block.Selectable || !block.Expandable {
		return detailExpansionSymbolState{}, false
	}
	return detailExpansionSymbolState{role: block.Role, expanded: block.Expanded}, true
}

func (m Model) detailExpansionSymbolOverrideForEntry(role RenderIntent, entryIndex int, expanded bool) string {
	if selectedEntry, ok := m.selectedUserTranscriptEntry(); ok && selectedEntry == entryIndex {
		return renderRoleSymbol(">", m.detailExpansionSymbolStyle(role)) + " "
	}
	selectedEntry, ok := m.resolveDetailSelection()
	if !ok || selectedEntry != entryIndex {
		return ""
	}
	return m.detailExpansionSymbolPrefix(role, expanded)
}

func (m *Model) refreshDetailViewport() {
	if m == nil {
		return
	}
	viewport := m.detailViewProjection().DetailViewport(ProjectionViewportState{
		ViewportLines: m.viewportLines,
		Scroll:        m.detailScroll,
		BottomAnchor:  m.detailBottomAnchor,
		BottomOffset:  m.detailBottomOffset,
	})
	m.detailBottomOffset = viewport.BottomOffset
}

func (m *Model) ensureDetailScrollResolved() {
	if m == nil || !m.detailBottomAnchor {
		return
	}
	bottomOffset := m.detailBottomOffset
	viewport := m.detailViewProjection().DetailViewport(ProjectionViewportState{
		ViewportLines: m.viewportLines,
		BottomAnchor:  true,
		BottomOffset:  bottomOffset,
	})
	m.detailScroll = viewport.Scroll
	m.detailBottomAnchor = false
	m.detailBottomOffset = 0
}
