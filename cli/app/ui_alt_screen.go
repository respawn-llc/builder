package app

import (
	"context"
	"core/cli/tui"
	"core/shared/clientui"
	"core/shared/serverapi"
	"errors"
	"os"
	"strings"

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
	prevSurface := m.surface()
	next, _ := m.view.Update(tui.SetModeMsg{Mode: options.target, SkipDetailWarmup: options.skipDetailWarmup})
	if casted, ok := next.(tui.Model); ok {
		m.view = casted
	}
	nextMode := m.view.Mode()
	if nextMode != tui.ModeOngoing {
		m.helpVisible = false
	} else if prevMode != nextMode && m.inputMode() == uiInputModeMain {
		m.restorePrimaryInputMode()
	}
	surfaceTransitionCmd := tea.Cmd(nil)
	if !options.preserveSurface && (nextMode == tui.ModeOngoing || nextMode == tui.ModeDetail) {
		surfaceTransitionCmd = m.activateSurfaceFrom(prevSurface, surfaceForTranscriptMode(nextMode), options.suppressAltScreen)
	}
	clearCmd := m.clearCmdForModeTransition(prevMode, nextMode)
	transitionCmd := tea.Cmd(nil)
	if !options.suppressAltScreen && surfaceTransitionCmd == nil {
		transitionCmd = m.altScreenCmdForModeTransition(prevMode, nextMode)
	}
	detailLoadCmd := m.detailLoadCmdForModeTransition(prevMode, nextMode)
	if clearCmd == nil && surfaceTransitionCmd == nil && transitionCmd == nil && detailLoadCmd == nil {
		return nil
	}
	return sequenceCmds(clearCmd, surfaceTransitionCmd, transitionCmd, detailLoadCmd)
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
	if prev == next || next != tui.ModeDetail {
		return nil
	}
	return m.loadDetailTranscriptPageCmd(m.detailTranscript.requestedPageForDetailEntry())
}

func (m *uiModel) loadDetailTranscriptPageCmd(req clientui.TranscriptPageRequest) tea.Cmd {
	sessionID := strings.TrimSpace(m.sessionID)
	if sessionID == "" && m.engine != nil {
		sessionID = strings.TrimSpace(m.engine.SessionView().SessionID)
	}
	client := m.statusConfig.SessionViews
	return func() tea.Msg {
		if client == nil {
			return detailTranscriptLoadMsg{sessionID: sessionID, request: req, err: errors.New("session view client is required")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), uiRuntimeHydrationReadTimeout)
		defer cancel()
		resp, err := client.GetSessionTranscriptPage(ctx, serverapi.SessionTranscriptPageRequest{
			SessionID:   sessionID,
			Cursor:      req.Cursor,
			NewerCursor: req.NewerCursor,
		})
		return detailTranscriptLoadMsg{sessionID: sessionID, request: req, page: resp.Transcript, err: err}
	}
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
