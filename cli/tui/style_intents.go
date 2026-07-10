package tui

import (
	"core/cli/tui/transcriptrender"
	"core/shared/theme"

	"github.com/charmbracelet/lipgloss"
)

type StyleIntent uint16

const (
	ThemeForeground StyleIntent = 1 << iota
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

func transcriptSpanStyle(span transcriptrender.Span, themeName string) lipgloss.Style {
	resolved := transcriptrender.ResolveSpanStyle(span, themeName)
	style := lipgloss.NewStyle()
	switch resolved.Foreground.Kind {
	case transcriptrender.ResolvedForegroundTheme:
		style = style.Foreground(resolved.Foreground.Theme.Lipgloss())
	case transcriptrender.ResolvedForegroundRGB:
		style = style.Foreground(lipgloss.Color(resolved.Foreground.RGB.Hex()))
	default:
		panic("transcript span resolved an invalid foreground kind")
	}
	if resolved.Faint {
		style = style.Faint(true)
	}
	if resolved.Bold {
		style = style.Bold(true)
	}
	if resolved.Italic {
		style = style.Italic(true)
	}
	if resolved.Underline {
		style = style.Underline(true)
	}
	return style
}
