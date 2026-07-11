package prompts

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRenderSystemPromptTemplateUsesTypedFields(t *testing.T) {
	rendered := renderSystemPromptTemplate("calls={{.EstimatedToolCallsForContext}} cmd={{.LaunchCommand}} run edit={{.EditingToolName}}", SystemPromptTemplateArgs{
		EstimatedToolCallsForContext: 123,
		EditingToolName:              "edit",
	}, "")
	if !strings.Contains(rendered, "calls=123") {
		t.Fatalf("expected estimated tool calls rendered, got %q", rendered)
	}
	expectedCmd := "cmd=" + LaunchCommand() + " run"
	if !strings.Contains(rendered, expectedCmd) || strings.Contains(rendered, "{{") {
		t.Fatalf("expected %q in rendered output, got %q", expectedCmd, rendered)
	}
	if !strings.Contains(rendered, "edit=edit") {
		t.Fatalf("expected editing tool name rendered, got %q", rendered)
	}
}

func TestCustomSystemPromptResolvesDefaultSystemPromptPlaceholder(t *testing.T) {
	defaultPrompt := BaseSystemPrompt(SystemPromptTemplateArgs{
		EstimatedToolCallsForContext: 123,
	})
	rendered, err := RenderCustomSystemPrompt("custom\n{{.DefaultSystemPrompt}}", false, SystemPromptTemplateArgs{
		EstimatedToolCallsForContext: 123,
	})
	if err != nil {
		t.Fatalf("RenderCustomSystemPrompt: %v", err)
	}
	if !strings.Contains(rendered, "custom\n") {
		t.Fatalf("expected custom prefix, got %q", rendered)
	}
	if !strings.Contains(rendered, defaultPrompt) || strings.Contains(rendered, "{{") {
		t.Fatalf("expected default prompt placeholder rendered, got %q", rendered)
	}
}

func TestCustomSystemPromptResolvesDefaultSystemPromptSectionPlaceholders(t *testing.T) {
	rendered, err := RenderCustomSystemPrompt(strings.Join([]string{
		"{{.DefaultSystemPromptPersonality}}",
		"{{.DefaultSystemPromptHarnessWorkflowAutonomy}}",
		"{{.DefaultSystemPromptAmbiguityAndOutputQuality}}",
		"{{.DefaultSystemPromptFinalAnswerAndFormatting}}",
		"{{.DefaultSystemPromptDelegation}}",
	}, "\n---\n"), false, SystemPromptTemplateArgs{
		EstimatedToolCallsForContext: 123,
		EditingToolName:              "patch",
	})
	if err != nil {
		t.Fatalf("RenderCustomSystemPrompt: %v", err)
	}
	if !strings.Contains(rendered, LaunchCommand()) {
		t.Fatalf("expected section prompts to substitute the launch command, got %q", rendered)
	}
	if strings.Contains(rendered, "{{") {
		t.Fatalf("expected section placeholders rendered, got %q", rendered)
	}
}

func TestBaseSystemPromptAssemblesDefaultSections(t *testing.T) {
	rendered := BaseSystemPrompt(SystemPromptTemplateArgs{
		EstimatedToolCallsForContext: 123,
		EditingToolName:              "patch",
	})
	if strings.TrimSpace(rendered) == "" {
		t.Fatal("expected base prompt to assemble a non-empty prompt")
	}
	if strings.Contains(rendered, "{{") {
		t.Fatalf("expected base prompt placeholders rendered, got %q", rendered)
	}
}

func TestDefaultSystemPromptAssemblyCannotReferenceFullDefaultPrompt(t *testing.T) {
	_, err := renderSystemPromptTemplateErr("{{.DefaultSystemPrompt}}", SystemPromptTemplateArgs{
		EstimatedToolCallsForContext: 123,
		EditingToolName:              "patch",
	}, "")
	if err == nil {
		t.Fatal("expected default system prompt assembly to reject DefaultSystemPrompt recursion")
	}
	var placeholderErr *UnknownTemplatePlaceholderError
	if !errors.As(err, &placeholderErr) {
		t.Fatalf("expected UnknownTemplatePlaceholderError, got %v", err)
	}
	if placeholderErr.Placeholder != "DefaultSystemPrompt" {
		t.Fatalf("expected placeholder DefaultSystemPrompt, got %q", placeholderErr.Placeholder)
	}
}

func TestCustomSystemPromptRejectsRemovedManualEditInstructionPlaceholder(t *testing.T) {
	_, err := RenderCustomSystemPrompt("{{.ManualEditInstruction}}", false, SystemPromptTemplateArgs{
		EstimatedToolCallsForContext: 123,
		EditingToolName:              "patch",
	})
	if err == nil {
		t.Fatal("expected removed ManualEditInstruction placeholder to fail")
	}
	var placeholderErr *UnknownTemplatePlaceholderError
	if !errors.As(err, &placeholderErr) {
		t.Fatalf("expected UnknownTemplatePlaceholderError, got %v", err)
	}
	if placeholderErr.Placeholder != "ManualEditInstruction" {
		t.Fatalf("expected placeholder ManualEditInstruction, got %q", placeholderErr.Placeholder)
	}
}

func TestCustomSystemPromptRejectsRemovedBuilderCommandPlaceholder(t *testing.T) {
	_, err := RenderCustomSystemPrompt("{{.BuilderCommand}}", false, SystemPromptTemplateArgs{
		EstimatedToolCallsForContext: 123,
		EditingToolName:              "patch",
	})
	if err == nil {
		t.Fatal("expected removed BuilderCommand placeholder to fail")
	}
	var placeholderErr *UnknownTemplatePlaceholderError
	if !errors.As(err, &placeholderErr) {
		t.Fatalf("expected UnknownTemplatePlaceholderError, got %v", err)
	}
	if placeholderErr.Placeholder != "BuilderCommand" {
		t.Fatalf("expected placeholder BuilderCommand, got %q", placeholderErr.Placeholder)
	}
}

func TestRenderWorkflowTaskInstructionsUsesCompletionModeFragment(t *testing.T) {
	toolInstructions, err := RenderWorkflowToolCompletionInstructions("workflow-1")
	if err != nil {
		t.Fatalf("RenderWorkflowToolCompletionInstructions: %v", err)
	}
	rendered, err := RenderWorkflowTaskInstructions(WorkflowNodeContextArgs{
		TaskId:          "task-1",
		TaskShortId:     "BUI-1",
		TaskTitle:       "Smoke test",
		TaskBody:        "Ask three questions.",
		WorkflowId:      "workflow-1",
		WorkflowShortId: "workflow-1",
		NodeId:          "node-1",
		NodeKey:         "triaging",
		NodeDisplayName: "Triaging",
		ContextMode:     "new_session",
		Transitions: []WorkflowTransition{
			{ID: "actionable", DisplayName: "Actionable"},
			{ID: "not_actionable", DisplayName: "Not Actionable"},
		},
		NodePrompt: "Triage the ticket.",
	}, toolInstructions)
	if err != nil {
		t.Fatalf("RenderWorkflowTaskInstructions: %v", err)
	}
	// Substituted variables: short id (in the launch command), the transition
	// id/display pair, and the node prompt body must all be injected.
	for _, want := range []string{
		LaunchCommand() + " task show BUI-1",
		"actionable (Actionable)",
		"Triage the ticket.",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected workflow instructions to substitute %q, got %q", want, rendered)
		}
	}
	// The tool-completion fragment passed in must be embedded into the output.
	if !strings.Contains(rendered, toolInstructions) {
		t.Fatalf("expected workflow instructions to embed the completion fragment, got %q", rendered)
	}
	if strings.Contains(rendered, "{{") {
		t.Fatalf("expected workflow instruction placeholders rendered, got %q", rendered)
	}
}

func TestWorkflowTaskInstructionsCommentReminderTemplateData(t *testing.T) {
	zero := newWorkflowTaskInstructionsTemplateData(workflowInstructionsTestArgs(0), "complete the workflow")
	if zero.ShowTaskCommentsReminder {
		t.Fatalf("zero-comment task set ShowTaskCommentsReminder=true: %+v", zero)
	}

	one := newWorkflowTaskInstructionsTemplateData(workflowInstructionsTestArgs(1), "complete the workflow")
	if !one.ShowTaskCommentsReminder {
		t.Fatalf("one-comment task did not set ShowTaskCommentsReminder: %+v", one)
	}
	if one.TaskCommentsLabel != "1 comment" {
		t.Fatalf("one-comment label = %q, want singular grammar", one.TaskCommentsLabel)
	}
	expectedCommand := LaunchCommand() + " task comment list BUI-1"
	if one.TaskCommentListCommand != expectedCommand {
		t.Fatalf("task comment list command = %q, want %q", one.TaskCommentListCommand, expectedCommand)
	}

	many := newWorkflowTaskInstructionsTemplateData(workflowInstructionsTestArgs(3), "complete the workflow")
	if !many.ShowTaskCommentsReminder {
		t.Fatalf("multi-comment task did not set ShowTaskCommentsReminder: %+v", many)
	}
	if many.TaskCommentsLabel != "3 comments" {
		t.Fatalf("multi-comment label = %q, want plural grammar", many.TaskCommentsLabel)
	}
}

func TestWorkflowCompletionExamplesUseContractShape(t *testing.T) {
	examples := workflowCompletionExamples(WorkflowCompletionInstructionsArgs{
		WorkflowShortID: "workflow-1",
		Contract: WorkflowCompletionContract{
			Transitions: []WorkflowCompletionTransition{
				{
					ID:          "approve",
					DisplayName: "Approve",
					Parameters:  []WorkflowCompletionParameter{{Key: "summary", Description: "Summary of accepted work."}},
				},
				{
					ID:          "block",
					DisplayName: "Block",
					Parameters:  []WorkflowCompletionParameter{{Key: "reason", Description: "Blocking reason."}},
				},
			},
		},
	})
	if len(examples) != 2 {
		t.Fatalf("example count = %d, want 2: %+v", len(examples), examples)
	}
	if examples[0].TransitionID != "approve" || examples[1].TransitionID != "block" {
		t.Fatalf("example transition order = %+v, want contract order", examples)
	}
	if !strings.Contains(examples[0].ShellCommand, "--transition") || !strings.Contains(examples[0].ShellCommand, "--summary") {
		t.Fatalf("shell example did not include transition and dynamic parameter flags: %+v", examples[0])
	}
	payload := map[string]string{}
	if err := json.Unmarshal([]byte(examples[0].JSON), &payload); err != nil {
		t.Fatalf("json example must decode as object: %v\n%s", err, examples[0].JSON)
	}
	if payload["transition"] != "approve" || strings.TrimSpace(payload["summary"]) == "" || strings.TrimSpace(payload["commentary"]) == "" {
		t.Fatalf("json example payload = %+v, want transition, commentary, and selected parameter", payload)
	}
}

func TestWorkflowCompletionExamplesInferSingleTransition(t *testing.T) {
	examples := workflowCompletionExamples(WorkflowCompletionInstructionsArgs{
		Contract: WorkflowCompletionContract{
			Transitions: []WorkflowCompletionTransition{{
				ID:         "done",
				Parameters: []WorkflowCompletionParameter{{Key: "summary", Description: "Completion summary."}},
			}},
		},
	})
	if len(examples) != 1 {
		t.Fatalf("example count = %d, want 1: %+v", len(examples), examples)
	}
	if strings.Contains(examples[0].ShellCommand, "--transition") {
		t.Fatalf("single-transition shell example should infer transition: %+v", examples[0])
	}
	payload := map[string]string{}
	if err := json.Unmarshal([]byte(examples[0].JSON), &payload); err != nil {
		t.Fatalf("json example must decode as object: %v\n%s", err, examples[0].JSON)
	}
	if _, ok := payload["transition"]; ok {
		t.Fatalf("single-transition json example should omit transition: %+v", payload)
	}
	if payload["summary"] == "" {
		t.Fatalf("single-transition json example missing selected parameter: %+v", payload)
	}
}

func TestWorkflowCompletionShellExamplesSingleQuoteUntrustedDescriptions(t *testing.T) {
	examples := workflowCompletionExamples(WorkflowCompletionInstructionsArgs{
		Contract: WorkflowCompletionContract{
			Transitions: []WorkflowCompletionTransition{{
				ID: "done",
				Parameters: []WorkflowCompletionParameter{{
					Key:         "summary",
					Description: "Summary with $(danger) and `danger` and user's quote.",
				}},
			}},
		},
	})
	if len(examples) != 1 {
		t.Fatalf("example count = %d, want 1: %+v", len(examples), examples)
	}
	command := examples[0].ShellCommand
	if strings.Contains(command, "\"Summary with $(danger)") {
		t.Fatalf("shell example used double quotes around untrusted description: %s", command)
	}
	for _, want := range []string{"'Summary with $(danger) and `danger` and user", "'\"'\"'", "s quote.'"} {
		if !strings.Contains(command, want) {
			t.Fatalf("shell example = %q, want safe single-quote fragment %q", command, want)
		}
	}
}

func TestRenderWorkflowCompletionInstructionsRenderTemplates(t *testing.T) {
	args := WorkflowCompletionInstructionsArgs{
		WorkflowShortID: "workflow-1",
		Contract: WorkflowCompletionContract{
			Transitions: []WorkflowCompletionTransition{{
				ID:         "done",
				Parameters: []WorkflowCompletionParameter{{Key: "summary", Description: "Completion summary."}},
			}},
		},
	}
	for name, render := range map[string]func(WorkflowCompletionInstructionsArgs) (string, error){
		"shell":        RenderWorkflowShellCompletionInstructions,
		"unstructured": RenderWorkflowUnstructuredCompletionInstructions,
	} {
		t.Run(name, func(t *testing.T) {
			rendered, err := render(args)
			if err != nil {
				t.Fatalf("render %s workflow completion instructions: %v", name, err)
			}
			if strings.Contains(rendered, "{{") {
				t.Fatalf("expected workflow completion placeholders rendered, got %q", rendered)
			}
			if strings.TrimSpace(rendered) == "" {
				t.Fatal("expected non-empty completion instructions")
			}
		})
	}
}

func workflowInstructionsTestArgs(taskNumberOfComments int64) WorkflowNodeContextArgs {
	return WorkflowNodeContextArgs{
		TaskId:               "task-1",
		TaskShortId:          "BUI-1",
		TaskTitle:            "Smoke test",
		TaskBody:             "Ask three questions.",
		WorkflowId:           "workflow-1",
		WorkflowShortId:      "workflow-1",
		NodeId:               "node-1",
		NodeKey:              "triaging",
		NodeDisplayName:      "Triaging",
		ContextMode:          "new_session",
		TaskNumberOfComments: taskNumberOfComments,
		Transitions: []WorkflowTransition{
			{ID: "actionable", DisplayName: "Actionable"},
			{ID: "not_actionable", DisplayName: "Not Actionable"},
		},
		NodePrompt: "Triage the ticket.",
	}
}

func TestRenderGoalNudgePrompt(t *testing.T) {
	rendered := RenderGoalNudgePrompt("ship /goal mode", "active")
	// The objective and the launch command must both be substituted in.
	for _, want := range []string{
		"ship /goal mode",
		LaunchCommand(),
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected goal nudge to substitute %q, got %q", want, rendered)
		}
	}
	if strings.Contains(rendered, "{{") {
		t.Fatalf("expected goal nudge placeholders rendered, got %q", rendered)
	}
}

func TestRenderWorkflowNudgePrompt(t *testing.T) {
	rendered, err := RenderWorkflowNudgePrompt(
		"transition is required",
		"Call complete_node with the selected transition.",
		"Ship workflow execution.",
		"Continue working toward the active session goal.",
	)
	if err != nil {
		t.Fatalf("RenderWorkflowNudgePrompt: %v", err)
	}
	for _, want := range []string{
		"transition is required",
		"Call complete_node with the selected transition.",
		"Ship workflow execution.",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected workflow nudge to substitute %q, got %q", want, rendered)
		}
	}
	if strings.Contains(rendered, "{{") {
		t.Fatalf("expected workflow nudge placeholders rendered, got %q", rendered)
	}
}

func TestRenderWorkflowNudgePromptRequiresReasonAndNodeCompletionInstructions(t *testing.T) {
	tests := []struct {
		name         string
		reason       string
		instructions string
	}{
		{name: "missing rejection reason", instructions: "Call complete_node."},
		{name: "missing node completion instructions", reason: "transition is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := RenderWorkflowNudgePrompt(tt.reason, tt.instructions, "", ""); err == nil {
				t.Fatal("RenderWorkflowNudgePrompt accepted an incomplete nudge")
			}
		})
	}
}

func TestRenderGoalSetPrompt(t *testing.T) {
	rendered := RenderGoalSetPrompt("ship /goal mode")
	if !strings.Contains(rendered, "ship /goal mode") {
		t.Fatalf("expected goal set prompt to contain objective, got %q", rendered)
	}
	if strings.Contains(rendered, "{{") {
		t.Fatalf("expected goal set placeholders rendered, got %q", rendered)
	}
}

func TestRenderGoalResumePrompt(t *testing.T) {
	rendered := RenderGoalResumePrompt("ship /goal mode")
	if !strings.Contains(rendered, "ship /goal mode") {
		t.Fatalf("expected goal resume prompt to contain objective, got %q", rendered)
	}
	if strings.Contains(rendered, "{{") {
		t.Fatalf("expected goal resume placeholders rendered, got %q", rendered)
	}
}

func TestRenderGoalAlreadyCompletePrompt(t *testing.T) {
	rendered := RenderGoalAlreadyCompletePrompt("ship /goal mode")
	if !strings.Contains(rendered, "ship /goal mode") {
		t.Fatalf("expected already-complete prompt to substitute the objective, got %q", rendered)
	}
	if strings.Contains(rendered, "{{") {
		t.Fatalf("expected already-complete placeholders rendered, got %q", rendered)
	}
}

func TestRenderGoalAgentDuplicateSetDeniedPrompt(t *testing.T) {
	rendered := RenderGoalAgentDuplicateSetDeniedPrompt("ship /goal mode\n\n- preserve markdown", "active")
	// The multi-line objective (markdown preserved) and the status argument must
	// both be substituted into the rendered prompt.
	for _, want := range []string{
		"ship /goal mode\n\n- preserve markdown",
		"active",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("duplicate set prompt missing substituted %q: %q", want, rendered)
		}
	}
	if strings.Contains(rendered, "{{") {
		t.Fatalf("expected duplicate set placeholders rendered, got %q", rendered)
	}
}

func TestRenderGoalCompleteConfirmRequiredPrompt(t *testing.T) {
	rendered := RenderGoalCompleteConfirmRequiredPrompt("ship /goal mode\n\n- preserve markdown")
	if !strings.Contains(rendered, "ship /goal mode\n\n- preserve markdown") {
		t.Fatalf("complete confirm prompt missing substituted objective: %q", rendered)
	}
	if strings.Contains(rendered, "{{") {
		t.Fatalf("expected complete confirm placeholders rendered, got %q", rendered)
	}
}
