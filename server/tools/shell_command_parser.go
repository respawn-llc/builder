package tools

import (
	"path"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

type LiteralShellInvocation struct {
	Args []string
}

func ExtractLiteralShellInvocations(command string) []LiteralShellInvocation {
	parser := syntax.NewParser()
	file, err := parser.Parse(strings.NewReader(command), "")
	if err != nil || file == nil {
		return nil
	}
	var invocations []LiteralShellInvocation
	syntax.Walk(file, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok {
			return true
		}
		args, ok := literalCallArgs(call)
		if !ok {
			return true
		}
		args, ok = unwrapLiteralCommand(args)
		if !ok {
			return true
		}
		invocations = append(invocations, LiteralShellInvocation{Args: args})
		return true
	})
	return invocations
}

func ParseSimpleShellCommand(command string) ([]string, bool) {
	parser := syntax.NewParser()
	file, err := parser.Parse(strings.NewReader(command), "")
	if err != nil || file == nil || len(file.Stmts) != 1 {
		return nil, false
	}

	stmt := file.Stmts[0]
	if stmt == nil || stmt.Cmd == nil || stmt.Negated || stmt.Background || stmt.Coprocess || len(stmt.Redirs) > 0 {
		return nil, false
	}

	callExpr, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(callExpr.Assigns) > 0 {
		return nil, false
	}
	args, ok := literalCallArgs(callExpr)
	if !ok {
		return nil, false
	}
	return unwrapLiteralCommand(args)
}

func NormalizeShellCommandName(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	base := path.Base(strings.ReplaceAll(command, "\\", "/"))
	base = strings.ToLower(strings.TrimSpace(base))
	base = strings.TrimSuffix(base, ".exe")
	base = strings.TrimSuffix(base, ".cmd")
	base = strings.TrimSuffix(base, ".bat")
	base = strings.TrimSuffix(base, ".com")
	return base
}

func literalWord(word *syntax.Word) (string, bool) {
	if word == nil || len(word.Parts) == 0 {
		return "", false
	}

	var out strings.Builder
	for _, part := range word.Parts {
		switch x := part.(type) {
		case *syntax.Lit:
			out.WriteString(x.Value)
		case *syntax.SglQuoted:
			out.WriteString(x.Value)
		case *syntax.DblQuoted:
			for _, nested := range x.Parts {
				lit, ok := nested.(*syntax.Lit)
				if !ok {
					return "", false
				}
				out.WriteString(lit.Value)
			}
		default:
			return "", false
		}
	}

	return out.String(), true
}

func literalCallArgs(call *syntax.CallExpr) ([]string, bool) {
	if call == nil || len(call.Args) == 0 {
		return nil, false
	}
	args := make([]string, 0, len(call.Args))
	for _, arg := range call.Args {
		literal, ok := literalWord(arg)
		if !ok || (len(args) == 0 && literal == "") {
			return nil, false
		}
		args = append(args, literal)
	}
	return args, true
}

func unwrapLiteralCommand(args []string) ([]string, bool) {
	for len(args) > 0 {
		switch NormalizeShellCommandName(args[0]) {
		case "command":
			args = args[1:]
			if len(args) > 0 && args[0] == "--" {
				args = args[1:]
			}
			if len(args) == 0 || strings.HasPrefix(args[0], "-") {
				return nil, false
			}
		case "env":
			args = args[1:]
			if len(args) > 0 && args[0] == "--" {
				args = args[1:]
			}
			for len(args) > 0 && isEnvironmentAssignment(args[0]) {
				args = args[1:]
			}
			if len(args) > 0 && args[0] == "--" {
				args = args[1:]
			}
			if len(args) == 0 || strings.HasPrefix(args[0], "-") {
				return nil, false
			}
		default:
			return append([]string(nil), args...), true
		}
	}
	return nil, false
}

func isEnvironmentAssignment(value string) bool {
	name, _, found := strings.Cut(value, "=")
	if !found || name == "" {
		return false
	}
	for index, character := range name {
		if character == '_' ||
			character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}
