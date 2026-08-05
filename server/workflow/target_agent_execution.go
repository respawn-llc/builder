package workflow

import "strings"

// TargetAgentExecutionSelectionRequest contains the complete immutable input
// needed to materialize an Agent target assignment at a transition boundary.
// Protected values are supplied separately from ordinary parameter flow so
// arbitrary output keys cannot select an assignment.
type TargetAgentExecutionSelectionRequest struct {
	Edge                Edge
	FallbackRole        string
	Catalog             TargetAgentCatalog
	SubmittedRole       string
	SubmittedThinking   string
	ThinkingDescription string
	RetainedSession     *AgentExecutionSelection
}

// PlanTargetAgentExecutionSelection composes the role and thinking planners
// into the one transition-target assignment decision.
func PlanTargetAgentExecutionSelection(request TargetAgentExecutionSelectionRequest) (AgentExecutionSelection, error) {
	return planTargetAgentExecutionSelection(request)
}

func planTargetAgentExecutionSelection(request TargetAgentExecutionSelectionRequest) (AgentExecutionSelection, error) {
	if request.RetainedSession != nil {
		selection := request.RetainedSession.Clone()
		selection.Origin = AssigneeOriginRetainedSession
		if CanonicalThinkingSelection(request.Edge.ThinkingSelection) == ThinkingSelectionPreviousNode {
			role := TargetAgentRole{Identity: selection.Assignee}
			if request.Catalog != nil {
				if resolved, ok := request.Catalog.ResolveConfiguredRole(selection.Assignee); ok {
					role = resolved
				}
			}
			thinking, err := PlanTargetAgentThinkingSelection(TargetAgentThinkingSelectionRequest{
				OverrideEnabled:     true,
				TargetRole:          role,
				EligibleRoles:       []TargetAgentRole{role},
				SubmittedValue:      request.SubmittedThinking,
				AuthoredDescription: request.ThinkingDescription,
			})
			if err != nil {
				return AgentExecutionSelection{}, err
			}
			selection.Thinking = nil
			if strings.TrimSpace(thinking.Value) != "" {
				value, err := NewThinkingValue(thinking.Value)
				if err != nil {
					return AgentExecutionSelection{}, err
				}
				selection.Thinking = &value
			}
		}
		if err := selection.Validate(); err != nil {
			return AgentExecutionSelection{}, err
		}
		return selection, nil
	}

	assigneeSelection := CanonicalAssigneeSelection(request.Edge.AssigneeSelection)
	roleSelection, err := PlanTargetAgentSelection(request.Catalog, TargetAgentSelectionRequest{
		FallbackRole:    request.FallbackRole,
		OverrideEnabled: assigneeSelection == AssigneeSelectionPreviousNode,
		SubmittedRole:   request.SubmittedRole,
	})
	if err != nil {
		return AgentExecutionSelection{}, err
	}

	eligibleRoles := []TargetAgentRole{roleSelection.Role}
	if assigneeSelection == AssigneeSelectionPreviousNode {
		eligibleRoles = request.Catalog.ExplicitCallableRoles()
	}
	thinkingSelection, err := PlanTargetAgentThinkingSelection(TargetAgentThinkingSelectionRequest{
		OverrideEnabled:     CanonicalThinkingSelection(request.Edge.ThinkingSelection) == ThinkingSelectionPreviousNode,
		TargetRole:          roleSelection.Role,
		EligibleRoles:       eligibleRoles,
		SubmittedValue:      request.SubmittedThinking,
		AuthoredDescription: request.ThinkingDescription,
	})
	if err != nil {
		return AgentExecutionSelection{}, err
	}

	var thinking *ThinkingValue
	if strings.TrimSpace(thinkingSelection.Value) != "" {
		value, err := NewThinkingValue(thinkingSelection.Value)
		if err != nil {
			return AgentExecutionSelection{}, err
		}
		thinking = &value
	}
	origin := AssigneeOriginConfiguredFallback
	if assigneeSelection == AssigneeSelectionPreviousNode {
		origin = AssigneeOriginTransitionSelected
	}
	return NewAgentExecutionSelection(roleSelection.Role.Identity, thinking, origin)
}
