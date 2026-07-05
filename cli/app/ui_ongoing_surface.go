package app

import (
	"fmt"
	"time"

	"core/cli/tui/ongoing"

	tea "github.com/charmbracelet/bubbletea"
)

type ongoingWidthRehydrationDebounceMsg struct {
	token uint64
}

type ongoingNormalBufferOwnedMsg struct {
	owned bool
}

func WithUIOngoingSurface(surface *ongoing.Surface) UIOption {
	return func(m *uiModel) {
		m.ongoingSurface = surface
		m.syncRendererOutputGate()
	}
}

func WithUIOngoingTranscriptController(controller *ongoingTranscriptController) UIOption {
	return func(m *uiModel) {
		m.ongoingTranscript = controller
	}
}

func WithUIOngoingTranscriptEvents(events <-chan ongoingTranscriptEvent) UIOption {
	return func(m *uiModel) {
		m.ongoingEvents = events
	}
}

func WithUIOngoingTranscriptReopen(request func()) UIOption {
	return func(m *uiModel) {
		m.requestOngoingOpen = request
	}
}

func (m *uiModel) nativeOngoingSurfaceActive() bool {
	return m != nil && m.ongoingSurface != nil && m.surface() == uiSurfaceOngoingTranscript
}

func (m *uiModel) scheduleOngoingWidthRehydration() tea.Cmd {
	if m == nil {
		return nil
	}
	m.ongoingWidthToken++
	token := m.ongoingWidthToken
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		return ongoingWidthRehydrationDebounceMsg{token: token}
	})
}

func (m *uiModel) handleOngoingSurfaceError(err error) tea.Cmd {
	if err == nil {
		return nil
	}
	if m != nil && m.debugMode {
		panic(err)
	}
	if m != nil {
		m.exitAction = UIActionExit
		m.forcedLocalExit = true
		m.transientStatus = fmt.Sprintf("ongoing terminal surface failed: %v", err)
		m.transientStatusKind = uiStatusNoticeError
		m.logf("ongoing.surface.error err=%q", err.Error())
	}
	return nil
}

func (m *uiModel) renderNativeOngoingSurface() tea.Cmd {
	if m == nil || m.ongoingSurface == nil || !m.nativeOngoingSurfaceActive() {
		return nil
	}
	result, err := m.ongoingSurface.Render(m.ongoingFrameInput())
	if err != nil {
		return m.handleOngoingSurfaceError(err)
	}
	return m.handleOngoingResult(result)
}

func (m *uiModel) updateOngoingOwnershipBeforeSurfaceTransition(prev, next uiSurface) {
	if m == nil || m.ongoingTranscript == nil || prev == next {
		return
	}
	if prev == uiSurfaceOngoingTranscript && next.wantsAltScreen() {
		if result, err := m.ongoingTranscript.SetNormalBufferOwned(false); err != nil {
			_ = m.handleOngoingSurfaceError(err)
		} else {
			_ = m.handleOngoingResult(result)
		}
	}
}

func (m *uiModel) ongoingOwnershipAfterSurfaceTransitionCmd(prev, next uiSurface) tea.Cmd {
	if m == nil || m.ongoingTranscript == nil || prev == next {
		return nil
	}
	if prev.wantsAltScreen() && next == uiSurfaceOngoingTranscript {
		return func() tea.Msg {
			return ongoingNormalBufferOwnedMsg{owned: true}
		}
	}
	return nil
}

func (m *uiModel) setOngoingNormalBufferOwned(owned bool) tea.Cmd {
	if m == nil || m.ongoingTranscript == nil {
		return nil
	}
	if owned {
		if cmd := m.applyPendingOngoingScratchReset(); cmd != nil {
			return cmd
		}
	}
	result, err := m.ongoingTranscript.SetNormalBufferOwned(owned)
	if err != nil {
		return m.handleOngoingSurfaceError(err)
	}
	return m.handleOngoingResult(result)
}

func waitOngoingTranscriptEvent(events <-chan ongoingTranscriptEvent) tea.Cmd {
	if events == nil {
		return nil
	}
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return nil
		}
		return event
	}
}

func (m *uiModel) handleOngoingResult(result ongoing.Result) tea.Cmd {
	switch result.Action {
	case ongoing.ResultNoop:
		return nil
	case ongoing.ResultScheduleWidthRehydration:
		return m.scheduleOngoingWidthRehydration()
	case ongoing.ResultRequestScratchRehydration:
		if m != nil && m.ongoingSurface != nil && m.nativeOngoingSurfaceActive() {
			if _, err := m.ongoingSurface.ResetForScratchHydration(result.Reason, m.ongoingFrameInput()); err != nil {
				return m.handleOngoingSurfaceError(err)
			}
			m.pendingOngoingScratchReset = nil
		} else if m != nil {
			reason := result.Reason
			m.pendingOngoingScratchReset = &reason
		}
		if m != nil && m.ongoingTranscript != nil {
			m.ongoingTranscript.ResetForScratchHydration()
		}
		if m != nil && m.requestOngoingOpen != nil {
			m.requestOngoingOpen()
		}
		return nil
	default:
		if m != nil && m.debugMode {
			panic(fmt.Sprintf("unknown ongoing surface result action %q", result.Action))
		}
		return nil
	}
}

func (m *uiModel) applyPendingOngoingScratchReset() tea.Cmd {
	if m == nil || m.pendingOngoingScratchReset == nil {
		return nil
	}
	reason := *m.pendingOngoingScratchReset
	m.pendingOngoingScratchReset = nil
	if m.ongoingSurface == nil {
		return nil
	}
	if _, err := m.ongoingSurface.ResetForScratchHydration(reason, m.ongoingFrameInput()); err != nil {
		return m.handleOngoingSurfaceError(err)
	}
	return nil
}

func (m *uiModel) ongoingFrameInput() ongoing.FrameInput {
	if m == nil {
		return ongoing.FrameInput{}
	}
	style := uiThemeStyles(m.theme)
	layout := m.layout()
	width := layout.effectiveWidth()
	height := layout.effectiveHeight()
	var sections []ongoing.FrameSection
	appendSection := func(kind ongoing.FrameSectionKind, lines []string) {
		if len(lines) == 0 {
			return
		}
		sections = append(sections, ongoing.FrameSection{Kind: kind, Lines: append([]string(nil), lines...)})
	}
	appendSection(ongoing.FrameSectionPicker, layout.renderActivePicker(width))
	appendSection(ongoing.FrameSectionQueuedOrSteered, layout.renderQueuedMessagesPane(width))
	appendSection(ongoing.FrameSectionHelp, layout.renderHelpPane(width, helpPaneMaxLines(height, 1, 0, 0), style))
	appendSection(ongoing.FrameSectionInput, layout.renderInputLines(width, style))
	if selected, ok := m.selectedPromptHistoryText(); ok {
		appendSection(ongoing.FrameSectionPromptHistory, []string{selected})
	}
	statusLine := layout.renderStatusLine(width, style)
	if statusLine != "" {
		appendSection(ongoing.FrameSectionStatus, []string{statusLine})
	}
	cursor := layout.inputPaneCursor(width)
	visible := cursor.Visible
	column := cursor.Col + 1
	if !visible {
		visible = true
		column = clampCursor(m.inputCursor, len([]rune(m.input))) + 1
	}
	return ongoing.FrameInput{
		Size:     ongoing.Size{Width: width, Height: height},
		Sections: sections,
		Cursor: ongoing.Cursor{
			Visible: visible,
			Row:     height,
			Column:  column,
		},
	}
}
