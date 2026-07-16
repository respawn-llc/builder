package llm

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

func currentReasoningStatus(markdownText string) *ReasoningStatus {
	source := []byte(markdownText)
	document := goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser().Parse(text.NewReader(source))

	var status *ReasoningStatus
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		emphasis, ok := node.(*ast.Emphasis)
		if !ok || emphasis.Level != 2 || emphasis.Parent() == nil || emphasis.Parent().Type() != ast.TypeBlock {
			return ast.WalkContinue, nil
		}
		content, ok := emphasis.FirstChild().(*ast.Text)
		if !ok || content.NextSibling() != nil {
			return ast.WalkContinue, nil
		}
		value := strings.TrimSpace(string(content.Value(source)))
		if value == "" {
			return ast.WalkContinue, nil
		}
		status = &ReasoningStatus{Text: value}
		return ast.WalkStop, nil
	})
	return status
}
