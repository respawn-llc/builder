package prompts_test

import (
	"testing"

	"core/prompts"
)

func TestDelegationPlaceholderRendersEmpty(t *testing.T) {
	args := prompts.SystemPromptTemplateArgs{
		EstimatedToolCallsForContext: 252,
		EditingToolName:              "patch",
	}
	for _, fixture := range []struct {
		template string
		want     string
	}{
		{template: "{{.DefaultSystemPromptDelegation}}", want: ""},
		{template: "before{{.DefaultSystemPromptDelegation}}after", want: "beforeafter"},
		{template: "{{if .DefaultSystemPromptDelegation}}present{{else}}empty{{end}}", want: "empty"},
	} {
		got, err := prompts.RenderCustomSystemPrompt(fixture.template, false, args)
		if err != nil {
			t.Fatalf("render accepted placeholder: %v", err)
		}
		if got != fixture.want {
			t.Fatalf("rendered template = %q, want %q", got, fixture.want)
		}
	}
}
