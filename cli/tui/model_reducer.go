package tui

import (
	"core/shared/transcript"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type modelUpdateResult struct {
	viewportChanged    bool
	ongoingBaseChanged bool
	ongoingChanged     bool
	detailChanged      bool
	forceDetailRefresh bool
	autoFollowOngoing  bool
}

func (m *Model) reduce(msg tea.Msg) {
	result := modelUpdateResult{}
	switch msg := msg.(type) {
	case tea.KeyMsg:
		m.reduceKeyMsg(msg)
	case tea.MouseMsg:
		m.reduceMouseMsg(msg)
	case ToggleModeMsg:
		m.reduceToggleModeMsg(msg)
	case SetModeMsg:
		m.reduceSetModeMsg(msg)
	case SetViewportLinesMsg:
		result.viewportChanged = m.reduceViewportLinesMsg(msg)
	case SetViewportSizeMsg:
		m.reduceViewportSizeMsg(msg, &result)
	case AppendTranscriptMsg:
		m.reduceAppendTranscriptMsg(msg, &result)
	case SetConversationMsg:
		m.reduceSetConversationMsg(msg, &result)
	case SetSelectedTranscriptEntryMsg:
		m.reduceSetSelectedTranscriptEntryMsg(msg, &result)
	case FocusTranscriptEntryMsg:
		m.reduceFocusTranscriptEntryMsg(msg)
	case StreamAssistantMsg:
		m.reduceStreamAssistantMsg(msg, &result)
	case ClearOngoingAssistantMsg:
		m.reduceClearOngoingAssistantMsg(&result)
	case UpsertStreamingReasoningMsg:
		m.reduceUpsertStreamingReasoningMsg(msg, &result)
	case ClearStreamingReasoningMsg:
		m.reduceClearStreamingReasoningMsg(&result)
	case CommitAssistantMsg:
		m.reduceCommitAssistantMsg(&result)
	case SetOngoingErrorMsg:
		m.ongoingError = FormatOngoingError(msg.Err)
	case ClearOngoingErrorMsg:
		m.ongoingError = ""
	}

	m.applyUpdateResult(result)
}

func (m *Model) reduceKeyMsg(msg tea.KeyMsg) {
	switch m.mode {
	case ModeDetail:
		m.reduceDetailKeyMsg(msg)
	default:
		m.reduceOngoingKeyMsg(msg)
	}
}

func (m *Model) reduceMouseMsg(msg tea.MouseMsg) {
	switch m.mode {
	case ModeDetail:
		m.reduceDetailMouseMsg(msg)
	default:
		m.reduceOngoingMouseMsg(msg)
	}
}

func (m *Model) reduceToggleModeMsg(msg ToggleModeMsg) {
	target := ModeDetail
	if m.mode == ModeDetail {
		target = ModeOngoing
	}
	m.reduceSetModeMsg(SetModeMsg{Mode: target, SkipDetailWarmup: msg.SkipDetailWarmup})
}

func (m *Model) reduceSetModeMsg(msg SetModeMsg) {
	if msg.Mode == "" || msg.Mode == m.mode {
		return
	}
	*m = m.transitionMode(msg.Mode, msg.SkipDetailWarmup)
}

func (m *Model) reduceOngoingKeyMsg(msg tea.KeyMsg) {
	switch msg.Type {
	case tea.KeyTab:
		*m = m.transitionMode(ModeDetail, false)
	}
}

func (m *Model) reduceDetailKeyMsg(msg tea.KeyMsg) {
	switch msg.Type {
	case tea.KeyTab:
		*m = m.transitionMode(ModeOngoing, false)
	case tea.KeyUp:
		m.navigateDetailSelection(-1)
	case tea.KeyDown:
		m.navigateDetailSelection(1)
	case tea.KeyEnter:
		if m.compactDetail {
			m.toggleSelectedDetailExpansion()
		}
	case tea.KeyPgUp:
		*m = m.scrollDetail(-max(1, m.viewportLines-1))
	case tea.KeyPgDown:
		*m = m.scrollDetail(max(1, m.viewportLines-1))
	}
}

func (m *Model) reduceOngoingMouseMsg(msg tea.MouseMsg) {
}

func (m *Model) reduceDetailMouseMsg(msg tea.MouseMsg) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		*m = m.scrollDetail(-1)
	case tea.MouseButtonWheelDown:
		*m = m.scrollDetail(1)
	}
}

func (m *Model) reduceViewportLinesMsg(msg SetViewportLinesMsg) bool {
	if msg.Lines <= 0 {
		return false
	}
	if m.viewportLines == msg.Lines {
		return false
	}
	m.viewportLines = msg.Lines
	return true
}

func (m *Model) reduceViewportSizeMsg(msg SetViewportSizeMsg, result *modelUpdateResult) {
	if result == nil {
		return
	}
	if msg.Lines > 0 && m.viewportLines != msg.Lines {
		m.viewportLines = msg.Lines
		result.viewportChanged = true
	}
	if msg.Width <= 0 || m.viewportWidth == msg.Width {
		return
	}
	m.viewportWidth = msg.Width
	result.ongoingBaseChanged = true
	result.ongoingChanged = true
	result.detailChanged = true
	if m.mode == ModeDetail {
		result.forceDetailRefresh = true
	}
}

func (m *Model) reduceAppendTranscriptMsg(msg AppendTranscriptMsg, result *modelUpdateResult) {
	m.resolveDetailScrollBeforeLiveTranscriptChange()
	role := TranscriptRoleFromWire(string(msg.Role))
	m.transcriptInput.Entries = append(m.transcriptInput.Entries, TranscriptEntry{
		Visibility:        transcript.NormalizeEntryVisibility(msg.Visibility),
		Transient:         msg.Transient,
		Committed:         msg.Committed,
		Role:              role,
		Text:              msg.Text,
		CondensedText:     msg.CondensedText,
		Phase:             msg.Phase,
		MessageType:       msg.MessageType,
		SourcePath:        strings.TrimSpace(msg.SourcePath),
		CompactLabel:      strings.TrimSpace(msg.CompactLabel),
		ToolResultSummary: strings.TrimSpace(msg.ToolResultSummary),
		ToolCallID:        strings.TrimSpace(msg.ToolCallID),
		NoticeID:          strings.TrimSpace(msg.NoticeID),
		ToolCall:          cloneToolCallMeta(msg.ToolCall),
	})
	m.advanceTranscriptEntriesRevision()
	m.transcriptInput.TotalEntries = max(m.transcriptInput.TotalEntries, m.transcriptInput.BaseOffset+len(m.transcriptInput.Entries))
	result.autoFollowOngoing = true
	result.ongoingBaseChanged = true
	result.ongoingChanged = true
	result.detailChanged = true
}

func (m *Model) reduceSetConversationMsg(msg SetConversationMsg, result *modelUpdateResult) {
	anchorEntry, anchorOffset, preserveAnchor := m.detailViewportAnchor()
	previousBaseOffset := m.transcriptInput.BaseOffset
	previousTotalEntries := m.transcriptInput.TotalEntries
	previousEntries := append([]TranscriptEntry(nil), m.transcriptInput.Entries...)
	previousOngoing := m.transcriptInput.Ongoing
	entries := make([]TranscriptEntry, len(msg.Entries))
	copy(entries, msg.Entries)
	for i := range entries {
		entries[i].Visibility = transcript.NormalizeEntryVisibility(entries[i].Visibility)
		entries[i].Role = TranscriptRoleFromWire(string(entries[i].Role))
		entries[i].ToolCallID = strings.TrimSpace(entries[i].ToolCallID)
		entries[i].SourcePath = strings.TrimSpace(entries[i].SourcePath)
		entries[i].CompactLabel = strings.TrimSpace(entries[i].CompactLabel)
		entries[i].ToolResultSummary = strings.TrimSpace(entries[i].ToolResultSummary)
		entries[i].ToolCall = cloneToolCallMeta(entries[i].ToolCall)
	}
	if msg.BaseOffset < 0 {
		msg.BaseOffset = 0
	}
	totalEntries := msg.TotalEntries
	if totalEntries < msg.BaseOffset+len(entries) {
		totalEntries = msg.BaseOffset + len(entries)
	}
	entriesChanged := !transcriptEntriesEqual(previousEntries, entries)
	projectionChanged := previousBaseOffset != msg.BaseOffset ||
		previousTotalEntries != totalEntries ||
		previousOngoing != msg.Ongoing
	m.transcriptInput.Entries = entries
	if entriesChanged {
		m.advanceTranscriptEntriesRevision()
	} else if projectionChanged {
		m.advanceTranscriptProjectionRevision()
	}
	m.transcriptInput.BaseOffset = msg.BaseOffset
	m.transcriptInput.TotalEntries = totalEntries
	m.transcriptInput.Ongoing = msg.Ongoing
	m.ongoingError = strings.TrimSpace(msg.OngoingError)
	if _, ok := m.localTranscriptIndex(m.selectedTranscriptEntry); !ok {
		m.selectedTranscriptActive = false
	}
	if _, ok := m.localTranscriptIndex(m.detailSelectedEntry); !ok {
		m.detailSelectedActive = false
	}
	m.reconcileDetailExpandedEntries(previousBaseOffset, previousEntries)
	result.autoFollowOngoing = true
	result.ongoingBaseChanged = true
	result.ongoingChanged = true
	if m.mode != ModeDetail {
		result.detailChanged = true
		return
	}
	m.refreshDetailViewport()
	if m.compactDetail {
		m.ensureDetailSelection()
	}
	if preserveAnchor {
		if start, _, ok := m.detailLineRangeForEntry(anchorEntry); ok {
			m.detailScroll = clamp(start+anchorOffset, 0, m.maxDetailScroll())
			m.detailBottomAnchor = false
			m.detailBottomOffset = 0
			m.refreshDetailViewport()
		}
	}
}

func (m *Model) reconcileDetailExpandedEntries(previousBaseOffset int, previousEntries []TranscriptEntry) {
	if m == nil || len(m.detailExpandedEntries) == 0 {
		return
	}
	for entryIndex := range m.detailExpandedEntries {
		currentLocal, currentOK := m.localTranscriptIndex(entryIndex)
		previousLocal, previousOK := transcriptLocalIndex(previousBaseOffset, len(previousEntries), entryIndex)
		if !currentOK || !previousOK || !detailExpansionEntryMatches(previousEntries[previousLocal], m.transcriptInput.Entries[currentLocal]) {
			delete(m.detailExpandedEntries, entryIndex)
		}
	}
}

func transcriptLocalIndex(baseOffset int, entryCount int, entryIndex int) (int, bool) {
	local := entryIndex - baseOffset
	if local < 0 || local >= entryCount {
		return 0, false
	}
	return local, true
}

func detailExpansionEntryMatches(left TranscriptEntry, right TranscriptEntry) bool {
	return left.Visibility == right.Visibility &&
		left.RollbackTargetID == right.RollbackTargetID &&
		left.Transient == right.Transient &&
		left.Committed == right.Committed &&
		left.Role == right.Role &&
		left.Text == right.Text &&
		left.CondensedText == right.CondensedText &&
		left.Phase == right.Phase &&
		left.MessageType == right.MessageType &&
		left.SourcePath == right.SourcePath &&
		left.CompactLabel == right.CompactLabel &&
		left.ToolResultSummary == right.ToolResultSummary &&
		left.ToolCallID == right.ToolCallID &&
		transcript.ToolCallMetaEqual(left.ToolCall, right.ToolCall)
}

func transcriptEntriesEqual(left []TranscriptEntry, right []TranscriptEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for idx := range left {
		if !detailExpansionEntryMatches(left[idx], right[idx]) {
			return false
		}
	}
	return true
}

func (m *Model) navigateDetailSelection(delta int) {
	if m == nil || delta == 0 {
		return
	}
	if !m.compactDetail {
		*m = m.scrollDetail(delta)
		return
	}
	if m.moveDetailSelectionTowardCenterAtScrollEdge(delta) {
		return
	}
	if moved := m.scrollDetailLine(delta); moved {
		m.focusVisibleDetailEntry(m.viewportLines / 2)
		return
	}
	m.moveDetailSelectionWithinViewport(delta)
}

func (m *Model) toggleSelectedDetailExpansion() {
	if m == nil {
		return
	}
	m.ensureDetailSelection()
	if !m.detailSelectedActive {
		return
	}
	lookup := newDetailProjectionLookup(m.detailViewProjection())
	blockIndex := lookup.blockIndexForEntry(m.detailSelectedEntry)
	blocks := lookup.blocks
	if blockIndex < 0 || blockIndex >= len(blocks) || !blocks[blockIndex].Expandable {
		return
	}
	if m.detailExpandedEntries == nil {
		m.detailExpandedEntries = make(map[int]struct{})
	}
	if _, ok := m.detailExpandedEntries[m.detailSelectedEntry]; ok {
		delete(m.detailExpandedEntries, m.detailSelectedEntry)
	} else {
		m.detailExpandedEntries[m.detailSelectedEntry] = struct{}{}
	}
	m.scrollDetailSelectionIntoView()
	m.refreshDetailViewport()
}

func (m *Model) scrollDetailSelectionIntoView() {
	if m == nil || !m.detailSelectedActive {
		return
	}
	start, end, ok := m.detailLineRangeForEntry(m.detailSelectedEntry)
	if !ok {
		return
	}
	m.ensureDetailScrollResolved()
	if start < m.detailScroll {
		m.detailScroll = start
	} else if end >= m.detailScroll+m.viewportLines {
		m.detailScroll = max(0, end-m.viewportLines+1)
	}
	m.detailScroll = clamp(m.detailScroll, 0, m.maxDetailScroll())
	m.detailBottomAnchor = false
	m.detailBottomOffset = 0
}

func (m *Model) detailViewportAnchor() (int, int, bool) {
	if m == nil || m.mode != ModeDetail || m.detailBottomAnchor {
		return 0, 0, false
	}
	for _, entryIndex := range m.detailViewProjection().DetailViewport(m.currentDetailViewportState()).Owners {
		if entryIndex < 0 {
			continue
		}
		start, _, ok := m.detailLineRangeForEntry(entryIndex)
		if !ok {
			return 0, 0, false
		}
		return entryIndex, max(0, m.detailScroll-start), true
	}
	return 0, 0, false
}

func (m *Model) reduceSetSelectedTranscriptEntryMsg(msg SetSelectedTranscriptEntryMsg, result *modelUpdateResult) {
	m.selectedTranscriptEntry = msg.EntryIndex
	m.selectedTranscriptActive = msg.Active
	result.ongoingChanged = true
	if m.mode == ModeDetail && msg.RefreshDetailSnapshot {
		result.detailChanged = true
		result.forceDetailRefresh = true
	}
}

func (m *Model) reduceFocusTranscriptEntryMsg(msg FocusTranscriptEntryMsg) {
	switch m.mode {
	case ModeOngoing:
	case ModeDetail:
		if start, end, ok := m.detailLineRangeForEntry(msg.EntryIndex); ok {
			m.ensureDetailScrollResolved()
			m.detailScroll = clamp(focusedScrollTarget(start, end, m.viewportLines, msg), 0, m.maxDetailScroll())
			m.detailBottomAnchor = false
			m.detailBottomOffset = 0
			m.refreshDetailViewport()
		}
	}
}

func (m *Model) reduceStreamAssistantMsg(msg StreamAssistantMsg, result *modelUpdateResult) {
	m.transcriptInput.Ongoing += msg.Delta
	m.advanceTranscriptProjectionRevision()
	result.autoFollowOngoing = true
	result.ongoingChanged = true
	if m.mode == ModeDetail {
		result.detailChanged = true
		return
	}
	result.detailChanged = true
}

func (m *Model) reduceClearOngoingAssistantMsg(result *modelUpdateResult) {
	m.resolveDetailScrollBeforeLiveTranscriptChange()
	hadOngoing := m.transcriptInput.Ongoing != ""
	m.transcriptInput.Ongoing = ""
	if hadOngoing {
		m.advanceTranscriptProjectionRevision()
	}
	result.ongoingChanged = true
	result.detailChanged = true
}

func (m *Model) reduceUpsertStreamingReasoningMsg(msg UpsertStreamingReasoningMsg, result *modelUpdateResult) {
	key := strings.TrimSpace(msg.Key)
	if key == "" {
		return
	}
	role := TranscriptRoleFromWire(msg.Role)
	if role == TranscriptRoleUnknown {
		role = TranscriptRoleReasoning
	}
	text := strings.TrimSpace(msg.Text)
	updated := false
	for i := range m.transcriptInput.StreamingReasoning {
		if m.transcriptInput.StreamingReasoning[i].Key != key {
			continue
		}
		updated = true
		if text == "" {
			m.transcriptInput.StreamingReasoning = append(m.transcriptInput.StreamingReasoning[:i], m.transcriptInput.StreamingReasoning[i+1:]...)
		} else {
			m.transcriptInput.StreamingReasoning[i].Role = role
			m.transcriptInput.StreamingReasoning[i].Text = text
		}
		break
	}
	if !updated && text != "" {
		m.transcriptInput.StreamingReasoning = append(m.transcriptInput.StreamingReasoning, StreamingReasoningEntry{Key: key, Role: role, Text: text})
	}
	if updated || text != "" {
		m.advanceTranscriptProjectionRevision()
	}
	result.detailChanged = true
	if m.mode == ModeDetail {
		result.forceDetailRefresh = true
	}
}

func (m *Model) reduceClearStreamingReasoningMsg(result *modelUpdateResult) {
	if len(m.transcriptInput.StreamingReasoning) == 0 {
		return
	}
	m.transcriptInput.StreamingReasoning = nil
	m.advanceTranscriptProjectionRevision()
	result.detailChanged = true
	if m.mode == ModeDetail {
		result.forceDetailRefresh = true
	}
}

func (m *Model) reduceCommitAssistantMsg(result *modelUpdateResult) {
	if m.transcriptInput.Ongoing == "" {
		return
	}
	m.resolveDetailScrollBeforeLiveTranscriptChange()
	m.transcriptInput.Entries = append(m.transcriptInput.Entries, TranscriptEntry{Role: TranscriptRoleAssistant, Text: m.transcriptInput.Ongoing})
	m.transcriptInput.Ongoing = ""
	m.advanceTranscriptEntriesRevision()
	m.transcriptInput.TotalEntries = max(m.transcriptInput.TotalEntries, m.transcriptInput.BaseOffset+len(m.transcriptInput.Entries))
	result.autoFollowOngoing = true
	result.ongoingBaseChanged = true
	result.ongoingChanged = true
	result.detailChanged = true
}

func (m *Model) resolveDetailScrollBeforeLiveTranscriptChange() {
	if m == nil || m.mode != ModeDetail || m.detailBottomAnchor {
		return
	}
	m.ensureDetailScrollResolved()
	m.detailBottomAnchor = false
	m.detailBottomOffset = 0
}

func (m *Model) advanceTranscriptProjectionRevision() {
	m.transcriptInput.Revision++
	if m.transcriptInput.Revision <= 0 {
		m.transcriptInput.Revision = 1
	}
}

func (m *Model) advanceTranscriptEntriesRevision() {
	m.advanceTranscriptProjectionRevision()
	m.transcriptInput.EntriesRevision++
	if m.transcriptInput.EntriesRevision <= 0 {
		m.transcriptInput.EntriesRevision = 1
	}
}

func (m *Model) applyUpdateResult(result modelUpdateResult) {
	if result.forceDetailRefresh || (m.mode == ModeDetail && result.detailChanged) {
		m.refreshDetailViewport()
		if m.compactDetail {
			m.ensureDetailSelection()
		}
	}

	if m.mode == ModeDetail {
		if !m.detailBottomAnchor {
			m.detailScroll = clamp(m.detailScroll, 0, m.maxDetailScroll())
		}
		m.refreshDetailViewport()
		if result.viewportChanged && m.compactDetail && m.detailBottomAnchor {
			m.focusVisibleDetailEntry(len(m.detailViewProjection().DetailViewport(m.currentDetailViewportState()).Owners) - 1)
		}
	}
}

func focusedScrollTarget(start, end, viewportLines int, msg FocusTranscriptEntryMsg) int {
	target := start
	if msg.Bottom {
		return end - viewportLines + 1
	}
	if msg.Center {
		midpoint := (start + end) / 2
		return midpoint - viewportLines/2
	}
	return target
}
