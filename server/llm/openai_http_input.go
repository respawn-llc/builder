package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/prompts"
	"core/server/llm/openaiwire"
	"core/shared/textutil"
	"core/shared/toolspec"

	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
)

// ErrOpenAIInputItemUnprepared reports that provider-neutral history reached
// the OpenAI serializer without a valid provider-ready Raw payload.
var ErrOpenAIInputItemUnprepared = errors.New("openai input item is not prepared")

type OpenAIInputPreparationDetail string

const (
	OpenAIInputPreparationMissingRaw           OpenAIInputPreparationDetail = "missing_raw"
	OpenAIInputPreparationInvalidRaw           OpenAIInputPreparationDetail = "invalid_raw"
	OpenAIInputInvariantEmptyContent           OpenAIInputPreparationDetail = "empty_content"
	OpenAIInputInvariantEmptyCallID            OpenAIInputPreparationDetail = "empty_call_id"
	OpenAIInputInvariantEmptyArguments         OpenAIInputPreparationDetail = "empty_arguments"
	OpenAIInputInvariantInvalidOutputJSON      OpenAIInputPreparationDetail = "invalid_output_json"
	OpenAIInputInvariantEmptyReasoningID       OpenAIInputPreparationDetail = "empty_reasoning_id"
	OpenAIInputInvariantEmptyCompactionContent OpenAIInputPreparationDetail = "empty_compaction_content"
	OpenAIInputInvariantUnsupportedType        OpenAIInputPreparationDetail = "unsupported_type"
)

type OpenAIInputItemPreparationError struct {
	Index     int
	Type      ResponseItemType
	Name      *string
	CallID    *string
	State     OpenAIInputPreparationDetail
	Invariant OpenAIInputPreparationDetail
}

func (e *OpenAIInputItemPreparationError) Error() string {
	return fmt.Sprintf("openai input item at index %d is not prepared (type=%q name=%s call_id=%s state=%q invariant=%q)", e.Index, e.Type, formatOptionalOpenAIInputFact(e.Name), formatOptionalOpenAIInputFact(e.CallID), e.State, e.Invariant)
}

func (e *OpenAIInputItemPreparationError) Unwrap() error { return ErrOpenAIInputItemUnprepared }

func buildResponsesInput(canonical []ResponseItem) ([]responses.ResponseInputItemUnionParam, error) {
	items := make([]responses.ResponseInputItemUnionParam, 0, len(canonical))
	for idx, item := range canonical {
		raw := bytes.TrimSpace(item.Raw)
		if len(raw) == 0 {
			return nil, newOpenAIInputItemPreparationError(idx, item, OpenAIInputPreparationMissingRaw)
		}
		if !json.Valid(raw) {
			return nil, newOpenAIInputItemPreparationError(idx, item, OpenAIInputPreparationInvalidRaw)
		}
		items = append(items, param.Override[responses.ResponseInputItemUnionParam](append(json.RawMessage(nil), raw...)))
	}
	return items, nil
}

func newOpenAIInputItemPreparationError(index int, item ResponseItem, state OpenAIInputPreparationDetail) error {
	invariant := unpreparedOpenAIInputInvariant(item)
	if state == OpenAIInputPreparationInvalidRaw {
		invariant = OpenAIInputPreparationInvalidRaw
	}
	return &OpenAIInputItemPreparationError{
		Index: index, Type: item.Type, Name: optionalTrimmedPointer(item.Name),
		CallID: optionalFirstTrimmedPointer(item.CallID, item.ID), State: state, Invariant: invariant,
	}
}

func formatOptionalOpenAIInputFact(value *string) string {
	if value == nil {
		return "null"
	}
	return fmt.Sprintf("%q", *value)
}

func unpreparedOpenAIInputInvariant(item ResponseItem) OpenAIInputPreparationDetail {
	switch item.Type {
	case ResponseItemTypeMessage:
		if _, present := textutil.OptionalTrimmed(item.Content); !present {
			return OpenAIInputInvariantEmptyContent
		}
	case ResponseItemTypeFunctionCall:
		if _, present := textutil.FirstOptionalTrimmed(item.CallID, item.ID); !present {
			return OpenAIInputInvariantEmptyCallID
		}
		if strings.TrimSpace(string(item.Arguments)) == "" {
			return OpenAIInputInvariantEmptyArguments
		}
	case ResponseItemTypeFunctionCallOutput:
		if _, present := textutil.OptionalTrimmed(item.CallID); !present {
			return OpenAIInputInvariantEmptyCallID
		}
		if !json.Valid(item.Output) {
			return OpenAIInputInvariantInvalidOutputJSON
		}
	case ResponseItemTypeCustomToolCall:
		if _, present := textutil.FirstOptionalTrimmed(item.CallID, item.ID); !present {
			return OpenAIInputInvariantEmptyCallID
		}
	case ResponseItemTypeCustomToolOutput:
		if _, present := textutil.OptionalTrimmed(item.CallID); !present {
			return OpenAIInputInvariantEmptyCallID
		}
		if !json.Valid(item.Output) {
			return OpenAIInputInvariantInvalidOutputJSON
		}
	case ResponseItemTypeReasoning:
		if _, present := textutil.OptionalTrimmed(item.ID); !present {
			return OpenAIInputInvariantEmptyReasoningID
		}
	case ResponseItemTypeCompaction:
		if _, present := textutil.OptionalTrimmed(item.EncryptedContent); !present {
			return OpenAIInputInvariantEmptyCompactionContent
		}
	default:
		return OpenAIInputInvariantUnsupportedType
	}
	return OpenAIInputPreparationMissingRaw
}

func normalizeToolArguments(arguments string) string {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return "{}"
	}
	if json.Valid([]byte(arguments)) {
		return textutil.CompactNoHTMLEscape([]byte(arguments))
	}
	quoted, _ := json.Marshal(arguments)
	return textutil.CompactNoHTMLEscape(quoted)
}

func normalizeToolInput(arguments string) json.RawMessage {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return json.RawMessage(`{}`)
	}
	if json.Valid([]byte(arguments)) {
		return json.RawMessage(textutil.CompactNoHTMLEscape([]byte(arguments)))
	}
	quoted, _ := json.Marshal(arguments)
	return json.RawMessage(textutil.CompactNoHTMLEscape(quoted))
}

// PrepareOpenAIInputItems stamps provider-ready OpenAI input payloads onto
// locally materialized response items. The transport can then pass Raw through
// without making history-shape decisions at request serialization time.
func PrepareOpenAIInputItems(items []ResponseItem) []ResponseItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]ResponseItem, 0, len(items))
	for _, item := range items {
		out = append(out, prepareOpenAIInputItem(item)...)
	}
	return out
}

func prepareOpenAIInputItem(item ResponseItem) []ResponseItem {
	copyItem := CloneResponseItems([]ResponseItem{item})[0]
	if len(bytes.TrimSpace(copyItem.Raw)) > 0 {
		return []ResponseItem{copyItem}
	}
	if promoted, ok := promotedOpenAIViewImageFileItems(copyItem); ok {
		return promoted
	}
	if raw, ok := openAIInputRawForResponseItem(copyItem); ok {
		copyItem.Raw = raw
	}
	return []ResponseItem{copyItem}
}

type openAIInputTextContentRaw struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type openAIOutputTextContentRaw struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Annotations []any  `json:"annotations,omitempty"`
}

type openAIInputMessageRaw struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content any    `json:"content"`
	Status  string `json:"status,omitempty"`
	Phase   string `json:"phase,omitempty"`
}

type openAIFunctionCallRaw struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAICustomToolCallRaw struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Name   string `json:"name"`
	Input  string `json:"input"`
}

type openAIReasoningSummaryRaw struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type openAIReasoningRaw struct {
	Type             string                      `json:"type"`
	ID               string                      `json:"id"`
	Summary          []openAIReasoningSummaryRaw `json:"summary"`
	EncryptedContent string                      `json:"encrypted_content,omitempty"`
}

type openAICompactionRaw struct {
	Type             string `json:"type"`
	ID               string `json:"id,omitempty"`
	EncryptedContent string `json:"encrypted_content"`
}

func openAIInputRawForResponseItem(item ResponseItem) (json.RawMessage, bool) {
	switch item.Type {
	case ResponseItemTypeMessage:
		return openAIMessageInputRaw(item)
	case ResponseItemTypeFunctionCall:
		callID, present := textutil.FirstOptionalTrimmed(item.CallID, item.ID)
		arguments := strings.TrimSpace(string(item.Arguments))
		if !present || arguments == "" {
			return nil, false
		}
		name, _ := textutil.OptionalTrimmed(item.Name)
		return marshalOpenAIInputRaw(openAIFunctionCallRaw{
			Type:      string(ResponseItemTypeFunctionCall),
			CallID:    callID,
			Name:      name,
			Arguments: arguments,
		})
	case ResponseItemTypeFunctionCallOutput:
		callID, present := textutil.OptionalTrimmed(item.CallID)
		if !present {
			return nil, false
		}
		raw, err := openaiwire.NewFunctionCallOutput(callID, item.Output)
		if err != nil {
			return nil, false
		}
		return raw.Bytes(), true
	case ResponseItemTypeCustomToolCall:
		callID, present := textutil.FirstOptionalTrimmed(item.CallID, item.ID)
		if !present {
			return nil, false
		}
		name, _ := textutil.OptionalTrimmed(item.Name)
		input, _ := textutil.OptionalExact(item.CustomInput)
		return marshalOpenAIInputRaw(openAICustomToolCallRaw{
			Type:   string(ResponseItemTypeCustomToolCall),
			CallID: callID,
			Name:   name,
			Input:  input,
		})
	case ResponseItemTypeCustomToolOutput:
		callID, present := textutil.OptionalTrimmed(item.CallID)
		if !present {
			return nil, false
		}
		raw, err := openaiwire.NewCustomToolOutput(callID, item.Output)
		if err != nil {
			return nil, false
		}
		return raw.Bytes(), true
	case ResponseItemTypeReasoning:
		id, present := textutil.OptionalTrimmed(item.ID)
		if !present {
			return nil, false
		}
		summary := make([]openAIReasoningSummaryRaw, 0, len(item.ReasoningSummary))
		for _, entry := range item.ReasoningSummary {
			text := strings.TrimSpace(entry.Text)
			if text == "" {
				continue
			}
			summary = append(summary, openAIReasoningSummaryRaw{Type: "summary_text", Text: text})
		}
		encrypted, _ := textutil.OptionalTrimmed(item.EncryptedContent)
		return marshalOpenAIInputRaw(openAIReasoningRaw{
			Type:             string(ResponseItemTypeReasoning),
			ID:               id,
			Summary:          summary,
			EncryptedContent: encrypted,
		})
	case ResponseItemTypeCompaction:
		encrypted, present := textutil.OptionalTrimmed(item.EncryptedContent)
		if !present {
			return nil, false
		}
		id, _ := textutil.OptionalTrimmed(item.ID)
		return marshalOpenAIInputRaw(openAICompactionRaw{
			Type:             string(ResponseItemTypeCompaction),
			ID:               id,
			EncryptedContent: encrypted,
		})
	default:
		return nil, false
	}
}

func openAIMessageInputRaw(item ResponseItem) (json.RawMessage, bool) {
	text, present := textutil.OptionalExact(item.Content)
	if !present {
		return nil, false
	}
	if item.MessageType != nil && *item.MessageType == MessageTypeCompactionSummary {
		text = prompts.CompactionSummaryPrefix + "\n\n" + strings.TrimSpace(text)
	}
	role := ""
	if item.Role != nil {
		role = strings.TrimSpace(string(*item.Role))
	}
	blankFinal := role == string(RoleAssistant) &&
		item.Phase != nil &&
		*item.Phase == MessagePhaseFinal &&
		item.MessageType == nil
	if strings.TrimSpace(text) == "" && !blankFinal {
		return nil, false
	}
	if role == string(RoleAssistant) {
		content := []openAIOutputTextContentRaw{{
			Type:        "output_text",
			Text:        text,
			Annotations: []any{},
		}}
		raw := openAIInputMessageRaw{
			Type:    "message",
			Role:    string(RoleAssistant),
			Content: content,
			Status:  "completed",
		}
		if item.Phase != nil {
			raw.Phase = string(*item.Phase)
		}
		return marshalOpenAIInputRaw(raw)
	}
	switch role {
	case string(RoleSystem), string(RoleDeveloper), string(RoleUser):
	default:
		role = string(RoleUser)
	}
	return marshalOpenAIInputRaw(openAIInputMessageRaw{
		Type:    "message",
		Role:    role,
		Content: []openAIInputTextContentRaw{{Type: "input_text", Text: text}},
	})
}

func promotedOpenAIViewImageFileItems(item ResponseItem) ([]ResponseItem, bool) {
	name, hasName := textutil.OptionalTrimmed(item.Name)
	if item.Type != ResponseItemTypeFunctionCallOutput ||
		!hasName ||
		name != string(toolspec.ToolViewImage) {
		return nil, false
	}
	callID, present := textutil.OptionalTrimmed(item.CallID)
	if !present {
		return nil, false
	}
	content, ok := openaiwire.InputContentItems(item.Output)
	if !ok {
		return nil, false
	}
	promotedRaw, promoted := promotedOpenAIInputMessageRaw(content)
	if !promoted {
		return nil, false
	}
	output := CloneResponseItems([]ResponseItem{item})[0]
	promotedOutput, err := openaiwire.NewFunctionCallOutput(callID, json.RawMessage(`"attached file content"`))
	if err != nil {
		return nil, false
	}
	output.Raw = promotedOutput.Bytes()
	return []ResponseItem{
		output,
		{
			Type:         ResponseItemTypeOther,
			Name:         textutil.Value(string(toolspec.ToolViewImage)),
			CallID:       textutil.Value(callID),
			Raw:          promotedRaw,
			LinkedCallID: textutil.Value(callID),
			LinkKind:     textutil.Value(ResponseItemLinkToolOutputAttachment),
		},
	}, true
}

func optionalTrimmedPointer[T ~string](value *T) *string {
	trimmed, present := textutil.OptionalTrimmed(value)
	if !present {
		return nil
	}
	return &trimmed
}

func optionalFirstTrimmedPointer[T ~string](values ...*T) *string {
	trimmed, present := textutil.FirstOptionalTrimmed(values...)
	if !present {
		return nil
	}
	return &trimmed
}

func promotedOpenAIInputMessageRaw(content []openaiwire.InputContent) (json.RawMessage, bool) {
	if len(content) == 0 {
		return nil, false
	}
	hasInputFile := false
	for _, item := range content {
		if item.Type == "input_file" {
			hasInputFile = true
			break
		}
	}
	if !hasInputFile {
		return nil, false
	}
	return marshalOpenAIInputRaw(openAIInputMessageRaw{
		Type:    "message",
		Role:    string(RoleUser),
		Content: content,
	})
}

func marshalOpenAIInputRaw(value any) (json.RawMessage, bool) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, false
	}
	return append(json.RawMessage(nil), bytes.TrimSpace(buf.Bytes())...), true
}
