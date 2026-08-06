package runtime

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
)

type ResultGroupFlushReason uint8

const (
	ResultGroupFlushStepBoundary ResultGroupFlushReason = iota + 1
	ResultGroupFlushQuestion
	ResultGroupFlushApproval
	ResultGroupFlushCompleteNode
)

func (r ResultGroupFlushReason) String() string {
	switch r {
	case ResultGroupFlushStepBoundary:
		return "step_boundary"
	case ResultGroupFlushQuestion:
		return "question"
	case ResultGroupFlushApproval:
		return "approval"
	case ResultGroupFlushCompleteNode:
		return "complete_node"
	default:
		return fmt.Sprintf("unknown(%d)", r)
	}
}

type ResultGroupFlushObservation struct {
	Reason      ResultGroupFlushReason
	ResultCount int
	RecordCount int
	Latency     time.Duration
	Succeeded   bool
}

type ResultGroupDurabilityObserver interface {
	ObserveResultGroupFlush(ResultGroupFlushObservation)
}

type resultGroupCallIdentity struct {
	CallID     string
	Name       toolspec.ID
	OutputKind session.ToolOutputKind
}

type resultGroupUnit struct {
	result tools.Result
}

type resultGroupSlot struct {
	ordinal int
	call    resultGroupCallIdentity
	result  *resultGroupUnit
}

type resultGroupCollectorState uint8

const (
	resultGroupCollectorActive resultGroupCollectorState = iota + 1
	resultGroupCollectorAborted
	resultGroupCollectorClosed
)

type resultGroupReportOutcome uint8

const (
	resultGroupReportAccepted resultGroupReportOutcome = iota + 1
	resultGroupReportIgnoredAfterAbort
)

type resultGroupFatal struct {
	Committed bool
	Cause     error
}

func (f resultGroupFatal) Error() string {
	if f.Cause == nil {
		return fmt.Sprintf("result group durability failed (committed=%t)", f.Committed)
	}
	return fmt.Sprintf(
		"result group durability failed (committed=%t): %v",
		f.Committed,
		f.Cause,
	)
}

func (f resultGroupFatal) Unwrap() error {
	return f.Cause
}

func (f resultGroupFatal) RuntimeAbortDisposition() (bool, error) {
	return f.Committed, f.Cause
}

func resultGroupFatalFromError(err error) (*resultGroupFatal, bool) {
	var fatal *resultGroupFatal
	if !errors.As(err, &fatal) {
		return nil, false
	}
	return fatal, true
}

type resultGroupCollector struct {
	slots  []resultGroupSlot
	cursor int
	state  resultGroupCollectorState
	fatal  atomic.Pointer[resultGroupFatal]
}

func newResultGroupCollector(roster []resultGroupCallIdentity) (*resultGroupCollector, error) {
	if len(roster) == 0 {
		return nil, errors.New("result group roster is required")
	}
	slots := make([]resultGroupSlot, len(roster))
	seen := make(map[string]struct{}, len(roster))
	for ordinal, call := range roster {
		call.CallID = strings.TrimSpace(call.CallID)
		if call.CallID == "" {
			return nil, fmt.Errorf("result group call %d ID is required", ordinal)
		}
		if _, exists := seen[call.CallID]; exists {
			return nil, fmt.Errorf("result group call ID %q is duplicated", call.CallID)
		}
		seen[call.CallID] = struct{}{}
		switch call.OutputKind {
		case session.ToolOutputKindFunction, session.ToolOutputKindCustom:
		default:
			return nil, fmt.Errorf(
				"result group call %q has invalid output kind %q",
				call.CallID,
				call.OutputKind,
			)
		}
		slots[ordinal] = resultGroupSlot{
			ordinal: ordinal,
			call:    call,
		}
	}
	return &resultGroupCollector{
		slots: slots,
		state: resultGroupCollectorActive,
	}, nil
}

func (c *resultGroupCollector) report(
	callID string,
	unit resultGroupUnit,
) (resultGroupReportOutcome, error) {
	if c == nil {
		return 0, errors.New("result group collector is required")
	}
	switch c.state {
	case resultGroupCollectorAborted:
		return resultGroupReportIgnoredAfterAbort, nil
	case resultGroupCollectorClosed:
		return 0, errors.New("result group collector is closed")
	case resultGroupCollectorActive:
	default:
		return 0, fmt.Errorf("result group collector has invalid state %d", c.state)
	}
	callID = strings.TrimSpace(callID)
	for index := range c.slots {
		slot := &c.slots[index]
		if slot.call.CallID != callID {
			continue
		}
		if slot.result != nil {
			return 0, fmt.Errorf("result group call %q was already reported", callID)
		}
		copyUnit := cloneResultGroupUnit(unit)
		slot.result = &copyUnit
		return resultGroupReportAccepted, nil
	}
	return 0, fmt.Errorf("result group call %q is not in the roster", callID)
}

func (c *resultGroupCollector) readyPrefix() []resultGroupUnit {
	if c == nil || c.state != resultGroupCollectorActive {
		return nil
	}
	result := make([]resultGroupUnit, 0, len(c.slots)-c.cursor)
	for index := c.cursor; index < len(c.slots); index++ {
		if c.slots[index].result == nil {
			break
		}
		result = append(result, cloneResultGroupUnit(*c.slots[index].result))
	}
	return result
}

func (c *resultGroupCollector) advanceReadyPrefix(count int) error {
	if c == nil {
		return errors.New("result group collector is required")
	}
	if c.state != resultGroupCollectorActive {
		return fmt.Errorf(
			"result group collector cannot advance in state %d",
			c.state,
		)
	}
	ready := c.readyPrefix()
	if count <= 0 || count > len(ready) {
		return fmt.Errorf(
			"result group cursor advance %d exceeds ready prefix %d at cursor %d",
			count,
			len(ready),
			c.cursor,
		)
	}
	c.cursor += count
	return nil
}

func (c *resultGroupCollector) requireCompleteForClose() error {
	if c == nil {
		return errors.New("result group collector is required")
	}
	if c.state == resultGroupCollectorAborted {
		return nil
	}
	if c.state != resultGroupCollectorActive {
		return fmt.Errorf(
			"result group collector cannot prepare close in state %d",
			c.state,
		)
	}
	for index := c.cursor; index < len(c.slots); index++ {
		if c.slots[index].result == nil {
			return fmt.Errorf(
				"result group close found empty slot ordinal=%d call_id=%q cursor=%d slots=%d",
				c.slots[index].ordinal,
				c.slots[index].call.CallID,
				c.cursor,
				len(c.slots),
			)
		}
	}
	return nil
}

func (c *resultGroupCollector) abort(fatal resultGroupFatal) {
	if c == nil {
		return
	}
	if fatal.Cause == nil {
		panic("result group fatal cause is required")
	}
	copyFatal := fatal
	if c.fatal.CompareAndSwap(nil, &copyFatal) {
		c.state = resultGroupCollectorAborted
	}
}

func (c *resultGroupCollector) fatalSnapshot() *resultGroupFatal {
	if c == nil {
		return nil
	}
	fatal := c.fatal.Load()
	if fatal == nil {
		return nil
	}
	copyFatal := *fatal
	return &copyFatal
}

func (c *resultGroupCollector) close() error {
	if c == nil {
		return errors.New("result group collector is required")
	}
	switch c.state {
	case resultGroupCollectorAborted:
		c.state = resultGroupCollectorClosed
		return nil
	case resultGroupCollectorActive:
		if err := c.requireCompleteForClose(); err != nil {
			return err
		}
		if c.cursor != len(c.slots) {
			return fmt.Errorf(
				"result group close cursor=%d slots=%d",
				c.cursor,
				len(c.slots),
			)
		}
		c.state = resultGroupCollectorClosed
		return nil
	case resultGroupCollectorClosed:
		return errors.New("result group collector is already closed")
	default:
		return fmt.Errorf("result group collector has invalid state %d", c.state)
	}
}

func cloneResultGroupUnit(unit resultGroupUnit) resultGroupUnit {
	return resultGroupUnit{result: cloneToolResult(unit.result)}
}

type resultGroupPreparedUnit struct {
	completion            finalizedToolCompletion
	storedCompletion      storedToolCompletion
	backgroundSessionID   string
	hasBackgroundSession  bool
	output                llm.Message
	feedback              *storedLocalEntry
	completionRecordIndex int
	feedbackRecordIndex   *int
	outputRecordIndex     int
}

type resultGroupProjectionPlan struct {
	payloads []session.EventRecordPayload
	units    []resultGroupPreparedUnit
}

func resultGroupRosterFromAcceptedCalls(calls acceptedResponseCalls) []resultGroupCallIdentity {
	ordered := calls.toolCalls()
	roster := make([]resultGroupCallIdentity, len(ordered))
	for index, call := range ordered {
		roster[index] = resultGroupIdentityFromToolCall(call)
	}
	return roster
}

func resultGroupRosterFromPreparedCalls(calls []executorToolCall) []resultGroupCallIdentity {
	roster := make([]resultGroupCallIdentity, len(calls))
	for index, call := range calls {
		roster[index] = resultGroupIdentityFromToolCall(call.call)
	}
	return roster
}

func resultGroupIdentityFromToolCall(call llm.ToolCall) resultGroupCallIdentity {
	outputKind := session.ToolOutputKindFunction
	if call.Custom {
		outputKind = session.ToolOutputKindCustom
	}
	return resultGroupCallIdentity{
		CallID:     call.ID,
		Name:       toolspec.ID(call.Name),
		OutputKind: outputKind,
	}
}

func (e *Engine) flushResultGroup(
	stepID string,
	collector *resultGroupCollector,
	reason ResultGroupFlushReason,
) error {
	if collector == nil {
		return errors.New("result group collector is required")
	}
	if err := validateResultGroupFlushReason(reason); err != nil {
		return err
	}
	if fatal := collector.fatalSnapshot(); fatal != nil {
		return fatal
	}
	ready := collector.readyPrefix()
	if len(ready) == 0 {
		return nil
	}
	plan, err := e.prepareResultGroupProjection(stepID, collector, ready)
	if err != nil {
		return err
	}
	started := time.Now()
	records, receipt, appendErr := e.eventLog.AppendRecordsAtomic(
		textutil.OptionalExactString(stepID),
		plan.payloads,
	)
	if e.cfg.DurabilityObserver != nil {
		e.cfg.DurabilityObserver.ObserveResultGroupFlush(ResultGroupFlushObservation{
			Reason:      reason,
			ResultCount: len(ready),
			RecordCount: len(plan.payloads),
			Latency:     time.Since(started),
			Succeeded:   appendErr == nil,
		})
	}
	if !receipt.Committed {
		fatal := resultGroupFatal{Committed: false, Cause: appendErr}
		if fatal.Cause == nil {
			fatal.Cause = errors.New("result group append did not commit")
		}
		collector.abort(fatal)
		return fatal
	}
	if err := collector.advanceReadyPrefix(len(ready)); err != nil {
		panic(fmt.Sprintf(
			"committed result group failed cursor advance (step_id=%q results=%d records=%d error=%v)",
			stepID,
			len(ready),
			len(records),
			err,
		))
	}
	if projectionErr := e.applyResultGroupProjection(stepID, plan, records); projectionErr != nil {
		fatal := resultGroupFatal{Committed: true, Cause: projectionErr}
		collector.abort(fatal)
		return fatal
	}
	if appendErr != nil {
		fatal := resultGroupFatal{Committed: true, Cause: appendErr}
		collector.abort(fatal)
		return fatal
	}
	return nil
}

func (e *Engine) closeResultGroup(
	stepID string,
	collector *resultGroupCollector,
) error {
	if collector == nil {
		return errors.New("result group collector is required")
	}
	if fatal := collector.fatalSnapshot(); fatal != nil {
		closeErr := collector.close()
		return errors.Join(fatal, closeErr)
	}
	if err := collector.requireCompleteForClose(); err != nil {
		return err
	}
	if len(collector.readyPrefix()) > 0 {
		if err := e.flushResultGroup(
			stepID,
			collector,
			ResultGroupFlushStepBoundary,
		); err != nil {
			closeErr := collector.close()
			return errors.Join(err, closeErr)
		}
	}
	return collector.close()
}

func validateResultGroupFlushReason(reason ResultGroupFlushReason) error {
	switch reason {
	case ResultGroupFlushStepBoundary,
		ResultGroupFlushQuestion,
		ResultGroupFlushApproval,
		ResultGroupFlushCompleteNode:
		return nil
	default:
		return fmt.Errorf("unknown result group flush reason %d", reason)
	}
}

func (e *Engine) prepareResultGroupProjection(
	stepID string,
	collector *resultGroupCollector,
	units []resultGroupUnit,
) (resultGroupProjectionPlan, error) {
	plan := resultGroupProjectionPlan{
		payloads: make([]session.EventRecordPayload, 0, len(units)*2),
		units:    make([]resultGroupPreparedUnit, 0, len(units)),
	}
	for index, unit := range units {
		slotIndex := collector.cursor + index
		if slotIndex >= len(collector.slots) {
			return resultGroupProjectionPlan{}, fmt.Errorf(
				"result group projection slot %d exceeds roster %d",
				slotIndex,
				len(collector.slots),
			)
		}
		slot := collector.slots[slotIndex]
		if unit.result.CallID != slot.call.CallID {
			return resultGroupProjectionPlan{}, fmt.Errorf(
				"result group unit call %q does not match slot %q at ordinal %d",
				unit.result.CallID,
				slot.call.CallID,
				slot.ordinal,
			)
		}
		completion := e.finalizeLiveToolCompletion(unit.result)
		stored, backgroundSessionID, hasBackgroundSession :=
			e.prepareStoredToolCompletion(completion.Result)
		completionRecord, err := sessionToolCompletionRecordFromStored(stored)
		if err != nil {
			return resultGroupProjectionPlan{}, fmt.Errorf(
				"adapt result group completion %q: %w",
				slot.call.CallID,
				err,
			)
		}
		prepared := resultGroupPreparedUnit{
			completion:            completion,
			storedCompletion:      stored,
			backgroundSessionID:   backgroundSessionID,
			hasBackgroundSession:  hasBackgroundSession,
			completionRecordIndex: len(plan.payloads),
		}
		plan.payloads = append(plan.payloads, completionRecord)
		if completion.OperatorFeedback != nil {
			feedback, err := normalizeStoredLocalEntry(*completion.OperatorFeedback)
			if err != nil {
				return resultGroupProjectionPlan{}, fmt.Errorf(
					"normalize result group feedback %q: %w",
					slot.call.CallID,
					err,
				)
			}
			feedbackRecord, err := sessionLocalEntryRecordFromRuntime(feedback)
			if err != nil {
				return resultGroupProjectionPlan{}, fmt.Errorf(
					"adapt result group feedback %q: %w",
					slot.call.CallID,
					err,
				)
			}
			feedbackIndex := len(plan.payloads)
			prepared.feedback = &feedback
			prepared.feedbackRecordIndex = &feedbackIndex
			plan.payloads = append(plan.payloads, feedbackRecord)
		}
		output := llm.Message{
			Role:        llm.RoleTool,
			Content:     textutil.Value(string(completion.Result.Output)),
			ToolCallID:  textutil.Value(completion.Result.CallID),
			Name:        textutil.Value(string(completion.Result.Name)),
			MessageType: llm.ToolOutputMessageType(slot.call.OutputKind == session.ToolOutputKindCustom),
		}
		output = normalizeMessageForTranscript(output, e.transcriptWorkingDir())
		output, err = normalizePersistedMessageWorktreeContext(output)
		if err != nil {
			return resultGroupProjectionPlan{}, err
		}
		if err := e.transcriptRuntimeState().ValidateMessage(stepID, output); err != nil {
			return resultGroupProjectionPlan{}, fmt.Errorf(
				"validate result group output projection %q: %w",
				slot.call.CallID,
				err,
			)
		}
		outputRecord, err := sessionMessageRecordFromLLM(output)
		if err != nil {
			return resultGroupProjectionPlan{}, fmt.Errorf(
				"adapt result group output %q: %w",
				slot.call.CallID,
				err,
			)
		}
		prepared.output = output
		prepared.outputRecordIndex = len(plan.payloads)
		plan.payloads = append(plan.payloads, outputRecord)
		plan.units = append(plan.units, prepared)
	}
	return plan, nil
}

func (e *Engine) applyResultGroupProjection(
	stepID string,
	plan resultGroupProjectionPlan,
	records []session.EventRecord,
) error {
	if len(records) != len(plan.payloads) {
		return fmt.Errorf(
			"committed result group returned %d records, want %d",
			len(records),
			len(plan.payloads),
		)
	}
	type projectedResultGroupUnit struct {
		unit                 resultGroupPreparedUnit
		completionProvenance TranscriptCommittedRowProvenance
		feedbackProvenance   *TranscriptCommittedRowProvenance
	}
	projected := make([]projectedResultGroupUnit, 0, len(plan.units))
	for _, unit := range plan.units {
		if _, live := e.transcriptRuntimeState().liveToolLedger().Lookup(
			unit.completion.Result.CallID,
		); !live {
			return fmt.Errorf(
				"project committed result group: live tool call %q is unavailable",
				unit.completion.Result.CallID,
			)
		}
	}
	for _, unit := range plan.units {
		completionProvenance, err := transcriptProvenanceFromRecord(
			records[unit.completionRecordIndex],
		)
		if err != nil {
			return err
		}
		outputProvenance, err := transcriptProvenanceFromRecord(
			records[unit.outputRecordIndex],
		)
		if err != nil {
			return err
		}
		var feedbackProvenance *TranscriptCommittedRowProvenance
		if unit.feedbackRecordIndex != nil {
			value, err := transcriptProvenanceFromRecord(
				records[*unit.feedbackRecordIndex],
			)
			if err != nil {
				return err
			}
			feedbackProvenance = &value
		}
		e.applyCommittedStoredToolCompletion(
			unit.storedCompletion,
			unit.backgroundSessionID,
			unit.hasBackgroundSession,
			&completionProvenance,
		)
		e.transcriptRuntimeState().CompleteLiveTool(unit.completion.Result.CallID)
		if unit.feedback != nil {
			entry := localEntryChatEntryForStep(*unit.feedback, stepID)
			e.transcriptRuntimeState().AppendLocalEntryRecord(
				*entry,
				unit.feedback.AfterToolCallID,
				feedbackProvenance,
			)
		}
		if mutation := tokenUsageMutationForMessage(unit.output); mutation == tokenUsageMutationSignificant {
			e.markCurrentRequestShapeDirtyForSignificantMutation()
		} else {
			e.markCurrentRequestShapeDirty()
		}
		if err := e.transcriptRuntimeState().AppendMessage(
			stepID,
			unit.output,
			&outputProvenance,
		); err != nil {
			return fmt.Errorf(
				"append result group output projection %q: %w",
				unit.completion.Result.CallID,
				err,
			)
		}
		projected = append(projected, projectedResultGroupUnit{
			unit:                 unit,
			completionProvenance: completionProvenance,
			feedbackProvenance:   feedbackProvenance,
		})
	}
	for _, projection := range projected {
		result := cloneToolResult(projection.unit.completion.Result)
		if err := e.emitRaw(Event{
			Kind:                       EventToolCallCompleted,
			StepID:                     stepID,
			ToolResult:                 &result,
			CommittedTranscriptChanged: true,
			CommittedProvenance:        &projection.completionProvenance,
		}); err != nil {
			return err
		}
		if projection.unit.feedback != nil {
			entry := localEntryChatEntryForStep(*projection.unit.feedback, stepID)
			if err := e.emitRaw(Event{
				Kind:                       EventLocalEntryAdded,
				StepID:                     stepID,
				LocalEntry:                 entry,
				CommittedTranscriptChanged: true,
				CommittedProvenance:        projection.feedbackProvenance,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}
