package app

import (
	"core/cli/tui"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

var writeTerminalSequence = func(sequence string) {
	_, _ = os.Stdout.WriteString(sequence)
}

func (m *uiModel) toggleTranscriptMode() tea.Cmd {
	target := tui.ModeDetail
	if m.view.Mode() == tui.ModeDetail {
		target = tui.ModeOngoing
	}
	return m.transitionTranscriptModeWithOptions(transcriptModeTransitionOptions{
		target:           target,
		skipDetailWarmup: false,
	})
}

type transcriptModeTransitionOptions struct {
	target            tui.Mode
	skipDetailWarmup  bool
	suppressAltScreen bool
	preserveSurface   bool
}

func (m *uiModel) transitionTranscriptModeWithOptions(options transcriptModeTransitionOptions) tea.Cmd {
	prevMode := m.view.Mode()
	m.forwardToView(tui.SetModeMsg{Mode: options.target, SkipDetailWarmup: options.skipDetailWarmup})
	nextMode := m.view.Mode()
	if nextMode != tui.ModeOngoing {
		m.helpVisible = false
	} else if prevMode != nextMode && m.inputMode() == uiInputModeMain {
		m.restorePrimaryInputMode()
	}
	if !options.preserveSurface && (nextMode == tui.ModeOngoing || nextMode == tui.ModeDetail) {
		m.activeSurface = surfaceForTranscriptMode(nextMode)
		m.syncRendererOutputGate()
	}
	clearCmd := m.clearCmdForModeTransition(prevMode, nextMode)
	transitionCmd := tea.Cmd(nil)
	if !options.suppressAltScreen {
		transitionCmd = m.altScreenCmdForModeTransition(prevMode, nextMode)
	}
	detailLoadCmd := m.detailLoadCmdForModeTransition(prevMode, nextMode)
	if clearCmd == nil && transitionCmd == nil && detailLoadCmd == nil {
		return nil
	}
	return sequenceCmds(clearCmd, transitionCmd, detailLoadCmd)
}

func (m *uiModel) clearCmdForModeTransition(prev, next tui.Mode) tea.Cmd {
	if prev == next {
		return nil
	}
	if next != tui.ModeDetail {
		return nil
	}
	return nil
}

func (m *uiModel) detailLoadCmdForModeTransition(prev, next tui.Mode) tea.Cmd {
	return nil
}

func sequenceCmds(cmds ...tea.Cmd) tea.Cmd {
	filtered := make([]tea.Cmd, 0, len(cmds))
	for _, cmd := range cmds {
		if cmd != nil {
			filtered = append(filtered, cmd)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	if len(filtered) == 1 {
		return filtered[0]
	}
	return tea.Sequence(filtered...)
}

func (m *uiModel) altScreenCmdForModeTransition(prev, next tui.Mode) tea.Cmd {
	if prev == next {
		return nil
	}
	return m.altScreenCmdForSurfaceTransition(surfaceForTranscriptMode(prev), surfaceForTranscriptMode(next))
}

func enableAlternateScrollCmd() tea.Cmd {
	return func() tea.Msg {
		writeTerminalSequence("\x1b[?1007h")
		return nil
	}
}

func disableAlternateScrollCmd() tea.Cmd {
	return func() tea.Msg {
		writeTerminalSequence("\x1b[?1007l")
		return nil
	}
}
