package app

import (
	"fmt"

	"core/cli/tui/ongoing"
	"core/cli/tui/transcriptrender"

	tea "github.com/charmbracelet/bubbletea"
)

type ongoingNormalBufferOwnedMsg struct {
	owned bool
}

func WithUIOngoingSurface(surface *ongoing.Surface) UIOption {
	return func(m *uiModel) {
		m.ongoingSurface = surface
		m.syncRendererOutputGate()
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
	return tea.Quit
}

func (m *uiModel) renderNativeOngoingSurface() tea.Cmd {
	if m == nil || m.ongoingSurface == nil || !m.nativeOngoingSurfaceActive() {
		return nil
	}
	var result ongoing.Result
	var err error
	if m.ongoingTranscript != nil {
		result, err = m.ongoingTranscript.Render()
	} else {
		result, err = m.ongoingSurface.Render(m.ongoingFrameInput())
	}
	if err != nil {
		return m.handleOngoingSurfaceError(err)
	}
	return m.handleOngoingResult(result)
}

func (m *uiModel) batchWithNativeOngoingRepaint(cmd tea.Cmd) tea.Cmd {
	repaintCmd := m.renderNativeOngoingSurface()
	return tea.Batch(cmd, repaintCmd)
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
	if isOngoingNormalBufferRestoreTransition(prev, next) {
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
	resizeRepaint := owned && m.pendingOngoingResizeRepaint
	if resizeRepaint {
		m.pendingOngoingResizeRepaint = false
	}
	result, err := m.ongoingTranscript.SetNormalBufferOwned(owned)
	if err != nil {
		return m.handleOngoingSurfaceError(err)
	}
	resultCmd := m.handleOngoingResult(result)
	if resizeRepaint && result.Action == ongoing.ResultNoop {
		repaintResult, repaintErr := m.ongoingTranscript.Render()
		if repaintErr != nil {
			return m.handleOngoingSurfaceError(repaintErr)
		}
		return tea.Batch(resultCmd, m.handleOngoingResult(repaintResult))
	}
	return resultCmd
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
	m.pendingOngoingResizeRepaint = false
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
	appendStyledSection := func(kind ongoing.FrameSectionKind, lines []transcriptrender.Line) {
		if len(lines) == 0 {
			return
		}
		sections = append(sections, ongoing.FrameSection{Kind: kind, StyledLines: append([]transcriptrender.Line(nil), lines...)})
	}
	appendSection(ongoing.FrameSectionPicker, layout.renderActivePicker(width))
	appendStyledSection(ongoing.FrameSectionQueuedOrSteered, layout.renderQueuedMessageLines(width))
	appendSection(ongoing.FrameSectionHelp, layout.renderHelpPane(width, helpPaneMaxLines(height, 1, 0, 0), style))
	appendSection(ongoing.FrameSectionInput, layout.renderInputLines(width, style))
	if selected, ok := m.selectedPromptHistoryText(); ok {
		appendSection(ongoing.FrameSectionPromptHistory, terminalSafeFrameLinesForWidth([]string{selected}, width))
	}
	statusLine := layout.renderStatusLine(width, style)
	if statusLine != "" {
		appendSection(ongoing.FrameSectionStatus, []string{statusLine})
	}
	cursor := layout.inputPaneCursor(width)
	visible := cursor.Visible
	column := cursor.Col + 1
	cursorSectionRow := cursor.Row
	if !visible {
		visible = true
		column = clampCursor(m.inputCursor, len([]rune(m.input))) + 1
		cursorSectionRow = 1
	}
	frame := ongoing.FrameInput{
		Size:         ongoing.Size{Width: width, Height: height},
		Theme:        m.theme,
		SpinnerFrame: m.spinnerFrame,
		Sections:     sections,
		Cursor: ongoing.Cursor{
			Visible: visible,
			Row:     height,
			Column:  column,
			Target: &ongoing.CursorTarget{
				SectionKind: ongoing.FrameSectionInput,
				Row:         cursorSectionRow,
			},
		},
	}
	frame.Cursor.Row = ongoingFrameInputCursorTerminalRow(frame, cursorSectionRow)
	return frame
}

func ongoingFrameInputCursorSectionRow(frame ongoing.FrameInput) (int, bool) {
	if !frame.Cursor.Visible {
		return 0, false
	}
	if frame.Cursor.Target != nil && frame.Cursor.Target.SectionKind == ongoing.FrameSectionInput && frame.Cursor.Target.Row > 0 {
		return frame.Cursor.Target.Row, true
	}
	start, end, ok := ongoingFrameSectionTerminalRows(frame, ongoing.FrameSectionInput)
	if !ok || frame.Cursor.Row < start || frame.Cursor.Row > end {
		return 0, false
	}
	return frame.Cursor.Row - start + 1, true
}

func ongoingFrameInputCursorTerminalRow(frame ongoing.FrameInput, cursorSectionRow int) int {
	start, end, ok := ongoingFrameSectionTerminalRows(frame, ongoing.FrameSectionInput)
	if !ok {
		return clampTerminalCursorRow(frame.Cursor.Row, frame.Size.Height)
	}
	if cursorSectionRow <= 0 {
		cursorSectionRow = 1
	}
	inputSectionLines := end - start + 1
	if cursorSectionRow > inputSectionLines {
		cursorSectionRow = inputSectionLines
	}
	return clampTerminalCursorRow(start+cursorSectionRow-1, frame.Size.Height)
}

func ongoingFrameSectionTerminalRows(frame ongoing.FrameInput, kind ongoing.FrameSectionKind) (int, int, bool) {
	totalSectionLines := 0
	for _, section := range frame.Sections {
		totalSectionLines += ongoingFrameSectionLineCount(section)
	}
	row := frame.Size.Height - totalSectionLines + 1
	for _, section := range frame.Sections {
		count := ongoingFrameSectionLineCount(section)
		if section.Kind == kind {
			return row, row + count - 1, true
		}
		row += count
	}
	return 0, 0, false
}

func ongoingFrameSectionLineCount(section ongoing.FrameSection) int {
	return len(section.StyledLines) + len(section.Lines)
}

func clampTerminalCursorRow(row int, height int) int {
	if height <= 0 {
		return row
	}
	if row < 1 {
		return 1
	}
	if row > height {
		return height
	}
	return row
}
