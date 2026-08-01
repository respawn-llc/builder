package workflow

import (
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

type PromptInputReference struct {
	Name        string
	Placeholder string
}

type PromptTemplateReferences struct {
	Params      []PromptParameterReference
	PriorParams []PromptPriorParameterReference
	Inputs      []PromptInputReference
	Invalid     []PromptReferenceIssue
}

type PromptReferenceIssue struct {
	Placeholder string
	Message     string
}

func ExtractPromptTemplateReferences(promptTemplate string) (PromptTemplateReferences, error) {
	prompt := strings.TrimSpace(promptTemplate)
	if prompt == "" {
		return PromptTemplateReferences{}, nil
	}
	tmpl, err := template.New("workflow node prompt validation").Parse(prompt)
	if err != nil {
		return PromptTemplateReferences{}, err
	}
	refs := PromptTemplateReferences{}
	for _, parsed := range tmpl.Templates() {
		if parsed.Tree != nil {
			walkTemplateNode(parsed.Tree.Root, &refs)
		}
	}
	return refs, nil
}

func walkTemplateNode(node parse.Node, refs *PromptTemplateReferences) {
	switch typed := node.(type) {
	case nil:
		return
	case *parse.ListNode:
		walkTemplateNodeList(typed, refs)
	case *parse.ActionNode:
		walkTemplateNode(typed.Pipe, refs)
	case *parse.IfNode:
		walkTemplateNode(typed.Pipe, refs)
		walkTemplateNodeList(typed.List, refs)
		walkTemplateNodeList(typed.ElseList, refs)
	case *parse.RangeNode:
		walkTemplateNode(typed.Pipe, refs)
		walkTemplateNodeList(typed.List, refs)
		walkTemplateNodeList(typed.ElseList, refs)
	case *parse.WithNode:
		walkTemplateNode(typed.Pipe, refs)
		walkTemplateNodeList(typed.List, refs)
		walkTemplateNodeList(typed.ElseList, refs)
	case *parse.TemplateNode:
		walkTemplateNode(typed.Pipe, refs)
	case *parse.PipeNode:
		for _, command := range typed.Cmds {
			walkTemplateNode(command, refs)
		}
	case *parse.CommandNode:
		if len(typed.Args) > 0 {
			if ident, ok := typed.Args[0].(*parse.IdentifierNode); ok && ident.Ident == "index" && indexCommandTouchesPromptNamespace(typed.Args[1:]) {
				refs.Invalid = append(refs.Invalid, PromptReferenceIssue{Placeholder: "index", Message: "dynamic prompt reference lookup is not supported"})
			}
		}
		for _, arg := range typed.Args {
			walkTemplateNode(arg, refs)
		}
	case *parse.ChainNode:
		walkTemplateNode(typed.Node, refs)
		if len(typed.Field) > 0 && promptNamespace(typed.Field[0]) {
			refs.Invalid = append(refs.Invalid, PromptReferenceIssue{Placeholder: "." + strings.Join(typed.Field, "."), Message: "prompt reference shape is unsupported"})
		}
	case *parse.FieldNode:
		recordPromptFieldReference(typed.Ident, refs)
	case *parse.VariableNode:
		if variableTouchesPromptNamespace(typed.Ident) {
			refs.Invalid = append(refs.Invalid, PromptReferenceIssue{Placeholder: strings.Join(typed.Ident, "."), Message: "variable prompt reference lookup is not supported"})
		}
	}
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

func walkTemplateNodeList(list *parse.ListNode, refs *PromptTemplateReferences) {
	if list == nil {
		return
	}
	for _, node := range list.Nodes {
		walkTemplateNode(node, refs)
	}
}

func recordPromptFieldReference(ident []string, refs *PromptTemplateReferences) {
	if len(ident) == 0 {
		return
	}
	placeholder := "." + strings.Join(ident, ".")
	switch ident[0] {
	case "Inputs":
		if len(ident) != 2 {
			refs.Invalid = append(refs.Invalid, PromptReferenceIssue{Placeholder: placeholder, Message: ".Inputs references must use .Inputs.<input_name>"})
			return
		}
		refs.Inputs = append(refs.Inputs, PromptInputReference{Name: ident[1], Placeholder: placeholder})
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
