package workflow

import (
	"fmt"
	"sort"
	"strings"
)

type TargetAgentThinkingSelectionRequest struct {
	OverrideEnabled     bool
	TargetRole          TargetAgentRole
	EligibleRoles       []TargetAgentRole
	SubmittedValue      string
	AuthoredDescription string
}

type TargetAgentThinkingSelection struct {
	Value                 string
	Exposed               bool
	SubmittedValueIgnored bool
}

func (c ThinkingCapability) Canonical() ThinkingCapability {
	c.Levels = canonicalThinkingLevels(c.Levels)
	return c
}

func UnionTargetAgentThinkingCapabilities(roles []TargetAgentRole) ThinkingCapability {
	union := ThinkingCapability{}
	for _, role := range roles {
		capability := role.Thinking.Canonical()
		union.ReasoningCapable = union.ReasoningCapable || capability.ReasoningCapable
		if !capability.Finite {
			union.Finite = false
			continue
		}
		if len(roles) == 0 || (!union.Finite && len(union.Levels) == 0) {
			union.Finite = true
		}
		union.Levels = append(union.Levels, capability.Levels...)
	}
	if len(roles) > 0 {
		union.Finite = true
		for _, role := range roles {
			if !role.Thinking.Finite {
				union.Finite = false
				union.Levels = nil
				break
			}
		}
	}
	union.Levels = canonicalThinkingLevels(union.Levels)
	return union
}

func DefaultTargetAgentThinkingDescription(capability ThinkingCapability) string {
	capability = capability.Canonical()
	if !capability.Finite || len(capability.Levels) == 0 {
		return ""
	}
	return "Override the thinking level for the next node, available levels: " + strings.Join(capability.Levels, ", ")
}

func PlanTargetAgentThinkingSelection(request TargetAgentThinkingSelectionRequest) (TargetAgentThinkingSelection, error) {
	if !request.OverrideEnabled {
		return TargetAgentThinkingSelection{Value: strings.TrimSpace(request.TargetRole.ConfiguredThinking)}, nil
	}
	roles := request.EligibleRoles
	if len(roles) == 0 {
		roles = []TargetAgentRole{request.TargetRole}
	}
	capability := UnionTargetAgentThinkingCapabilities(roles)
	if !capability.ReasoningCapable {
		return TargetAgentThinkingSelection{Value: strings.TrimSpace(request.TargetRole.ConfiguredThinking)}, nil
	}
	targetCapability := request.TargetRole.Thinking.Canonical()
	if !capability.Finite {
		value := strings.TrimSpace(request.SubmittedValue)
		if value == "" {
			return TargetAgentThinkingSelection{}, TargetAgentSelectionError{Code: TargetAgentSelectionErrorThinkingRequired}
		}
		if strings.TrimSpace(request.AuthoredDescription) == "" {
			return TargetAgentThinkingSelection{}, TargetAgentSelectionError{Code: TargetAgentSelectionErrorThinkingDescriptionRequired}
		}
		if targetCapability.Finite && !containsThinkingLevel(targetCapability.Levels, value) {
			return TargetAgentThinkingSelection{}, TargetAgentSelectionError{Code: TargetAgentSelectionErrorUnsupportedThinking, Role: value}
		}
		return TargetAgentThinkingSelection{Value: value, Exposed: true}, nil
	}
	switch len(capability.Levels) {
	case 0:
		return TargetAgentThinkingSelection{
			Value:                 strings.TrimSpace(request.TargetRole.ConfiguredThinking),
			SubmittedValueIgnored: strings.TrimSpace(request.SubmittedValue) != "",
		}, nil
	case 1:
		if targetCapability.Finite && !containsThinkingLevel(targetCapability.Levels, capability.Levels[0]) {
			return TargetAgentThinkingSelection{
				Value:                 strings.TrimSpace(request.TargetRole.ConfiguredThinking),
				SubmittedValueIgnored: strings.TrimSpace(request.SubmittedValue) != "",
			}, nil
		}
		return TargetAgentThinkingSelection{
			Value:                 capability.Levels[0],
			SubmittedValueIgnored: strings.TrimSpace(request.SubmittedValue) != "",
		}, nil
	}
	value := strings.TrimSpace(request.SubmittedValue)
	if value == "" {
		return TargetAgentThinkingSelection{}, TargetAgentSelectionError{Code: TargetAgentSelectionErrorThinkingRequired}
	}
	if !containsThinkingLevel(capability.Levels, value) {
		return TargetAgentThinkingSelection{}, TargetAgentSelectionError{Code: TargetAgentSelectionErrorUnsupportedThinking, Role: value}
	}
	if targetCapability.Finite && !containsThinkingLevel(targetCapability.Levels, value) {
		return TargetAgentThinkingSelection{}, TargetAgentSelectionError{Code: TargetAgentSelectionErrorUnsupportedThinking, Role: value}
	}
	return TargetAgentThinkingSelection{Value: value, Exposed: true}, nil
}

func canonicalThinkingLevels(levels []string) []string {
	seen := make(map[string]struct{}, len(levels))
	out := make([]string, 0, len(levels))
	for _, raw := range levels {
		level := strings.TrimSpace(raw)
		if level == "" {
			continue
		}
		if _, exists := seen[level]; exists {
			continue
		}
		seen[level] = struct{}{}
		out = append(out, level)
	}
	sort.Strings(out)
	return out
}

func containsThinkingLevel(levels []string, value string) bool {
	for _, level := range levels {
		if level == value {
			return true
		}
	}
	return false
}

func (e TargetAgentSelectionError) thinkingErrorMessage() string {
	switch e.Code {
	case TargetAgentSelectionErrorThinkingRequired:
		return "thinking level is required"
	case TargetAgentSelectionErrorThinkingDescriptionRequired:
		return "thinking parameter requires an authored description for an open model catalog"
	case TargetAgentSelectionErrorUnsupportedThinking:
		return fmt.Sprintf("thinking level %q is unsupported", e.Role)
	default:
		return string(e.Code)
	}
}
