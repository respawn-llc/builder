package textutil

import (
	"strings"
)

const promptArgumentsPlaceholder = "$ARGUMENTS"

func ExpandPromptTemplate(prompt, arguments string) string {
	trimmedArguments := strings.TrimSpace(arguments)
	if strings.Contains(prompt, promptArgumentsPlaceholder) {
		return strings.ReplaceAll(prompt, promptArgumentsPlaceholder, trimmedArguments)
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
