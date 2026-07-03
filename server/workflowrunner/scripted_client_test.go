package workflowrunner

import (
	"core/internal/testharness/scriptedllm"
	"core/server/llm"
)

var ErrScriptedRuntime = scriptedllm.ErrScriptExhausted

type ScriptedRuntimeStep = scriptedllm.Step
type ScriptedClient = scriptedllm.LegacyClient

func NewScriptedClient(caps llm.ProviderCapabilities, steps ...ScriptedRuntimeStep) *ScriptedClient {
	return scriptedllm.NewLegacyOnlyClient(caps, steps...)
}

func NewDefaultScriptedClient(steps ...ScriptedRuntimeStep) *ScriptedClient {
	return scriptedllm.NewLegacyOnlyClient(scriptedllm.DefaultProviderCapabilities(), steps...)
}

func ScriptedFinalAnswer(content string) ScriptedRuntimeStep {
	return scriptedllm.FinalAnswer(content)
}

func ScriptedToolBatch(content string, calls ...llm.ToolCall) ScriptedRuntimeStep {
	return scriptedllm.ToolBatch(content, calls...)
}

func ScriptedAskQuestion(callID string, input []byte) ScriptedRuntimeStep {
	return scriptedllm.AskQuestion(callID, input)
}

func ScriptedRuntimeError(err error) ScriptedRuntimeStep {
	return scriptedllm.RuntimeError(err)
}

func ScriptedCancellation() ScriptedRuntimeStep {
	return scriptedllm.Cancellation()
}
