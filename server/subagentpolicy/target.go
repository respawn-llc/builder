package subagentpolicy

import "core/shared/serverapi"

type TargetKind uint8

const (
	TargetOmittedBase TargetKind = iota
	TargetExplicitBase
	TargetNamed
)

type Target struct {
	Kind     TargetKind
	Selector string
}

type Caller struct {
	Workflow bool
}

func TargetFromOverride(override serverapi.RunPromptAgentRoleOverride) Target {
	if !override.Present {
		return Target{Kind: TargetOmittedBase}
	}
	if override.Default {
		return Target{Kind: TargetExplicitBase}
	}
	return Target{Kind: TargetNamed, Selector: override.Role}
}
