package app

import (
	"strings"

	"core/cli/tui"
	"core/shared/clientui"
)

type runtimeTranscriptPageDecisionKind uint8

const (
	runtimeTranscriptPageDecisionApply runtimeTranscriptPageDecisionKind = iota + 1
	runtimeTranscriptPageDecisionReject
)

type runtimeTranscriptPageState struct {
	entries                 []tui.TranscriptEntry
	baseOffset              int
	totalEntries            int
	revision                int64
	effectiveRevision       int64
	effectiveCommittedCount int
	viewMode                tui.Mode
	liveOngoing             string
	liveOngoingError        string
	transcriptLiveDirty     bool
	reasoningLiveDirty      bool
}

type runtimeTranscriptPageSnapshot struct {
	entries                 []tui.TranscriptEntry
	baseOffset              int
	totalEntries            int
	revision                int64
	effectiveRevision       int64
	effectiveCommittedCount int
	viewMode                tui.Mode
	liveOngoing             string
	liveOngoingError        string
	transcriptLiveDirty     bool
	reasoningLiveDirty      bool
}

type runtimeTranscriptPageReduction struct {
	decision                       runtimeTranscriptPageDecisionKind
	request                        clientui.TranscriptPageRequest
	page                           clientui.TranscriptPage
	entries                        []tui.TranscriptEntry
	rejectReason                   string
	branch                         string
	preserveLiveAssistantOngoing   bool
	duplicateCommittedAssistantEnd bool
	preserveLiveReasoning          bool
	shouldApplyRecentTail          bool
}

func newRuntimeTranscriptPageState(snapshot runtimeTranscriptPageSnapshot) runtimeTranscriptPageState {
	return runtimeTranscriptPageState{
		entries:                 append([]tui.TranscriptEntry(nil), snapshot.entries...),
		baseOffset:              snapshot.baseOffset,
		totalEntries:            snapshot.totalEntries,
		revision:                snapshot.revision,
		effectiveRevision:       snapshot.effectiveRevision,
		effectiveCommittedCount: snapshot.effectiveCommittedCount,
		viewMode:                snapshot.viewMode,
		liveOngoing:             snapshot.liveOngoing,
		liveOngoingError:        snapshot.liveOngoingError,
		transcriptLiveDirty:     snapshot.transcriptLiveDirty,
		reasoningLiveDirty:      snapshot.reasoningLiveDirty,
	}
}

func reduceRuntimeTranscriptPage(state runtimeTranscriptPageState, req clientui.TranscriptPageRequest, page clientui.TranscriptPage, recoveryCause clientui.TranscriptRecoveryCause) runtimeTranscriptPageReduction {
	pageReq := req
	if shouldPreserveLiveAssistantOngoingForRuntimeTranscriptPage(state, pageReq, page) {
		page.Streaming = state.liveOngoing
		page.StreamingError = state.liveOngoingError
	}
	entries := transcriptEntriesFromPage(page)
	duplicateCommittedAssistantEnd := authoritativePageDuplicatesCommittedAssistantOngoing(entries, page.Streaming, state.liveOngoing)
	if duplicateCommittedAssistantEnd {
		page.Streaming = ""
		page.StreamingError = ""
	}
	preserveLiveReasoning := shouldPreserveLiveReasoningForRuntimeTranscriptPage(state, page)
	shouldApplyRecentTail := isRecentTailTranscriptRequest(pageReq)
	reduction := runtimeTranscriptPageReduction{
		decision:                       runtimeTranscriptPageDecisionApply,
		request:                        pageReq,
		page:                           page,
		entries:                        entries,
		branch:                         runtimeTranscriptPageApplyBranch(state, pageReq),
		preserveLiveAssistantOngoing:   page.Streaming == state.liveOngoing && strings.TrimSpace(state.liveOngoing) != "",
		duplicateCommittedAssistantEnd: duplicateCommittedAssistantEnd,
		preserveLiveReasoning:          preserveLiveReasoning,
		shouldApplyRecentTail:          shouldApplyRecentTail,
	}
	if reason := runtimeTranscriptPageReplacementRejectReason(state, pageReq, page); reason != "" {
		reduction.decision = runtimeTranscriptPageDecisionReject
		reduction.rejectReason = reason
	}
	return reduction
}

func replacesRecentTailForRuntimeTranscriptPage(state runtimeTranscriptPageState, req clientui.TranscriptPageRequest) bool {
	return isRecentTailTranscriptRequest(req) && state.viewMode != tui.ModeDetail
}

func runtimeTranscriptPageApplyBranch(state runtimeTranscriptPageState, req clientui.TranscriptPageRequest) string {
	if replacesRecentTailForRuntimeTranscriptPage(state, req) {
		return "recent_tail_replace"
	}
	return "detail_merge"
}

func shouldPreserveLiveAssistantOngoingForRuntimeTranscriptPage(state runtimeTranscriptPageState, req clientui.TranscriptPageRequest, page clientui.TranscriptPage) bool {
	if replacesRecentTailForRuntimeTranscriptPage(state, req) {
		return false
	}
	effectiveRevision, _ := state.effectiveCommittedState()
	if page.Revision <= 0 || page.Revision != effectiveRevision {
		return false
	}
	trimmedLiveOngoing := strings.TrimSpace(state.liveOngoing)
	if trimmedLiveOngoing == "" || strings.TrimSpace(page.Streaming) != "" {
		return false
	}
	for idx := len(page.Entries) - 1; idx >= 0; idx-- {
		entry := page.Entries[idx]
		if strings.TrimSpace(entry.Text) == "" && strings.TrimSpace(entry.CondensedText) == "" {
			continue
		}
		if tui.TranscriptRoleFromWire(entry.Role) != tui.TranscriptRoleAssistant {
			continue
		}
		return strings.TrimSpace(entry.Text) != trimmedLiveOngoing
	}
	return true
}

func shouldPreserveLiveReasoningForRuntimeTranscriptPage(state runtimeTranscriptPageState, page clientui.TranscriptPage) bool {
	if !state.reasoningLiveDirty {
		return false
	}
	if page.Revision <= 0 {
		return true
	}
	return page.Revision <= state.revision
}

func runtimeTranscriptPageReplacementRejectReason(state runtimeTranscriptPageState, req clientui.TranscriptPageRequest, page clientui.TranscriptPage) string {
	effectiveRevision, effectiveCommittedCount := state.effectiveCommittedState()
	if page.Revision <= 0 {
		if effectiveRevision > 0 {
			return "stale_revision"
		}
		return ""
	}
	if page.Revision < effectiveRevision {
		return "stale_revision"
	}
	if !replacesRecentTailForRuntimeTranscriptPage(state, req) {
		return ""
	}
	if page.Revision == effectiveRevision && runtimeTranscriptPageTotalEntries(state, page) < effectiveCommittedCount {
		return "stale_total_entries"
	}
	if page.Revision == effectiveRevision && strings.TrimSpace(state.liveOngoing) != "" && strings.TrimSpace(page.Streaming) == "" {
		if authoritativePageCommitsLiveAssistantOngoing(state, page) {
			return ""
		}
		if authoritativePageDuplicatesCommittedAssistantOngoing(transcriptEntriesFromPage(page), page.Streaming, state.liveOngoing) {
			return ""
		}
		if committedTranscriptAlreadyMatchesAssistantOngoing(state.entries, state.liveOngoing) {
			return ""
		}
		return "same_revision_would_clear_ongoing"
	}
	if state.transcriptLiveDirty && page.Revision == effectiveRevision && shouldAcceptEqualRevisionTailReplacement(state, page) {
		return ""
	}
	if state.transcriptLiveDirty && page.Revision <= effectiveRevision {
		return "live_dirty_same_or_older_revision"
	}
	return ""
}

func (state runtimeTranscriptPageState) effectiveCommittedState() (int64, int) {
	revision := state.effectiveRevision
	if revision == 0 {
		revision = state.revision
	}
	count := state.effectiveCommittedCount
	if count == 0 {
		count = state.baseOffset + len(committedTranscriptEntriesForApp(state.entries))
	}
	return revision, max(state.totalEntries, count)
}

func authoritativePageCommitsLiveAssistantOngoing(state runtimeTranscriptPageState, page clientui.TranscriptPage) bool {
	trimmedLiveOngoing := strings.TrimSpace(state.liveOngoing)
	if trimmedLiveOngoing == "" || strings.TrimSpace(page.Streaming) != "" {
		return false
	}
	if len(page.Entries) == 0 {
		return false
	}
	currentStart := state.baseOffset
	currentEnd := currentStart + len(state.entries)
	for idx := len(page.Entries) - 1; idx >= 0; idx-- {
		entry := page.Entries[idx]
		if strings.TrimSpace(entry.Text) == "" && strings.TrimSpace(entry.CondensedText) == "" {
			continue
		}
		if tui.TranscriptRoleFromWire(entry.Role) != tui.TranscriptRoleAssistant {
			continue
		}
		if strings.TrimSpace(entry.Text) != trimmedLiveOngoing {
			return false
		}
		absolute := runtimeTranscriptPageStartEntry(state, page) + idx
		return absolute < currentStart || absolute >= currentEnd
	}
	return false
}

func committedTranscriptAlreadyMatchesAssistantOngoing(entries []tui.TranscriptEntry, liveOngoing string) bool {
	trimmedLiveOngoing := strings.TrimSpace(liveOngoing)
	if trimmedLiveOngoing == "" {
		return false
	}
	committed := committedTranscriptEntriesForApp(entries)
	for idx := len(committed) - 1; idx >= 0; idx-- {
		entry := committed[idx]
		if strings.TrimSpace(entry.Text) == "" && strings.TrimSpace(entry.CondensedText) == "" {
			continue
		}
		if entry.Role != tui.TranscriptRoleAssistant {
			return false
		}
		return strings.TrimSpace(entry.Text) == trimmedLiveOngoing
	}
	return false
}

func shouldAcceptEqualRevisionTailReplacement(state runtimeTranscriptPageState, page clientui.TranscriptPage) bool {
	currentStart := state.baseOffset
	currentEnd := currentStart + len(state.entries)
	pageStart := runtimeTranscriptPageStartEntry(state, page)
	pageEnd := pageStart + len(page.Entries)
	if pageStart > currentStart || pageEnd < currentEnd {
		return false
	}
	if pageEnd > currentEnd {
		return true
	}
	if state.liveOngoing != page.Streaming {
		return true
	}
	if state.liveOngoingError != page.StreamingError {
		return true
	}
	return false
}

func runtimeTranscriptPageStartEntry(state runtimeTranscriptPageState, page clientui.TranscriptPage) int {
	if !page.HasMoreAbove {
		return 0
	}
	totalEntries := runtimeTranscriptPageTotalEntries(state, page)
	start := totalEntries - len(page.Entries)
	if start < 0 {
		return 0
	}
	return start
}

func runtimeTranscriptPageTotalEntries(state runtimeTranscriptPageState, page clientui.TranscriptPage) int {
	if !page.HasMoreAbove && !page.HasMoreBelow {
		return len(page.Entries)
	}
	_, effectiveCommittedCount := state.effectiveCommittedState()
	return max(effectiveCommittedCount, len(page.Entries))
}
