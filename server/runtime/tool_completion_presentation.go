package runtime

import (
	"fmt"

	"core/server/llm"
	"core/server/tools"
	"core/shared/transcript"
)

type toolCompletionFinalization struct {
	tools.Result
	Diagnostic *transcript.DeveloperDiagnostic
}

func (e *Engine) finalizeLiveToolCompletion(result tools.Result) toolCompletionFinalization {
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

func toolResultWithTranscriptPresentation(result tools.Result, call llm.ToolCall, workingDir string) toolCompletionFinalization {
	callMeta := transcriptToolCallMeta(call, workingDir)
	if callMeta == nil {
		panic(fmt.Sprintf(
			"tool result presentation invariant violated: call metadata is unavailable (call_id=%q tool=%q)",
			result.CallID,
			result.Name,
		))
	}
	outcome := transcript.ToolResultPresentationOutcomeSuccessful
	if result.IsError {
		outcome = transcript.ToolResultPresentationOutcomeFailed
	}
	finalized, err := transcript.ApplyToolResultPresentationDelta(*callMeta, result.PresentationDelta, outcome)
	result.PresentationDelta = nil
	if err != nil {
		finalized = transcript.NormalizeToolCallMeta(*callMeta)
		diagnostic := transcript.NewDeletionFactMismatchDeveloperDiagnostic(result.CallID, *err)
		result.Presentation = &finalized
		return toolCompletionFinalization{Result: result, Diagnostic: &diagnostic}
	}
	result.Presentation = &finalized
	return toolCompletionFinalization{Result: result}
}
