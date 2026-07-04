package tui

import (
	"core/shared/theme"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type StyleIntent uint16

const (
	ThemeForeground StyleIntent = 1 << iota
	Subdued
	PrimaryForeground
	SuccessForeground
	WarningForeground
	ErrorForeground
	Faint
	ShellPreview
	SyntaxHighlighted
	DiffAdded
	DiffRemoved
)

func (intent StyleIntent) Has(flag StyleIntent) bool {
	return intent&flag != 0
}

func ApplyThemeStyleIntents(text, themeName string, intents StyleIntent) string {
	if text == "" {
		return text
	}
	palette := theme.ResolvePalette(themeName)
	style := lipgloss.NewStyle()
	switch {
	case intents.Has(Subdued):
		style = style.Foreground(palette.Transcript.Subdued.Lipgloss()).Faint(true)
	case intents.Has(PrimaryForeground):
		style = style.Foreground(palette.App.Primary.Lipgloss())
	case intents.Has(SuccessForeground):
		style = style.Foreground(palette.Transcript.Success.Lipgloss())
	case intents.Has(WarningForeground):
		style = style.Foreground(palette.Transcript.Warning.Lipgloss())
	case intents.Has(ErrorForeground):
		style = style.Foreground(palette.Transcript.Error.Lipgloss())
	case intents.Has(ThemeForeground):
		style = style.Foreground(palette.Transcript.Foreground.Lipgloss())
	}
	if intents.Has(Faint) {
		style = style.Faint(true)
	}
	return style.Render(text)
}

func RenderAskQuestionMarkdownLines(question string, _ string, _ int) []string {
	lines := strings.Split(question, "\n")
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}
