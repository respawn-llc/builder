package core

import (
	"sort"
	"strings"

	"core/server/launch"
	"core/server/llm"
	"core/server/workflow"
	"core/shared/config"
	"core/shared/toolspec"
)

type configTargetAgentCatalog struct {
	settings config.Settings
}

type configRoleResolver = configTargetAgentCatalog

func (r configTargetAgentCatalog) ResolveConfiguredRole(role string) (workflow.TargetAgentRole, bool) {
	trimmed := strings.TrimSpace(role)
	if trimmed == "" {
		return workflow.TargetAgentRole{}, false
	}
	if workflow.IsDefaultAgentRole(trimmed) {
		return targetAgentRoleFromSettings(workflow.DefaultAgentRole, r.settings, false), true
	}
	lookup := config.LookupSubagentRole(r.settings, trimmed)
	if lookup.Status != config.SubagentRoleLookupPresent || lookup.NormalizedSelector == nil {
		return workflow.TargetAgentRole{}, false
	}
	effective, err := launch.ResolveConfiguredSubagentSettings(r.settings, *lookup.NormalizedSelector)
	if err != nil {
		return workflow.TargetAgentRole{}, false
	}
	targetRole := targetAgentRoleFromSettings(*lookup.NormalizedSelector, effective, lookup.Role.AgentCallableSet && lookup.Role.AgentCallable)
	return targetRole, true
}

func (r configTargetAgentCatalog) ExplicitCallableRoles() []workflow.TargetAgentRole {
	roles := make([]workflow.TargetAgentRole, 0, len(r.settings.Subagents))
	for name, role := range r.settings.Subagents {
		if !role.AgentCallableSet || !role.AgentCallable {
			continue
		}
		resolved, ok := r.ResolveConfiguredRole(name)
		if ok {
			roles = append(roles, resolved)
		}
	}
	sort.Slice(roles, func(left, right int) bool {
		return roles[left].Identity < roles[right].Identity
	})
	return roles
}

func targetAgentRoleFromSettings(identity string, settings config.Settings, explicitCallable bool) workflow.TargetAgentRole {
	model := strings.TrimSpace(settings.Model)
	_, known := llm.LookupModelCapabilityContract(model)
	capability := workflow.ThinkingCapability{
		ReasoningCapable: llm.SupportsReasoningEffortModel(model),
		Finite:           known,
	}
	if known {
		capability.Levels = llm.SupportedThinkingLevelsModel(model)
	}
	return workflow.TargetAgentRole{
		Identity:              identity,
		QuestionsEnabled:      settings.EnabledTools[toolspec.ToolAskQuestion],
		ExplicitAgentCallable: explicitCallable,
		Model:                 model,
		ConfiguredThinking:    strings.TrimSpace(settings.ThinkingLevel),
		Thinking:              capability,
	}
}
