package core

import (
	"strings"

	"core/server/workflow"
	"core/shared/config"
)

type configRoleResolver struct {
	settings config.Settings
}

func (r configRoleResolver) RoleExists(role string) bool {
	trimmed := strings.TrimSpace(role)
	if trimmed == "" {
		return false
	}
	if workflow.IsDefaultAgentRole(trimmed) {
		return true
	}
	return config.LookupSubagentRole(r.settings, trimmed).Status == config.SubagentRoleLookupPresent
}
