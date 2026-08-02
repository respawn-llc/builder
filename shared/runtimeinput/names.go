package runtimeinput

import (
	"fmt"
	"strings"
	"unicode"
)

type PromptCommandName struct {
	Identifier string
}

const NamespacePrompt = "prompt"

type CommandToken struct {
	Namespace  string
	Identifier *string
}

func ParseCommandToken(raw string) (CommandToken, error) {
	namespace, identifier, found := strings.Cut(strings.TrimSpace(raw), ":")
	if !found || namespace == "" {
		return CommandToken{}, fmt.Errorf("command token %q must contain a namespace", raw)
	}
	token := CommandToken{Namespace: strings.ToLower(namespace)}
	if identifier != "" {
		token.Identifier = &identifier
	}
	return token, nil
}

func ParsePromptCommandName(raw string) (PromptCommandName, error) {
	token, err := ParseCommandToken(raw)
	if err != nil || token.Namespace != NamespacePrompt || token.Identifier == nil {
		return PromptCommandName{}, fmt.Errorf("prompt command name %q must use the prompt namespace", raw)
	}
	rawNamespace, _, _ := strings.Cut(strings.TrimSpace(raw), ":")
	if rawNamespace != NamespacePrompt {
		return PromptCommandName{}, fmt.Errorf("prompt command name %q is not canonical", raw)
	}
	identifier := *token.Identifier
	normalized := NormalizeIdentifier(identifier)
	if normalized == "" || normalized != identifier {
		return PromptCommandName{}, fmt.Errorf("prompt command name %q is not canonical", raw)
	}
	return PromptCommandName{Identifier: normalized}, nil
}

func (n PromptCommandName) String() string {
	if n.Identifier == "" {
		panic("invalid empty prompt command name")
	}
	return NamespacePrompt + ":" + n.Identifier
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
