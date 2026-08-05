package workflow

import (
	"fmt"
	"strings"
)

// TransitionParameterContractRequest contains the immutable facts needed to
// prepare one Edge's model-facing completion parameters.
type TransitionParameterContractRequest struct {
	Edge                         Edge
	SourceKind                   NodeKind
	TargetKind                   NodeKind
	TargetRole                   string
	RetainedTargetRole           *TargetAgentRole
	FanOut                       bool
	TargetSessionResolved        bool
	TargetSessionPolicy          AssigneeSessionPolicy
	Catalog                      TargetAgentCatalog
	RequireExecutionDescriptions bool
	FallbackRole                 string
	SubmittedRole                string
	SubmittedThinking            string
	ThinkingDescription          string
	RetainedSession              *AgentExecutionSelection
	MaterializeSelection         bool
}

type TransitionParameterContract struct {
	Parameters      []Parameter
	KnownParameters []Parameter
	Consumption     ProtectedParameterConsumption
	Thinking        ThinkingCapability
}

type TransitionSelectionPlan struct {
	Applicability      EdgeSelectorApplicability
	Contract           TransitionParameterContract
	ExecutionSelection *AgentExecutionSelection
}

func PlanTransitionSelection(request TransitionParameterContractRequest) (TransitionSelectionPlan, error) {
	plan := TransitionSelectionPlan{
		Applicability: resolveEdgeSelectorApplicability(
			request.Edge,
			request.SourceKind,
			request.TargetKind,
			request.FanOut,
			request.Catalog,
			request.TargetRole,
		),
	}
	contract, err := planTransitionParameterContract(request)
	if err != nil {
		return plan, err
	}
	plan.Contract = contract
	if request.MaterializeSelection {
		selection, err := planTargetAgentExecutionSelection(TargetAgentExecutionSelectionRequest{
			Edge:                request.Edge,
			FallbackRole:        request.FallbackRole,
			Catalog:             request.Catalog,
			SubmittedRole:       request.SubmittedRole,
			SubmittedThinking:   request.SubmittedThinking,
			ThinkingDescription: request.ThinkingDescription,
			RetainedSession:     request.RetainedSession,
		})
		if err != nil {
			return plan, err
		}
		plan.ExecutionSelection = &selection
	}
	return plan, nil
}

func PlanTransitionParameterContract(request TransitionParameterContractRequest) (TransitionParameterContract, error) {
	plan, err := PlanTransitionSelection(request)
	if err != nil {
		return TransitionParameterContract{}, err
	}
	return plan.Contract, nil
}

func planTransitionParameterContract(request TransitionParameterContractRequest) (TransitionParameterContract, error) {
	roles := transitionThinkingRoles(request)
	thinking := UnionTargetAgentThinkingCapabilities(roles)
	consumption := ResolveProtectedParameterConsumption(ProtectedParameterConsumptionRequest{
		Edge:                  request.Edge,
		SourceKind:            request.SourceKind,
		TargetKind:            request.TargetKind,
		FanOut:                request.FanOut,
		TargetSessionResolved: request.TargetSessionResolved,
		TargetSessionPolicy:   request.TargetSessionPolicy,
		ExplicitCallableRoles: explicitCallableRoleCount(request.Catalog),
		Thinking:              thinking,
	})
	planned := TransitionParameterContract{
		Consumption: consumption,
		Thinking:    thinking,
	}
	for _, parameter := range request.Edge.Parameters {
		purpose := CanonicalParameterPurpose(parameter.Purpose)
		if !validParameterPurpose(purpose) {
			return TransitionParameterContract{}, fmt.Errorf("transition parameter %q has invalid purpose %q", parameter.Key, purpose)
		}
		switch purpose {
		case ParameterPurposeOrdinary:
			planned.Parameters = append(planned.Parameters, parameter)
			planned.KnownParameters = append(planned.KnownParameters, parameter)
		case ParameterPurposeTargetAssignee:
			if consumption.Assignee == ProtectedParameterConsumptionRejectUnknown {
				continue
			}
			planned.KnownParameters = append(planned.KnownParameters, parameter)
			if consumption.Assignee != ProtectedParameterConsumptionRequiredValidate {
				continue
			}
			parameter.Description = transitionParameterDescription(
				parameter.Description,
				DefaultTargetAgentRoleDescription(explicitCallableRoles(request.Catalog)),
			)
			planned.Parameters = append(planned.Parameters, parameter)
		case ParameterPurposeTargetThinking:
			if consumption.Thinking == ProtectedParameterConsumptionRejectUnknown {
				continue
			}
			planned.KnownParameters = append(planned.KnownParameters, parameter)
			if consumption.Thinking != ProtectedParameterConsumptionRequiredValidate {
				continue
			}
			parameter.Description = transitionParameterDescription(
				parameter.Description,
				DefaultTargetAgentThinkingDescription(thinking),
			)
			if request.RequireExecutionDescriptions && !thinking.Finite && strings.TrimSpace(parameter.Description) == "" {
				return TransitionParameterContract{}, TargetAgentSelectionError{
					Code: TargetAgentSelectionErrorThinkingDescriptionRequired,
				}
			}
			planned.Parameters = append(planned.Parameters, parameter)
		}
	}
	return planned, nil
}

func transitionThinkingRoles(request TransitionParameterContractRequest) []TargetAgentRole {
	if request.RetainedTargetRole != nil {
		return []TargetAgentRole{canonicalTargetAgentRole(*request.RetainedTargetRole)}
	}
	if request.Catalog == nil {
		return nil
	}
	if CanonicalAssigneeSelection(request.Edge.AssigneeSelection) == AssigneeSelectionPreviousNode {
		return request.Catalog.ExplicitCallableRoles()
	}
	role, ok := request.Catalog.ResolveConfiguredRole(request.TargetRole)
	if !ok {
		return nil
	}
	return []TargetAgentRole{role}
}

func explicitCallableRoles(catalog TargetAgentCatalog) []TargetAgentRole {
	if catalog == nil {
		return nil
	}
	return catalog.ExplicitCallableRoles()
}

func explicitCallableRoleCount(catalog TargetAgentCatalog) int {
	return len(explicitCallableRoles(catalog))
}

func transitionParameterDescription(authored, derived string) string {
	if strings.TrimSpace(authored) != "" {
		return strings.TrimSpace(authored)
	}
	return strings.TrimSpace(derived)
}

func validParameterPurpose(purpose ParameterPurpose) bool {
	switch purpose {
	case ParameterPurposeOrdinary, ParameterPurposeTargetAssignee, ParameterPurposeTargetThinking:
		return true
	default:
		return false
	}
}
