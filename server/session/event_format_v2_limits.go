package session

import (
	"bytes"
	"fmt"

	"github.com/go-faster/jx"
)

func validateEventRecordV2FieldNames(line []byte) error {
	inspectionReader := &eventRecordInspectionReader{
		reader: bytes.NewReader(line),
	}
	decoder := jx.Decode(inspectionReader, int(eventLogScanChunkSize))
	return inspectEventRecordObject(decoder, inspectionReader, func(
		decoder *jx.Decoder,
		field string,
	) error {
		if field != "payload" || decoder.Next() != jx.Object {
			return decoder.Skip()
		}
		if err := inspectEventRecordObject(
			decoder,
			inspectionReader,
			func(decoder *jx.Decoder, _ string) error {
				return decoder.Skip()
			},
		); err != nil {
			return fmt.Errorf("event payload: %w", err)
		}
		return nil
	})
}

func validateEventRecordV2ToolName(name string) error {
	if len(name) > eventRecordDiscriminatorMaxBytes {
		return fmt.Errorf(
			"tool name exceeds %d UTF-8 bytes",
			eventRecordDiscriminatorMaxBytes,
		)
	}
	return nil
}

func validateEventRecordV2MessageToolNames(message MessageRecord) error {
	for index, call := range message.ToolCalls {
		if err := validateEventRecordV2ToolName(call.Name); err != nil {
			return fmt.Errorf("message tool call %d: %w", index, err)
		}
	}
	return nil
}
