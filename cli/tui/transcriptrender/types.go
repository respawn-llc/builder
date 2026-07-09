package transcriptrender

import "core/shared/clientui"

type Mode uint8

const (
	ModeOngoing Mode = iota
	ModeOngoingCollapsed
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
	Spans []Span
}

type Span struct {
	Text  string
	Role  StyleRole
	Faint bool
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
	for _, span := range l.Spans {
		out += span.Text
	}
	return out
}
