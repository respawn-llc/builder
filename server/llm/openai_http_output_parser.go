package llm

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"core/shared/textutil"

	"github.com/openai/openai-go/v3/responses"
)

type responseOutputItemParser interface {
	Parse(item responses.ResponseOutputItemUnion, providerPhase *ProviderPhase) (parsedResponseOutputItem, error)
}

type parsedResponseOutputItem struct {
	CanonicalItems    []ResponseItem
	AssistantSegments []assistantOutputSegment
	ToolCalls         []ToolCall
	Reasoning         []ReasoningEntry
	ReasoningItems    []ReasoningItem
}

type responseOutputItemParsers struct {
	byType map[string]responseOutputItemParser
}

type responseOutputItemParserRegistration struct {
	itemType string
	parser   responseOutputItemParser
}

func newResponseOutputItemParsers(registrations ...responseOutputItemParserRegistration) responseOutputItemParsers {
	byType := make(map[string]responseOutputItemParser, len(registrations))
	for _, registration := range registrations {
		byType[registration.itemType] = registration.parser
	}
	return responseOutputItemParsers{byType: byType}
}

func parseOutputItems(items []responses.ResponseOutputItemUnion) ([]ResponseItem, *string, MessagePhase, *ProviderPhase, []ToolCall, []ReasoningEntry, []ReasoningItem, error) {
	parsers := newResponseOutputItemParsers(
		responseOutputItemParserRegistration{itemType: "message", parser: messageOutputItemParser{}},
		responseOutputItemParserRegistration{itemType: "function_call", parser: functionCallOutputItemParser{}},
		responseOutputItemParserRegistration{itemType: "custom_tool_call", parser: customToolCallOutputItemParser{}},
		responseOutputItemParserRegistration{itemType: "reasoning", parser: reasoningOutputItemParser{}},
		responseOutputItemParserRegistration{itemType: "compaction", parser: compactionOutputItemParser{}},
	)
	canonical := make([]ResponseItem, 0, len(items))
	assistantSegments := make([]assistantOutputSegment, 0, len(items))
	toolCalls := make([]ToolCall, 0, len(items))
	reasoning := make([]ReasoningEntry, 0, len(items))
	reasoningItems := make([]ReasoningItem, 0, len(items))

	for outputIndex, item := range items {
		parsed, ok := parsers.byType[item.Type]
		if !ok {
			raw := json.RawMessage(item.RawJSON())
			if len(raw) > 0 && json.Valid(raw) {
				canonical = append(canonical, ResponseItem{Type: ResponseItemTypeOther, ID: textutil.OptionalExactString(item.ID), OutputIndex: int64(outputIndex), Raw: raw})
			}
			continue
		}
		providerPhase := AbsentProviderPhase()
		if item.Type == "message" {
			var err error
			providerPhase, err = decodeProviderPhase(item.RawJSON())
			if err != nil {
				return nil, nil, "", nil, nil, nil, nil, err
			}
		}
		contribution, parseErr := parsed.Parse(item, providerPhase)
		if parseErr != nil {
			return nil, nil, "", nil, nil, nil, nil, parseErr
		}
		stampParsedOutputIndex(&contribution, int64(outputIndex))
		canonical = append(canonical, contribution.CanonicalItems...)
		assistantSegments = append(assistantSegments, contribution.AssistantSegments...)
		toolCalls = append(toolCalls, contribution.ToolCalls...)
		reasoning = append(reasoning, contribution.Reasoning...)
		reasoningItems = append(reasoningItems, contribution.ReasoningItems...)
	}

	assistantText, assistantPhase, providerPhase, _, _, _ := resolveAssistantOutput(assistantSegments)
	return canonical, assistantText, assistantPhase, providerPhase, toolCalls, reasoning, reasoningItems, nil
}

type providerPhaseEnvelope struct {
	Phase nullableProviderPhase `json:"phase"`
}

type nullableProviderPhase struct {
	phase   *ProviderPhase
	present bool
}

func (p *nullableProviderPhase) UnmarshalJSON(data []byte) error {
	p.present = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		p.phase = AbsentProviderPhase()
		return nil
	}
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("assistant phase must be commentary, final_answer, or null: %w", err)
	}
	switch MessagePhase(raw) {
	case MessagePhaseCommentary:
		p.phase = CommentaryProviderPhase()
	case MessagePhaseFinal:
		p.phase = FinalProviderPhase()
	default:
		return fmt.Errorf("assistant phase has unsupported value %q", raw)
	}
	return nil
}

func decodeProviderPhase(raw string) (*ProviderPhase, error) {
	var envelope providerPhaseEnvelope
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return nil, fmt.Errorf("decode assistant phase: %w", err)
	}
	if !envelope.Phase.present {
		return AbsentProviderPhase(), nil
	}
	return envelope.Phase.phase, nil
}

func stampParsedOutputIndex(parsed *parsedResponseOutputItem, outputIndex int64) {
	if parsed == nil {
		return
	}
	for idx := range parsed.CanonicalItems {
		parsed.CanonicalItems[idx].OutputIndex = outputIndex
	}
	for idx := range parsed.AssistantSegments {
		parsed.AssistantSegments[idx].OutputIndex = outputIndex
	}
	for idx := range parsed.Reasoning {
		if parsed.Reasoning[idx].SourceCoordinate == nil {
			parsed.Reasoning[idx].SourceCoordinate = &ReasoningSourceCoordinate{}
		}
		parsed.Reasoning[idx].SourceCoordinate.OutputIndex = textutil.Pointer(&outputIndex)
	}
}

type messageOutputItemParser struct{}

func (messageOutputItemParser) Parse(item responses.ResponseOutputItemUnion, providerPhase *ProviderPhase) (parsedResponseOutputItem, error) {
	role := Role(strings.TrimSpace(string(item.Role)))
	if role == "" {
		role = RoleAssistant
	}
	textParts := make([]string, 0, len(item.Content))
	for _, part := range item.Content {
		if part.Type == "output_text" || part.Type == "text" || part.Type == "input_text" {
			textParts = append(textParts, part.Text)
		}
	}
	text := strings.Join(textParts, "")
	phase := providerPhaseProjection(providerPhase)
	var typedPhase *MessagePhase
	if phase != "" {
		typedPhase = textutil.Value(phase)
	}
	content, err := resolveOpenAIMessageContent(item, role, phase, text)
	if err != nil {
		return parsedResponseOutputItem{}, err
	}
	raw := json.RawMessage(item.RawJSON())
	parsed := parsedResponseOutputItem{
		CanonicalItems: []ResponseItem{{
			Type:    ResponseItemTypeMessage,
			Role:    textutil.Value(role),
			Phase:   typedPhase,
			ID:      textutil.OptionalExactString(item.ID),
			Content: content,
			Raw:     raw,
		}},
	}
	if role == RoleAssistant {
		parsed.AssistantSegments = append(parsed.AssistantSegments, assistantOutputSegment{
			Text:          text,
			Content:       textutil.Pointer(content),
			Phase:         phase,
			ProviderPhase: providerPhase,
		})
	}
	return parsed, nil
}

func resolveOpenAIMessageContent(item responses.ResponseOutputItemUnion, role Role, phase MessagePhase, text string) (*string, error) {
	raw := strings.TrimSpace(item.JSON.Content.Raw())
	if raw == "" || raw == "null" {
		return nil, nil
	}
	if !item.JSON.Content.Valid() {
		return nil, fmt.Errorf("decode assistant content: provider content field has invalid JSON type")
	}
	var parts []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &parts); err != nil {
		return nil, fmt.Errorf("decode assistant content: %w", err)
	}
	hasTextPart, err := hasExplicitOpenAITextPart(parts)
	if err != nil {
		return nil, err
	}
	if len(parts) > 0 && !hasTextPart {
		return nil, nil
	}
	return resolveAssistantContent(role, phase, textutil.Value(text)), nil
}

func hasExplicitOpenAITextPart(parts []json.RawMessage) (bool, error) {
	for _, rawPart := range parts {
		var part struct {
			Type string          `json:"type"`
			Text json.RawMessage `json:"text"`
		}
		if err := json.Unmarshal(rawPart, &part); err != nil {
			return false, fmt.Errorf("decode assistant content part: %w", err)
		}
		if part.Type != "output_text" && part.Type != "text" && part.Type != "input_text" {
			continue
		}
		rawText := strings.TrimSpace(string(part.Text))
		if rawText == "" || rawText == "null" {
			continue
		}
		var text string
		if err := json.Unmarshal(part.Text, &text); err != nil {
			return false, fmt.Errorf("decode assistant text part: expected string: %w", err)
		}
		return true, nil
	}
	return false, nil
}

type functionCallOutputItemParser struct{}

func (functionCallOutputItemParser) Parse(item responses.ResponseOutputItemUnion, _ *ProviderPhase) (parsedResponseOutputItem, error) {
	call := item.AsFunctionCall()
	callID := textutil.FirstNonEmpty(strings.TrimSpace(call.CallID), strings.TrimSpace(call.ID))
	name := strings.TrimSpace(call.Name)
	if callID == "" && name == "" {
		return parsedResponseOutputItem{}, nil
	}
	arguments := normalizeToolInput(call.Arguments)
	raw := json.RawMessage(item.RawJSON())
	return parsedResponseOutputItem{
		CanonicalItems: []ResponseItem{{
			Type:      ResponseItemTypeFunctionCall,
			ID:        textutil.OptionalTrimmedString(call.ID),
			CallID:    textutil.OptionalTrimmedString(callID),
			Name:      textutil.OptionalExactString(call.Name),
			Arguments: arguments,
			Raw:       raw,
		}},
		ToolCalls: []ToolCall{{
			ID:    callID,
			Name:  call.Name,
			Input: arguments,
		}},
	}, nil
}

type reasoningOutputItemParser struct{}

type customToolCallOutputItemParser struct{}

func (customToolCallOutputItemParser) Parse(item responses.ResponseOutputItemUnion, _ *ProviderPhase) (parsedResponseOutputItem, error) {
	call := item.AsCustomToolCall()
	callID := textutil.FirstNonEmpty(strings.TrimSpace(call.CallID), strings.TrimSpace(call.ID))
	name := strings.TrimSpace(call.Name)
	if callID == "" && name == "" {
		return parsedResponseOutputItem{}, nil
	}
	raw := json.RawMessage(item.RawJSON())
	return parsedResponseOutputItem{
		CanonicalItems: []ResponseItem{{
			Type:        ResponseItemTypeCustomToolCall,
			ID:          textutil.OptionalTrimmedString(call.ID),
			CallID:      textutil.OptionalTrimmedString(callID),
			Name:        textutil.OptionalExactString(call.Name),
			CustomInput: textutil.OptionalExactString(call.Input),
			Raw:         raw,
		}},
		ToolCalls: []ToolCall{{
			ID:          callID,
			Name:        call.Name,
			Input:       normalizeToolInput(call.Input),
			Custom:      true,
			CustomInput: textutil.OptionalExactString(call.Input),
		}},
	}, nil
}

func (reasoningOutputItemParser) Parse(item responses.ResponseOutputItemUnion, _ *ProviderPhase) (parsedResponseOutputItem, error) {
	reasoningItem := item.AsReasoning()
	summaries := make([]ReasoningEntry, 0, len(reasoningItem.Summary))
	reasoning := make([]ReasoningEntry, 0, len(reasoningItem.Summary))
	for summaryIndex, summary := range reasoningItem.Summary {
		text := strings.TrimSpace(summary.Text)
		if text == "" {
			continue
		}
		index := int64(summaryIndex)
		identity := &ReasoningItemIdentity{PartIndex: &index}
		if itemID := strings.TrimSpace(reasoningItem.ID); itemID != "" {
			identity.ItemID = itemID
		} else {
			identity = nil
		}
		entry := ReasoningEntry{
			Role: textutil.Value(reasoningRoleSummary),
			Text: text,
			SourceCoordinate: &ReasoningSourceCoordinate{
				PartIndex: textutil.Pointer(&index),
			},
			ItemIdentity: identity,
		}
		summaries = append(summaries, entry)
		reasoning = append(reasoning, entry)
	}
	raw := json.RawMessage(item.RawJSON())
	parsed := parsedResponseOutputItem{
		CanonicalItems: []ResponseItem{{
			Type:             ResponseItemTypeReasoning,
			ID:               textutil.OptionalTrimmedString(reasoningItem.ID),
			ReasoningSummary: summaries,
			EncryptedContent: textutil.OptionalTrimmedString(reasoningItem.EncryptedContent),
			Raw:              raw,
		}},
		Reasoning: reasoning,
	}
	if id := strings.TrimSpace(reasoningItem.ID); id != "" {
		if encrypted := strings.TrimSpace(reasoningItem.EncryptedContent); encrypted != "" {
			parsed.ReasoningItems = append(parsed.ReasoningItems, ReasoningItem{ID: id, EncryptedContent: encrypted})
		}
	}
	return parsed, nil
}

type compactionOutputItemParser struct{}

func (compactionOutputItemParser) Parse(item responses.ResponseOutputItemUnion, _ *ProviderPhase) (parsedResponseOutputItem, error) {
	compactionItem := item.AsCompaction()
	return parsedResponseOutputItem{
		CanonicalItems: []ResponseItem{{
			Type:             ResponseItemTypeCompaction,
			ID:               textutil.OptionalTrimmedString(compactionItem.ID),
			EncryptedContent: textutil.OptionalTrimmedString(compactionItem.EncryptedContent),
			Raw:              json.RawMessage(item.RawJSON()),
		}},
	}, nil
}

type assistantOutputSegment struct {
	Text          string
	Content       *string
	DeltaText     string
	Phase         MessagePhase
	ProviderPhase *ProviderPhase
	OutputIndex   int64
}

func resolveAssistantOutput(segments []assistantOutputSegment) (*string, MessagePhase, *ProviderPhase, int64, string, bool) {
	if len(segments) == 0 {
		return nil, "", AbsentProviderPhase(), 0, "", false
	}
	sorted := append([]assistantOutputSegment(nil), segments...)
	slices.SortFunc(sorted, func(a, b assistantOutputSegment) int {
		return cmp.Compare(a.OutputIndex, b.OutputIndex)
	})
	last := len(sorted) - 1
	if sorted[last].Phase == "" {
		return textutil.Pointer(sorted[last].Content), "", sorted[last].ProviderPhase, sorted[last].OutputIndex, sorted[last].DeltaText, true
	}
	phase := sorted[last].Phase
	start := last
	for start > 0 {
		if sorted[start-1].Phase != phase {
			break
		}
		start--
	}
	textParts := make([]string, 0, last-start+1)
	deltaParts := make([]string, 0, last-start+1)
	contentPresent := false
	for i := start; i <= last; i++ {
		textParts = append(textParts, sorted[i].Text)
		deltaParts = append(deltaParts, sorted[i].DeltaText)
		contentPresent = contentPresent || sorted[i].Content != nil
	}
	if !contentPresent {
		return nil, phase, sorted[last].ProviderPhase, sorted[start].OutputIndex, strings.Join(deltaParts, ""), true
	}
	return textutil.Value(strings.Join(textParts, "")), phase, sorted[last].ProviderPhase, sorted[start].OutputIndex, strings.Join(deltaParts, ""), true
}
