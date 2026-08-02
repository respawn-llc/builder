package runtimeinput

import (
	"fmt"
	"strings"
	"unicode"
)

type PromptCommandName struct {
	Identifier string
}

type CommandToken struct {
	Namespace  string
	Identifier string
}

func ParseCommandToken(raw string) (CommandToken, error) {
	namespace, identifier, found := strings.Cut(strings.TrimSpace(raw), ":")
	if !found || namespace == "" || identifier == "" {
		return CommandToken{}, fmt.Errorf("command token %q must contain a namespace and identifier", raw)
	}
	return CommandToken{Namespace: namespace, Identifier: identifier}, nil
}

func ParsePromptCommandName(raw string) (PromptCommandName, error) {
	token, err := ParseCommandToken(raw)
	if err != nil || token.Namespace != "prompt" {
		return PromptCommandName{}, fmt.Errorf("prompt command name %q must use the prompt namespace", raw)
	}
	normalized := NormalizeIdentifier(token.Identifier)
	if normalized == "" || normalized != token.Identifier {
		return PromptCommandName{}, fmt.Errorf("prompt command name %q is not canonical", raw)
	}
	return PromptCommandName{Identifier: normalized}, nil
}

func (n PromptCommandName) String() string {
	if n.Identifier == "" {
		return ""
	}
	return "prompt:" + n.Identifier
}

func NormalizeIdentifier(raw string) string {
	var builder strings.Builder
	lastUnderscore := false
	for _, r := range strings.TrimSpace(raw) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			builder.WriteRune(unicode.ToLower(r))
			lastUnderscore = false
		case unicode.IsSpace(r) || r == '_':
			if builder.Len() == 0 || lastUnderscore {
				continue
			}
			builder.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(builder.String(), "_")
}
