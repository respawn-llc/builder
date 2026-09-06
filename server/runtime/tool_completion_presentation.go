package runtime

import (
	"fmt"
	"strings"

	"core/server/llm"
	"core/server/tools"
	"core/shared/toolspec"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"
)

type toolCompletionPresentationPanic struct {
	CallID   string
	ToolName toolspec.ID
	Mismatch *patchformat.WholeFileDeletionFactMismatch
}

func (p toolCompletionPresentationPanic) Error() string {
	mismatch := "whole-file deletion fact mismatch is absent"
	if p.Mismatch != nil {
		mismatch = p.Mismatch.Error()
	}
	return fmt.Sprintf(
		"tool completion presentation mismatch (call_id=%q tool=%q mismatch=%s)",
		p.CallID,
		p.ToolName,
		mismatch,
	)
}

type finalizedToolCompletion struct {
	Result           tools.Result
	OperatorFeedback *storedLocalEntry
}

func (e *Engine) finalizeLiveToolCompletion(result tools.Result) finalizedToolCompletion {
	if result.Presentation != nil {
		panic(fmt.Sprintf(
			"tool result presentation invariant violated: live completion already has finalized presentation (call_id=%q tool=%q)",
			result.CallID,
			result.Name,
		))
	}
	call, ok, snapshotErr := e.transcriptRuntimeState().ToolCallSnapshot(result.CallID)
	if snapshotErr != nil {
		panic(fmt.Sprintf(
			"tool result presentation invariant violated: authoritative call presentation is invalid (call_id=%q tool=%q): %v",
			result.CallID,
			result.Name,
			snapshotErr,
		))
	}
	if !ok {
		panic(fmt.Sprintf(
			"tool result presentation invariant violated: authoritative call is unavailable at completion boundary (call_id=%q tool=%q)",
			result.CallID,
			result.Name,
		))
	}
	finalized, mismatch := toolResultWithTranscriptPresentation(
		result,
		call,
		e.transcriptWorkingDir(),
	)
	if mismatch == nil {
		return finalizedToolCompletion{Result: finalized}
	}
	failure := toolCompletionPresentationPanic{
		CallID:   result.CallID,
		ToolName: result.Name,
		Mismatch: mismatch,
	}
	if e.cfg.Debug {
		panic(failure)
	}

	callMeta := transcriptToolCallMeta(call, e.transcriptWorkingDir())
	if callMeta == nil {
		panic(fmt.Sprintf(
			"tool result presentation invariant violated: call metadata is unavailable for release fallback (call_id=%q tool=%q)",
			result.CallID,
			result.Name,
		))
	}
	result.PresentationDelta = nil
	fallback := transcript.NormalizeToolCallMeta(*callMeta)
	result.Presentation = &fallback
	callID := strings.TrimSpace(result.CallID)
	if callID == "" {
		panic(fmt.Sprintf(
			"tool completion presentation fallback requires a call identity (tool=%q)",
			result.Name,
		))
	}
	return finalizedToolCompletion{
		Result: result,
		OperatorFeedback: &storedLocalEntry{
			Visibility:      transcript.EntryVisibilityAuto,
			Role:            string(transcript.EntryRoleDeveloperErrorFeedback),
			Text:            failure.Error(),
			AfterToolCallID: &callID,
		},
	}
}

func toolResultWithTranscriptPresentation(
	result tools.Result,
	call llm.ToolCall,
	workingDir string,
) (tools.Result, *patchformat.WholeFileDeletionFactMismatch) {
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
	finalized, mismatch := transcript.ApplyToolResultPresentationDelta(
		*callMeta,
		result.PresentationDelta,
		outcome,
	)
	if mismatch != nil {
		return result, mismatch
	}
	result.PresentationDelta = nil
	result.Presentation = &finalized
	return result, nil
}
