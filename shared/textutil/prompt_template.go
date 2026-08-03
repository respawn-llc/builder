package textutil

import "strings"

const promptArgumentsPlaceholder = "$ARGUMENTS"

type promptTemplatePart struct {
	literal       string
	argumentsSlot bool
}

func ExpandPromptTemplate(prompt, arguments string) string {
	trimmedArguments := strings.TrimSpace(arguments)
	parts, hasArgumentsSlot := parsePromptTemplate(prompt)
	if hasArgumentsSlot {
		var expanded strings.Builder
		expanded.Grow(len(prompt) + len(trimmedArguments))
		for _, part := range parts {
			if part.argumentsSlot {
				expanded.WriteString(trimmedArguments)
				continue
			}
			expanded.WriteString(part.literal)
		}
		return expanded.String()
	}
	if trimmedArguments == "" {
		return prompt
	}
	base := strings.TrimRight(prompt, "\n")
	if base == "" {
		return trimmedArguments
	}
	return base + "\n\n" + trimmedArguments
}

func parsePromptTemplate(prompt string) ([]promptTemplatePart, bool) {
	parts := make([]promptTemplatePart, 0, 1)
	literalStart := 0
	hasArgumentsSlot := false
	for index := 0; index < len(prompt); index++ {
		if prompt[index] != '$' || !isArgumentsPlaceholderAt(prompt, index) {
			continue
		}
		if literalStart < index {
			parts = append(parts, promptTemplatePart{literal: prompt[literalStart:index]})
		}
		parts = append(parts, promptTemplatePart{argumentsSlot: true})
		hasArgumentsSlot = true
		index += len(promptArgumentsPlaceholder) - 1
		literalStart = index + 1
	}
	if literalStart < len(prompt) || !hasArgumentsSlot {
		parts = append(parts, promptTemplatePart{literal: prompt[literalStart:]})
	}
	return parts, hasArgumentsSlot
}

func isArgumentsPlaceholderAt(prompt string, index int) bool {
	if index > 0 && isPromptTokenCharacter(prompt[index-1]) {
		return false
	}
	end := index + len(promptArgumentsPlaceholder)
	if end > len(prompt) {
		return false
	}
	for offset := 0; offset < len(promptArgumentsPlaceholder); offset++ {
		if prompt[index+offset] != promptArgumentsPlaceholder[offset] {
			return false
		}
	}
	return end == len(prompt) || !isPromptTokenCharacter(prompt[end])
}

func isPromptTokenCharacter(value byte) bool {
	return value == '_' ||
		value >= '0' && value <= '9' ||
		value >= 'A' && value <= 'Z' ||
		value >= 'a' && value <= 'z'
}
