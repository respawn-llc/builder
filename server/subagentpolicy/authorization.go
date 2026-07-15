package subagentpolicy

import (
	"core/shared/config"
	"core/shared/serverapi"
)

func Authorize(settings config.Settings, caller *Caller, target Target) error {
	context := config.SubagentInvocationContextOrdinary
	if caller != nil && caller.Workflow {
		context = config.SubagentInvocationContextWorkflow
	}
	if caller != nil && caller.AgentRole != nil {
		if err := authorizeNamed(settings, context, *caller.AgentRole); err != nil {
			return err
		}
	}
	if target.Kind != TargetNamed {
		return nil
	}
	lookup := config.LookupSubagentRole(settings, target.Selector)
	if lookup.Status == config.SubagentRoleLookupInvalid {
		return denial(serverapi.SubagentLaunchDenialInvalidTarget, nil, nil)
	}
	if lookup.Status == config.SubagentRoleLookupMissing {
		return denial(serverapi.SubagentLaunchDenialTargetMissing, lookup.NormalizedSelector, available(settings, context))
	}
	if caller == nil {
		return nil
	}
	return authorizeNamed(settings, context, target.Selector)
}

func authorizeNamed(settings config.Settings, context config.SubagentInvocationContext, selector string) error {
	lookup := config.LookupSubagentRole(settings, selector)
	if lookup.Status == config.SubagentRoleLookupInvalid {
		return denial(serverapi.SubagentLaunchDenialInvalidTarget, nil, nil)
	}
	if lookup.Status == config.SubagentRoleLookupMissing {
		return denial(serverapi.SubagentLaunchDenialTargetMissing, lookup.NormalizedSelector, available(settings, context))
	}
	if !namedTargetAllowed(settings, context, lookup) {
		return denial(serverapi.SubagentLaunchDenialNotCallable, lookup.NormalizedSelector, available(settings, context))
	}
	return nil
}

func namedTargetAllowed(settings config.Settings, context config.SubagentInvocationContext, lookup config.SubagentRoleLookup) bool {
	if lookup.Status != config.SubagentRoleLookupPresent || !config.SubagentRoleCallable(lookup.Role) {
		return false
	}
	return context != config.SubagentInvocationContextWorkflow ||
		*lookup.NormalizedSelector == config.BuiltInSubagentRoleFast ||
		(settings.Workflow.Subagents && config.SubagentRoleWorkflowCallable(lookup.Role))
}
