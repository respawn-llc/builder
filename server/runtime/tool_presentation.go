package runtime

import (
	"core/server/llm"
	"core/server/tools"
	"core/shared/transcript"
	"fmt"
	"os"
	goruntime "runtime"
	"strings"
)

func normalizeMessageForTranscript(msg llm.Message, workingDir string) llm.Message {
	normalized, err := normalizeMessageForTranscriptChecked(msg, workingDir)
	if err != nil {
		panic(err)
	}
	return normalized
}

func normalizeMessageForTranscriptChecked(msg llm.Message, workingDir string) (llm.Message, error) {
	if len(msg.ToolCalls) == 0 {
		return msg, nil
	}
	normalized := msg
	calls, err := normalizeToolCallsForTranscriptChecked(msg.ToolCalls, workingDir)
	if err != nil {
		return llm.Message{}, err
	}
	normalized.ToolCalls = calls
	return normalized, nil
}

func normalizeToolCallsForTranscript(calls []llm.ToolCall, workingDir string) []llm.ToolCall {
	normalized, err := normalizeToolCallsForTranscriptChecked(calls, workingDir)
	if err != nil {
		panic(err)
	}
	return normalized
}

func normalizeToolCallsForTranscriptChecked(calls []llm.ToolCall, workingDir string) ([]llm.ToolCall, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	out := make([]llm.ToolCall, 0, len(calls))
	for _, call := range calls {
		normalized, err := normalizeToolCallForTranscriptChecked(call, workingDir)
		if err != nil {
			return nil, err
		}
		out = append(out, normalized)
	}
	return out, nil
}

func normalizeToolCallForTranscript(call llm.ToolCall, workingDir string) llm.ToolCall {
	normalized, err := normalizeToolCallForTranscriptChecked(call, workingDir)
	if err != nil {
		panic(err)
	}
	return normalized
}

func normalizeToolCallForTranscriptChecked(call llm.ToolCall, workingDir string) (llm.ToolCall, error) {
	normalized := call
	meta, err := transcriptToolCallMetaChecked(call, workingDir)
	if err != nil {
		return llm.ToolCall{}, err
	}
	if meta == nil {
		return normalized, nil
	}
	presentation, err := transcript.TryEncodeToolCallMeta(*meta)
	if err != nil {
		return llm.ToolCall{}, fmt.Errorf(
			"encode transcript presentation for tool call %q: %w",
			call.ID,
			err,
		)
	}
	normalized.Presentation = presentation
	return normalized, nil
}

func transcriptToolCallMeta(call llm.ToolCall, workingDir string) *transcript.ToolCallMeta {
	meta, err := transcriptToolCallMetaChecked(call, workingDir)
	if err != nil {
		panic(err)
	}
	return meta
}

func transcriptToolCallMetaChecked(call llm.ToolCall, workingDir string) (*transcript.ToolCallMeta, error) {
	decoded := transcript.DecodeToolCallMeta(call.Presentation)
	switch decoded.Kind {
	case transcript.ToolCallMetaDecodeCurrent, transcript.ToolCallMetaDecodeLegacyNormalized:
		if decoded.Meta == nil {
			return nil, fmt.Errorf(
				"tool call %q metadata decode outcome %d has no metadata",
				call.ID,
				decoded.Kind,
			)
		}
		return decoded.Meta, nil
	case transcript.ToolCallMetaDecodeAbsent, transcript.ToolCallMetaDecodeInvalid:
	default:
		return nil, fmt.Errorf(
			"tool call %q metadata decode returned unknown outcome %d",
			call.ID,
			decoded.Kind,
		)
	}
	return buildToolCallMeta(call, workingDir), nil
}

func buildToolCallMeta(call llm.ToolCall, workingDir string) *transcript.ToolCallMeta {
	input := call.Input
	if call.Custom && call.CustomInput != nil &&
		strings.TrimSpace(*call.CustomInput) != "" {
		input = normalizeRuntimeToolInput(*call.CustomInput)
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
	meta, err := decodeToolCallMetaChecked(call)
	if err != nil {
		panic(err)
	}
	return meta
}

func decodeToolCallMetaChecked(call llm.ToolCall) (*transcript.ToolCallMeta, error) {
	result := transcript.DecodeToolCallMeta(call.Presentation)
	switch result.Kind {
	case transcript.ToolCallMetaDecodeAbsent:
		return nil, nil
	case transcript.ToolCallMetaDecodeCurrent, transcript.ToolCallMetaDecodeLegacyNormalized:
		if result.Meta == nil {
			return nil, fmt.Errorf(
				"tool call %q metadata decode outcome %d has no metadata",
				call.ID,
				result.Kind,
			)
		}
		return result.Meta, nil
	case transcript.ToolCallMetaDecodeInvalid:
		return nil, fmt.Errorf(
			"tool call %q has invalid present transcript presentation: %w",
			call.ID,
			result.Cause,
		)
	default:
		return nil, fmt.Errorf(
			"tool call %q metadata decode returned unknown outcome %d",
			call.ID,
			result.Kind,
		)
	}
}
