package runtime

import (
	"fmt"

	"core/server/llm"
	"core/server/tools"
	"core/shared/transcript"
)

func (e *Engine) finalizeLiveToolCompletion(result tools.Result) tools.Result {
	if result.Presentation != nil {
		panic(fmt.Sprintf(
			"tool result presentation invariant violated: live completion already has finalized presentation (call_id=%q tool=%q)",
			result.CallID,
			result.Name,
		))
	}
	call, ok := e.transcriptRuntimeState().ToolCallSnapshot(result.CallID)
	if !ok {
		panic(fmt.Sprintf(
			"tool result presentation invariant violated: authoritative call is unavailable at completion boundary (call_id=%q tool=%q)",
			result.CallID,
			result.Name,
		))
	}
	return toolResultWithTranscriptPresentation(result, call, e.transcriptWorkingDir())
}

func toolResultWithTranscriptPresentation(result tools.Result, call llm.ToolCall, workingDir string) tools.Result {
	callMeta := transcriptToolCallMeta(call, workingDir)
	if callMeta == nil {
		panic(fmt.Sprintf(
			"tool result presentation invariant violated: call metadata is unavailable (call_id=%q tool=%q)",
			result.CallID,
			result.Name,
		))
	}
	finalized := transcript.ApplyToolResultPresentationDelta(*callMeta, result.PresentationDelta)
	result.PresentationDelta = nil
	result.Presentation = &finalized
	return result
}
