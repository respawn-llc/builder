package runtime

import (
	"core/server/llm"
	"core/server/tools"
	"core/shared/transcript"
	"os"
	goruntime "runtime"
	"strings"
)

func normalizeMessageForTranscript(msg llm.Message, workingDir string) llm.Message {
	if len(msg.ToolCalls) == 0 {
		return msg
	}
	normalized := msg
	normalized.ToolCalls = normalizeToolCallsForTranscript(msg.ToolCalls, workingDir)
	return normalized
}

func normalizeToolCallsForTranscript(calls []llm.ToolCall, workingDir string) []llm.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]llm.ToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, normalizeToolCallForTranscript(call, workingDir))
	}
	return out
}

func normalizeToolCallForTranscript(call llm.ToolCall, workingDir string) llm.ToolCall {
	normalized := call
	meta := transcriptToolCallMeta(call, workingDir)
	if meta == nil {
		return normalized
	}
	normalized.Presentation = transcript.EncodeToolCallMeta(*meta)
	return normalized
}

func transcriptToolCallMeta(call llm.ToolCall, workingDir string) *transcript.ToolCallMeta {
	if meta := decodeToolCallMeta(call); meta != nil {
		return meta
	}
	input := call.Input
	if call.Custom && strings.TrimSpace(call.CustomInput) != "" {
		input = normalizeRuntimeToolInput(call.CustomInput)
	}
	built := tools.BuildCallTranscriptMeta(call.Name, tools.ToolCallContext{
		WorkingDir:       workingDir,
		DefaultShellPath: currentTranscriptDefaultShellPath(),
		GOOS:             goruntime.GOOS,
	}, input)
	return &built
}

func currentTranscriptDefaultShellPath() string {
	if shellPath := strings.TrimSpace(os.Getenv("SHELL")); shellPath != "" {
		return shellPath
	}
	return strings.TrimSpace(os.Getenv("COMSPEC"))
}

func decodeToolCallMeta(call llm.ToolCall) *transcript.ToolCallMeta {
	meta, ok := transcript.DecodeToolCallMeta(call.Presentation)
	if !ok {
		return nil
	}
	return meta
}
