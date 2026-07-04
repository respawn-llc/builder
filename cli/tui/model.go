package tui

import tea "github.com/charmbracelet/bubbletea"

type Mode string

const (
	ModeOngoing Mode = "ongoing"
	ModeDetail  Mode = "detail"
)

type ToggleModeMsg struct {
	SkipDetailWarmup bool
}

type SetModeMsg struct {
	Mode             Mode
	SkipDetailWarmup bool
}

type SetViewportLinesMsg struct {
	Lines int
}

type SetViewportSizeMsg struct {
	Lines int
	Width int
}

type Option func(*Model)

func WithTheme(_ string) Option {
	return func(*Model) {}
}

type Model struct {
	mode          Mode
	viewportLines int
	viewportWidth int
}

func NewModel(opts ...Option) Model {
	m := Model{
		mode:          ModeOngoing,
		viewportLines: 24,
		viewportWidth: 80,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&m)
		}
	}
	return m
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case ToggleModeMsg:
		if m.mode == ModeDetail {
			m.mode = ModeOngoing
		} else {
			m.mode = ModeDetail
		}
	case SetModeMsg:
		if msg.Mode == ModeOngoing || msg.Mode == ModeDetail {
			m.mode = msg.Mode
		}
	case SetViewportLinesMsg:
		if msg.Lines > 0 {
			m.viewportLines = msg.Lines
		}
	case SetViewportSizeMsg:
		if msg.Lines > 0 {
			m.viewportLines = msg.Lines
		}
		if msg.Width > 0 {
			m.viewportWidth = msg.Width
		}
	case tea.KeyMsg:
		if msg.String() == "tab" {
			if m.mode == ModeDetail {
				m.mode = ModeOngoing
			} else {
				m.mode = ModeDetail
			}
		}
	}
	return m, nil
}

func (m Model) View() string {
	return ""
}

func (m Model) Mode() Mode {
	return m.mode
}
