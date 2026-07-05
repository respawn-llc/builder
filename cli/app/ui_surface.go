package app

import (
	"core/cli/tui"

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
	return m.activateSurfaceFrom(m.surface(), surface, false)
}

func (m *uiModel) activateSurfaceFrom(prev, surface uiSurface, suppressAltScreen bool) tea.Cmd {
	if surface == "" {
		surface = surfaceForTranscriptMode(m.view.Mode())
	}
	m.updateOngoingOwnershipBeforeSurfaceTransition(prev, surface)
	m.activeSurface = surface
	m.syncRendererOutputGate()
	if prev == surface || suppressAltScreen {
		return nil
	}
	return sequenceCmds(
		m.altScreenCmdForSurfaceTransition(prev, surface),
		m.ongoingOwnershipAfterSurfaceTransitionCmd(prev, surface),
	)
}

type uiPresentationFeatureReducer struct {
	model *uiModel
}

func (m *uiModel) presentationReducer() uiPresentationFeatureReducer {
	return uiPresentationFeatureReducer{model: m}
}

func (r uiPresentationFeatureReducer) Update(msg tea.Msg) uiFeatureUpdateResult {
	return uiFeatureUpdateResult{}
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
			return tea.Sequence(disableAlternateScrollCmd(), tea.ExitAltScreen)
		}
		return tea.ExitAltScreen
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
