package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"core/shared/runtimeids"
	"core/shared/transcript"
)

type recordAppendOutcome struct {
	records       []EventRecord
	committed     bool
	endByteCursor *int64
}

type EventRecordAppendInput struct {
	StepID                    *string
	Payload                   EventRecordPayload
	committedAtUnixMs         *transcript.CommittedAtUnixMs
	preserveCommittedAt       bool
	generatedRecoveredWarning bool
}

func projectEventPayloadForVersion(version int, payload EventRecordPayload) (EventRecordPayload, error) {
	switch version {
	case EventLogVersionV2:
		return payload, nil
	case EventLogVersionV1:
		completion, ok := payload.(ToolCompletionRecord)
		if !ok || completion.QuestionAnswer == nil {
			return payload, nil
		}
		completion.QuestionAnswer = nil
		return completion, nil
	default:
		return nil, fmt.Errorf("unsupported event log version %d", version)
	}
}

type EventRecordAppendResult struct {
	Record EventRecord
	CommitReceipt
	EndByteCursor *int64
}

func (c MaterializedEventLog) AppendRecord(
	stepID *string,
	payload EventRecordPayload,
) (EventRecord, CommitReceipt, error) {
	outcome, err := c.appendRecordInputsAtomic([]EventRecordAppendInput{{
		StepID: stepID, Payload: payload,
	}})
	if len(outcome.records) != 1 {
		return EventRecord{}, CommitReceipt{Committed: outcome.committed}, errors.Join(
			err,
			fmt.Errorf(
				"typed event append produced %d records, want 1",
				len(outcome.records),
			),
		)
	}
	return outcome.records[0], CommitReceipt{Committed: outcome.committed}, err
}

func (c MaterializedEventLog) AppendRecordsAtomic(
	stepID *string,
	payloads []EventRecordPayload,
) ([]EventRecord, CommitReceipt, error) {
	inputs := make([]EventRecordAppendInput, len(payloads))
	for index, payload := range payloads {
		inputs[index] = EventRecordAppendInput{StepID: stepID, Payload: payload}
	}
	return c.AppendRecordBatchAtomic(inputs)
}

func (c MaterializedEventLog) AppendRecordBatchAtomic(inputs []EventRecordAppendInput) ([]EventRecord, CommitReceipt, error) {
	outcome, err := c.appendRecordInputsAtomic(inputs)
	return outcome.records, CommitReceipt{Committed: outcome.committed}, err
}

func (c MaterializedEventLog) AppendReplayRecords(
	records []EventRecord,
) ([]EventRecord, error) {
	outcome, err := c.appendReplayRecords(records, false)
	return outcome.records, err
}

func (c MaterializedEventLog) appendReplayRecordsWithEndByteCursor(
	records []EventRecord,
) (recordAppendOutcome, error) {
	return c.appendReplayRecords(records, true)
}

func (c MaterializedEventLog) appendReplayRecords(
	records []EventRecord,
	requireEndByteCursor bool,
) (recordAppendOutcome, error) {
	if requireEndByteCursor && c.store == nil {
		return recordAppendOutcome{}, errors.New(
			"materialized event log owning Store is required",
		)
	}
	inputs := make([]EventRecordAppendInput, len(records))
	for index, record := range records {
		payload, err := record.Payload()
		if err != nil {
			return recordAppendOutcome{}, fmt.Errorf(
				"read replay event record %d payload: %w",
				index,
				err,
			)
		}
		inputs[index] = EventRecordAppendInput{
			StepID:              record.StepID(),
			Payload:             payload,
			committedAtUnixMs:   record.CommittedAtUnixMs(),
			preserveCommittedAt: true,
		}
	}
	outcome, err := c.appendRecordInputsAtomic(inputs)
	if err == nil && requireEndByteCursor &&
		(outcome.endByteCursor == nil || *outcome.endByteCursor <= 0) {
		err = errors.New(
			"replayed typed records did not produce a positive event-log byte cursor",
		)
	}
	return outcome, err
}

func (c MaterializedEventLog) AppendCompactionHistoryReplacement(
	stepID *string,
	record HistoryReplacementRecord,
) (EventRecord, CommitReceipt, error) {
	outcome, err := c.appendRecordInputsAtomic([]EventRecordAppendInput{{
		StepID: stepID, Payload: record,
	}})
	if len(outcome.records) != 1 {
		return EventRecord{}, CommitReceipt{Committed: outcome.committed}, errors.Join(
			err,
			fmt.Errorf(
				"typed compaction append produced %d records, want 1",
				len(outcome.records),
			),
		)
	}
	return outcome.records[0], CommitReceipt{Committed: outcome.committed}, err
}

func (c MaterializedEventLog) AppendGeneratedRecoveredWarning(
	record LocalEntryRecord,
) (CommitReceipt, error) {
	if c.store != nil && c.store.Meta().GeneratedRecoveredWarningIssued {
		return CommitReceipt{Committed: true}, nil
	}
	outcome, err := c.appendRecordInputsAtomic([]EventRecordAppendInput{{
		Payload:                   record,
		generatedRecoveredWarning: true,
	}})
	receipt := CommitReceipt{Committed: outcome.committed}
	if err != nil {
		return receipt, err
	}
	switch len(outcome.records) {
	case 0:
		return CommitReceipt{Committed: true}, nil
	case 1:
		return receipt, nil
	default:
		return receipt, fmt.Errorf(
			"generated recovered warning append produced %d records, want at most 1",
			len(outcome.records),
		)
	}
}

func (c MaterializedEventLog) AppendRecordWithEndByteCursor(
	stepID *string,
	payload EventRecordPayload,
) (EventRecordAppendResult, error) {
	if c.store == nil {
		return EventRecordAppendResult{}, errors.New(
			"materialized event log owning Store is required",
		)
	}
	outcome, err := c.appendRecordInputsAtomic([]EventRecordAppendInput{{
		StepID: stepID, Payload: payload,
	}})
	result := EventRecordAppendResult{
		CommitReceipt: CommitReceipt{Committed: outcome.committed},
		EndByteCursor: outcome.endByteCursor,
	}
	if len(outcome.records) == 1 {
		result.Record = outcome.records[0]
	} else {
		err = errors.Join(err, fmt.Errorf(
			"typed cursor append produced %d records, want 1",
			len(outcome.records),
		))
	}
	if err == nil && (result.EndByteCursor == nil || *result.EndByteCursor <= 0) {
		err = errors.New(
			"committed typed event append did not produce a positive event-log byte cursor",
		)
	}
	return result, err
}

func (c MaterializedEventLog) appendRecordInputsAtomic(
	inputs []EventRecordAppendInput,
) (outcome recordAppendOutcome, resultErr error) {
	if c.store == nil {
		return recordAppendOutcome{}, errors.New(
			"materialized event log owning Store is required",
		)
	}
	if len(inputs) == 0 {
		return recordAppendOutcome{}, nil
	}
	s := c.store
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	lock, lockPath, err := acquireEventLogPersistenceLock(s.sessionDir)
	if err != nil {
		failure := &EventLogPersistenceError{
			Certainty: EventLogCommitNotCommitted,
			Cause:     fmt.Errorf("acquire event-log persistence lock: %w", err),
		}
		s.mu.Lock()
		s.eventLogFailure = failure
		s.mu.Unlock()
		return recordAppendOutcome{}, failure
	}
	defer joinEventLogPersistenceLockRelease(&resultErr, lock, lockPath)
	s.mu.Lock()
	log := s.materializedEventLog
	if s.eventLogFailure != nil {
		failure := s.eventLogFailure
		s.mu.Unlock()
		return recordAppendOutcome{}, failure
	}
	if c.log == nil || log != c.log {
		s.mu.Unlock()
		return recordAppendOutcome{}, errors.New(
			"event append requires materialized event-log capability",
		)
	}
	records := make([]EventRecord, 0, len(inputs))
	sequence := log.lastSequence
	appendNow := storeTimestamp(s.options)
	appendTimeUnixMs, err := transcript.NewCommittedAtUnixMs(appendNow.UnixMilli())
	if err != nil {
		s.mu.Unlock()
		return recordAppendOutcome{}, fmt.Errorf("store clock committed time: %w", err)
	}
	for index, input := range inputs {
		sequence++
		committedAtUnixMs := input.committedAtUnixMs
		payload, err := projectEventPayloadForVersion(log.version, input.Payload)
		if err != nil {
			s.mu.Unlock()
			return recordAppendOutcome{}, fmt.Errorf(
				"project event record %d for event-log v%d: %w",
				index,
				log.version,
				err,
			)
		}
		if !input.preserveCommittedAt {
			eligible, err := eventPayloadEligibleForCommittedTime(payload)
			if err != nil {
				s.mu.Unlock()
				return recordAppendOutcome{}, fmt.Errorf(
					"evaluate committed time eligibility for event record %d: %w",
					index,
					err,
				)
			}
			if eligible {
				committedAtUnixMs = &appendTimeUnixMs
			}
		}
		record, err := newEventRecord(
			sequence,
			input.StepID,
			payload,
			committedAtUnixMs,
		)
		if err != nil {
			s.mu.Unlock()
			return recordAppendOutcome{}, fmt.Errorf(
				"build typed event record %d: %w",
				index,
				err,
			)
		}
		records = append(records, record)
	}

	projection, err := s.appendProjectionFromRecordsLocked(records, inputs, appendNow)
	if err != nil {
		s.mu.Unlock()
		return recordAppendOutcome{records: records}, err
	}
	endOffset, err := log.appendRecords(records)
	if err != nil {
		var persistenceErr *EventLogPersistenceError
		if errors.As(err, &persistenceErr) {
			s.eventLogFailure = persistenceErr
		}
		s.mu.Unlock()
		return recordAppendOutcome{records: records}, err
	}
	endByteCursor := &endOffset
	s.applyCommittedAppendProjectionLocked(projection)
	projector := s.options.appendProjector
	s.mu.Unlock()
	if projector != nil {
		_ = projector(context.Background(), projection)
	}

	outcome = recordAppendOutcome{
		records:       records,
		committed:     true,
		endByteCursor: endByteCursor,
	}
	return outcome, nil
}

func (s *Store) appendProjectionFromRecordsLocked(
	records []EventRecord,
	inputs []EventRecordAppendInput,
	appendedAt time.Time,
) (AppendProjection, error) {
	sessionID, err := runtimeids.ParseSessionID(s.meta.SessionID)
	if err != nil {
		return AppendProjection{}, fmt.Errorf("parse append projection session id: %w", err)
	}
	projection := AppendProjection{
		SessionID:     sessionID,
		FirstSequence: records[0].Seq(),
		LastSequence:  records[len(records)-1].Seq(),
		AppendedAt:    appendedAt,
	}
	assignmentProjection := Meta{
		ActiveWorkflowAssignment:      cloneMessageRecord(s.meta.ActiveWorkflowAssignment),
		ActiveWorkflowAssignmentState: cloneActiveWorkflowAssignmentState(s.meta.ActiveWorkflowAssignmentState),
	}
	if err := advanceActiveWorkflowAssignmentFromRecords(&assignmentProjection, records); err != nil {
		return AppendProjection{}, err
	}
	projection.activeWorkflowAssignment = cloneMessageRecord(assignmentProjection.ActiveWorkflowAssignment)
	projection.activeWorkflowAssignmentState = cloneActiveWorkflowAssignmentState(assignmentProjection.ActiveWorkflowAssignmentState)
	for index, record := range records {
		payload, err := record.Payload()
		if err != nil {
			return AppendProjection{}, err
		}
		message, ok := payload.(MessageRecord)
		if ok && hasVisibleUserMessageFields(message.Role, message.MessageType, message.Content) {
			projection.ConversationEstablished = true
			if s.meta.FirstPromptPreview == "" && projection.FirstPromptPreview == nil {
				preview := normalizeFirstPromptPreview(*message.Content)
				if preview != "" {
					projection.FirstPromptPreview = &preview
				}
			}
		}
		if inputs[index].generatedRecoveredWarning {
			projection.GeneratedRecoveredWarningIssued = true
		}
	}
	return projection, nil
}

func (s *Store) applyCommittedAppendProjectionLocked(projection AppendProjection) {
	applyAppendProjectionToMeta(&s.meta, projection)
	if projection.ConversationEstablished {
		s.conversationFreshness = ConversationFreshnessEstablished
	}
}

func applyAppendProjectionToMeta(meta *Meta, projection AppendProjection) {
	if meta == nil {
		return
	}
	if projection.FirstPromptPreview != nil && meta.FirstPromptPreview == "" {
		meta.FirstPromptPreview = *projection.FirstPromptPreview
	}
	if projection.ConversationEstablished {
		meta.ConversationEstablished = true
	}
	if projection.GeneratedRecoveredWarningIssued {
		meta.GeneratedRecoveredWarningIssued = true
	}
	if projection.AppendedAt.After(meta.UpdatedAt) {
		meta.UpdatedAt = projection.AppendedAt
	}
	meta.ActiveWorkflowAssignment = cloneMessageRecord(projection.activeWorkflowAssignment)
	meta.ActiveWorkflowAssignmentState = cloneActiveWorkflowAssignmentState(projection.activeWorkflowAssignmentState)
}
