package app

import (
	"errors"
	"strings"

	"core/cli/tui"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

const nativeMaxPendingEmissions = 1000

var errNativeStableNonAppend = errors.New("native stable output cannot apply non-append committed transcript change")
var errNativeStableNonGapHydration = errors.New("native stable output cannot hydrate non-gap committed transcript divergence")
var errNativeAssistantStreamFinalizerMismatch = errors.New("native active assistant stream finalizer does not extend streamed source")
var errNativeAssistantStreamScratchAbort = errors.New("native scratch hydration could not abort active assistant stream")

type nativePendingEmissionKind uint8

const (
	nativePendingEmissionEntries nativePendingEmissionKind = iota + 1
	nativePendingEmissionScratch
)

type nativePendingEmission struct {
	kind           nativePendingEmissionKind
	entries        []tui.TranscriptEntry
	prependDivider bool
	fatalOnFailure bool
}

type nativeAssistantStreamFinalizerEmission struct {
	entries                 []tui.TranscriptEntry
	skippedLeadingCommitted int
	suffix                  string
	phase                   clientui.MessagePhase
}

func (m *uiModel) nativeStableOutputReady() bool {
	return m.nativeStableOutputReadyIgnoringScratch() && !m.nativeScratchHydrationPending
}

func (m *uiModel) nativeStableOutputReadyIgnoringScratch() bool {
	if m == nil || !m.nativeSurfaceConfigured() {
		return false
	}
	if !m.nativeSurfaceEnabled() || !m.nativeStableSurfaceReadyForCurrentGeometry() {
		return false
	}
	return m.nativeNormalBufferAvailable()
}

func (m *uiModel) emitNativeCommittedEntries(entries []tui.TranscriptEntry, prependDivider bool) error {
	if len(entries) == 0 || m == nil || m.nativeSurface == nil || !m.nativeSurface.initialized() {
		return nil
	}
	if m.nativeSurface.AssistantStreaming() {
		return m.queueNativeEmission(nativePendingEmission{
			kind:           nativePendingEmissionEntries,
			entries:        cloneTUITranscriptEntries(entries),
			prependDivider: prependDivider,
		})
	}
	lines := m.nativeProjectionLinesForCommittedEntries(entries, prependDivider)
	if err := m.steerNativeProjectionLines(lines); err != nil {
		return err
	}
	if len(lines) > 0 {
		m.nativeImmutableTranscriptWritten = true
	}
	return nil
}

func (m *uiModel) nativeCommittedEntriesAfterActiveAssistantFinalizer(entries []tui.TranscriptEntry, streamText string) ([]tui.TranscriptEntry, int, error) {
	if m == nil || m.nativeSurface == nil || !m.nativeSurface.AssistantStreaming() || streamText == "" {
		return entries, 0, nil
	}
	planned, err := planNativeAssistantStreamFinalizerEmission(entries, streamText)
	if err != nil {
		return nil, 0, err
	}
	if planned.suffix != "" {
		handled, err := m.streamNativeAssistantDelta(planned.suffix, planned.phase)
		if handled && err != nil {
			return nil, 0, err
		}
	}
	if err := m.finishNativeAssistantStreaming(); err != nil {
		return nil, 0, err
	}
	if err := m.drainNativePendingEmissionsAfterAssistantFinalizer(); err != nil {
		return nil, 0, err
	}
	return planned.entries, planned.skippedLeadingCommitted, nil
}

func planNativeAssistantStreamFinalizerEmission(entries []tui.TranscriptEntry, streamText string) (nativeAssistantStreamFinalizerEmission, error) {
	planned := nativeAssistantStreamFinalizerEmission{
		entries: make([]tui.TranscriptEntry, 0, len(entries)),
		phase:   clientui.MessagePhaseFinal,
	}
	emittedCommittedBeforeFinalizer := false
	finalized := false
	for _, entry := range entries {
		if finalized {
			planned.entries = append(planned.entries, entry)
			if !(entry.Transient && !entry.Committed) {
				emittedCommittedBeforeFinalizer = true
			}
			continue
		}
		if entry.Transient && !entry.Committed {
			continue
		}
		if entry.Role != tui.TranscriptRoleAssistant {
			planned.entries = append(planned.entries, entry)
			emittedCommittedBeforeFinalizer = true
			continue
		}
		if !isFinalAssistantTranscriptEntry(entry) {
			return nativeAssistantStreamFinalizerEmission{}, errNativeAssistantStreamFinalizerMismatch
		}
		suffix, ok := assistantFinalTextExtendsStream(streamText, entry.Text)
		if !ok {
			return nativeAssistantStreamFinalizerEmission{}, errNativeAssistantStreamFinalizerMismatch
		}
		phase := entry.Phase
		if strings.TrimSpace(string(phase)) == "" {
			phase = clientui.MessagePhaseFinal
		}
		planned.suffix = suffix
		planned.phase = phase
		if !emittedCommittedBeforeFinalizer {
			planned.skippedLeadingCommitted++
		}
		finalized = true
	}
	return planned, nil
}

func (m *uiModel) appendNativeScratchTranscript(entries []tui.TranscriptEntry) error {
	if m == nil || !m.nativeStableOutputReadyIgnoringScratch() {
		return nil
	}
	if m.nativeSurface.AssistantStreaming() {
		if err := m.abortNativeAssistantStreamForScratch(); err != nil {
			return err
		}
	}
	lines := m.nativeProjectionLinesForCommittedEntries(entries, false)
	if err := m.steerNativeProjectionLines(lines); err != nil {
		return err
	}
	if len(lines) > 0 {
		m.nativeImmutableTranscriptWritten = true
	}
	return nil
}

func (m *uiModel) abortNativeAssistantStreamForScratch() error {
	if m == nil || m.nativeSurface == nil || !m.nativeSurface.AssistantStreaming() {
		return nil
	}
	m.nativeSurface.clearLiveFrame()
	m.nativeSurface.Drop()
	if !m.nativeSurface.ensure(m.termWidth, m.termHeight) {
		return errNativeAssistantStreamScratchAbort
	}
	m.nativeAssistantStreamIncomplete = false
	return nil
}

func (m *uiModel) queueNativeEmission(emission nativePendingEmission) error {
	if m == nil || emission.kind == 0 {
		return nil
	}
	switch emission.kind {
	case nativePendingEmissionEntries:
		if len(emission.entries) == 0 {
			return nil
		}
	case nativePendingEmissionScratch:
	default:
		return nil
	}
	m.nativePendingEmissions = append(m.nativePendingEmissions, emission)
	if len(m.nativePendingEmissions) > nativeMaxPendingEmissions {
		m.nativePendingEmissions = nil
		m.nativeScratchHydrationPending = true
	}
	return nil
}

func (m *uiModel) drainNativePendingEmissions() tea.Cmd {
	if m == nil || !m.nativeStableOutputReadyIgnoringScratch() || len(m.nativePendingEmissions) == 0 {
		return nil
	}
	pending := append([]nativePendingEmission(nil), m.nativePendingEmissions...)
	m.nativePendingEmissions = nil
	for _, emission := range pending {
		switch emission.kind {
		case nativePendingEmissionScratch:
			if err := m.appendNativeScratchTranscript(emission.entries); err != nil {
				if emission.fatalOnFailure {
					return m.nativeScratchHydrationFailed(err)
				}
				return m.nativeSurfaceErrorCmd("native pending scratch emission", err)
			}
			m.nativeScratchHydrationPending = false
		case nativePendingEmissionEntries:
			if m.nativeScratchHydrationPending {
				m.nativePendingEmissions = append(m.nativePendingEmissions, emission)
				continue
			}
			if err := m.emitNativeCommittedEntries(emission.entries, emission.prependDivider); err != nil {
				return m.nativeSurfaceErrorCmd("native pending emission", err)
			}
			if m.nativeScratchHydrationPending {
				return m.requestRuntimeNativeScratchTranscriptSync()
			}
		}
	}
	return nil
}

func (m *uiModel) drainNativePendingEmissionsAfterAssistantFinalizer() error {
	if m == nil || m.nativeScratchHydrationPending || !m.nativeStableOutputReadyIgnoringScratch() || len(m.nativePendingEmissions) == 0 {
		return nil
	}
	for len(m.nativePendingEmissions) > 0 {
		emission := m.nativePendingEmissions[0]
		if emission.kind != nativePendingEmissionEntries {
			return nil
		}
		m.nativePendingEmissions = m.nativePendingEmissions[1:]
		if err := m.emitNativeCommittedEntries(emission.entries, emission.prependDivider); err != nil {
			m.nativePendingEmissions = append([]nativePendingEmission{emission}, m.nativePendingEmissions...)
			return err
		}
		if m.nativeScratchHydrationPending {
			return nil
		}
	}
	return nil
}

func (m *uiModel) nativeProjectionLinesForCommittedEntries(entries []tui.TranscriptEntry, prependDivider bool) []tui.TranscriptProjectionLine {
	projection := m.nativeCommittedProjectionForEntries(entries)
	lines := projection.Lines(tui.TranscriptDivider)
	if !prependDivider || len(lines) == 0 {
		return lines
	}
	if lines[0].Kind == tui.VisibleLineDivider {
		return lines
	}
	out := make([]tui.TranscriptProjectionLine, 0, len(lines)+1)
	out = append(out, tui.TranscriptProjectionLine{Kind: tui.VisibleLineDivider, Text: tui.TranscriptDivider})
	out = append(out, lines...)
	return out
}

func (m *uiModel) nativePrependDividerBeforeRange(rangeStart int, entries []tui.TranscriptEntry) bool {
	if m == nil || rangeStart <= 0 || len(entries) == 0 || rangeStart > len(m.transcriptEntries) {
		return false
	}
	previousEntry, ok := lastCommittedTranscriptEntry(m.transcriptEntries[:rangeStart])
	if !ok {
		return false
	}
	previousProjection := m.nativeCommittedProjectionForEntries([]tui.TranscriptEntry{previousEntry})
	currentProjection := m.nativeCommittedProjectionForEntries(entries)
	if len(previousProjection.Blocks) == 0 || len(currentProjection.Blocks) == 0 {
		return false
	}
	return previousProjection.Blocks[len(previousProjection.Blocks)-1].DividerGroup != currentProjection.Blocks[0].DividerGroup
}

func lastCommittedTranscriptEntry(entries []tui.TranscriptEntry) (tui.TranscriptEntry, bool) {
	for idx := len(entries) - 1; idx >= 0; idx-- {
		entry := entries[idx]
		if entry.Transient && !entry.Committed {
			continue
		}
		return entry, true
	}
	return tui.TranscriptEntry{}, false
}

func (m *uiModel) requestRuntimeNativeScratchTranscriptSync() tea.Cmd {
	if m == nil || !m.hasRuntimeClient() {
		return nil
	}
	m.nativeScratchHydrationPending = true
	decision := m.startRuntimeTranscriptSyncRequest(runtimeTranscriptSyncRequestForPage(clientui.TranscriptPageRequest{}, false, runtimeTranscriptSyncCauseNativeScratch, clientui.TranscriptRecoveryCauseNone))
	return decision.cmd
}

func (m *uiModel) nativeScratchHydrationFailed(err error) tea.Cmd {
	if err == nil {
		return nil
	}
	m.nativePendingEmissions = nil
	cmd := m.nativeSurfaceErrorCmd("native scratch hydration", err)
	m.nativeScratchHydrationPending = true
	return sequenceCmds(cmd, tea.Quit)
}

func assistantFinalTextExtendsStream(streamText string, finalText string) (string, bool) {
	if streamText == "" {
		return finalText, true
	}
	if finalText == streamText {
		return "", true
	}
	if strings.HasPrefix(finalText, streamText) {
		return strings.TrimPrefix(finalText, streamText), true
	}
	return "", false
}
