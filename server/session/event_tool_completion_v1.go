package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/server/llm/openaiwire"
	"core/shared/invariant"
	"core/shared/transcript"
)

type ToolOutputKind string

const (
	ToolOutputKindFunction ToolOutputKind = "function"
	ToolOutputKindCustom   ToolOutputKind = "custom"
)

type ProviderInputItemType string

const (
	ProviderInputItemTypeFunctionCallOutput ProviderInputItemType = "function_call_output"
	ProviderInputItemTypeCustomToolOutput   ProviderInputItemType = "custom_tool_call_output"
	ProviderInputItemTypeOther              ProviderInputItemType = "other"
)

type ProviderItemLinkKind string

const (
	ProviderItemLinkToolOutputAttachment ProviderItemLinkKind = "tool_output_attachment"
)

type ToolCompletionProviderItem struct {
	Type         ProviderInputItemType `json:"type"`
	Name         *string               `json:"name,omitempty"`
	CallID       *string               `json:"call_id,omitempty"`
	Raw          json.RawMessage       `json:"raw"`
	LinkedCallID *string               `json:"linked_call_id,omitempty"`
	LinkKind     *ProviderItemLinkKind `json:"link_kind,omitempty"`
}

type ToolCompletionRecord struct {
	CallID        string                       `json:"call_id"`
	Name          string                       `json:"name"`
	OutputKind    ToolOutputKind               `json:"output_kind"`
	IsError       bool                         `json:"is_error"`
	Output        json.RawMessage              `json:"output"`
	Summary       *string                      `json:"summary,omitempty"`
	CondensedText *string                      `json:"condensed_text,omitempty"`
	Presentation  json.RawMessage              `json:"presentation,omitempty"`
	ProviderItems []ToolCompletionProviderItem `json:"provider_items,omitempty"`
}

type toolCompletionRecordV1Wire struct {
	CallID        string                       `json:"call_id"`
	Name          string                       `json:"name"`
	OutputKind    ToolOutputKind               `json:"output_kind"`
	IsError       *bool                        `json:"is_error"`
	Output        json.RawMessage              `json:"output"`
	Summary       *string                      `json:"summary,omitempty"`
	CondensedText *string                      `json:"condensed_text,omitempty"`
	Presentation  json.RawMessage              `json:"presentation,omitempty"`
	ProviderItems []ToolCompletionProviderItem `json:"provider_items,omitempty"`
}

var ErrToolCompletionProviderItem = errors.New("invalid tool completion provider item")

type ToolCompletionProviderItemErrorReason string

const (
	ToolCompletionProviderItemUnsupportedType ToolCompletionProviderItemErrorReason = "unsupported_type"
	ToolCompletionProviderItemMissingRaw      ToolCompletionProviderItemErrorReason = "missing_raw"
	ToolCompletionProviderItemInvalidRaw      ToolCompletionProviderItemErrorReason = "invalid_raw"
	ToolCompletionProviderItemInvalidFacts    ToolCompletionProviderItemErrorReason = "invalid_facts"
)

type ToolCompletionProviderItemError struct {
	Index  int
	Type   ProviderInputItemType
	Reason ToolCompletionProviderItemErrorReason
}

func (e ToolCompletionProviderItemError) Error() string {
	return fmt.Sprintf(
		"tool completion provider item %d is invalid (type=%q reason=%q)",
		e.Index,
		e.Type,
		e.Reason,
	)
}

func (e ToolCompletionProviderItemError) Unwrap() error {
	return ErrToolCompletionProviderItem
}

func (ToolCompletionRecord) eventKind() EventKind {
	return EventKindToolCompletion
}

func (r ToolCompletionRecord) validate() error {
	_, err := normalizeToolCompletionRecord(r)
	return err
}

func normalizeToolCompletionRecord(record ToolCompletionRecord) (ToolCompletionRecord, error) {
	record, err := normalizeToolCompletionSemanticFields(record)
	if err != nil {
		return ToolCompletionRecord{}, err
	}
	switch record.OutputKind {
	case ToolOutputKindFunction, ToolOutputKindCustom:
	default:
		return ToolCompletionRecord{}, fmt.Errorf("unsupported tool output kind %q", record.OutputKind)
	}
	if len(record.ProviderItems) > 0 {
		record.ProviderItems = append([]ToolCompletionProviderItem(nil), record.ProviderItems...)
		for index := range record.ProviderItems {
			item, itemErr := normalizeToolCompletionProviderItem(index, record, record.ProviderItems[index])
			if itemErr != nil {
				return ToolCompletionRecord{}, itemErr
			}
			record.ProviderItems[index] = item
		}
	}
	return record, nil
}

func normalizeToolCompletionSemanticFields(record ToolCompletionRecord) (ToolCompletionRecord, error) {
	record, err := normalizeToolCompletionFallbackFields(record)
	if err != nil {
		return ToolCompletionRecord{}, err
	}
	if record.Name == "" {
		return ToolCompletionRecord{}, fmt.Errorf("tool name is required")
	}
	return record, nil
}

func normalizeToolCompletionFallbackFields(record ToolCompletionRecord) (ToolCompletionRecord, error) {
	record.CallID = strings.TrimSpace(record.CallID)
	record.Name = strings.TrimSpace(record.Name)
	if record.CallID == "" {
		return ToolCompletionRecord{}, fmt.Errorf("call identity is required")
	}
	if err := validateJSONValue("output", record.Output); err != nil {
		return ToolCompletionRecord{}, err
	}
	record.Output = append(json.RawMessage(nil), record.Output...)
	var err error
	if record.Summary, err = normalizeOptionalEventText("summary", record.Summary); err != nil {
		return ToolCompletionRecord{}, err
	}
	if record.CondensedText, err = normalizeOptionalEventText("condensed text", record.CondensedText); err != nil {
		return ToolCompletionRecord{}, err
	}
	if len(record.Presentation) > 0 {
		if err := validateJSONValue("presentation", record.Presentation); err != nil {
			return ToolCompletionRecord{}, err
		}
		if _, ok := transcript.DecodeToolCallMeta(record.Presentation); !ok {
			return ToolCompletionRecord{}, fmt.Errorf("presentation is invalid")
		}
		record.Presentation = append(json.RawMessage(nil), record.Presentation...)
	}
	return record, nil
}

func normalizeToolCompletionProviderItem(
	index int,
	record ToolCompletionRecord,
	item ToolCompletionProviderItem,
) (ToolCompletionProviderItem, error) {
	var err error
	if item.Name, err = normalizeOptionalEventIdentity("provider item name", item.Name); err != nil {
		return ToolCompletionProviderItem{}, providerItemFactsError(index, item.Type)
	}
	if item.CallID, err = normalizeOptionalEventIdentity("provider item call identity", item.CallID); err != nil {
		return ToolCompletionProviderItem{}, providerItemFactsError(index, item.Type)
	}
	if item.LinkedCallID, err = normalizeOptionalEventIdentity("linked call identity", item.LinkedCallID); err != nil {
		return ToolCompletionProviderItem{}, providerItemFactsError(index, item.Type)
	}
	switch item.Type {
	case ProviderInputItemTypeFunctionCallOutput, ProviderInputItemTypeCustomToolOutput:
		if item.CallID == nil || *item.CallID != record.CallID || item.LinkedCallID != nil || item.LinkKind != nil {
			return ToolCompletionProviderItem{}, providerItemFactsError(index, item.Type)
		}
		expectedKind := ToolOutputKindFunction
		if item.Type == ProviderInputItemTypeCustomToolOutput {
			expectedKind = ToolOutputKindCustom
		}
		if record.OutputKind != expectedKind {
			return ToolCompletionProviderItem{}, providerItemFactsError(index, item.Type)
		}
		if len(bytes.TrimSpace(item.Raw)) == 0 {
			raw, rawErr := encodeMissingProviderOutputRaw(item.Type, record.CallID, record.Output)
			if rawErr != nil {
				return ToolCompletionProviderItem{}, providerItemFactsError(index, item.Type)
			}
			item.Raw = raw
		}
	case ProviderInputItemTypeOther:
		if len(bytes.TrimSpace(item.Raw)) == 0 {
			return ToolCompletionProviderItem{}, ToolCompletionProviderItemError{
				Index: index, Type: item.Type, Reason: ToolCompletionProviderItemMissingRaw,
			}
		}
		if item.LinkKind != nil {
			if *item.LinkKind != ProviderItemLinkToolOutputAttachment ||
				item.LinkedCallID == nil ||
				*item.LinkedCallID != record.CallID {
				return ToolCompletionProviderItem{}, providerItemFactsError(index, item.Type)
			}
			linkKind := *item.LinkKind
			item.LinkKind = &linkKind
		} else if item.LinkedCallID != nil {
			return ToolCompletionProviderItem{}, providerItemFactsError(index, item.Type)
		}
	default:
		return ToolCompletionProviderItem{}, ToolCompletionProviderItemError{
			Index: index, Type: item.Type, Reason: ToolCompletionProviderItemUnsupportedType,
		}
	}
	if !json.Valid(item.Raw) {
		return ToolCompletionProviderItem{}, ToolCompletionProviderItemError{
			Index: index, Type: item.Type, Reason: ToolCompletionProviderItemInvalidRaw,
		}
	}
	item.Raw = append(json.RawMessage(nil), item.Raw...)
	return item, nil
}

func encodeMissingProviderOutputRaw(
	itemType ProviderInputItemType,
	callID string,
	output json.RawMessage,
) (json.RawMessage, error) {
	switch itemType {
	case ProviderInputItemTypeFunctionCallOutput:
		raw, err := openaiwire.NewFunctionCallOutput(callID, output)
		if err != nil {
			return nil, err
		}
		return raw.Bytes(), nil
	case ProviderInputItemTypeCustomToolOutput:
		raw, err := openaiwire.NewCustomToolOutput(callID, output)
		if err != nil {
			return nil, err
		}
		return raw.Bytes(), nil
	default:
		return nil, fmt.Errorf("provider item type %q cannot generate output Raw", itemType)
	}
}

func providerItemFactsError(index int, itemType ProviderInputItemType) error {
	return ToolCompletionProviderItemError{
		Index: index, Type: itemType, Reason: ToolCompletionProviderItemInvalidFacts,
	}
}

func encodeToolCompletionRecordV1(record ToolCompletionRecord) ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for index, field := range []struct {
		name  string
		value any
	}{
		{"call_id", record.CallID},
		{"name", record.Name},
		{"output_kind", record.OutputKind},
		{"is_error", record.IsError},
		{"output", record.Output},
	} {
		if err := writeMarshaledJSONField(&buffer, field.name, field.value, index > 0); err != nil {
			return nil, err
		}
	}
	if record.Summary != nil {
		if err := writeMarshaledJSONField(&buffer, "summary", record.Summary, true); err != nil {
			return nil, err
		}
	}
	if record.CondensedText != nil {
		if err := writeMarshaledJSONField(&buffer, "condensed_text", record.CondensedText, true); err != nil {
			return nil, err
		}
	}
	if len(record.Presentation) > 0 {
		if err := writeJSONField(&buffer, "presentation", record.Presentation, true); err != nil {
			return nil, err
		}
	}
	if len(record.ProviderItems) > 0 {
		buffer.WriteString(`,"provider_items":[`)
		for index, item := range record.ProviderItems {
			if index > 0 {
				buffer.WriteByte(',')
			}
			if err := encodeToolCompletionProviderItemV1(&buffer, item); err != nil {
				return nil, err
			}
		}
		buffer.WriteByte(']')
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

func encodeToolCompletionProviderItemV1(
	buffer *bytes.Buffer,
	item ToolCompletionProviderItem,
) error {
	buffer.WriteByte('{')
	if err := writeMarshaledJSONField(buffer, "type", item.Type, false); err != nil {
		return err
	}
	if item.Name != nil {
		if err := writeMarshaledJSONField(buffer, "name", item.Name, true); err != nil {
			return err
		}
	}
	if item.CallID != nil {
		if err := writeMarshaledJSONField(buffer, "call_id", item.CallID, true); err != nil {
			return err
		}
	}
	if err := writeJSONField(buffer, "raw", item.Raw, true); err != nil {
		return err
	}
	if item.LinkedCallID != nil {
		if err := writeMarshaledJSONField(buffer, "linked_call_id", item.LinkedCallID, true); err != nil {
			return err
		}
	}
	if item.LinkKind != nil {
		if err := writeMarshaledJSONField(buffer, "link_kind", item.LinkKind, true); err != nil {
			return err
		}
	}
	buffer.WriteByte('}')
	return nil
}

func writeMarshaledJSONField(
	buffer *bytes.Buffer,
	name string,
	value any,
	comma bool,
) error {
	encoded, err := marshalSessionJSON(value)
	if err != nil {
		return err
	}
	return writeJSONField(buffer, name, encoded, comma)
}

func writeJSONField(buffer *bytes.Buffer, name string, value []byte, comma bool) error {
	encodedName, err := marshalSessionJSON(name)
	if err != nil {
		return err
	}
	if comma {
		buffer.WriteByte(',')
	}
	buffer.Write(encodedName)
	buffer.WriteByte(':')
	buffer.Write(value)
	return nil
}

func marshalSessionJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		invariant.NewPolicy().Check(false, invariant.FailureDiagnostic(
			invariant.ScopeSessionPersistence,
			"marshal_session_v1_json",
			err,
		))
		return nil, fmt.Errorf("marshal session v1 JSON: %w", err)
	}
	return encoded, nil
}
