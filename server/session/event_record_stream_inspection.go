package session

import (
	"errors"
	"fmt"
	"io"

	"github.com/go-faster/jx"
)

type eventRecordStreamInspection struct {
	Sequence          int64
	Kind              EventKind
	QuestionCandidate bool
}

const eventRecordInspectionTokenMaxBytes = eventRecordDiscriminatorMaxBytes * 8

var errEventRecordInspectionTokenTooLarge = errors.New(
	"event record inspected JSON token exceeds fixed size limit",
)

type eventRecordInspectionReader struct {
	reader    io.Reader
	remaining int64
	bounded   bool
}

func (r *eventRecordInspectionReader) Read(buffer []byte) (int, error) {
	if !r.bounded {
		return r.reader.Read(buffer)
	}
	if r.remaining <= 0 {
		return 0, errEventRecordInspectionTokenTooLarge
	}
	if int64(len(buffer)) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	read, err := r.reader.Read(buffer)
	r.remaining -= int64(read)
	return read, err
}

func (r *eventRecordInspectionReader) boundNextToken() {
	r.remaining = eventRecordInspectionTokenMaxBytes
	r.bounded = true
}

func (r *eventRecordInspectionReader) allowStreamingValue() {
	r.bounded = false
}

func inspectEventRecordStream(
	reader io.Reader,
	version int,
) (eventRecordStreamInspection, error) {
	if version != EventLogVersionV1 && version != EventLogVersionV2 {
		return eventRecordStreamInspection{}, fmt.Errorf(
			"unsupported event log version %d",
			version,
		)
	}
	inspectionReader := &eventRecordInspectionReader{reader: reader}
	decoder := jx.Decode(inspectionReader, int(eventLogScanChunkSize))
	var inspection eventRecordStreamInspection
	var sequencePresent bool
	var kindPresent bool
	var toolName *string
	var toolIsError *bool
	var questionAnswerPresent bool
	if err := inspectEventRecordObject(decoder, inspectionReader, func(
		decoder *jx.Decoder,
		field string,
	) error {
		switch field {
		case "seq":
			value, err := decoder.Int64()
			if err != nil {
				return fmt.Errorf("decode event sequence: %w", err)
			}
			inspection.Sequence = value
			sequencePresent = true
		case "kind":
			value, err := inspectEventRecordString(decoder, inspectionReader)
			if err != nil {
				return fmt.Errorf("decode event kind: %w", err)
			}
			inspection.Kind = EventKind(value)
			kindPresent = true
		case "payload":
			name, isError, answerPresent, err := inspectEventToolCompletionPayload(
				decoder,
				inspectionReader,
			)
			if err != nil {
				return fmt.Errorf("decode event payload: %w", err)
			}
			toolName = name
			toolIsError = isError
			questionAnswerPresent = answerPresent
		default:
			return decoder.Skip()
		}
		return nil
	}); err != nil {
		return eventRecordStreamInspection{}, err
	}
	if err := inspectEventRecordEOF(decoder); err != nil {
		return eventRecordStreamInspection{}, err
	}
	if !sequencePresent || inspection.Sequence <= 0 {
		return eventRecordStreamInspection{}, fmt.Errorf(
			"event sequence must be positive: %d",
			inspection.Sequence,
		)
	}
	if !kindPresent {
		return eventRecordStreamInspection{}, errors.New("event kind is required")
	}
	if err := validateEventKind(inspection.Kind); err != nil {
		return eventRecordStreamInspection{}, err
	}
	if inspection.Kind != EventKindToolCompletion {
		return inspection, nil
	}
	if toolName == nil || toolIsError == nil {
		return eventRecordStreamInspection{}, errors.New(
			"tool completion name and is_error are required",
		)
	}
	inspection.QuestionCandidate = *toolName == askQuestionToolName && !*toolIsError
	if version == EventLogVersionV2 {
		if err := validateV2QuestionAnswerPlacement(
			*toolName,
			*toolIsError,
			questionAnswerPresent,
		); err != nil {
			return eventRecordStreamInspection{}, err
		}
	}
	return inspection, nil
}

func inspectHistoryReplacementRecordStream(reader io.Reader) error {
	inspectionReader := &eventRecordInspectionReader{reader: reader}
	decoder := jx.Decode(inspectionReader, int(eventLogScanChunkSize))
	var payloadPresent bool
	replacement := HistoryReplacementRecord{}
	if err := inspectEventRecordObject(decoder, inspectionReader, func(
		decoder *jx.Decoder,
		field string,
	) error {
		if field != "payload" {
			return decoder.Skip()
		}
		if payloadPresent {
			return errors.New("history replacement payload must not be repeated")
		}
		payloadPresent = true
		if decoder.Next() != jx.Object {
			if err := decoder.Skip(); err != nil {
				return err
			}
			return errors.New("history replacement payload must be a JSON object")
		}
		return inspectEventRecordObject(decoder, inspectionReader, func(
			decoder *jx.Decoder,
			field string,
		) error {
			switch field {
			case "engine":
				value, err := inspectEventRecordString(decoder, inspectionReader)
				if err != nil {
					return err
				}
				replacement.Engine = value
			case "mode":
				value, err := inspectEventRecordString(decoder, inspectionReader)
				if err != nil {
					return err
				}
				replacement.Mode = CompactionMode(value)
			case "compaction_number":
				value, err := inspectOptionalEventRecordInt(decoder)
				if err != nil {
					return err
				}
				replacement.CompactionNumber = value
			case "committed_entry_start":
				value, err := inspectOptionalEventRecordInt(decoder)
				if err != nil {
					return err
				}
				replacement.CommittedEntryStart = value
			case "pending_handoff_future_message",
				"last_committed_assistant_final_answer":
				return inspectOptionalEventRecordType(decoder, jx.String, field)
			case "latest_rollback_candidate":
				return inspectOptionalEventRecordType(decoder, jx.Object, field)
			case "items":
				return inspectOptionalEventRecordType(decoder, jx.Array, field)
			default:
				return decoder.Skip()
			}
			return nil
		})
	}); err != nil {
		return err
	}
	if err := inspectEventRecordEOF(decoder); err != nil {
		return err
	}
	if !payloadPresent {
		return errors.New("history replacement payload is required")
	}
	if _, err := normalizeHistoryReplacementRecord(replacement); err != nil {
		return fmt.Errorf("validate history replacement payload: %w", err)
	}
	return nil
}

func inspectOptionalEventRecordInt(decoder *jx.Decoder) (*int, error) {
	if decoder.Next() == jx.Null {
		if err := decoder.Skip(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	value, err := decoder.Int()
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func inspectOptionalEventRecordType(
	decoder *jx.Decoder,
	expected jx.Type,
	field string,
) error {
	actual := decoder.Next()
	if actual != jx.Null && actual != expected {
		if err := decoder.Skip(); err != nil {
			return err
		}
		return fmt.Errorf(
			"history replacement field %q must be %s or null",
			field,
			expected,
		)
	}
	return decoder.Skip()
}

func inspectEventRecordEOF(decoder *jx.Decoder) error {
	if err := decoder.Skip(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func inspectEventToolCompletionPayload(
	decoder *jx.Decoder,
	inspectionReader *eventRecordInspectionReader,
) (name *string, isError *bool, questionAnswerPresent bool, resultErr error) {
	if decoder.Next() != jx.Object {
		if err := decoder.Skip(); err != nil {
			return nil, nil, false, err
		}
		return nil, nil, false, errors.New(
			"tool completion payload must be a JSON object",
		)
	}
	err := inspectEventRecordObject(decoder, inspectionReader, func(
		decoder *jx.Decoder,
		field string,
	) error {
		switch field {
		case "name":
			value, err := inspectEventRecordString(decoder, inspectionReader)
			if err != nil {
				return err
			}
			name = &value
		case "is_error":
			value, err := decoder.Bool()
			if err != nil {
				return err
			}
			isError = &value
		case "question_answer":
			questionAnswerPresent = decoder.Next() != jx.Null
			return decoder.Skip()
		default:
			return decoder.Skip()
		}
		return nil
	})
	return name, isError, questionAnswerPresent, err
}

func inspectEventRecordObject(
	decoder *jx.Decoder,
	inspectionReader *eventRecordInspectionReader,
	inspect func(decoder *jx.Decoder, field string) error,
) error {
	inspectionReader.boundNextToken()
	defer inspectionReader.allowStreamingValue()
	return decoder.Obj(func(decoder *jx.Decoder, field string) error {
		inspectionReader.allowStreamingValue()
		defer inspectionReader.boundNextToken()
		if err := inspectEventRecordFieldName(field); err != nil {
			return err
		}
		return inspect(decoder, field)
	})
}

func inspectEventRecordString(
	decoder *jx.Decoder,
	inspectionReader *eventRecordInspectionReader,
) (string, error) {
	inspectionReader.boundNextToken()
	defer inspectionReader.allowStreamingValue()
	value, err := decoder.Str()
	if err != nil {
		return "", err
	}
	if len(value) > eventRecordDiscriminatorMaxBytes {
		return "", fmt.Errorf(
			"event record discriminator exceeds %d UTF-8 bytes",
			eventRecordDiscriminatorMaxBytes,
		)
	}
	return value, nil
}

func inspectEventRecordFieldName(field string) error {
	if len(field) > eventRecordDiscriminatorMaxBytes {
		return fmt.Errorf(
			"event record field name exceeds %d UTF-8 bytes",
			eventRecordDiscriminatorMaxBytes,
		)
	}
	return nil
}
