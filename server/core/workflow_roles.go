package core

import (
	"strings"

	"core/server/workflow"
	"core/shared/config"
	"core/shared/toolspec"
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

func (r configRoleResolver) RoleToolEnabled(role string, tool toolspec.ID) bool {
	trimmed := strings.TrimSpace(role)
	if workflow.IsDefaultAgentRole(trimmed) {
		return r.settings.EnabledTools[tool]
	}
	lookup := config.LookupSubagentRole(r.settings, trimmed)
	if lookup.Status != config.SubagentRoleLookupPresent {
		return false
	}
	return config.EffectiveSubagentRoleTools(r.settings.EnabledTools, lookup.Role)[tool]
}
