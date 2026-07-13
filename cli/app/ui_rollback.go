package app

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"core/cli/tui"
	"core/shared/clientui"
	"core/shared/rollbacktarget"
	"core/shared/valuecopy"

	tea "github.com/charmbracelet/bubbletea"
)

const uiRollbackNavigationTimeout = 20 * time.Second

var errRollbackNavigationTimedOut = errors.New("rollback history search timed out")

func (m *uiModel) beginRollbackSelectionHydration() tea.Cmd {
	if m == nil {
		return nil
	}
	restoreDetailTranscript := m.detailTranscript.clone()
	restoreDetailPresentation := m.view.DetailPresentationSnapshot()
	m.rollback = uiRollbackState{
		phase:                     uiRollbackPhaseAwaitingNewest,
		restoreDetailTranscript:   &restoreDetailTranscript,
		restoreDetailPresentation: &restoreDetailPresentation,
	}
	cancelCmd := m.cancelPendingDetailTranscriptRequest()
	loadCmd := m.loadDetailTranscriptPageWithNoticePolicyCmd(
		clientui.TranscriptPageRequest{},
		uiDetailTranscriptLoadingNoticeSilent,
	)
	if m.pendingDetailTranscript == nil {
		m.rollback.phase = uiRollbackPhaseInactive
	}
	return sequenceCmds(cancelCmd, loadCmd)
}

func (m *uiModel) activateRollbackSelectionFromNewestPage() tea.Cmd {
	if m == nil || !m.rollback.isAwaitingNewest() {
		return nil
	}
	if !m.rollbackActivationStillEligible() {
		m.resetRollbackState()
		return nil
	}
	m.refreshRollbackCandidates()
	if len(m.rollback.candidates) == 0 {
		request, targetID, ok := m.rollbackCandidatePageRequest()
		if !ok {
			m.resetRollbackState()
			return nil
		}
		m.rollback.phase = uiRollbackPhaseAwaitingOlderCandidate
		m.rollback.activationTargetID = targetID
		cmd := m.loadDetailTranscriptPageWithNoticePolicyCmd(
			request,
			uiDetailTranscriptLoadingNoticeSilent,
		)
		if m.pendingDetailTranscript == nil {
			m.resetRollbackState()
		}
		return cmd
	}
	return m.activateRollbackSelectionFromLoadedCandidate(
		m.rollback.candidates[len(m.rollback.candidates)-1],
	)
}

func (m *uiModel) rollbackCandidatePageRequest() (clientui.TranscriptPageRequest, *string, bool) {
	if m == nil {
		return clientui.TranscriptPageRequest{}, nil, false
	}
	if locator := m.detailTranscript.latestRollbackCandidate; locator != nil {
		targetID := rollbackTargetIDForCandidateLocator(*locator)
		cursor := locator.CandidatePageEndByte
		return clientui.TranscriptPageRequest{Cursor: &cursor}, &targetID, true
	}
	request, ok := m.detailTranscript.pageBefore()
	return request, nil, ok
}

func (m *uiModel) activateRollbackSelectionFromOlderCandidatePage() tea.Cmd {
	if m == nil || !m.rollback.isAwaitingOlderCandidate() {
		return nil
	}
	if !m.rollbackActivationStillEligible() {
		m.resetRollbackState()
		return nil
	}
	m.refreshRollbackCandidates()
	if m.rollback.activationTargetID != nil {
		index, ok := m.rollbackCandidateIndex(*m.rollback.activationTargetID)
		if !ok {
			m.resetRollbackState()
			return m.sendTransientStatusWithNoticeID(
				"Rollback candidate locator did not resolve to its target",
				uiStatusNoticeError,
				transientStatusDuration,
				uiStatusNoticeReplace,
				"",
			)
		}
		return m.activateRollbackSelectionFromLoadedCandidate(m.rollback.candidates[index])
	}
	if len(m.rollback.candidates) == 0 {
		m.resetRollbackState()
		return nil
	}
	return m.activateRollbackSelectionFromLoadedCandidate(
		m.rollback.candidates[len(m.rollback.candidates)-1],
	)
}

func (m *uiModel) activateRollbackSelectionFromLoadedCandidate(candidate rollbackCandidate) tea.Cmd {
	m.rollback.activationTargetID = nil
	m.setSelectedRollbackCandidate(candidate)
	m.rollback.phase = uiRollbackPhaseSelection
	m.setInputMode(uiInputModeRollbackSelection)
	m.clearInput()
	overlayCmd := m.pushRollbackOverlayIfNeeded()
	m.applyRollbackSelectionHighlight()
	return overlayCmd
}

func (m *uiModel) rollbackActivationStillEligible() bool {
	if m == nil || !m.rollback.isAwaitingActivation() {
		return false
	}
	return m.surface().isTranscript() &&
		(m.view.Mode() == tui.ModeOngoing || m.view.Mode() == tui.ModeDetail) &&
		m.inputMode() == uiInputModeMain &&
		!m.blocksRuntimeInput() &&
		strings.TrimSpace(m.input) == ""
}

func (m *uiModel) refreshRollbackCandidates() {
	if m == nil {
		return
	}
	m.rollback.candidates = rollbackCandidatesFromRows(m.detailTranscript.entries)
	if m.rollback.selectedTargetID == nil {
		return
	}
	if _, _, ok := m.selectedRollbackCandidate(); !ok {
		m.rollback.selectedTargetID = nil
	}
}

func rollbackCandidatesFromRows(rows []clientui.TranscriptCommittedRow) []rollbackCandidate {
	candidates := make([]rollbackCandidate, 0)
	for _, row := range rows {
		if row.Visibility == clientui.EntryVisibilityHidden ||
			row.Kind != clientui.TranscriptRowUser ||
			row.User == nil ||
			row.User.RollbackTargetID == nil {
			continue
		}
		targetID := *row.User.RollbackTargetID
		if strings.TrimSpace(targetID) == "" {
			panic("rollback candidate has an empty rollback target id")
		}
		candidates = append(candidates, rollbackCandidate{
			RollbackTargetID: targetID,
			Text:             row.User.Text,
		})
	}
	return candidates
}

func (m *uiModel) stopRollbackSelectionMode() {
	if m == nil {
		return
	}
	m.resetRollbackState()
	m.restorePrimaryInputMode()
}

func (m *uiModel) applyRollbackSelectionHighlight() {
	selected, _, ok := m.selectedRollbackCandidate()
	if !ok {
		return
	}
	m.forwardToView(tui.SelectDetailTranscriptRollbackTargetMsg{
		RollbackTargetID: selected.RollbackTargetID,
		Center:           true,
	})
}

func (m *uiModel) moveRollbackSelection(delta int) {
	if m == nil || !m.rollback.isSelecting() || m.rollback.pendingNavigation != nil || delta == 0 {
		return
	}
	selected, index, ok := m.selectedRollbackCandidate()
	if !ok {
		return
	}
	next := index + delta
	if next < 0 || next >= len(m.rollback.candidates) {
		m.setSelectedRollbackCandidate(selected)
		m.applyRollbackSelectionHighlight()
		return
	}
	m.setSelectedRollbackCandidate(m.rollback.candidates[next])
	m.applyRollbackSelectionHighlight()
}

func (m *uiModel) moveRollbackSelectionWithPaging(delta int) tea.Cmd {
	if cmd := m.requestRollbackSelectionPage(delta); cmd != nil {
		return cmd
	}
	m.moveRollbackSelection(delta)
	return nil
}

func (m *uiModel) jumpRollbackSelection(delta int) {
	if m == nil || !m.rollback.isSelecting() || m.rollback.pendingNavigation != nil ||
		len(m.rollback.candidates) == 0 || (delta != -1 && delta != 1) {
		return
	}
	targetIndex := 0
	if delta > 0 {
		targetIndex = len(m.rollback.candidates) - 1
	}
	m.setSelectedRollbackCandidate(m.rollback.candidates[targetIndex])
	m.applyRollbackSelectionHighlight()
}

func (m *uiModel) pageRollbackSelection(delta int) tea.Cmd {
	if cmd := m.requestRollbackSelectionPage(delta); cmd != nil {
		return cmd
	}
	m.jumpRollbackSelection(delta)
	return nil
}

func (m *uiModel) requestRollbackSelectionPage(delta int) tea.Cmd {
	if m == nil || !m.rollback.isSelecting() || m.rollback.pendingNavigation != nil ||
		m.pendingDetailTranscript != nil || (delta != -1 && delta != 1) {
		return nil
	}
	selected, index, ok := m.selectedRollbackCandidate()
	if !ok {
		return nil
	}
	var (
		request   clientui.TranscriptPageRequest
		direction tui.DetailTranscriptPageDirection
	)
	switch {
	case delta < 0 && index == 0:
		var available bool
		request, available = m.detailTranscript.pageBefore()
		if !available {
			return nil
		}
		direction = tui.DetailTranscriptPageOlder
	case delta > 0 && index == len(m.rollback.candidates)-1:
		if m.rollbackCandidateIsLatest(selected) {
			return nil
		}
		var available bool
		request, available = m.detailTranscript.pageAfter()
		if !available {
			return nil
		}
		direction = tui.DetailTranscriptPageNewer
	default:
		return nil
	}
	m.rollback.pendingNavigation = &uiRollbackPageNavigation{
		direction:              direction,
		anchorRollbackTargetID: selected.RollbackTargetID,
		request:                request,
		deadline:               time.Now().Add(uiRollbackNavigationTimeout),
	}
	cmd := m.loadRollbackNavigationPageCmd(request)
	if m.pendingDetailTranscript == nil {
		m.rollback.pendingNavigation = nil
	}
	return cmd
}

func (m *uiModel) loadRollbackNavigationPageCmd(request clientui.TranscriptPageRequest) tea.Cmd {
	if m == nil || m.rollback.pendingNavigation == nil ||
		!pageRequestEqual(request, m.rollback.pendingNavigation.request) {
		return nil
	}
	deadline := m.rollback.pendingNavigation.deadline
	return m.loadDetailTranscriptPageWithOptionsCmd(request, uiDetailTranscriptLoadOptions{
		noticePolicy: uiDetailTranscriptLoadingNoticeVisible,
		deadline:     &deadline,
	})
}

func (m *uiModel) isRollbackLocatorActivationRequest(request clientui.TranscriptPageRequest) bool {
	return m != nil &&
		m.rollback.isAwaitingOlderCandidate() &&
		m.rollback.activationTargetID != nil &&
		request.Cursor != nil &&
		request.NewerCursor == nil
}

func (m *uiModel) rollbackCandidateIsLatest(candidate rollbackCandidate) bool {
	if m == nil || m.detailTranscript.latestRollbackCandidate == nil {
		return false
	}
	return candidate.RollbackTargetID ==
		rollbackTargetIDForCandidateLocator(*m.detailTranscript.latestRollbackCandidate)
}

func rollbackTargetIDForCandidateLocator(locator rollbacktarget.CandidateLocator) string {
	if err := locator.Validate(); err != nil {
		panic(fmt.Sprintf("invalid latest rollback candidate locator: %v", err))
	}
	targetID := rollbacktarget.EncodeUserMessageSeq(locator.UserMessageSeq)
	if targetID == "" {
		panic(fmt.Sprintf("failed to encode rollback target for user message sequence %d", locator.UserMessageSeq))
	}
	return targetID
}

func (m *uiModel) reconcileRollbackDetailPageLoad(request clientui.TranscriptPageRequest) tea.Cmd {
	if m == nil {
		return nil
	}
	if m.rollback.isAwaitingNewest() && request.Cursor == nil && request.NewerCursor == nil {
		return m.activateRollbackSelectionFromNewestPage()
	}
	if m.rollback.isAwaitingOlderCandidate() && request.Cursor != nil && request.NewerCursor == nil {
		return m.activateRollbackSelectionFromOlderCandidatePage()
	}
	pending := m.rollback.pendingNavigation
	if pending == nil || !pageRequestEqual(request, pending.request) {
		return nil
	}
	m.rollback.pendingNavigation = nil
	m.refreshRollbackCandidates()
	if len(m.rollback.candidates) == 0 {
		m.resetRollbackState()
		return m.sendTransientStatusWithNoticeID(
			"Rollback navigation produced no selectable candidate",
			uiStatusNoticeError,
			transientStatusDuration,
			uiStatusNoticeReplace,
			"",
		)
	}
	anchorIndex, anchorFound := m.rollbackCandidateIndex(pending.anchorRollbackTargetID)
	nextIndex := 0
	nextFound := false
	switch pending.direction {
	case tui.DetailTranscriptPageOlder:
		if anchorFound && anchorIndex > 0 {
			nextIndex = anchorIndex - 1
			nextFound = true
		} else if !anchorFound {
			nextIndex = len(m.rollback.candidates) - 1
			nextFound = true
		}
	case tui.DetailTranscriptPageNewer:
		if anchorFound && anchorIndex+1 < len(m.rollback.candidates) {
			nextIndex = anchorIndex + 1
			nextFound = true
		} else if !anchorFound {
			nextIndex = 0
			nextFound = true
		}
	}
	if !nextFound {
		if !anchorFound {
			return nil
		}
		nextIndex = anchorIndex
	}
	if nextIndex >= len(m.rollback.candidates) {
		return nil
	}
	m.setSelectedRollbackCandidate(m.rollback.candidates[nextIndex])
	m.applyRollbackSelectionHighlight()
	return nil
}

func (m *uiModel) continueRollbackNavigationAcrossCandidateFreePage(
	request clientui.TranscriptPageRequest,
	page clientui.TranscriptPage,
) (tea.Cmd, bool) {
	if m == nil || m.rollback.pendingNavigation == nil ||
		!pageRequestEqual(request, m.rollback.pendingNavigation.request) ||
		len(rollbackCandidatesFromRows(page.Entries)) > 0 {
		return nil, false
	}
	nextRequest, ok := rollbackNavigationRequestAfterPage(
		m.rollback.pendingNavigation.direction,
		page,
	)
	if !ok {
		m.rollback.pendingNavigation = nil
		return nil, true
	}
	m.rollback.pendingNavigation.request = nextRequest
	m.rollback.pendingNavigation.skippedCandidateFreePage = true
	cmd := m.loadRollbackNavigationPageCmd(nextRequest)
	if m.pendingDetailTranscript == nil {
		m.rollback.pendingNavigation = nil
	}
	return cmd, true
}

func rollbackNavigationRequestAfterPage(
	direction tui.DetailTranscriptPageDirection,
	page clientui.TranscriptPage,
) (clientui.TranscriptPageRequest, bool) {
	switch direction {
	case tui.DetailTranscriptPageOlder:
		if page.HasMoreAbove && page.OlderCursor != nil {
			return clientui.TranscriptPageRequest{
				Cursor: valuecopy.Pointer(page.OlderCursor),
			}, true
		}
	case tui.DetailTranscriptPageNewer:
		if page.HasMoreBelow && page.NewerCursor != nil {
			return clientui.TranscriptPageRequest{
				NewerCursor: valuecopy.Pointer(page.NewerCursor),
			}, true
		}
	}
	return clientui.TranscriptPageRequest{}, false
}

func (m *uiModel) rollbackIsolatedPageAnchor(
	request clientui.TranscriptPageRequest,
) (tui.DetailTranscriptPageAnchor, bool) {
	if m.isRollbackLocatorActivationRequest(request) {
		return tui.DetailTranscriptAnchorBottom, true
	}
	pending := m.rollback.pendingNavigation
	if pending == nil || !pending.skippedCandidateFreePage ||
		!pageRequestEqual(request, pending.request) {
		return tui.DetailTranscriptAnchorDefault, false
	}
	switch pending.direction {
	case tui.DetailTranscriptPageOlder:
		return tui.DetailTranscriptAnchorBottom, true
	case tui.DetailTranscriptPageNewer:
		return tui.DetailTranscriptAnchorTop, true
	default:
		panic(fmt.Sprintf("invalid rollback page navigation direction: %d", pending.direction))
	}
}

func (m *uiModel) rollbackNavigationDeadlineExceeded(request clientui.TranscriptPageRequest) bool {
	pending := m.rollback.pendingNavigation
	return pending != nil &&
		pageRequestEqual(request, pending.request) &&
		!time.Now().Before(pending.deadline)
}

func (m *uiModel) rollbackDetailPageLoadFailed(request clientui.TranscriptPageRequest) {
	if m == nil {
		return
	}
	if m.rollback.isAwaitingNewest() && request.Cursor == nil && request.NewerCursor == nil {
		m.resetRollbackState()
		return
	}
	if m.rollback.isAwaitingOlderCandidate() && request.Cursor != nil && request.NewerCursor == nil {
		m.resetRollbackState()
		return
	}
	if m.rollback.pendingNavigation != nil &&
		pageRequestEqual(request, m.rollback.pendingNavigation.request) {
		m.rollback.pendingNavigation = nil
	}
}

func (m *uiModel) beginRollbackEditing() bool {
	selected, _, ok := m.selectedRollbackCandidate()
	if !ok || !m.rollback.isSelecting() || m.rollback.pendingNavigation != nil {
		return false
	}
	selectedCopy := selected
	m.rollback.editingCandidate = &selectedCopy
	m.rollback.phase = uiRollbackPhaseEditing
	m.setInputMode(uiInputModeRollbackEdit)
	m.replaceMainInput(selected.Text, len([]rune(selected.Text)))
	m.applyRollbackSelectionHighlight()
	return true
}

func (m *uiModel) cancelRollbackEditingBackToSelection() bool {
	if m == nil || !m.rollback.isEditing() {
		return false
	}
	m.rollback.editingCandidate = nil
	m.rollback.phase = uiRollbackPhaseSelection
	m.setInputMode(uiInputModeRollbackSelection)
	m.replaceMainInput("", 0)
	m.applyRollbackSelectionHighlight()
	return len(m.rollback.candidates) > 0
}

func (m *uiModel) selectedRollbackCandidate() (rollbackCandidate, int, bool) {
	if m == nil || m.rollback.selectedTargetID == nil {
		return rollbackCandidate{}, 0, false
	}
	index, ok := m.rollbackCandidateIndex(*m.rollback.selectedTargetID)
	if !ok {
		return rollbackCandidate{}, 0, false
	}
	return m.rollback.candidates[index], index, true
}

func (m *uiModel) rollbackCandidateIndex(targetID string) (int, bool) {
	for index, candidate := range m.rollback.candidates {
		if candidate.RollbackTargetID == targetID {
			return index, true
		}
	}
	return 0, false
}

func (m *uiModel) setSelectedRollbackCandidate(candidate rollbackCandidate) {
	targetID := candidate.RollbackTargetID
	m.rollback.selectedTargetID = &targetID
}

func (m *uiModel) resetRollbackState() {
	if m == nil {
		return
	}
	restoreDetailTranscript := m.rollback.restoreDetailTranscript
	restoreDetailPresentation := m.rollback.restoreDetailPresentation
	m.rollback = uiRollbackState{phase: uiRollbackPhaseInactive}
	if restoreDetailTranscript == nil {
		return
	}
	m.detailTranscript = restoreDetailTranscript.clone()
	if !m.detailTranscript.loaded {
		m.forwardToView(tui.ResetDetailTranscriptMsg{})
		return
	}
	m.forwardToView(tui.SetDetailTranscriptPageMsg{
		Page: m.detailTranscript.page(),
	})
	if restoreDetailPresentation != nil {
		m.forwardToView(tui.RestoreDetailPresentationMsg{Snapshot: *restoreDetailPresentation})
	}
}

func (m *uiModel) discardRollbackStateForSessionReplacement() tea.Cmd {
	if m == nil {
		return nil
	}
	wasRollback := m.rollback.isActive() || m.rollback.isAwaitingActivation() ||
		m.inputMode() == uiInputModeRollbackSelection ||
		m.inputMode() == uiInputModeRollbackEdit
	closeCmd := m.popRollbackOverlay()
	m.rollback = uiRollbackState{phase: uiRollbackPhaseInactive}
	if wasRollback {
		m.clearInput()
		m.restorePrimaryInputMode()
	}
	return closeCmd
}

func (m *uiModel) pushRollbackOverlayIfNeeded() tea.Cmd {
	if m.surface() == uiSurfaceRollbackSelection {
		return nil
	}
	if m.rollback.restoreTranscriptMode == nil {
		restoreMode := m.view.Mode()
		m.rollback.restoreTranscriptMode = &restoreMode
	}
	surfaceCmd := m.activateSurface(uiSurfaceRollbackSelection)
	if m.view.Mode() != tui.ModeOngoing {
		return surfaceCmd
	}
	transitionCmd := m.transitionTranscriptModeWithOptions(transcriptModeTransitionOptions{
		target:            tui.ModeDetail,
		skipDetailWarmup:  true,
		suppressAltScreen: true,
		preserveSurface:   true,
	})
	return sequenceCmds(surfaceCmd, transitionCmd)
}

func (m *uiModel) popRollbackOverlay() tea.Cmd {
	if m.surface() != uiSurfaceRollbackSelection {
		return nil
	}
	restoreMode := m.view.Mode()
	if m.rollback.restoreTranscriptMode != nil {
		restoreMode = *m.rollback.restoreTranscriptMode
	}
	m.rollback.restoreTranscriptMode = nil
	surfaceCmd := m.activateSurface(surfaceForTranscriptMode(restoreMode))
	if restoreMode == m.view.Mode() {
		return surfaceCmd
	}
	transitionCmd := m.transitionTranscriptModeWithOptions(transcriptModeTransitionOptions{
		target:            restoreMode,
		skipDetailWarmup:  true,
		suppressAltScreen: true,
		preserveSurface:   true,
	})
	return sequenceCmds(surfaceCmd, transitionCmd)
}
