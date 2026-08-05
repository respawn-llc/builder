package workflow

import "strings"

type SelectorApplicabilityReason string

const (
	SelectorApplicabilityEligible                 SelectorApplicabilityReason = "eligible"
	SelectorApplicabilityTopology                 SelectorApplicabilityReason = "topology"
	SelectorApplicabilityContextSource            SelectorApplicabilityReason = "context_source"
	SelectorApplicabilityNoCallableRoles          SelectorApplicabilityReason = "no_callable_roles"
	SelectorApplicabilityNoThinkingSupport        SelectorApplicabilityReason = "no_thinking_support"
	SelectorApplicabilityUnavailableConfiguration SelectorApplicabilityReason = "unavailable_configuration"
	SelectorApplicabilitySoleCallableRole         SelectorApplicabilityReason = "sole_callable_role"
	SelectorApplicabilityNoThinkingLevels         SelectorApplicabilityReason = "no_thinking_levels"
	SelectorApplicabilitySoleThinkingLevel        SelectorApplicabilityReason = "sole_thinking_level"
)

type SelectorApplicability struct {
	Available        bool
	ParameterVisible bool
	Reason           SelectorApplicabilityReason
}

type EdgeSelectorApplicability struct {
	Assignee SelectorApplicability
	Thinking SelectorApplicability
}

func ResolveEdgeSelectorApplicability(
	edge Edge,
	source NodeKind,
	target NodeKind,
	fanOut bool,
	catalog TargetAgentCatalog,
	targetRole string,
) EdgeSelectorApplicability {
	eligibleTopology := !fanOut &&
		(source == NodeKindAgent || source == NodeKindScript) &&
		target == NodeKindAgent
	if !eligibleTopology {
		return EdgeSelectorApplicability{
			Assignee: SelectorApplicability{Reason: SelectorApplicabilityTopology},
			Thinking: SelectorApplicability{Reason: SelectorApplicabilityTopology},
		}
	}
	if edge.ContextMode == ContextModeContinueSession &&
		CanonicalContextSource(edge.ContextSource).Kind != ContextSourcePreviousTargetOrNew {
		return EdgeSelectorApplicability{
			Assignee: SelectorApplicability{Reason: SelectorApplicabilityContextSource},
			Thinking: thinkingApplicability(edge, catalog, targetRole),
		}
	}
	roles := []TargetAgentRole(nil)
	if catalog != nil {
		roles = catalog.ExplicitCallableRoles()
	}
	assignee := SelectorApplicability{Reason: SelectorApplicabilityNoCallableRoles}
	switch len(roles) {
	case 1:
		assignee.Available = true
		assignee.Reason = SelectorApplicabilitySoleCallableRole
	case 2:
		assignee.Available = true
		assignee.ParameterVisible = true
		assignee.Reason = SelectorApplicabilityEligible
	default:
		if len(roles) > 1 {
			assignee.Available = true
			assignee.ParameterVisible = true
			assignee.Reason = SelectorApplicabilityEligible
		}
	}
	return EdgeSelectorApplicability{
		Assignee: assignee,
		Thinking: thinkingApplicability(edge, catalog, targetRole),
	}
}

func thinkingApplicability(edge Edge, catalog TargetAgentCatalog, targetRole string) SelectorApplicability {
	var roles []TargetAgentRole
	if catalog != nil {
		if CanonicalAssigneeSelection(edge.AssigneeSelection) == AssigneeSelectionPreviousNode {
			roles = catalog.ExplicitCallableRoles()
		} else if role, ok := catalog.ResolveConfiguredRole(strings.TrimSpace(targetRole)); ok {
			roles = []TargetAgentRole{role}
		}
	}
	if len(roles) == 0 {
		return SelectorApplicability{Reason: SelectorApplicabilityUnavailableConfiguration}
	}
	union := UnionTargetAgentThinkingCapabilities(roles)
	if union.Finite && len(union.Levels) == 0 {
		return SelectorApplicability{Available: true, Reason: SelectorApplicabilityNoThinkingLevels}
	}
	if union.Finite && len(union.Levels) == 1 {
		return SelectorApplicability{Available: true, Reason: SelectorApplicabilitySoleThinkingLevel}
	}
	for _, role := range roles {
		if role.Thinking.ReasoningCapable {
			return SelectorApplicability{Available: true, ParameterVisible: true, Reason: SelectorApplicabilityEligible}
		}
	}
	return SelectorApplicability{Reason: SelectorApplicabilityNoThinkingSupport}
}
