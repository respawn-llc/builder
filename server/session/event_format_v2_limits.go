package session

import (
	"encoding/json"
	"fmt"
)

func validateEventRecordV2FieldNames(line []byte) error {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(line, &envelope); err != nil {
		return fmt.Errorf("decode event record field names: %w", err)
	}
	for field, value := range envelope {
		if err := validateEventRecordV2FieldName(field); err != nil {
			return err
		}
		if field != "payload" {
			continue
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(value, &payload); err != nil {
			continue
		}
		for field := range payload {
			if err := validateEventRecordV2FieldName(field); err != nil {
				return fmt.Errorf("event payload: %w", err)
			}
		}
	}
	return nil
}
func validateEventRecordV2FieldName(field string) error {
	if len(field) > eventRecordDiscriminatorMaxBytes {
		return fmt.Errorf(
			"field name exceeds %d UTF-8 bytes",
			eventRecordDiscriminatorMaxBytes,
		)
	}
	return nil
}
