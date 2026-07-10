package transcriptrender

import (
	"core/shared/clientui"
	"core/shared/theme"
)

type Mode uint8

const (
	ModeOngoing Mode = iota
	ModeOngoingCollapsed
	ModeOngoingFull
	ModeDetailCollapsed
	ModeDetailExpanded
)

type Row struct {
	Group clientui.TranscriptRowKind
	Lines []Line
}

type StyleRole uint8

const (
	StyleRoleUser StyleRole = iota
	StyleRoleAssistant
	StyleRoleMarkdownCode
	StyleRoleTool
	StyleRoleToolSuccess
	StyleRoleToolError
	StyleRoleToolShell
	StyleRoleToolShellPrimary
	StyleRoleToolShellSecondary
	StyleRoleToolShellWarning
	StyleRoleToolShellError
	StyleRoleToolPatch
	StyleRoleToolQuestion
	StyleRoleToolWebSearch
	StyleRoleNotice
	StyleRoleNoticeForeground
	StyleRoleNoticeForegroundFaint
	StyleRoleNoticePrimary
	StyleRoleNoticeSecondary
	StyleRoleNoticeReviewer
	StyleRoleWarning
	StyleRoleError
)

type ColorRole uint8

const (
	ColorRoleForeground ColorRole = iota
	ColorRolePrimary
	ColorRoleSecondary
	ColorRoleUser
	ColorRoleAssistant
	ColorRoleTool
	ColorRoleToolSuccess
	ColorRoleToolError
	ColorRoleSuccess
	ColorRoleWarning
	ColorRoleError
	ColorRoleSubdued
)

func ColorRoleForStyle(role StyleRole) ColorRole {
	switch role {
	case StyleRoleUser,
		StyleRoleAssistant,
		StyleRoleTool,
		StyleRoleToolShell,
		StyleRoleToolPatch,
		StyleRoleToolQuestion,
		StyleRoleToolWebSearch,
		StyleRoleNoticeForeground,
		StyleRoleNoticeForegroundFaint:
		return ColorRoleForeground
	case StyleRoleNoticePrimary:
		return ColorRolePrimary
	case StyleRoleMarkdownCode:
		return ColorRolePrimary
	case StyleRoleNoticeSecondary:
		return ColorRoleSecondary
	case StyleRoleNoticeReviewer:
		return ColorRoleSuccess
	case StyleRoleToolShellPrimary:
		return ColorRolePrimary
	case StyleRoleToolShellSecondary:
		return ColorRoleSecondary
	case StyleRoleToolShellWarning:
		return ColorRoleWarning
	case StyleRoleToolShellError:
		return ColorRoleError
	case StyleRoleToolSuccess:
		return ColorRoleToolSuccess
	case StyleRoleToolError:
		return ColorRoleError
	case StyleRoleWarning:
		return ColorRoleWarning
	case StyleRoleError:
		return ColorRoleError
	case StyleRoleNotice:
		return ColorRoleSubdued
	default:
		return ColorRoleTool
	}
}

type Line struct {
	LeadingSymbol *Span
	Spans         []Span
}

type Span struct {
	Text      string
	Role      StyleRole
	Faint     bool
	Bold      bool
	Italic    bool
	Underline bool
}

type ResolvedSpanStyle struct {
	Foreground theme.Color
	Faint      bool
	Bold       bool
	Italic     bool
	Underline  bool
}

func ResolveSpanStyle(span Span, themeName string) ResolvedSpanStyle {
	return ResolvedSpanStyle{
		Foreground: ColorForRole(ColorRoleForStyle(span.Role), themeName),
		Faint:      span.Faint,
		Bold:       span.Bold,
		Italic:     span.Italic,
		Underline:  span.Underline,
	}
}

func PlainLines(lines []Line) []string {
	if len(lines) == 0 {
		return nil
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, line.Plain())
	}
	return out
}

func (l Line) Plain() string {
	out := ""
	if l.LeadingSymbol != nil {
		out += l.LeadingSymbol.Text
	}
	for _, span := range l.Spans {
		out += span.Text
	}
	return out
}

func (l Line) WithLeadingSymbolText(text string) Line {
	if l.LeadingSymbol == nil {
		return l
	}
	symbol := *l.LeadingSymbol
	symbol.Text = text
	l.LeadingSymbol = &symbol
	return l
}
