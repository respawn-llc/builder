package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
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

// missingToolOutputInterruptedOutput is the honest result recorded for a tool
// call that was left unanswered (typically interrupted) and can no longer be
// re-executed. It tells the model the call never produced a result rather than
// fabricating a successful one or silently erasing the call from history.
var missingToolOutputInterruptedOutput = json.RawMessage(`{"error":"Tool execution was interrupted before a result was produced. No output is available for this call."}`)

func missingToolOutputInterruptedResult(callID string, name toolspec.ID) tools.Result {
	return tools.Result{
		CallID:  callID,
		Name:    name,
		IsError: true,
		Output:  append(json.RawMessage(nil), missingToolOutputInterruptedOutput...),
	}
}

var missingToolOutputUnavailableOutput = json.RawMessage(`{"error":"No committed output is available for this tool call."}`)

type missingToolOutputRepairDisposition uint8

const (
	missingToolOutputRepairFreshResource missingToolOutputRepairDisposition = iota + 1
	missingToolOutputRepairLiveProvider400
)

type missingToolOutputRepairPolicy struct {
	output                   json.RawMessage
	repairKind               transcript.ToolOutputRepairKind
	deferToPendingToolStarts bool
}

func missingToolOutputPolicy(disposition missingToolOutputRepairDisposition) (missingToolOutputRepairPolicy, error) {
	switch disposition {
	case missingToolOutputRepairFreshResource:
		return missingToolOutputRepairPolicy{
			output:     missingToolOutputUnavailableOutput,
			repairKind: transcript.ToolOutputRepairFreshResource,
		}, nil
	case missingToolOutputRepairLiveProvider400:
		return missingToolOutputRepairPolicy{
			output:                   missingToolOutputInterruptedOutput,
			repairKind:               transcript.ToolOutputRepairLiveProviderRejection,
			deferToPendingToolStarts: true,
		}, nil
	default:
		return missingToolOutputRepairPolicy{}, fmt.Errorf("unsupported missing tool output repair disposition %d", disposition)
	}
}

// danglingToolCall identifies a persisted tool call that lacks an output.
type danglingToolCall struct {
	callID string
	name   string
	stepID *string
}

type steeringMissingToolOutputRepair struct {
	repairStepID *string
	disposition  missingToolOutputRepairDisposition
	repaired     int
}

func (e *Engine) repairMissingToolOutputsByAppending(
	repairStepID *string,
	disposition missingToolOutputRepairDisposition,
) (int, error) {
	if e == nil || e.store == nil {
		return 0, nil
	}
	repair := &steeringMissingToolOutputRepair{repairStepID: textutil.Pointer(repairStepID), disposition: disposition}
	stepID, _ := textutil.OptionalValue(repairStepID)
	err := e.steer(stepID, steeringIntent{
		priority: steeringPriorityNormal,
		items:    []steeringItem{{missingToolOutputRepair: repair}},
	})
	return repair.repaired, err
}

func (e *Engine) repairMissingToolOutputsByAppendingRaw(
	repairStepID *string,
	disposition missingToolOutputRepairDisposition,
) (int, error) {
	policy, err := missingToolOutputPolicy(disposition)
	if err != nil {
		return 0, err
	}
	if repairStepID != nil {
		normalized := strings.TrimSpace(*repairStepID)
		if normalized == "" {
			return 0, errors.New("repair step id must be non-empty when present")
		}
		repairStepID = textutil.Value(normalized)
	}
	chat := e.transcriptRuntimeState().chatProjection()
	if chat == nil {
		return 0, nil
	}
	dangling := chat.danglingToolCalls()
	if len(dangling) == 0 {
		return 0, nil
	}
	if policy.deferToPendingToolStarts {
		repairable := dangling[:0]
		for _, call := range dangling {
			if _, pending := e.pendingToolCallStart(call.callID); !pending {
				repairable = append(repairable, call)
			}
		}
		dangling = repairable
		if len(dangling) == 0 {
			return 0, nil
		}
	}
	for index := range dangling {
		if dangling[index].stepID == nil {
			dangling[index].stepID = textutil.Pointer(repairStepID)
		}
		if dangling[index].stepID == nil {
			return 0, fmt.Errorf("repair dangling tool call %q: step id is required", dangling[index].callID)
		}
	}
	warning := storedLocalEntry{
		Visibility: transcript.EntryVisibilityOngoing,
		Role:       string(transcript.EntryRoleDeveloperErrorFeedback),
		ToolOutputRepair: &transcript.ToolOutputRepairNotice{
			Kind:  policy.repairKind,
			Count: len(dangling),
		},
	}
	prepared := make([]preparedFinalizedToolCompletion, 0, len(dangling))
	inputs := make([]session.EventRecordAppendInput, 0, len(dangling)+1)
	for index, call := range dangling {
		result := missingToolOutputInterruptedResult(call.callID, toolspec.ID(call.name))
		result.Output = append(json.RawMessage(nil), policy.output...)
		finalized := e.finalizeLiveToolCompletion(result)
		if finalized.OperatorFeedback != nil {
			return 0, fmt.Errorf(
				"repair dangling tool call %q produced unexpected presentation feedback",
				call.callID,
			)
		}
		if index == len(dangling)-1 {
			finalized.OperatorFeedback = &warning
		}
		completion, err := e.prepareFinalizedToolCompletion(finalized)
		if err != nil {
			return 0, fmt.Errorf("prepare dangling tool call %q repair: %w", call.callID, err)
		}
		prepared = append(prepared, completion)
		for recordIndex, payload := range completion.records {
			recordStepID := call.stepID
			if completion.feedback != nil && recordIndex == len(completion.records)-1 {
				recordStepID = repairStepID
			}
			inputs = append(inputs, session.EventRecordAppendInput{
				StepID:  textutil.Pointer(recordStepID),
				Payload: payload,
			})
		}
	}
	records, receipt, appendErr := e.eventLog.AppendRecordBatchAtomic(inputs)
	if !receipt.Committed {
		return 0, appendErr
	}
	recordIndex := 0
	var projectionErr error
	for index, completion := range prepared {
		nextRecordIndex := recordIndex + len(completion.records)
		feedbackStepID := dangling[index].stepID
		if completion.feedback != nil {
			feedbackStepID = repairStepID
		}
		applied, err := e.applyPreparedFinalizedToolCompletion(
			feedbackStepID,
			completion,
			records[recordIndex:nextRecordIndex],
		)
		recordIndex = nextRecordIndex
		if err != nil {
			projectionErr = errors.Join(projectionErr, err)
			continue
		}
		projectionErr = errors.Join(
			projectionErr,
			e.publishCommittedFinalizedToolCompletion(
				dangling[index].stepID,
				feedbackStepID,
				completion.completion,
				&applied.completionProvenance,
				applied.feedbackProvenance,
			),
		)
	}
	return len(dangling), errors.Join(appendErr, projectionErr)
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
		callID, present := textutil.FirstOptionalTrimmed(item.CallID, item.ID)
		if !present {
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
