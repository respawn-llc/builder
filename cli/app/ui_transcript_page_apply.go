package app

import (
	"strconv"
	"strings"

	"core/cli/app/internal/status"
	"core/cli/tui"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func (a uiRuntimeAdapter) applyProjectedChatSnapshot(snapshot clientui.ChatSnapshot) tea.Cmd {
	page := a.model.runtimeTranscript()
	page.Entries = cloneTranscriptEntries(snapshot.Entries)
	page.HasMoreAbove = false
	page.Streaming = snapshot.Streaming
	page.StreamingMetadata = cloneClientAssistantStreamMetadata(snapshot.StreamingMetadata)
	page.StreamingError = snapshot.StreamingError
	return a.applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, page, clientui.TranscriptRecoveryCauseNone)
}

func (a uiRuntimeAdapter) applyProjectedSessionMetadata(view clientui.RuntimeSessionView) tea.Cmd {
	m := a.model
	if len(m.startupCmds) > 0 {
		m.startupCmds = nil
	}
	previousWindowTitle := sessionTitle(m.sessionName)

	if transcriptPageSessionChanged(m.sessionID, view.SessionID) {
		m.detailTranscript.reset()
		m.transcriptRevision = 0
		m.transcriptLiveDirty = false
		m.reasoningLiveDirty = false
		m.clearDeferredCommittedTail("session_switch")
	}
	m.sessionID = strings.TrimSpace(view.SessionID)
	m.sessionName = strings.TrimSpace(view.SessionName)
	m.conversationFreshness = view.ConversationFreshness
	targetCmd := a.applyProjectedExecutionTarget(view.ExecutionTarget)
	if view.Transcript.Revision > m.transcriptRevision {
		m.transcriptRevision = view.Transcript.Revision
	}
	if view.Transcript.CommittedEntryCount > m.transcriptTotalEntries {
		m.transcriptTotalEntries = view.Transcript.CommittedEntryCount
	}
	titleCmd := tea.Cmd(nil)
	if previousWindowTitle != sessionTitle(m.sessionName) {
		titleCmd = tea.SetWindowTitle(sessionTitle(m.sessionName))
	}
	return sequenceCmds(titleCmd, targetCmd)
}

func (a uiRuntimeAdapter) applyProjectedExecutionTarget(target clientui.SessionExecutionTarget) tea.Cmd {
	m := a.model
	if m == nil {
		return nil
	}
	workdir := strings.TrimSpace(target.EffectiveWorkdir)
	if workdir == "" {
		workdir = strings.TrimSpace(target.WorkspaceRoot)
	}
	previousWorkdir := strings.TrimSpace(m.statusConfig.WorkspaceRoot)
	previousTarget := m.statusConfig.ExecutionTarget
	if workdir == "" {
		if !clientui.SessionExecutionTargetsEqual(previousTarget, target) {
			m.statusConfig.ExecutionTarget = target
		}
		return nil
	}
	targetChanged := !clientui.SessionExecutionTargetsEqual(previousTarget, target)
	workdirChanged := previousWorkdir != workdir
	m.statusConfig.WorkspaceRoot = workdir
	m.statusConfig.ExecutionTarget = target
	if !workdirChanged && !targetChanged {
		return nil
	}
	if workdirChanged {
		m.statusRepository = status.NewMemoryRepository()
		m.clearPathReferenceState()
	}
	if workdirChanged && m.pathReferenceSearch != nil {
		m.pathReferenceSearch.StartPrewarm(workdir)
	}
	return m.statusLineGitRefreshCmd()
}

func (a uiRuntimeAdapter) applyRuntimeTranscriptPageWithRecovery(req clientui.TranscriptPageRequest, page clientui.TranscriptPage, recoveryCause clientui.TranscriptRecoveryCause) tea.Cmd {
	return a.applyRuntimeTranscriptPageWithSyncCause(req, page, runtimeTranscriptSyncCauseCommittedConversation, recoveryCause)
}

func (a uiRuntimeAdapter) applyRuntimeTranscriptPageWithSyncCause(req clientui.TranscriptPageRequest, page clientui.TranscriptPage, syncCause runtimeTranscriptSyncCause, recoveryCause clientui.TranscriptRecoveryCause) tea.Cmd {
	m := a.model
	m.logTranscriptPageDiag("transcript.diag.client.apply_page_start", req, page, map[string]string{"path": "hydrate", "recovery_cause": string(recoveryCause)})
	if len(m.startupCmds) > 0 {
		m.startupCmds = nil
	}
	previousWindowTitle := sessionTitle(m.sessionName)

	if transcriptPageSessionChanged(m.sessionID, page.SessionID) {
		m.detailTranscript.reset()
		m.transcriptRevision = 0
		m.transcriptLiveDirty = false
		m.reasoningLiveDirty = false
		m.clearDeferredCommittedTail("session_switch")
	}
	m.sessionID = strings.TrimSpace(page.SessionID)
	if strings.TrimSpace(page.SessionName) != "" {
		m.sessionName = strings.TrimSpace(page.SessionName)
	}
	m.conversationFreshness = page.ConversationFreshness
	nativeSurfaceConfigured := m.nativeSurfaceConfigured()
	if nativeSurfaceConfigured && !m.nativeScratchHydrationPending && !m.nativeResizeRehydratePending() {
		m.ensureNativeStableSurfaceForCurrentGeometry()
	}
	reduction := reduceRuntimeTranscriptPage(newRuntimeTranscriptPageState(runtimeTranscriptPageSnapshotFromModel(m)), req, page, recoveryCause)
	pageReq := reduction.request
	page = reduction.page
	entries := reduction.entries
	if reduction.decision == runtimeTranscriptPageDecisionReject {
		m.logTranscriptPageDiag("transcript.diag.client.apply_page_reject", pageReq, page, map[string]string{
			"path":                    "hydrate",
			"reason":                  reduction.rejectReason,
			"recovery_cause":          string(recoveryCause),
			"replacement_branch":      reduction.branch,
			"preserve_live_reasoning": strconv.FormatBool(reduction.preserveLiveReasoning),
		})
		if previousWindowTitle != sessionTitle(m.sessionName) {
			return tea.SetWindowTitle(sessionTitle(m.sessionName))
		}
		return nil
	}
	if reduction.shouldApplyRecentTail {
		m.clearDeferredCommittedTail("authoritative_hydrate")
		a.applyAuthoritativeRecentTailPage(page, entries, reduction.preserveLiveReasoning)
	}
	m.refreshActiveAssistantStreamFromAuthoritativePageStreaming(page.Streaming, page.StreamingMetadata)
	m.detailTranscript.lastRequest = pageReq
	if isRecentTailTranscriptRequest(pageReq) && m.view.Mode() != tui.ModeDetail {
		m.detailTranscript.setKnownBounds(m.transcriptBaseOffset, m.transcriptTotalEntries)
		m.detailTranscript.syncTail(page)
		if m.view.Mode() != tui.ModeDetail {
			if !reduction.preserveLiveReasoning {
				m.forwardToView(tui.ClearStreamingReasoningMsg{})
			}
			m.forwardToView(tui.SetConversationMsg{
				BaseOffset:   m.transcriptBaseOffset,
				TotalEntries: m.transcriptTotalEntries,
				Entries:      entries,
				Ongoing:      page.Streaming,
				OngoingError: page.StreamingError,
			})
		}
	} else {
		if m.view.Mode() == tui.ModeDetail && m.detailTranscript.matchesPage(page) {
			m.detailTranscript.refreshEdgeCursors(page)
			m.transcriptRevision = max(m.transcriptRevision, page.Revision)
			if previousWindowTitle != sessionTitle(m.sessionName) {
				return tea.SetWindowTitle(sessionTitle(m.sessionName))
			}
			return nil
		}
		detailPinnedAwayFromTail := m.view.Mode() == tui.ModeDetail &&
			isRecentTailTranscriptRequest(pageReq) &&
			m.detailTranscript.loaded && m.detailTranscript.hasMoreBelow
		if !detailPinnedAwayFromTail {
			if pageReq.NewerCursor > 0 {
				m.detailTranscript.appendCursorPage(page)
			} else if pageReq.Cursor > 0 {
				m.detailTranscript.prependCursorPage(page)
			} else {
				m.detailTranscript.setKnownBounds(m.transcriptBaseOffset, m.transcriptTotalEntries)
				m.detailTranscript.apply(page)
			}
		}
		m.transcriptRevision = max(m.transcriptRevision, page.Revision)
		if !reduction.preserveLiveReasoning {
			m.reasoningLiveDirty = false
		}
		detailPage := m.detailTranscript.page()
		detailPage.SessionID = page.SessionID
		detailPage.SessionName = page.SessionName
		detailPage.ConversationFreshness = page.ConversationFreshness
		detailPage.Revision = page.Revision
		if m.view.Mode() == tui.ModeDetail && !detailPinnedAwayFromTail {
			if !reduction.preserveLiveReasoning {
				m.forwardToView(tui.ClearStreamingReasoningMsg{})
			}
			m.forwardToView(tui.SetConversationMsg{
				BaseOffset:   m.detailTranscript.offset,
				TotalEntries: m.detailTranscript.totalEntries,
				Entries:      transcriptEntriesFromPage(detailPage),
				Ongoing:      detailPage.Streaming,
				OngoingError: detailPage.StreamingError,
			})
			m.refreshRollbackCandidates()
		}
	}
	nativeFinishErr := error(nil)
	if strings.TrimSpace(page.Streaming) == "" {
		m.sawAssistantDelta = false
		m.clearActiveAssistantStreamSource()
		if !nativeSurfaceConfigured || !reduction.shouldApplyRecentTail {
			nativeFinishErr = m.finishNativeAssistantStreaming()
		}
	}
	cmds := make([]tea.Cmd, 0, 1)
	m.logTranscriptPageDiag("transcript.diag.client.apply_page_commit", pageReq, page, map[string]string{
		"path":                      "hydrate",
		"recovery_cause":            string(recoveryCause),
		"branch":                    reduction.branch,
		"preserve_live_reasoning":   strconv.FormatBool(reduction.preserveLiveReasoning),
		"transcript_revision_after": strconv.FormatInt(m.transcriptRevision, 10),
		"transcript_total_after":    strconv.Itoa(m.transcriptTotalEntries),
	})
	if previousWindowTitle != sessionTitle(m.sessionName) {
		cmds = append(cmds, tea.SetWindowTitle(sessionTitle(m.sessionName)))
	}
	if nativeSurfaceConfigured && reduction.shouldApplyRecentTail && m.shouldAppendNativeTranscriptPage(syncCause, recoveryCause) {
		if m.nativeSurfaceConfigured() && !m.nativeStableSurfaceReadyForCurrentGeometry() && !m.nativeResizeRehydratePending() {
			m.ensureNativeStableSurfaceForCurrentGeometry()
		}
		nativeScratchPageEntries := cloneTUITranscriptEntries(m.transcriptEntries)
		nativeScratchSupersedesPending := syncCause == runtimeTranscriptSyncCauseNativeScratch || recoveryCause == clientui.TranscriptRecoveryCauseStreamGap
		if nativeScratchSupersedesPending {
			m.nativePendingEmissions = nil
		}
		if m.nativeStableOutputReadyIgnoringScratch() {
			if err := m.appendNativeScratchTranscript(nativeScratchPageEntries); err != nil {
				if nativeScratchSupersedesPending {
					cmds = append(cmds, m.nativeScratchHydrationFailed(err))
				} else {
					cmds = append(cmds, m.nativeSurfaceErrorCmd("append native transcript page", err))
				}
			} else {
				m.nativeScratchHydrationPending = false
				m.nativeAssistantStreamIncomplete = page.Streaming != ""
			}
			if !nativeScratchSupersedesPending && !m.nativeScratchHydrationPending {
				cmds = append(cmds, m.drainNativePendingEmissions())
			}
		} else {
			m.nativeScratchHydrationPending = true
			m.nativeAssistantStreamIncomplete = page.Streaming != ""
			if err := m.queueNativeEmission(nativePendingEmission{kind: nativePendingEmissionScratch, entries: nativeScratchPageEntries, fatalOnFailure: nativeScratchSupersedesPending}); err != nil {
				if nativeScratchSupersedesPending {
					cmds = append(cmds, m.nativeScratchHydrationFailed(err))
				} else {
					cmds = append(cmds, m.nativeSurfaceErrorCmd("queue native scratch page", err))
				}
			}
		}
	}
	if nativeFinishErr != nil {
		cmds = append(cmds, m.nativeSurfaceErrorCmd("finish assistant stream", nativeFinishErr))
	}
	return sequenceCmds(cmds...)
}

func (m *uiModel) shouldAppendNativeTranscriptPage(syncCause runtimeTranscriptSyncCause, recoveryCause clientui.TranscriptRecoveryCause) bool {
	if m == nil {
		return false
	}
	if syncCause == runtimeTranscriptSyncCauseNativeScratch || recoveryCause == clientui.TranscriptRecoveryCauseStreamGap {
		return true
	}
	return syncCause == runtimeTranscriptSyncCauseBootstrap && !m.nativeImmutableTranscriptWritten
}

func (a uiRuntimeAdapter) applyAuthoritativeRecentTailPage(page clientui.TranscriptPage, entries []tui.TranscriptEntry, preserveLiveReasoning bool) {
	m := a.model
	if m == nil {
		return
	}
	totalEntries := max(m.transcriptTotalEntries, len(entries))
	if !page.HasMoreAbove {
		totalEntries = len(entries)
	}
	baseOffset := 0
	if page.HasMoreBelow && m.transcriptBaseOffset > 0 && m.transcriptBaseOffset+len(entries) <= totalEntries {
		baseOffset = m.transcriptBaseOffset
	} else if page.HasMoreAbove {
		baseOffset = totalEntries - len(entries)
		if baseOffset < 0 {
			baseOffset = 0
		}
	}
	m.transcriptBaseOffset = baseOffset
	m.transcriptEntries = append(m.transcriptEntries[:0], entries...)
	m.transcriptTotalEntries = max(totalEntries, baseOffset+len(entries))
	m.transcriptRevision = max(m.transcriptRevision, page.Revision)
	m.transcriptLiveDirty = false
	if !preserveLiveReasoning {
		m.reasoningLiveDirty = false
	}
	m.refreshRollbackCandidates()
}
