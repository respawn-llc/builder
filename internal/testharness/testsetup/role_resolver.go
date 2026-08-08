package testsetup

import (
	"sort"
	"strings"

	"core/server/workflow"
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

func (r RoleResolver) ResolveConfiguredRole(role string) (workflow.TargetAgentRole, bool) {
	trimmed := strings.TrimSpace(role)
	tools, exists := r[trimmed]
	if !exists {
		return workflow.TargetAgentRole{}, false
	}
	return workflow.TargetAgentRole{
		Identity:         strings.ToLower(trimmed),
		QuestionsEnabled: tools[toolspec.ToolAskQuestion],
	}, true
}

func (r RoleResolver) ExplicitCallableRoles() []workflow.TargetAgentRole {
	roles := make([]workflow.TargetAgentRole, 0, len(r))
	for role := range r {
		if workflow.IsDefaultAgentRole(role) {
			continue
		}
		roles = append(roles, workflow.TargetAgentRole{Identity: strings.ToLower(strings.TrimSpace(role)), ExplicitAgentCallable: true, QuestionsEnabled: r[role][toolspec.ToolAskQuestion]})
	}
	sort.Slice(roles, func(left, right int) bool {
		return roles[left].Identity < roles[right].Identity
	})
	return roles
}
