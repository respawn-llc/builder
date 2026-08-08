package workflow

import (
	"fmt"
	"sort"
	"strings"
)

type TargetAgentSelectionRequest struct {
	FallbackRole    string
	OverrideEnabled bool
	SubmittedRole   string
}

type TargetAgentSelection struct {
	Role                 TargetAgentRole
	QuestionsRequired    bool
	SubmittedRoleIgnored bool
}

type TargetAgentSelectionErrorCode string

const (
	TargetAgentSelectionErrorUnavailableRole             TargetAgentSelectionErrorCode = "workflow.target_agent.unavailable_role"
	TargetAgentSelectionErrorNoSelectableRoles           TargetAgentSelectionErrorCode = "workflow.target_agent.no_selectable_roles"
	TargetAgentSelectionErrorQuestionsDisabled           TargetAgentSelectionErrorCode = "workflow.target_agent.questions_disabled"
	TargetAgentSelectionErrorThinkingRequired            TargetAgentSelectionErrorCode = "workflow.target_agent.thinking_required"
	TargetAgentSelectionErrorThinkingDescriptionRequired TargetAgentSelectionErrorCode = "workflow.target_agent.thinking_description_required"
	TargetAgentSelectionErrorUnsupportedThinking         TargetAgentSelectionErrorCode = "workflow.target_agent.unsupported_thinking"
)

type TargetAgentSelectionError struct {
	Code TargetAgentSelectionErrorCode
	Role string
}

func (e TargetAgentSelectionError) Error() string {
	if e.Role == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Role)
}

func PlanTargetAgentSelection(catalog TargetAgentCatalog, request TargetAgentSelectionRequest) (TargetAgentSelection, error) {
	if catalog == nil {
		return TargetAgentSelection{}, TargetAgentSelectionError{Code: TargetAgentSelectionErrorUnavailableRole, Role: strings.TrimSpace(request.SubmittedRole)}
	}
	if !request.OverrideEnabled {
		roleName := strings.TrimSpace(request.FallbackRole)
		role, ok := catalog.ResolveConfiguredRole(roleName)
		if !ok {
			return TargetAgentSelection{}, TargetAgentSelectionError{Code: TargetAgentSelectionErrorUnavailableRole, Role: roleName}
		}
		role = canonicalTargetAgentRole(role)
		if !role.QuestionsEnabled {
			return TargetAgentSelection{}, TargetAgentSelectionError{Code: TargetAgentSelectionErrorQuestionsDisabled, Role: role.Identity}
		}
		return TargetAgentSelection{Role: role}, nil
	}

	roles := canonicalExplicitCallableRoles(catalog.ExplicitCallableRoles())
	switch len(roles) {
	case 0:
		return TargetAgentSelection{}, TargetAgentSelectionError{Code: TargetAgentSelectionErrorNoSelectableRoles}
	case 1:
		return TargetAgentSelection{
			Role:                 roles[0],
			QuestionsRequired:    true,
			SubmittedRoleIgnored: true,
		}, nil
	}

	submitted := strings.TrimSpace(request.SubmittedRole)
	identity, ok := canonicalAgentRoleIdentity(submitted)
	if !ok {
		return TargetAgentSelection{}, TargetAgentSelectionError{Code: TargetAgentSelectionErrorUnavailableRole, Role: submitted}
	}
	for _, role := range roles {
		if role.Identity == identity.value {
			return TargetAgentSelection{Role: role, QuestionsRequired: true}, nil
		}
	}
	return TargetAgentSelection{}, TargetAgentSelectionError{Code: TargetAgentSelectionErrorUnavailableRole, Role: submitted}
}

func canonicalExplicitCallableRoles(roles []TargetAgentRole) []TargetAgentRole {
	byIdentity := make(map[string]TargetAgentRole, len(roles))
	for _, role := range roles {
		if !role.ExplicitAgentCallable {
			continue
		}
		canonical := canonicalTargetAgentRole(role)
		if canonical.Identity == "" {
			continue
		}
		byIdentity[canonical.Identity] = canonical
	}
	out := make([]TargetAgentRole, 0, len(byIdentity))
	for _, role := range byIdentity {
		out = append(out, role)
	}
	sort.Slice(out, func(left, right int) bool {
		return out[left].Identity < out[right].Identity
	})
	return out
}

func DefaultTargetAgentRoleDescription(roles []TargetAgentRole) string {
	canonical := canonicalExplicitCallableRoles(roles)
	identities := make([]string, 0, len(canonical))
	for _, role := range canonical {
		identities = append(identities, role.Identity)
	}
	return "Override the subagent role for the next node, available roles: " + strings.Join(identities, ", ")
}

func canonicalTargetAgentRole(role TargetAgentRole) TargetAgentRole {
	if identity, ok := canonicalAgentRoleIdentity(role.Identity); ok {
		role.Identity = identity.value
	} else {
		role.Identity = ""
	}
	return role
}
