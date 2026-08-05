package workflow

import (
	"fmt"
	"strings"
	"text/template"
	"text/template/parse"
)

type PromptParameterReference struct {
	Name        string
	Placeholder string
}

type PromptPriorParameterReference struct {
	TransitionKey ModelKey
	ParameterKey  string
	Placeholder   string
}

type PromptTemplateReferences struct {
	Params      []PromptParameterReference
	PriorParams []PromptPriorParameterReference
	Invalid     []PromptReferenceIssue
}

type PromptReferenceIssue struct {
	Placeholder string
	Message     string
}

func ExtractPromptTemplateReferences(promptTemplate string) (PromptTemplateReferences, error) {
	refs := PromptTemplateReferences{}
	err := WalkPromptTemplateAST(promptTemplate, func(node parse.Node) error {
		switch typed := node.(type) {
		case *parse.CommandNode:
			if len(typed.Args) > 0 {
				if ident, ok := typed.Args[0].(*parse.IdentifierNode); ok &&
					ident.Ident == "index" &&
					indexCommandTouchesPromptNamespace(typed.Args[1:]) {
					refs.Invalid = append(refs.Invalid, PromptReferenceIssue{Placeholder: "index", Message: "dynamic prompt reference lookup is not supported"})
				}
			}
		case *parse.ChainNode:
			if len(typed.Field) > 0 && promptNamespace(typed.Field[0]) {
				refs.Invalid = append(refs.Invalid, PromptReferenceIssue{Placeholder: "." + strings.Join(typed.Field, "."), Message: "prompt reference shape is unsupported"})
			}
		case *parse.FieldNode:
			recordPromptFieldReference(typed.Ident, &refs)
		case *parse.VariableNode:
			if variableTouchesPromptNamespace(typed.Ident) {
				refs.Invalid = append(refs.Invalid, PromptReferenceIssue{Placeholder: strings.Join(typed.Ident, "."), Message: "variable prompt reference lookup is not supported"})
			}
		}
		return nil
	})
	return refs, err
}

// WalkPromptTemplateAST parses prompt and visits every syntax-tree node.
// Semantic interpretation belongs to the caller so historical
// migration rules can share the parser traversal without changing live rules.
func WalkPromptTemplateAST(promptTemplate string, visit func(parse.Node) error) error {
	if visit == nil {
		return fmt.Errorf("prompt AST visitor is required")
	}
	prompt := strings.TrimSpace(promptTemplate)
	if prompt == "" {
		return nil
	}
	tmpl, err := template.New("workflow prompt").Parse(prompt)
	if err != nil {
		return err
	}
	for _, parsed := range tmpl.Templates() {
		if parsed.Tree != nil {
			if err := walkPromptTemplateASTNode(parsed.Tree.Root, visit); err != nil {
				return err
			}
		}
	}
	return nil
}

func walkPromptTemplateASTNode(node parse.Node, visit func(parse.Node) error) error {
	switch typed := node.(type) {
	case nil:
		return nil
	case *parse.ListNode:
		if typed == nil {
			return nil
		}
		for _, child := range typed.Nodes {
			if err := walkPromptTemplateASTNode(child, visit); err != nil {
				return err
			}
		}
	case *parse.ActionNode:
		if typed == nil {
			return nil
		}
		if err := walkPromptTemplateASTNode(typed.Pipe, visit); err != nil {
			return err
		}
	case *parse.IfNode:
		if typed == nil {
			return nil
		}
		if err := walkPromptTemplateASTNode(typed.Pipe, visit); err != nil {
			return err
		}
		if err := walkPromptTemplateASTNode(typed.List, visit); err != nil {
			return err
		}
		if err := walkPromptTemplateASTNode(typed.ElseList, visit); err != nil {
			return err
		}
	case *parse.RangeNode:
		if typed == nil {
			return nil
		}
		if err := walkPromptTemplateASTNode(typed.Pipe, visit); err != nil {
			return err
		}
		if err := walkPromptTemplateASTNode(typed.List, visit); err != nil {
			return err
		}
		if err := walkPromptTemplateASTNode(typed.ElseList, visit); err != nil {
			return err
		}
	case *parse.WithNode:
		if typed == nil {
			return nil
		}
		if err := walkPromptTemplateASTNode(typed.Pipe, visit); err != nil {
			return err
		}
		if err := walkPromptTemplateASTNode(typed.List, visit); err != nil {
			return err
		}
		if err := walkPromptTemplateASTNode(typed.ElseList, visit); err != nil {
			return err
		}
	case *parse.TemplateNode:
		if typed == nil {
			return nil
		}
		if err := walkPromptTemplateASTNode(typed.Pipe, visit); err != nil {
			return err
		}
	case *parse.PipeNode:
		if typed == nil {
			return nil
		}
		for _, command := range typed.Cmds {
			if err := walkPromptTemplateASTNode(command, visit); err != nil {
				return err
			}
		}
	case *parse.CommandNode:
		if typed == nil {
			return nil
		}
		for _, arg := range typed.Args {
			if err := walkPromptTemplateASTNode(arg, visit); err != nil {
				return err
			}
		}
	case *parse.ChainNode:
		if typed == nil {
			return nil
		}
		if err := walkPromptTemplateASTNode(typed.Node, visit); err != nil {
			return err
		}
	}
	if node != nil {
		if err := visit(node); err != nil {
			return err
		}
	}
	return nil
}

func indexCommandTouchesPromptNamespace(args []parse.Node) bool {
	if len(args) == 0 {
		return false
	}
	if _, ok := args[0].(*parse.DotNode); ok {
		return true
	}
	for _, arg := range args {
		switch typed := arg.(type) {
		case *parse.FieldNode:
			if len(typed.Ident) > 0 && promptNamespace(typed.Ident[0]) {
				return true
			}
		case *parse.ChainNode:
			if len(typed.Field) > 0 && promptNamespace(typed.Field[0]) {
				return true
			}
			if indexCommandTouchesPromptNamespace([]parse.Node{typed.Node}) {
				return true
			}
		case *parse.VariableNode:
			if variableTouchesPromptNamespace(typed.Ident) {
				return true
			}
		case *parse.StringNode:
			if promptNamespace(typed.Text) {
				return true
			}
		}
	}
	return false
}

func variableTouchesPromptNamespace(ident []string) bool {
	for _, part := range ident {
		if part == "$Inputs" || part == "$Params" || promptNamespace(part) {
			return true
		}
	}
	return false
}

func recordPromptFieldReference(ident []string, refs *PromptTemplateReferences) {
	if len(ident) == 0 {
		return
	}
	placeholder := "." + strings.Join(ident, ".")
	switch ident[0] {
	case "Inputs":
		refs.Invalid = append(refs.Invalid, PromptReferenceIssue{Placeholder: placeholder, Message: ".Inputs prompt references are unsupported; use .Params.<parameter_key>"})
	case "Params":
		switch len(ident) {
		case 2:
			refs.Params = append(refs.Params, PromptParameterReference{Name: ident[1], Placeholder: placeholder})
		case 3:
			refs.PriorParams = append(refs.PriorParams, PromptPriorParameterReference{TransitionKey: ModelKey(ident[1]), ParameterKey: ident[2], Placeholder: placeholder})
		default:
			refs.Invalid = append(refs.Invalid, PromptReferenceIssue{Placeholder: placeholder, Message: ".Params references must use .Params.<parameter_key> or .Params.<transition_key>.<parameter_key>"})
		}
	default:
		if promptBuiltin(ident[0]) {
			if len(ident) != 1 {
				refs.Invalid = append(refs.Invalid, PromptReferenceIssue{Placeholder: placeholder, Message: "prompt built-in references must not be chained"})
			}
			return
		}
		refs.Invalid = append(refs.Invalid, PromptReferenceIssue{Placeholder: placeholder, Message: "prompt field reference is unsupported"})
	}
}

func promptNamespace(value string) bool {
	return value == "Inputs" || value == "Params"
}

func promptBuiltin(value string) bool {
	switch value {
	case "TaskId", "TaskShortId", "TaskTitle", "TaskBody", "NodeId", "NodeKey", "NodeDisplayName":
		return true
	default:
		return false
	}
}
