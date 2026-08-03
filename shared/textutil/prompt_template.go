package textutil

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const promptArgumentsPlaceholder = "$ARGUMENTS"

func ExpandPromptTemplate(prompt, arguments string) string {
	trimmedArguments := strings.TrimSpace(arguments)
	if expanded, replaced := replacePromptArgumentsTokens(prompt, trimmedArguments); replaced {
		return expanded
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

func replacePromptArgumentsTokens(prompt, arguments string) (string, bool) {
	var expanded strings.Builder
	literalStart := 0
	replaced := false
	for index := 0; index < len(prompt); index++ {
		if !isPromptArgumentsTokenAt(prompt, index) {
			continue
		}
		expanded.WriteString(prompt[literalStart:index])
		expanded.WriteString(arguments)
		index += len(promptArgumentsPlaceholder) - 1
		literalStart = index + 1
		replaced = true
	}
	if !replaced {
		return "", false
	}
	expanded.WriteString(prompt[literalStart:])
	return expanded.String(), true
}

func isPromptArgumentsTokenAt(prompt string, index int) bool {
	if index >= len(prompt) || prompt[index] != '$' || promptTokenCharacterBefore(prompt, index) {
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
	return !promptTokenCharacterAfter(prompt, end)
}

func promptTokenCharacterBefore(prompt string, index int) bool {
	if index <= 0 {
		return false
	}
	value, _ := utf8.DecodeLastRuneInString(prompt[:index])
	return isPromptTokenCharacter(value)
}

func promptTokenCharacterAfter(prompt string, index int) bool {
	if index >= len(prompt) {
		return false
	}
	value, _ := utf8.DecodeRuneInString(prompt[index:])
	return isPromptTokenCharacter(value)
}

func isPromptTokenCharacter(value rune) bool {
	return unicode.Is(unicode.Pc, value) ||
		unicode.IsLetter(value) ||
		unicode.IsDigit(value) ||
		unicode.IsMark(value)
}
