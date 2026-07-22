package session

import (
	"errors"
	"fmt"
	"io"
	"os"
)

type recordAppendOutcome struct {
	records       []EventRecord
	committed     bool
	endByteCursor *int64
}

type recordAppendInput struct {
	stepID  *string
	payload EventRecordPayload
}

type recordMetadataTransition func(*Meta) (bool, error)

type EventRecordAppendResult struct {
	Record EventRecord
	CommitReceipt
	EndByteCursor *int64
}

func (c MaterializedEventLog) AppendRecord(
	stepID *string,
	payload EventRecordPayload,
) (EventRecord, CommitReceipt, error) {
	outcome, err := c.appendRecordInputsAtomic([]recordAppendInput{{
		stepID: stepID, payload: payload,
	}}, nil)
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
	inputs := make([]recordAppendInput, len(payloads))
	for index, payload := range payloads {
		inputs[index] = recordAppendInput{stepID: stepID, payload: payload}
	}
	outcome, err := c.appendRecordInputsAtomic(inputs, nil)
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
	inputs := make([]recordAppendInput, len(records))
	for index, record := range records {
		payload, err := record.Payload()
		if err != nil {
			return recordAppendOutcome{}, fmt.Errorf(
				"read replay event record %d payload: %w",
				index,
				err,
			)
		}
		inputs[index] = recordAppendInput{
			stepID:  record.StepID(),
			payload: payload,
		}
	}
	outcome, err := c.appendRecordInputsAtomic(inputs, nil)
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
	outcome, err := c.appendRecordInputsAtomic([]recordAppendInput{{
		stepID: stepID, payload: record,
	}}, func(meta *Meta) (bool, error) {
		meta.UsageState = nil
		return true, nil
	})
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
	outcome, err := c.appendRecordInputsAtomic([]recordAppendInput{{
		payload: record,
	}}, func(meta *Meta) (bool, error) {
		if meta.GeneratedRecoveredWarningIssued {
			return false, nil
		}
		meta.GeneratedRecoveredWarningIssued = true
		return true, nil
	})
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
	outcome, err := c.appendRecordInputsAtomic([]recordAppendInput{{
		stepID: stepID, payload: payload,
	}}, nil)
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
	inputs []recordAppendInput,
	transition recordMetadataTransition,
) (recordAppendOutcome, error) {
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
	s.mu.Lock()
	log := s.materializedEventLog
	if c.log == nil || log != c.log {
		s.mu.Unlock()
		return recordAppendOutcome{}, errors.New(
			"event append requires materialized event-log capability",
		)
	}
	if log.lastSequence != s.meta.LastSequence {
		s.mu.Unlock()
		return recordAppendOutcome{}, fmt.Errorf(
			"materialized event-log revision %d does not match metadata revision %d",
			log.lastSequence,
			s.meta.LastSequence,
		)
	}
	records := make([]EventRecord, 0, len(inputs))
	sequence := log.lastSequence
	for index, input := range inputs {
		sequence++
		record, err := NewEventRecord(sequence, input.stepID, input.payload)
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

	previousMeta := cloneMeta(s.meta)
	previousFreshness := s.conversationFreshness
	if err := s.captureFirstPromptPreviewFromRecordsLocked(records); err != nil {
		s.mu.Unlock()
		return recordAppendOutcome{records: records}, err
	}
	if err := s.advanceConversationFreshnessFromRecordsLocked(records); err != nil {
		s.meta = previousMeta
		s.mu.Unlock()
		return recordAppendOutcome{records: records}, err
	}
	if transition != nil {
		applied, err := transition(&s.meta)
		if err != nil {
			s.meta = previousMeta
			s.conversationFreshness = previousFreshness
			s.mu.Unlock()
			return recordAppendOutcome{records: records}, err
		}
		if !applied {
			s.meta = previousMeta
			s.conversationFreshness = previousFreshness
			s.mu.Unlock()
			return recordAppendOutcome{}, nil
		}
	}
	if err := s.requireMetadataPersistenceLocked(); err != nil {
		s.meta = previousMeta
		s.conversationFreshness = previousFreshness
		s.mu.Unlock()
		return recordAppendOutcome{records: records}, err
	}

	postMeta := cloneMeta(s.meta)
	postMeta.LastSequence = records[len(records)-1].Seq()
	postMeta.UpdatedAt = s.options.now()
	endOffset, err := s.appendCurrentRecordsLocked(log, records, previousMeta, postMeta)
	if err != nil {
		s.meta = previousMeta
		s.conversationFreshness = previousFreshness
		s.mu.Unlock()
		return recordAppendOutcome{records: records}, err
	}
	endByteCursor := &endOffset
	s.meta = postMeta
	observation, err := s.persistMetaLocked()
	if err != nil {
		s.mu.Unlock()
		return recordAppendOutcome{
			records:       records,
			committed:     true,
			endByteCursor: endByteCursor,
		}, err
	}
	s.mu.Unlock()

	outcome := recordAppendOutcome{
		records:       records,
		committed:     true,
		endByteCursor: endByteCursor,
	}
	return outcome, s.observePersistence(observation)
}

func (s *Store) appendCurrentRecordsLocked(log *currentEventLog, records []EventRecord, preMeta Meta, postMeta Meta) (int64, error) {
	var recovery appendRecoveryRecord
	return log.appendRecordsWithTransaction(records, &currentEventLogAppendTransaction{
		prepare: func(startOffset int64, payload []byte) error {
			record, err := s.newAppendRecoveryRecord(preMeta, postMeta, appendRecoveryPrepared, &appendRecoveryEvents{
				StartOffset: startOffset, EndOffset: startOffset + int64(len(payload)),
				EventCount: len(records), FirstSequence: records[0].Seq(),
				LastSequence: records[len(records)-1].Seq(), SHA256: digestBytes(payload),
			})
			if err != nil {
				return err
			}
			recovery = record
			return s.writeAppendRecoveryRecord(recovery)
		},
		commit: func() error {
			recovery.Phase = appendRecoveryCommitted
			return s.writeAppendRecoveryRecord(recovery)
		},
		rollback: s.rollbackPreparedCurrentEventAppend,
	})
}

func (s *Store) rollbackPreparedCurrentEventAppend(fp *os.File, startOffset int64, appendErr error) error {
	rollbackErr := fp.Truncate(startOffset)
	if rollbackErr == nil {
		rollbackErr = fp.Sync()
	}
	if rollbackErr == nil {
		_, rollbackErr = fp.Seek(0, io.SeekEnd)
	}
	if rollbackErr == nil {
		rollbackErr = s.clearAppendRecoveryRecord()
	}
	if rollbackErr != nil {
		return s.closeMutationAuthorityLocked("rollback failed current event append", errors.Join(appendErr, rollbackErr))
	}
	return appendErr
}

func (s *Store) captureFirstPromptPreviewFromRecordsLocked(records []EventRecord) error {
	if s.meta.FirstPromptPreview != "" {
		return nil
	}
	for _, record := range records {
		payload, err := record.Payload()
		if err != nil {
			return err
		}
		message, ok := payload.(MessageRecord)
		if !ok || !hasVisibleUserMessageFields(
			message.Role,
			message.MessageType,
			message.Content,
		) {
			continue
		}
		preview := normalizeFirstPromptPreview(*message.Content)
		if preview != "" {
			s.meta.FirstPromptPreview = preview
			return nil
		}
	}
	return nil
}

func (s *Store) advanceConversationFreshnessFromRecordsLocked(records []EventRecord) error {
	if s.conversationFreshness == ConversationFreshnessEstablished {
		return nil
	}
	for _, record := range records {
		visible, err := hasVisibleUserMessageRecord(record)
		if err != nil {
			return err
		}
		if visible {
			s.conversationFreshness = ConversationFreshnessEstablished
			s.meta.ConversationEstablished = true
			return nil
		}
	}
	return nil
}
