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
	StyleRoleToolPatch
	StyleRoleToolQuestion
	StyleRoleToolWebSearch
	StyleRoleNotice
	StyleRoleWarning
	StyleRoleError
)

type ColorRole uint8

const (
	ColorRoleUser ColorRole = iota
	ColorRoleAssistant
	ColorRoleTool
	ColorRoleToolSuccess
	ColorRoleToolError
	ColorRoleWarning
	ColorRoleError
	ColorRoleSubdued
)

func ColorRoleForStyle(role StyleRole) ColorRole {
	switch role {
	case StyleRoleUser, StyleRoleToolQuestion:
		return ColorRoleUser
	case StyleRoleAssistant:
		return ColorRoleAssistant
	case StyleRoleToolSuccess:
		return ColorRoleToolSuccess
	case StyleRoleToolError:
		return ColorRoleToolError
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
