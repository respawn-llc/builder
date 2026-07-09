package transcriptrender

import "core/shared/theme"

func ColorForRole(role ColorRole, themeName string) theme.Color {
	tokens := theme.ResolvePalette(themeName)
	switch role {
	case ColorRoleForeground:
		return tokens.Transcript.Foreground
	case ColorRolePrimary:
		return tokens.App.Primary
	case ColorRoleSecondary:
		return tokens.App.Secondary
	case ColorRoleUser:
		return tokens.Transcript.User
	case ColorRoleAssistant:
		return tokens.Transcript.Assistant
	case ColorRoleToolSuccess:
		return tokens.Transcript.ToolSuccess
	case ColorRoleToolError:
		return tokens.Transcript.ToolError
	case ColorRoleSuccess:
		return tokens.Transcript.Success
	case ColorRoleWarning:
		return tokens.Transcript.Warning
	case ColorRoleError:
		return tokens.Transcript.Error
	case ColorRoleSubdued:
		return tokens.Transcript.Subdued
	default:
		return tokens.Transcript.Tool
	}
}
