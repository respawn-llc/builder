package runtime

import (
	"encoding/json"
	"fmt"
	"strings"

	"core/server/llm"
	"core/server/tools"
	"core/shared/toolspec"
	"core/shared/transcript"
)

// missingToolOutputAfterCollapseInvariant is the panic message for a
// missing-tool-output provider error observed once the compaction request has
// already been overflow-collapsed. Collapse only shrinks existing tool output
// payloads and never removes output items, so a request that still lacks an
// output after collapsing means the transcript itself is corrupt rather than
// merely interrupted; that is an unreachable state, not one to silently repair.
const missingToolOutputAfterCollapseInvariant = "compaction request still has a tool call without an output after overflow collapse; collapse preserves output items, so a missing-tool-output provider error here is an invariant violation"

// missingToolOutputRepairWarningTemplate is the operator-facing notice appended
// when the repair closes one or more interrupted tool calls.
const missingToolOutputRepairWarningTemplate = "Closed %d interrupted tool call(s) with a synthetic result to repair the transcript after a provider error"

// missingToolOutputInterruptedOutput is the honest result recorded for a tool
// call that was left unanswered (typically interrupted) and can no longer be
// re-executed. It tells the model the call never produced a result rather than
// fabricating a successful one or silently erasing the call from history.
var missingToolOutputInterruptedOutput = json.RawMessage(`{"error":"Tool execution was interrupted before a result was produced. No output is available for this call."}`)

// danglingToolCall identifies a persisted tool call that lacks an output.
type danglingToolCall struct {
	callID string
	name   string
}

// repairMissingToolOutputsByAppending closes any tool calls in the live
// projection that lack an output by appending an honest synthetic tool
// completion for each, plus one operator-facing warning. It returns the number
// of calls repaired.
//
// This is append-only: it persists new tool_completed events through the normal
// steering/completion path and never rewrites or removes existing history, so
// the prompt-cache prefix through each repaired call stays intact. The provider
// output item materialized for each completion automatically matches the
// original call kind (function vs custom) via the projection.
//
// It is a fallback for the resume path, which re-executes interrupted tool calls
// to obtain real outputs; when there are still pending tool-call starts to
// re-execute, this no-ops so it never pre-empts a real result.
func (e *Engine) repairMissingToolOutputsByAppending(stepID string) (int, error) {
	if e == nil || e.store == nil {
		return 0, nil
	}
	if e.pendingToolCallStartStore().Len() > 0 {
		return 0, nil
	}
	chat := e.transcriptRuntimeState().chatProjection()
	if chat == nil {
		return 0, nil
	}
	dangling := chat.danglingToolCalls()
	if len(dangling) == 0 {
		return 0, nil
	}
	intents := make([]steeringIntent, 0, len(dangling)+1)
	for _, call := range dangling {
		intents = append(intents, steerToolCompletionIntent(tools.Result{
			CallID:  call.callID,
			Name:    toolspec.ID(call.name),
			IsError: true,
			Output:  append(json.RawMessage(nil), missingToolOutputInterruptedOutput...),
		}))
	}
	intents = append(intents, steerLocalEntryIntent(storedLocalEntry{
		Visibility: transcript.EntryVisibilityAll,
		Role:       string(transcript.EntryRoleDeveloperErrorFeedback),
		Text:       fmt.Sprintf(missingToolOutputRepairWarningTemplate, len(dangling)),
	}))
	if err := e.steer(stepID, intents...); err != nil {
		return 0, err
	}
	return len(dangling), nil
}

// itemsHaveDanglingToolCalls reports whether a prepared request item sequence
// contains a tool call with no accompanying output item. The compaction request
// window materializes recorded completions into output items, so a dangling call
// here is exactly what a provider rejects with a missing-tool-output HTTP 400.
func itemsHaveDanglingToolCalls(items []llm.ResponseItem) bool {
	materialized := collectMaterializedToolCalls(items)
	for _, item := range items {
		if !isToolCallItem(item.Type) {
			continue
		}
		callID := strings.TrimSpace(item.CallID)
		if callID == "" {
			callID = strings.TrimSpace(item.ID)
		}
		if callID == "" {
			continue
		}
		if _, ok := materialized[callID]; ok {
			continue
		}
		return true
	}
	return false
}

// isMissingToolOutputProviderError reports whether a compaction send failed with
// a missing-tool-output HTTP 400: a non-overflow 400 whose request still carries
// a tool call without an output. Context-overflow 400s and 400s on requests with
// no dangling calls are explicitly excluded so they fall through as unrelated.
func isMissingToolOutputProviderError(err error, items []llm.ResponseItem) bool {
	if err == nil || !llm.HasHTTPStatus(err, 400) || llm.IsContextLengthOverflowError(err) {
		return false
	}
	return itemsHaveDanglingToolCalls(items)
}

// errorIsRepairableMissingToolOutput reports whether a provider error is a
// transient HTTP 400 caused by interrupted tool calls that lack outputs, i.e.
// one the append-only model request path will repair on retry. Callers use this
// to avoid recording a permanent failure for a request that is about to be
// repaired (for example, the precise token-count probe must not disable exact
// counting for the rest of the active list just because one probe observed a
// repairable malformed request).
func (e *Engine) errorIsRepairableMissingToolOutput(err error) bool {
	if err == nil || !llm.HasHTTPStatus(err, 400) {
		return false
	}
	if e == nil || e.store == nil {
		return false
	}
	chat := e.transcriptRuntimeState().chatProjection()
	if chat == nil {
		return false
	}
	return len(chat.danglingToolCalls()) > 0
}
