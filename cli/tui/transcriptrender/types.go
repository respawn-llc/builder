package transcriptrender

import "core/shared/clientui"

type Mode uint8

const (
	ModeOngoing Mode = iota
	ModeDetailCollapsed
	ModeDetailExpanded
)

type Row struct {
	Group clientui.TranscriptRowKind
	Lines []string
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
