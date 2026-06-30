package app

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

var nativeSurfaceResizeRehydrateDebounce = time.Second

type uiWindowFeatureReducer struct {
	model *uiModel
}

func (m *uiModel) windowReducer() uiWindowFeatureReducer {
	return uiWindowFeatureReducer{model: m}
}

func (r uiWindowFeatureReducer) Update(msg tea.Msg) uiFeatureUpdateResult {
	m := r.model
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		resized := m.windowSizeKnown && (m.termWidth != msg.Width || m.termHeight != msg.Height)
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		m.windowSizeKnown = true
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, m.scheduleNativeResizeRehydrate(resized))
	}
	return uiFeatureUpdateResult{}
}

func (m *uiModel) scheduleNativeResizeRehydrate(resized bool) tea.Cmd {
	if !resized || m == nil || !m.nativeSurfaceConfigured() || m.termWidth <= 0 || m.termHeight <= 0 {
		return nil
	}
	if strings.TrimSpace(m.view.OngoingStreamingText()) != "" || m.nativeSurface.AssistantStreaming() {
		m.nativeAssistantStreamIncomplete = true
	}
	m.nativeResizeRehydrateToken++
	m.nativeResizeRehydrateSettled = false
	token := m.nativeResizeRehydrateToken
	width := m.termWidth
	height := m.termHeight
	return tea.Tick(nativeSurfaceResizeRehydrateDebounce, func(time.Time) tea.Msg {
		return nativeSurfaceResizeRehydrateMsg{token: token, width: width, height: height}
	})
}
