package app

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type uiChoiceGroupKind uint8

const (
	uiChoiceGroupKindRadio uiChoiceGroupKind = iota
	uiChoiceGroupKindButton
)

type uiChoiceOption struct {
	Label string
}

func renderUIChoiceGroupLine(width int, theme string, kind uiChoiceGroupKind, options []uiChoiceOption, selectedIndex int) string {
	p := uiPalette(theme)
	selectedStyle := lipgloss.NewStyle().Foreground(p.primary).Bold(true)
	defaultStyle := lipgloss.NewStyle().Foreground(p.muted).Faint(true)
	return renderUIChoiceGroupLineStyled(width, kind, options, selectedIndex, selectedStyle, defaultStyle)
}

func renderUIChoiceGroupLineStyled(width int, kind uiChoiceGroupKind, options []uiChoiceOption, selectedIndex int, selectedStyle lipgloss.Style, defaultStyle lipgloss.Style) string {
	separator := "   "
	if uiChoiceGroupWidth(kind, options, separator) > width {
		separator = " "
	}
	b := strings.Builder{}
	used := 0
	for index, option := range options {
		label := strings.TrimSpace(option.Label)
		if label == "" {
			continue
		}
		if used > 0 {
			b.WriteString(defaultStyle.Render(separator))
		}
		selected := index == selectedIndex
		style := defaultStyle
		if selected {
			style = selectedStyle
		}
		switch kind {
		case uiChoiceGroupKindButton:
			b.WriteString(style.Render("[ " + label + " ]"))
		default:
			marker := "( )"
			if selected {
				marker = "(●)"
			}
			b.WriteString(style.Render(marker + " " + label))
		}
		used++
	}
	line := b.String()
	remaining := width - lipgloss.Width(line)
	if remaining > 0 {
		line += defaultStyle.Render(strings.Repeat(" ", remaining))
	}
	return line
}

func uiChoiceGroupWidth(kind uiChoiceGroupKind, options []uiChoiceOption, separator string) int {
	width := 0
	used := 0
	for _, option := range options {
		label := strings.TrimSpace(option.Label)
		if label == "" {
			continue
		}
		if used > 0 {
			width += lipgloss.Width(separator)
		}
		switch kind {
		case uiChoiceGroupKindButton:
			width += lipgloss.Width("[ " + label + " ]")
		default:
			width += lipgloss.Width("(●) " + label)
		}
		used++
	}
	return width
}
