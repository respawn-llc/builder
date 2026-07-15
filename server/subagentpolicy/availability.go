package subagentpolicy

import (
	"core/shared/config"
	"core/shared/serverapi"
)

func available(settings config.Settings, context config.SubagentInvocationContext) []string {
	names := config.AvailableSubagentRoleNames(settings, false)
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name == config.BuiltInSubagentRoleFast {
			continue
		}
		lookup := config.LookupSubagentRole(settings, name)
		if namedTargetAllowed(settings, context, lookup) {
			out = append(out, name)
		}
	}
	return out
}

func denial(kind serverapi.SubagentLaunchDenialKind, target *string, availableRoles []string) error {
	return &serverapi.SubagentLaunchDeniedError{Kind: kind, Target: target, AvailableRoles: availableRoles}
}
