package workflow

import "fmt"

type AssigneeSessionPolicy string

const (
	AssigneeSessionPolicyEstablishTarget    AssigneeSessionPolicy = "establish_target"
	AssigneeSessionPolicyRequireTargetMatch AssigneeSessionPolicy = "require_target_match"
	AssigneeSessionPolicyPreserve           AssigneeSessionPolicy = "preserve"
)

type AssigneeSessionPolicyRequest struct {
	ContextMode           ContextMode
	ContextSource         ContextSource
	TargetSessionResolved bool
}

func ResolveAssigneeSessionPolicy(request AssigneeSessionPolicyRequest) (AssigneeSessionPolicy, error) {
	source := CanonicalContextSource(request.ContextSource)
	targetOwned := false
	switch source.Kind {
	case ContextSourceImmediateSource, ContextSourceSelectedNode:
	case ContextSourcePreviousTarget:
		targetOwned = true
	case ContextSourcePreviousTargetOrNew:
		targetOwned = request.TargetSessionResolved
	default:
		return "", fmt.Errorf("assignee session policy does not support context source %q", source.Kind)
	}
	switch request.ContextMode {
	case ContextModeNewSession, ContextModeCompactAndContinueSession:
		return AssigneeSessionPolicyEstablishTarget, nil
	case ContextModeContinueSession:
		if source.Kind == ContextSourcePreviousTargetOrNew {
			if request.TargetSessionResolved {
				return AssigneeSessionPolicyPreserve, nil
			}
			return AssigneeSessionPolicyEstablishTarget, nil
		}
		if targetOwned {
			return AssigneeSessionPolicyPreserve, nil
		}
		return AssigneeSessionPolicyRequireTargetMatch, nil
	default:
		return "", fmt.Errorf("assignee session policy does not support context mode %q", request.ContextMode)
	}
}
