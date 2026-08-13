package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"core/shared/transcript"
)

type QuestionHistoryCursor struct {
	fp               *os.File
	version          int
	firstEventOffset int64
	position         int64
	initialSize      int64
	maxHandoffs      int
	historyWindows   int
	historyOmitted   bool
	done             bool
}

func OpenQuestionHistoryCursor(
	sessionDir string,
	maxHandoffs int,
) (_ *QuestionHistoryCursor, resultErr error) {
	if maxHandoffs < 1 {
		return nil, fmt.Errorf("maximum handoffs must be positive: %d", maxHandoffs)
	}
	path := filepath.Join(sessionDir, eventsFile)
	fp, err := openRegularSessionFile(path, "Question-history event log")
	if err != nil {
		return nil, fmt.Errorf("open Question-history event log: %w", err)
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, fp.Close())
		}
	}()
	info, err := fp.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat Question-history event log: %w", err)
	}
	if info.Size() == 0 {
		return nil, errors.New("Question-history event log is empty")
	}
	header, firstEventOffset, err := readCurrentEventLogHeader(fp)
	if err != nil {
		return nil, err
	}
	return &QuestionHistoryCursor{
		fp:               fp,
		version:          header.Version,
		firstEventOffset: firstEventOffset,
		position:         info.Size(),
		initialSize:      info.Size(),
		maxHandoffs:      maxHandoffs,
		historyWindows:   1,
	}, nil
}

func (c *QuestionHistoryCursor) Version() int {
	if c == nil {
		return 0
	}
	return c.version
}

func (c *QuestionHistoryCursor) InitialSize() int64 {
	if c == nil {
		return 0
	}
	return c.initialSize
}

func (c *QuestionHistoryCursor) HistoryOmitted() bool {
	return c != nil && c.historyOmitted
}

func (c *QuestionHistoryCursor) Close() error {
	if c == nil || c.fp == nil {
		return nil
	}
	err := c.fp.Close()
	c.fp = nil
	return err
}

func (c *QuestionHistoryCursor) Next() (*EventRecord, error) {
	if c == nil || c.fp == nil {
		return nil, errors.New("Question-history cursor is closed")
	}
	if c.done {
		return nil, nil
	}
	for c.position > c.firstEventOffset {
		recordOffset, lineEnd, terminated, err := previousCurrentEventLineRange(
			c.fp,
			c.position,
			c.firstEventOffset,
		)
		if err != nil {
			return nil, err
		}
		c.position = recordOffset
		if lineEnd == recordOffset {
			if !terminated {
				continue
			}
			return nil, fmt.Errorf(
				"Question-history event log contains an empty event line at byte %d",
				recordOffset,
			)
		}
		if !terminated {
			// A concurrent append may leave a torn newest line. The opened
			// cursor's contract permits ignoring that unfinished tail.
			continue
		}
		sequence, kind, questionCandidate, err := inspectQuestionHistoryEventEnvelope(
			c.fp,
			recordOffset,
			lineEnd,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"decode Question-history event record at byte %d: %w",
				recordOffset,
				err,
			)
		}
		if kind == EventKindHistoryReplace {
			if c.historyWindows == c.maxHandoffs {
				c.historyOmitted = recordOffset > c.firstEventOffset
				c.done = true
				return nil, nil
			}
			c.historyWindows++
			continue
		}
		if kind != EventKindToolCompletion || !questionCandidate {
			continue
		}
		record, err := decodeQuestionHistoryToolCompletionRecord(
			c.fp,
			recordOffset,
			lineEnd,
			c.version,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"decode Question-history event record at byte %d: %w",
				recordOffset,
				err,
			)
		}
		if record.Seq() != sequence {
			return nil, fmt.Errorf(
				"Question-history event sequence changed during decode at byte %d: inspected %d decoded %d",
				recordOffset,
				sequence,
				record.Seq(),
			)
		}
		return &record, nil
	}
	c.done = true
	return nil, nil
}

func previousCurrentEventLineRange(
	fp *os.File,
	endOffset int64,
	firstEventOffset int64,
) (startOffset int64, lineEnd int64, terminated bool, err error) {
	if endOffset <= firstEventOffset {
		return firstEventOffset, firstEventOffset, false, nil
	}
	lineEnd = endOffset
	lastByte := [1]byte{}
	if _, err := fp.ReadAt(lastByte[:], endOffset-1); err != nil {
		return 0, 0, false, fmt.Errorf("read current event line end: %w", err)
	}
	if lastByte[0] == '\n' {
		lineEnd--
		terminated = true
	}
	if lineEnd <= firstEventOffset {
		return firstEventOffset, firstEventOffset, terminated, nil
	}
	previousNewline, err := lastNewlineOffset(fp, lineEnd)
	if err != nil {
		return 0, 0, false, err
	}
	startOffset = previousNewline + 1
	if startOffset < firstEventOffset {
		startOffset = firstEventOffset
	}
	return startOffset, lineEnd, terminated, nil
}

func inspectQuestionHistoryEventEnvelope(
	fp *os.File,
	startOffset int64,
	endOffset int64,
) (int64, EventKind, bool, error) {
	decoder := json.NewDecoder(io.NewSectionReader(fp, startOffset, endOffset-startOffset))
	start, err := decoder.Token()
	if err != nil {
		return 0, "", false, err
	}
	if delimiter, ok := start.(json.Delim); !ok || delimiter != '{' {
		return 0, "", false, errors.New("event record must be a JSON object")
	}
	field, err := decoder.Token()
	if err != nil {
		return 0, "", false, err
	}
	if field != "seq" {
		return 0, "", false, fmt.Errorf("event record first field is %q, want seq", field)
	}
	var sequence int64
	if err := decoder.Decode(&sequence); err != nil {
		return 0, "", false, fmt.Errorf("decode event sequence: %w", err)
	}
	if sequence <= 0 {
		return 0, "", false, fmt.Errorf("event sequence must be positive: %d", sequence)
	}
	field, err = decoder.Token()
	if err != nil {
		return 0, "", false, err
	}
	if field != "kind" {
		return 0, "", false, fmt.Errorf("event record second field is %q, want kind", field)
	}
	var kind EventKind
	if err := decoder.Decode(&kind); err != nil {
		return 0, "", false, fmt.Errorf("decode event kind: %w", err)
	}
	switch kind {
	case EventKindMessage,
		EventKindLocalEntry,
		EventKindCacheRequest,
		EventKindCacheResponse,
		EventKindCacheWarning,
		EventKindReviewerFeedback,
		EventKindReviewerError:
		if err := validateQuestionHistoryIgnoredEnvelope(decoder); err != nil {
			return 0, "", false, err
		}
		return sequence, kind, false, nil
	case EventKindHistoryReplace:
		// Replacement payload items are explicitly irrelevant to Question
		// history. Stream through the envelope to validate its JSON without
		// materializing the payload.
		if err := validateQuestionHistoryIgnoredEnvelope(decoder); err != nil {
			return 0, "", false, err
		}
		return sequence, kind, false, nil
	case EventKindToolCompletion:
		candidate, err := inspectQuestionHistoryToolCompletion(decoder)
		return sequence, kind, candidate, err
	default:
		return 0, "", false, fmt.Errorf("unsupported event kind %q", kind)
	}
}

func validateQuestionHistoryIgnoredEnvelope(decoder *json.Decoder) error {
	for decoder.More() {
		field, err := decoder.Token()
		if err != nil {
			return err
		}
		if _, ok := field.(string); !ok {
			return errors.New("event record field name must be a string")
		}
		if err := discardQuestionHistoryJSONValue(decoder); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return fmt.Errorf("unexpected trailing JSON token %v", token)
	}
	return nil
}

func inspectQuestionHistoryToolCompletion(decoder *json.Decoder) (bool, error) {
	for decoder.More() {
		field, err := decoder.Token()
		if err != nil {
			return false, err
		}
		name, ok := field.(string)
		if !ok {
			return false, errors.New("event record field name must be a string")
		}
		if name != "payload" {
			if err := discardQuestionHistoryJSONValue(decoder); err != nil {
				return false, fmt.Errorf("decode event field %q: %w", name, err)
			}
			continue
		}
		start, err := decoder.Token()
		if err != nil {
			return false, err
		}
		if delimiter, ok := start.(json.Delim); !ok || delimiter != '{' {
			return false, errors.New("tool completion payload must be a JSON object")
		}
		var toolName *string
		var isError *bool
		for decoder.More() {
			field, err := decoder.Token()
			if err != nil {
				return false, err
			}
			payloadField, ok := field.(string)
			if !ok {
				return false, errors.New("tool completion field name must be a string")
			}
			switch payloadField {
			case "name":
				var value string
				if err := decoder.Decode(&value); err != nil {
					return false, fmt.Errorf("decode tool completion name: %w", err)
				}
				toolName = &value
			case "is_error":
				var value bool
				if err := decoder.Decode(&value); err != nil {
					return false, fmt.Errorf("decode tool completion error fact: %w", err)
				}
				isError = &value
			default:
				if err := discardQuestionHistoryJSONValue(decoder); err != nil {
					return false, fmt.Errorf("decode tool completion field %q: %w", payloadField, err)
				}
			}
			if toolName != nil && *toolName != askQuestionToolName {
				return false, nil
			}
			if toolName != nil && isError != nil {
				return *toolName == askQuestionToolName && !*isError, nil
			}
		}
		return false, errors.New("tool completion name and is_error are required")
	}
	return false, errors.New("tool completion payload is required")
}

func decodeQuestionHistoryToolCompletionRecord(
	fp *os.File,
	startOffset int64,
	endOffset int64,
	version int,
) (EventRecord, error) {
	decoder := json.NewDecoder(io.NewSectionReader(fp, startOffset, endOffset-startOffset))
	start, err := decoder.Token()
	if err != nil {
		return EventRecord{}, err
	}
	if delimiter, ok := start.(json.Delim); !ok || delimiter != '{' {
		return EventRecord{}, errors.New("event record must be a JSON object")
	}
	var sequence *int64
	var kind *EventKind
	var stepID *string
	var committedAt *transcript.CommittedAtUnixMs
	var completion *ToolCompletionRecord
	for decoder.More() {
		field, err := decoder.Token()
		if err != nil {
			return EventRecord{}, err
		}
		name, ok := field.(string)
		if !ok {
			return EventRecord{}, errors.New("event record field name must be a string")
		}
		switch name {
		case "seq":
			var value int64
			err = decoder.Decode(&value)
			sequence = &value
		case "kind":
			var value EventKind
			err = decoder.Decode(&value)
			kind = &value
		case "step_id":
			var value string
			err = decoder.Decode(&value)
			stepID = &value
		case "committed_at_unix_ms":
			var value transcript.CommittedAtUnixMs
			err = decoder.Decode(&value)
			committedAt = &value
		case "payload":
			var value ToolCompletionRecord
			value, err = decodeQuestionHistoryToolCompletionPayload(decoder, version)
			completion = &value
		default:
			err = discardQuestionHistoryJSONValue(decoder)
		}
		if err != nil {
			return EventRecord{}, fmt.Errorf("decode event field %q: %w", name, err)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return EventRecord{}, err
	}
	if sequence == nil || kind == nil || completion == nil {
		return EventRecord{}, errors.New("tool completion event sequence, kind, and payload are required")
	}
	if *kind != EventKindToolCompletion {
		return EventRecord{}, fmt.Errorf("event kind is %q, want %q", *kind, EventKindToolCompletion)
	}
	record, err := newEventRecord(*sequence, stepID, *completion, committedAt)
	if err != nil {
		return EventRecord{}, err
	}
	if version == EventLogVersionV2 {
		if err := validateEventRecordV2(record); err != nil {
			return EventRecord{}, fmt.Errorf(
				"event sequence %d kind %q: %w",
				record.Seq(),
				*kind,
				err,
			)
		}
	}
	return record, nil
}

func decodeQuestionHistoryToolCompletionPayload(
	decoder *json.Decoder,
	version int,
) (ToolCompletionRecord, error) {
	start, err := decoder.Token()
	if err != nil {
		return ToolCompletionRecord{}, err
	}
	if delimiter, ok := start.(json.Delim); !ok || delimiter != '{' {
		return ToolCompletionRecord{}, errors.New("tool completion payload must be a JSON object")
	}
	var completion ToolCompletionRecord
	var isError *bool
	for decoder.More() {
		field, err := decoder.Token()
		if err != nil {
			return ToolCompletionRecord{}, err
		}
		name, ok := field.(string)
		if !ok {
			return ToolCompletionRecord{}, errors.New("tool completion field name must be a string")
		}
		switch name {
		case "call_id":
			err = decoder.Decode(&completion.CallID)
		case "name":
			err = decoder.Decode(&completion.Name)
		case "output_kind":
			err = decoder.Decode(&completion.OutputKind)
		case "is_error":
			var value bool
			err = decoder.Decode(&value)
			isError = &value
		case "output":
			err = decoder.Decode(&completion.Output)
		case "summary":
			var value string
			err = decoder.Decode(&value)
			completion.Summary = &value
		case "condensed_text":
			var value string
			err = decoder.Decode(&value)
			completion.CondensedText = &value
		case "presentation":
			err = decoder.Decode(&completion.Presentation)
		case "provider_items":
			err = decoder.Decode(&completion.ProviderItems)
		case "question_answer":
			if version == EventLogVersionV2 {
				var value QuestionAnswerRecord
				err = decoder.Decode(&value)
				completion.QuestionAnswer = &value
			} else {
				err = discardQuestionHistoryJSONValue(decoder)
			}
		default:
			err = discardQuestionHistoryJSONValue(decoder)
		}
		if err != nil {
			return ToolCompletionRecord{}, fmt.Errorf(
				"decode tool completion field %q: %w",
				name,
				err,
			)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return ToolCompletionRecord{}, err
	}
	if isError == nil {
		return ToolCompletionRecord{}, errors.New("tool completion is_error is required")
	}
	completion.IsError = *isError
	return completion, nil
}

func discardQuestionHistoryJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		for decoder.More() {
			if _, err := decoder.Token(); err != nil {
				return err
			}
			if err := discardQuestionHistoryJSONValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := discardQuestionHistoryJSONValue(decoder); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	_, err = decoder.Token()
	return err
}
