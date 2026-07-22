package scriptedllm

import (
	"core/server/llm"
	"core/shared/textutil"
)

func FinalAnswer(content string) Step {
	return Step{Response: llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(content), Phase: textutil.Value(llm.MessagePhaseFinal)},
		Usage:     llm.Usage{WindowTokens: defaultContextWindowTokens},
	}}
}

func ToolBatch(content string, calls ...llm.ToolCall) Step {
	semanticContent := textutil.OptionalExactString(content)
	return Step{Response: llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: semanticContent, Phase: textutil.Value(llm.MessagePhaseCommentary)},
		ToolCalls: append([]llm.ToolCall(nil), calls...),
		Usage:     llm.Usage{WindowTokens: defaultContextWindowTokens},
	}}
}

func RuntimeError(err error) Step {
	return Step{Err: err}
}

func Cancellation() Step {
	return Step{Cancel: true}
}
