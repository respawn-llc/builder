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
	Materialization              *TransitionSelectionMaterializationRequest
}

type TransitionSelectionMaterializationRequest struct {
	FallbackRole        string
	SubmittedRole       string
	SubmittedThinking   string
	ThinkingDescription string
	RetainedSession     *AgentExecutionSelection
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

func PlanDefinitionTransitionSelection(
	definition Definition,
	edge Edge,
	catalog TargetAgentCatalog,
	requireExecutionDescriptions bool,
) (TransitionSelectionPlan, error) {
	var source Node
	var target Node
	var group TransitionGroup
	groupFound := false
	for _, candidate := range definition.TransitionGroups {
		if candidate.ID == edge.TransitionGroupID {
			group = candidate
			groupFound = true
			break
		}
	}
	if !groupFound {
		return TransitionSelectionPlan{}, fmt.Errorf("transition group %q is absent", edge.TransitionGroupID)
	}
	for _, candidate := range definition.Nodes {
		if NodeIDOf(candidate) == group.SourceNodeID {
			source = candidate
		}
		if NodeIDOf(candidate) == edge.TargetNodeID {
			target = candidate
		}
	}
	if source == nil {
		return TransitionSelectionPlan{}, fmt.Errorf("transition source node %q is absent", group.SourceNodeID)
	}
	if target == nil {
		return TransitionSelectionPlan{}, fmt.Errorf("transition target node %q is absent", edge.TargetNodeID)
	}
	fanOut := 0
	for _, candidate := range definition.Edges {
		if candidate.TransitionGroupID == edge.TransitionGroupID {
			fanOut++
		}
	}
	return PlanTransitionSelection(TransitionParameterContractRequest{
		Edge:                         edge,
		SourceKind:                   source.Kind(),
		TargetKind:                   target.Kind(),
		TargetRole:                   NodeSubagentRole(target),
		FanOut:                       fanOut > 1,
		Catalog:                      catalog,
		RequireExecutionDescriptions: requireExecutionDescriptions,
	})
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
	if request.Materialization != nil {
		selection, err := planTargetAgentExecutionSelection(TargetAgentExecutionSelectionRequest{
			Edge:                request.Edge,
			FallbackRole:        request.Materialization.FallbackRole,
			Catalog:             request.Catalog,
			SubmittedRole:       request.Materialization.SubmittedRole,
			SubmittedThinking:   request.Materialization.SubmittedThinking,
			ThinkingDescription: request.Materialization.ThinkingDescription,
			RetainedSession:     request.Materialization.RetainedSession,
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

// PlanTransitionProtectedParameterConsumption derives the protected-parameter
// consumption policy without validating the Edge's ordinary parameter shape.
func PlanTransitionProtectedParameterConsumption(request TransitionParameterContractRequest) ProtectedParameterConsumption {
	roles := transitionThinkingRoles(request)
	return ResolveProtectedParameterConsumption(ProtectedParameterConsumptionRequest{
		Edge:                  request.Edge,
		SourceKind:            request.SourceKind,
		TargetKind:            request.TargetKind,
		FanOut:                request.FanOut,
		TargetSessionResolved: request.TargetSessionResolved,
		TargetSessionPolicy:   request.TargetSessionPolicy,
		ExplicitCallableRoles: explicitCallableRoleCount(request.Catalog),
		Thinking:              UnionTargetAgentThinkingCapabilities(roles),
	})
}

func planTransitionParameterContract(request TransitionParameterContractRequest) (TransitionParameterContract, error) {
	thinking := UnionTargetAgentThinkingCapabilities(transitionThinkingRoles(request))
	consumption := PlanTransitionProtectedParameterConsumption(request)
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
