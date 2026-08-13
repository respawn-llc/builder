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
	decoder := jx.Decode(reader, int(eventLogScanChunkSize))
	var inspection eventRecordStreamInspection
	var sequencePresent bool
	var kindPresent bool
	var toolName *string
	var toolIsError *bool
	var questionAnswerPresent bool
	if err := decoder.Obj(func(decoder *jx.Decoder, field string) error {
		switch field {
		case "seq":
			value, err := decoder.Int64()
			if err != nil {
				return fmt.Errorf("decode event sequence: %w", err)
			}
			inspection.Sequence = value
			sequencePresent = true
		case "kind":
			value, err := decoder.Str()
			if err != nil {
				return fmt.Errorf("decode event kind: %w", err)
			}
			inspection.Kind = EventKind(value)
			kindPresent = true
		case "payload":
			name, isError, answerPresent, err := inspectEventToolCompletionPayload(decoder)
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
	if err := decoder.Skip(); !errors.Is(err, io.EOF) {
		if err == nil {
			return eventRecordStreamInspection{}, errors.New(
				"unexpected trailing JSON value",
			)
		}
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

func inspectEventToolCompletionPayload(
	decoder *jx.Decoder,
) (name *string, isError *bool, questionAnswerPresent bool, resultErr error) {
	if decoder.Next() != jx.Object {
		if err := decoder.Skip(); err != nil {
			return nil, nil, false, err
		}
		return nil, nil, false, errors.New(
			"tool completion payload must be a JSON object",
		)
	}
	err := decoder.Obj(func(decoder *jx.Decoder, field string) error {
		switch field {
		case "name":
			value, err := decoder.Str()
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
