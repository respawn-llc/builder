package runtimeinput

import (
	"fmt"
	"strings"
	"unicode"
)

type PromptCommandName struct {
	Identifier string
}

const (
	NamespacePrompt               = "prompt"
	PromptCommandReviewIdentifier = "review"
	PromptCommandInitIdentifier   = "init"
	PromptCommandReviewName       = NamespacePrompt + ":" + PromptCommandReviewIdentifier
	PromptCommandInitName         = NamespacePrompt + ":" + PromptCommandInitIdentifier
)

type BuiltinPromptCommand uint8

const (
	BuiltinPromptCommandReview BuiltinPromptCommand = iota + 1
	BuiltinPromptCommandInit
)

func BuiltinPromptCommands() []BuiltinPromptCommand {
	return []BuiltinPromptCommand{BuiltinPromptCommandReview, BuiltinPromptCommandInit}
}

func (c BuiltinPromptCommand) Name() string {
	switch c {
	case BuiltinPromptCommandReview:
		return PromptCommandReviewName
	case BuiltinPromptCommandInit:
		return PromptCommandInitName
	default:
		panic("invalid built-in prompt command")
	}
}

func (c BuiltinPromptCommand) Alias() string {
	switch c {
	case BuiltinPromptCommandReview:
		return PromptCommandReviewIdentifier
	case BuiltinPromptCommandInit:
		return PromptCommandInitIdentifier
	default:
		panic("invalid built-in prompt command")
	}
}

func BuiltinPromptCommandForName(name PromptCommandName) (*BuiltinPromptCommand, bool) {
	switch name.Identifier {
	case PromptCommandReviewIdentifier:
		command := BuiltinPromptCommandReview
		return &command, true
	case PromptCommandInitIdentifier:
		command := BuiltinPromptCommandInit
		return &command, true
	default:
		return nil, false
	}
}

func BuiltinPromptCommandForAlias(alias string) (*BuiltinPromptCommand, bool) {
	return BuiltinPromptCommandForName(PromptCommandName{Identifier: alias})
}

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
	if strings.TrimSpace(raw) != raw {
		return PromptCommandName{}, fmt.Errorf("prompt command name %q is not canonical", raw)
	}
	token, err := ParseCommandToken(raw)
	if err != nil || token.Namespace != NamespacePrompt || token.Identifier == nil {
		return PromptCommandName{}, fmt.Errorf("prompt command name %q must use the prompt namespace", raw)
	}
	rawNamespace, _, _ := strings.Cut(raw, ":")
	if rawNamespace != NamespacePrompt {
		return PromptCommandName{}, fmt.Errorf("prompt command name %q is not canonical", raw)
	}
	identifier := *token.Identifier
	normalized, normalizeErr := NormalizeIdentifier(identifier)
	if normalizeErr != nil || normalized != identifier {
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

func NormalizeIdentifier(raw string) (string, error) {
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
	normalized := strings.Trim(builder.String(), "_")
	if normalized == "" {
		return "", fmt.Errorf("identifier %q cannot be normalized", raw)
	}
	return normalized, nil
}
