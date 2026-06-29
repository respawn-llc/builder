package app

import (
	"core/cli/tui"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type uiSurface string

const (
	uiSurfaceOngoingTranscript uiSurface = "ongoing"
	uiSurfaceTranscriptDetail  uiSurface = "transcript_detail"
	uiSurfaceStatus            uiSurface = "status"
	uiSurfaceGoal              uiSurface = "goal"
	uiSurfaceWorktree          uiSurface = "worktree"
	uiSurfaceProcessList       uiSurface = "process_list"
	uiSurfaceRollbackSelection uiSurface = "rollback_selection"
)

func (m *uiModel) surface() uiSurface {
	if m == nil || m.activeSurface == "" {
		return surfaceForTranscriptMode(m.transcriptMode())
	}
	return m.activeSurface
}

func (m *uiModel) transcriptMode() tui.Mode {
	if m == nil {
		return tui.ModeOngoing
	}
	return m.view.Mode()
}

func surfaceForTranscriptMode(mode tui.Mode) uiSurface {
	if mode == tui.ModeDetail {
		return uiSurfaceTranscriptDetail
	}
	return uiSurfaceOngoingTranscript
}

func (surface uiSurface) isTranscript() bool {
	return surface == uiSurfaceOngoingTranscript || surface == uiSurfaceTranscriptDetail || surface == ""
}

func (surface uiSurface) wantsAltScreen() bool {
	switch surface {
	case uiSurfaceTranscriptDetail, uiSurfaceStatus, uiSurfaceGoal, uiSurfaceWorktree, uiSurfaceProcessList, uiSurfaceRollbackSelection:
		return true
	default:
		return false
	}
}

// wantsAlternateScroll reports whether a surface enables terminal alternate-scroll
// (`?1007`) while active. Per docs/dev/specs/tui-transcript.md, every alt-screen
// surface enables it except ongoing (never) and the rollback/edit picker (which
// renders inside alt-screen but ignores mouse and keeps alt-scroll off).
func (surface uiSurface) wantsAlternateScroll() bool {
	switch surface {
	case uiSurfaceTranscriptDetail, uiSurfaceStatus, uiSurfaceGoal, uiSurfaceWorktree, uiSurfaceProcessList:
		return true
	default:
		return false
	}
}

func (m *uiModel) restoreTranscriptSurface() tea.Cmd {
	next := surfaceForTranscriptMode(m.view.Mode())
	transitionCmd := m.activateSurface(next)
	return transitionCmd
}

func (m *uiModel) activateSurface(surface uiSurface) tea.Cmd {
	if surface == "" {
		surface = surfaceForTranscriptMode(m.view.Mode())
	}
	prev := m.surface()
	m.activeSurface = surface
	m.syncRendererOutputGate()
	if prev == surface {
		return nil
	}
	transitionCmd := m.altScreenCmdForSurfaceTransition(prev, surface)
	if surface == uiSurfaceOngoingTranscript {
		return sequenceCmds(transitionCmd, m.nativeSurfaceResizeRehydrateNowCmd())
	}
	return transitionCmd
}

type uiPresentationFeatureReducer struct {
	model *uiModel
}

func (m *uiModel) presentationReducer() uiPresentationFeatureReducer {
	return uiPresentationFeatureReducer{model: m}
}

func (r uiPresentationFeatureReducer) Update(msg tea.Msg) uiFeatureUpdateResult {
	m := r.model
	switch msg.(type) {
	case nativeSurfaceResumeMsg:
		m.syncRendererOutputGate()
		m.layout().syncViewport()
		if m.nativePhysicalAltScreenActive() {
			return handledUIFeatureUpdate(m, nativeSurfaceResumeRetryCmd())
		}
		if err := m.flushNativeSurfaceHoldoff(); err != nil {
			return handledUIFeatureUpdate(m, m.nativeSurfaceErrorCmd("flush native holdoff", err))
		}
		return handledUIFeatureUpdate(m, m.nativeSurfaceResizeRehydrateNowCmd())
	}
	switch msg := msg.(type) {
	case nativeSurfaceResizeRehydrateMsg:
		m.layout().syncViewport()
		if !m.nativeResizeRehydrateMessageCurrent(msg) {
			return handledUIFeatureUpdate(m, nil)
		}
		m.nativeResizeRehydrateSettled = true
		if m.surface() != uiSurfaceOngoingTranscript || m.altScreenActive || !m.nativeSurfaceConfigured() {
			return handledUIFeatureUpdate(m, nil)
		}
		if m.nativePhysicalAltScreenActive() {
			return handledUIFeatureUpdate(m, nativeSurfaceResizeRehydrateRetryCmd(msg))
		}
		surfaceReady := m.nativeSurface.ready(msg.width, msg.height)
		m.nativeResizeRehydrateActive = true
		if !surfaceReady {
			m.nativeSurface.Close()
		}
		if !surfaceReady && !m.nativeSurface.ensure(msg.width, msg.height) {
			m.nativeResizeRehydrateActive = false
			m.nativeResizeRehydrateToken = 0
			m.nativeResizeRehydrateSettled = false
			return handledUIFeatureUpdate(m, nil)
		}
		m.nativeResizeRehydrateActive = false
		m.nativeResizeRehydrateToken = 0
		m.nativeResizeRehydrateSettled = false
		if err := m.flushNativeSurfaceHoldoff(); err != nil {
			return handledUIFeatureUpdate(m, m.nativeSurfaceErrorCmd("resize flush native stable", err))
		}
		return handledUIFeatureUpdate(m, nil)
	}
	return uiFeatureUpdateResult{}
}

func nativeSurfaceResumeCmd() tea.Cmd {
	return func() tea.Msg {
		return nativeSurfaceResumeMsg{}
	}
}

func nativeSurfaceResumeRetryCmd() tea.Cmd {
	return tea.Tick(10*time.Millisecond, func(time.Time) tea.Msg {
		return nativeSurfaceResumeMsg{}
	})
}

func nativeSurfaceResizeRehydrateRetryCmd(msg nativeSurfaceResizeRehydrateMsg) tea.Cmd {
	return tea.Tick(10*time.Millisecond, func(time.Time) tea.Msg {
		return msg
	})
}

func (m *uiModel) nativeSurfaceResizeRehydrateNowCmd() tea.Cmd {
	if m == nil || m.nativeResizeRehydrateToken == 0 || !m.nativeResizeRehydrateSettled || m.termWidth <= 0 || m.termHeight <= 0 {
		return nil
	}
	msg := nativeSurfaceResizeRehydrateMsg{
		token:  m.nativeResizeRehydrateToken,
		width:  m.termWidth,
		height: m.termHeight,
	}
	return func() tea.Msg {
		return msg
	}
}

func (m *uiModel) nativeResizeRehydrateMessageCurrent(msg nativeSurfaceResizeRehydrateMsg) bool {
	return m != nil &&
		msg.token != 0 &&
		msg.token == m.nativeResizeRehydrateToken &&
		msg.width == m.termWidth &&
		msg.height == m.termHeight
}

func (m *uiModel) altScreenCmdForSurfaceTransition(prev, next uiSurface) tea.Cmd {
	prevWantsAlt := prev.wantsAltScreen()
	nextWantsAlt := next.wantsAltScreen()
	if !prevWantsAlt && nextWantsAlt && !m.altScreenActive {
		m.altScreenActive = true
		if next.wantsAlternateScroll() {
			return tea.Sequence(tea.EnterAltScreen, enableAlternateScrollCmd())
		}
		return tea.EnterAltScreen
	}
	if prevWantsAlt && !nextWantsAlt && m.altScreenActive {
		m.altScreenActive = false
		if prev.wantsAlternateScroll() {
			return tea.Sequence(disableAlternateScrollCmd(), tea.ExitAltScreen, nativeSurfaceResumeCmd())
		}
		return tea.Sequence(tea.ExitAltScreen, nativeSurfaceResumeCmd())
	}
	if prevWantsAlt && nextWantsAlt && m.altScreenActive {
		prevWantsAlternateScroll := prev.wantsAlternateScroll()
		nextWantsAlternateScroll := next.wantsAlternateScroll()
		if prevWantsAlternateScroll == nextWantsAlternateScroll {
			return nil
		}
		if nextWantsAlternateScroll {
			return enableAlternateScrollCmd()
		}
		return disableAlternateScrollCmd()
	}
	return nil
}
