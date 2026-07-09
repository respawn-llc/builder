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

func mergeToolCallMeta(callMeta, resultMeta *transcript.ToolCallMeta) *transcript.ToolCallMeta {
	if callMeta == nil {
		if resultMeta == nil {
			return nil
		}
		normalized := transcript.NormalizeToolCallMeta(*resultMeta)
		return &normalized
	}
	if resultMeta == nil {
		return callMeta
	}

	merged := *resultMeta
	if strings.TrimSpace(merged.ToolName) == "" {
		merged.ToolName = callMeta.ToolName
	}
	if merged.Presentation == "" {
		merged.Presentation = callMeta.Presentation
	}
	if merged.RenderBehavior == "" {
		merged.RenderBehavior = callMeta.RenderBehavior
	}
	merged.IsShell = merged.IsShell || callMeta.IsShell
	merged.UserInitiated = merged.UserInitiated || callMeta.UserInitiated
	if strings.TrimSpace(merged.Command) == "" {
		merged.Command = callMeta.Command
	}
	if strings.TrimSpace(merged.CompactText) == "" {
		merged.CompactText = callMeta.CompactText
	}
	if strings.TrimSpace(merged.InlineMeta) == "" {
		merged.InlineMeta = callMeta.InlineMeta
	}
	if strings.TrimSpace(merged.TimeoutLabel) == "" {
		merged.TimeoutLabel = callMeta.TimeoutLabel
	}
	if strings.TrimSpace(merged.PatchSummary) == "" {
		merged.PatchSummary = callMeta.PatchSummary
	}
	if strings.TrimSpace(merged.PatchDetail) == "" {
		merged.PatchDetail = callMeta.PatchDetail
	}
	if merged.PatchRender == nil {
		merged.PatchRender = callMeta.PatchRender
	}
	if merged.RenderHint == nil {
		merged.RenderHint = callMeta.RenderHint
	}
	if strings.TrimSpace(merged.Question) == "" {
		merged.Question = callMeta.Question
	}
	if len(merged.Suggestions) == 0 && len(callMeta.Suggestions) > 0 {
		merged.Suggestions = append([]string(nil), callMeta.Suggestions...)
	}
	if merged.RecommendedOptionIndex == 0 {
		merged.RecommendedOptionIndex = callMeta.RecommendedOptionIndex
	}
	merged.OmitSuccessfulResult = merged.OmitSuccessfulResult || callMeta.OmitSuccessfulResult
	merged.RawOutputRequested = merged.RawOutputRequested || callMeta.RawOutputRequested
	merged.OutputTruncated = merged.OutputTruncated || callMeta.OutputTruncated

	normalized := transcript.NormalizeToolCallMeta(merged)
	return &normalized
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
