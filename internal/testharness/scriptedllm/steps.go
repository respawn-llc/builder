package scriptedllm

import "core/server/llm"

func FinalAnswer(content string) Step {
	return Step{Response: llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: content, Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{WindowTokens: defaultContextWindowTokens},
	}}
}

func ToolBatch(content string, calls ...llm.ToolCall) Step {
	return Step{Response: llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: content, Phase: llm.MessagePhaseCommentary},
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
