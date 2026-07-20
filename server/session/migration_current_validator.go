package session

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

var currentEventEnvelopeFields = migrationKnownFieldSet{
	"seq",
	"kind",
	"step_id",
	"payload",
}

func validateCurrentEventLogComplete(
	path string,
	ledger *migrationResourceLedger,
) (lastSequence int64, resultErr error) {
	return walkCurrentEventLogComplete(path, ledger, nil)
}

func walkCurrentEventLogComplete(
	path string,
	ledger *migrationResourceLedger,
	visit func(EventRecord) error,
) (lastSequence int64, resultErr error) {
	if ledger == nil {
		return 0, fmt.Errorf("migration resource ledger is required")
	}
	source, err := openRegularSessionFile(path, "staged current event log")
	if err != nil {
		return 0, err
	}
	defer func() {
		if closeErr := source.Close(); closeErr != nil {
			resultErr = errors.Join(
				resultErr,
				fmt.Errorf("close staged current event log: %w", closeErr),
			)
		}
	}()
	_, firstEventOffset, err := readCurrentEventLogHeader(source)
	if err != nil {
		return 0, err
	}
	info, err := source.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat staged current event log: %w", err)
	}
	size := info.Size()
	previousSequence := int64(0)
	hasPrevious := false
	for offset := firstEventOffset; offset < size; {
		lineRange, nextOffset, terminated, err := nextLegacyEventLineRange(
			source,
			offset,
			size,
			ledger,
		)
		if err != nil {
			return 0, err
		}
		if !terminated {
			return 0, fmt.Errorf(
				"staged current event record at byte %d is not newline terminated",
				offset,
			)
		}
		if lineRange.Size() == 0 {
			return 0, fmt.Errorf(
				"staged current event log contains an empty record at byte %d",
				offset,
			)
		}
		var sequence int64
		var record EventRecord
		if visit == nil {
			sequence, err = validateCurrentEventRecordForMigration(
				source,
				lineRange,
				ledger,
			)
		} else {
			record, err = decodeCurrentEventRecordForMigration(
				source,
				lineRange,
				ledger,
			)
			sequence = record.Seq()
		}
		if err != nil {
			return 0, fmt.Errorf(
				"decode staged current event record at byte %d: %w",
				offset,
				err,
			)
		}
		if hasPrevious && sequence <= previousSequence {
			return 0, fmt.Errorf(
				"staged current event record sequence regressed at byte %d: previous=%d current=%d",
				offset,
				previousSequence,
				sequence,
			)
		}
		previousSequence = sequence
		hasPrevious = true
		if visit != nil {
			if err := visit(record); err != nil {
				return 0, err
			}
		}
		offset = nextOffset
	}
	return previousSequence, nil
}

var currentToolCompletionPayloadFields = migrationKnownFieldSet{
	"call_id",
	"name",
	"output_kind",
	"is_error",
	"output",
	"summary",
	"condensed_text",
	"presentation",
	"provider_items",
}

var currentToolCompletionProviderItemFields = migrationKnownFieldSet{
	"type",
	"name",
	"call_id",
	"raw",
	"linked_call_id",
	"link_kind",
}

func validateCurrentEventRecordForMigration(
	source *os.File,
	recordRange migrationJSONValueRange,
	ledger *migrationResourceLedger,
) (int64, error) {
	sequence, kind, _, payloadRange, err := scanCurrentEventEnvelopeForMigration(
		source,
		recordRange,
		ledger,
	)
	if err != nil {
		return 0, err
	}
	if kind != EventKindToolCompletion {
		if _, err := decodeCurrentEventRecordForMigration(source, recordRange, ledger); err != nil {
			return 0, err
		}
		return sequence, nil
	}
	if err := validateCurrentToolCompletionPayloadForMigration(
		source,
		payloadRange,
		ledger,
	); err != nil {
		return 0, fmt.Errorf("decode %s payload: %w", kind, err)
	}
	return sequence, nil
}

func validateCurrentToolCompletionPayloadForMigration(
	source io.ReaderAt,
	payloadRange migrationJSONValueRange,
	ledger *migrationResourceLedger,
) error {
	fields, err := scanLegacyObjectRange(
		source,
		payloadRange,
		ledger,
		currentToolCompletionPayloadFields,
	)
	if err != nil {
		return err
	}
	required := []struct {
		slot int
		name string
	}{
		{0, "call_id"},
		{1, "name"},
		{2, "output_kind"},
		{3, "is_error"},
		{4, "output"},
	}
	for _, field := range required {
		if _, present := fields.Value(field.slot); !present {
			return fmt.Errorf("%s is required", field.name)
		}
	}
	var callID string
	if valueRange, _ := fields.Value(0); decodeLegacyJSONRange(source, valueRange, &callID) != nil {
		return fmt.Errorf("call identity must be a string")
	}
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return fmt.Errorf("call identity is required")
	}
	var name string
	if valueRange, _ := fields.Value(1); decodeLegacyJSONRange(source, valueRange, &name) != nil {
		return fmt.Errorf("tool name must be a string")
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("tool name is required")
	}
	var outputKind ToolOutputKind
	if valueRange, _ := fields.Value(2); decodeLegacyJSONRange(source, valueRange, &outputKind) != nil {
		return fmt.Errorf("output kind must be a string")
	}
	switch outputKind {
	case ToolOutputKindFunction, ToolOutputKindCustom:
	default:
		return fmt.Errorf("unsupported tool output kind %q", outputKind)
	}
	var isError *bool
	if valueRange, _ := fields.Value(3); decodeLegacyJSONRange(source, valueRange, &isError) != nil {
		return fmt.Errorf("is_error must be a boolean")
	}
	if isError == nil {
		return fmt.Errorf("is_error is required")
	}
	for _, optional := range []struct {
		slot int
		name string
	}{
		{5, "summary"},
		{6, "condensed text"},
	} {
		valueRange, present := fields.Value(optional.slot)
		if !present {
			continue
		}
		var value *string
		if err := decodeLegacyJSONRange(source, valueRange, &value); err != nil {
			return fmt.Errorf("%s must be a string or null", optional.name)
		}
		if value != nil && strings.TrimSpace(*value) == "" {
			return fmt.Errorf("%s must be non-empty when present", optional.name)
		}
	}
	providerItemsRange, providerItemsPresent := fields.Value(8)
	if !providerItemsPresent {
		return nil
	}
	var providerItemsPrefix [1]byte
	if _, err := source.ReadAt(providerItemsPrefix[:], providerItemsRange.Start); err != nil {
		return fmt.Errorf("read provider items: %w", err)
	}
	if providerItemsPrefix[0] == 'n' {
		return nil
	}
	return scanLegacyObjectArrayRange(
		source,
		providerItemsRange,
		ledger,
		currentToolCompletionProviderItemFields,
		func(index int, item migrationScannedObject) error {
			typeRange, typePresent := item.Value(0)
			_, rawPresent := item.Value(3)
			if !typePresent || !rawPresent {
				return fmt.Errorf("provider item %d is missing type or Raw", index)
			}
			var itemType ProviderInputItemType
			if err := decodeLegacyJSONRange(source, typeRange, &itemType); err != nil {
				return fmt.Errorf("provider item %d type must be a string", index)
			}
			itemName, err := decodeCurrentOptionalProviderString(
				source,
				item,
				1,
				index,
				"name",
			)
			if err != nil {
				return err
			}
			itemCallID, err := decodeCurrentOptionalProviderString(
				source,
				item,
				2,
				index,
				"call identity",
			)
			if err != nil {
				return err
			}
			linkedCallID, err := decodeCurrentOptionalProviderString(
				source,
				item,
				4,
				index,
				"linked call identity",
			)
			if err != nil {
				return err
			}
			var linkKind *ProviderItemLinkKind
			if linkKindRange, present := item.Value(5); present {
				if err := decodeLegacyJSONRange(source, linkKindRange, &linkKind); err != nil {
					return fmt.Errorf("provider item %d link kind must be a string or null", index)
				}
			}
			_, err = normalizeToolCompletionProviderItem(
				index,
				ToolCompletionRecord{
					CallID:     callID,
					OutputKind: outputKind,
				},
				ToolCompletionProviderItem{
					Type:         itemType,
					Name:         itemName,
					CallID:       itemCallID,
					Raw:          []byte("null"),
					LinkedCallID: linkedCallID,
					LinkKind:     linkKind,
				},
			)
			if err != nil {
				return err
			}
			return nil
		},
	)
}

func decodeCurrentOptionalProviderString(
	source io.ReaderAt,
	item migrationScannedObject,
	slot int,
	index int,
	name string,
) (*string, error) {
	valueRange, present := item.Value(slot)
	if !present {
		return nil, nil
	}
	var value *string
	if err := decodeLegacyJSONRange(source, valueRange, &value); err != nil {
		return nil, fmt.Errorf("provider item %d %s must be a string or null", index, name)
	}
	return value, nil
}

func decodeCurrentEventRecordForMigration(
	source *os.File,
	recordRange migrationJSONValueRange,
	ledger *migrationResourceLedger,
) (EventRecord, error) {
	sequence, kind, stepID, payloadRange, err := scanCurrentEventEnvelopeForMigration(
		source,
		recordRange,
		ledger,
	)
	if err != nil {
		return EventRecord{}, err
	}
	payload, err := decodeEventRecordPayloadV1(
		kind,
		func(target any) error {
			return decodeLegacyJSONRange(source, payloadRange, target)
		},
	)
	if err != nil {
		return EventRecord{}, err
	}
	return NewEventRecord(sequence, stepID, payload)
}

func scanCurrentEventEnvelopeForMigration(
	source *os.File,
	recordRange migrationJSONValueRange,
	ledger *migrationResourceLedger,
) (int64, EventKind, *string, migrationJSONValueRange, error) {
	scanner, err := newMigrationJSONScanner(
		source,
		recordRange.Start,
		recordRange.End,
		ledger,
	)
	if err != nil {
		return 0, "", nil, migrationJSONValueRange{}, err
	}
	envelope, scanErr := scanner.ScanObject(currentEventEnvelopeFields)
	closeErr := scanner.Close()
	if scanErr != nil || closeErr != nil {
		return 0, "", nil, migrationJSONValueRange{}, errors.Join(scanErr, closeErr)
	}
	sequenceRange, sequencePresent := envelope.Value(0)
	kindRange, kindPresent := envelope.Value(1)
	stepIDRange, stepIDPresent := envelope.Value(2)
	payloadRange, payloadPresent := envelope.Value(3)
	if !sequencePresent || !kindPresent || !payloadPresent {
		return 0, "", nil, migrationJSONValueRange{}, fmt.Errorf(
			"current event envelope is missing a required field",
		)
	}
	var sequence int64
	if err := decodeLegacyJSONRange(source, sequenceRange, &sequence); err != nil {
		return 0, "", nil, migrationJSONValueRange{}, fmt.Errorf(
			"decode current event sequence: %w",
			err,
		)
	}
	var kind EventKind
	if err := decodeLegacyJSONRange(source, kindRange, &kind); err != nil {
		return 0, "", nil, migrationJSONValueRange{}, fmt.Errorf(
			"decode current event kind: %w",
			err,
		)
	}
	var stepID *string
	if stepIDPresent {
		if err := decodeLegacyJSONRange(source, stepIDRange, &stepID); err != nil {
			return 0, "", nil, migrationJSONValueRange{}, fmt.Errorf(
				"decode current event step identity: %w",
				err,
			)
		}
	}
	return sequence, kind, stepID, payloadRange, nil
}
