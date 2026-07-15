package testsetup

import (
	"strings"

	"core/shared/config"
	"core/shared/toolspec"
)

type RoleResolver map[string]map[toolspec.ID]bool

func QuestionsEnabled(roles ...string) RoleResolver {
	resolver := RoleResolver{
		config.DefaultSubagentRole: {toolspec.ToolAskQuestion: true},
	}
	for _, role := range roles {
		resolver[strings.TrimSpace(role)] = map[toolspec.ID]bool{toolspec.ToolAskQuestion: true}
	}
	return resolver
}

func (r RoleResolver) RoleExists(role string) bool {
	_, exists := r[strings.TrimSpace(role)]
	return exists
}

func (r RoleResolver) RoleToolEnabled(role string, tool toolspec.ID) bool {
	return r[strings.TrimSpace(role)][tool]
}
