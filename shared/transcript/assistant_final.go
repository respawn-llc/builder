package transcript

import "strings"

// AssistantFinalCandidate contains the cross-layer facts needed to classify an
// assistant final answer. Message metadata remains typed by each owning layer;
// this value carries only the semantic presence facts shared by those layers.
type AssistantFinalCandidate struct {
	IsAssistant    bool
	IsFinal        bool
	HasMessageType bool
	Content        *string
}

// IsBlankAssistantFinal reports whether an explicitly present, untyped
// assistant final answer contains only whitespace.
func IsBlankAssistantFinal(candidate AssistantFinalCandidate) bool {
	return candidate.IsAssistant &&
		candidate.IsFinal &&
		!candidate.HasMessageType &&
		candidate.Content != nil &&
		strings.TrimSpace(*candidate.Content) == ""
}
