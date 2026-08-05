package workflow

type ProtectedParameterConsumptionPolicy string

const (
	ProtectedParameterConsumptionRequiredValidate ProtectedParameterConsumptionPolicy = "required_validate"
	ProtectedParameterConsumptionIgnoreAuthorized ProtectedParameterConsumptionPolicy = "ignore_authorized"
	ProtectedParameterConsumptionRejectUnknown    ProtectedParameterConsumptionPolicy = "reject_unknown"
)

type ProtectedParameterConsumption struct {
	Assignee ProtectedParameterConsumptionPolicy
	Thinking ProtectedParameterConsumptionPolicy
}

type ProtectedParameterConsumptionRequest struct {
	Edge                  Edge
	SourceKind            NodeKind
	TargetKind            NodeKind
	FanOut                bool
	TargetSessionResolved bool
	ExplicitCallableRoles int
	Thinking              ThinkingCapability
}

func ResolveProtectedParameterConsumption(request ProtectedParameterConsumptionRequest) ProtectedParameterConsumption {
	result := ProtectedParameterConsumption{
		Assignee: ProtectedParameterConsumptionRejectUnknown,
		Thinking: ProtectedParameterConsumptionRejectUnknown,
	}
	eligible := !request.FanOut &&
		(request.SourceKind == NodeKindAgent || request.SourceKind == NodeKindScript) &&
		request.TargetKind == NodeKindAgent
	if CanonicalAssigneeSelection(request.Edge.AssigneeSelection) == AssigneeSelectionPreviousNode &&
		eligible &&
		!(request.Edge.ContextMode == ContextModeContinueSession &&
			CanonicalContextSource(request.Edge.ContextSource).Kind != ContextSourcePreviousTargetOrNew) {
		switch {
		case request.TargetSessionResolved:
			result.Assignee = ProtectedParameterConsumptionIgnoreAuthorized
		case request.ExplicitCallableRoles == 1:
			result.Assignee = ProtectedParameterConsumptionIgnoreAuthorized
		case request.ExplicitCallableRoles > 1:
			result.Assignee = ProtectedParameterConsumptionRequiredValidate
		}
	}
	if CanonicalThinkingSelection(request.Edge.ThinkingSelection) == ThinkingSelectionPreviousNode &&
		eligible &&
		request.Thinking.ReasoningCapable {
		if !request.Thinking.Finite || len(request.Thinking.Levels) > 1 {
			result.Thinking = ProtectedParameterConsumptionRequiredValidate
		} else {
			result.Thinking = ProtectedParameterConsumptionIgnoreAuthorized
		}
	}
	return result
}
