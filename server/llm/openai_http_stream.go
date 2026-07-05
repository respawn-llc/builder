package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/shared/llmerrors"
	"core/shared/textutil"
	"github.com/openai/openai-go/v3/responses"
)

type responseStreamAccumulator struct {
	callbacks         StreamCallbacks
	windowTokens      int
	assistantText     strings.Builder
	assistantMessages *assistantMessageAccumulator
	toolCalls         *toolCallAccumulator
	reasoning         *reasoningAccumulator
	passthrough       *passthroughOutputAccumulator
	completed         *responses.Response
	responseError     *responseStreamError
}

type responseStreamError struct {
	Raw              string
	ProviderContract *responseStreamProviderContract
}

type responseStreamProviderContract struct {
	Message string
}

func newResponseStreamAccumulator(callbacks StreamCallbacks, windowTokens int) *responseStreamAccumulator {
	return &responseStreamAccumulator{
		callbacks:         callbacks,
		windowTokens:      windowTokens,
		assistantMessages: newAssistantMessageAccumulator(),
		toolCalls:         newToolCallAccumulator(),
		reasoning:         newReasoningAccumulator(),
		passthrough:       newPassthroughOutputAccumulator(),
	}
}

func (a *responseStreamAccumulator) hasCompleted() bool {
	return a != nil && a.completed != nil
}

func (a *responseStreamAccumulator) Consume(evt responses.ResponseStreamEventUnion) {
	switch evt.Type {
	case "response.output_text.delta":
		if evt.Delta == "" {
			return
		}
		a.assistantText.WriteString(evt.Delta)
		if a.callbacks.OnAssistantDelta != nil {
			a.callbacks.OnAssistantDelta(AssistantDelta{Text: evt.Delta, Phase: a.assistantMessages.Phase(evt.OutputIndex)})
		}
	case "response.output_item.added", "response.output_item.done":
		a.assistantMessages.Upsert(evt.Item, evt.OutputIndex)
		a.toolCalls.UpsertFromOutput(evt.Item)
		a.reasoning.UpsertReasoningItem(evt.Item)
		a.passthrough.Upsert(evt.Item, evt.OutputIndex)
	case "response.function_call_arguments.delta":
		a.toolCalls.AppendArguments(evt.ItemID, evt.Delta)
	case "response.function_call_arguments.done":
		a.toolCalls.SetArguments(evt.ItemID, evt.Arguments)
	case "response.custom_tool_call_input.delta":
		a.toolCalls.AppendCustomInput(evt.ItemID, evt.Delta)
	case "response.custom_tool_call_input.done":
		a.toolCalls.SetCustomInput(evt.ItemID, evt.Input)
	case "response.reasoning_summary_text.delta":
		key := reasoningEventKey(evt.ItemID, evt.OutputIndex, evt.SummaryIndex)
		a.reasoning.Append(reasoningRoleSummary, key, evt.Delta)
		a.emitReasoningSummaryDelta(key)
	case "response.reasoning_summary_text.done":
		key := reasoningEventKey(evt.ItemID, evt.OutputIndex, evt.SummaryIndex)
		a.reasoning.Set(reasoningRoleSummary, key, evt.Text)
		a.emitReasoningSummaryDelta(key)
	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		if evt.Part.Type != "summary_text" {
			return
		}
		key := reasoningEventKey(evt.ItemID, evt.OutputIndex, evt.SummaryIndex)
		a.reasoning.Set(reasoningRoleSummary, key, evt.Part.Text)
		a.emitReasoningSummaryDelta(key)
	case "response.completed":
		completedEvent := evt.AsResponseCompleted()
		if !completedEvent.JSON.Response.Valid() || !completedEvent.Response.JSON.Output.Valid() {
			a.responseError = &responseStreamError{
				Raw: completedEvent.RawJSON(),
				ProviderContract: &responseStreamProviderContract{
					Message: "OpenAI-compatible Responses stream emitted response.completed without a valid response payload",
				},
			}
			return
		}
		completed := completedEvent.Response
		a.completed = &completed
	case "response.failed":
		failed := evt.AsResponseFailed()
		a.responseError = &responseStreamError{Raw: failed.RawJSON()}
	case "response.incomplete":
		incomplete := evt.AsResponseIncomplete()
		raw := incomplete.RawJSON()
		if strings.TrimSpace(incomplete.Response.IncompleteDetails.Reason) == "" {
			a.responseError = &responseStreamError{
				Raw: raw,
				ProviderContract: &responseStreamProviderContract{
					Message: "OpenAI-compatible Responses stream emitted response.incomplete without incomplete_details.reason",
				},
			}
			return
		}
		a.responseError = &responseStreamError{Raw: raw}
	case "error":
		a.responseError = &responseStreamError{Raw: evt.RawJSON()}
	}
}

func (a *responseStreamAccumulator) Err(providerID string, responseStatus *openAIResponseStatus) error {
	if a == nil || a.responseError == nil {
		return nil
	}
	if responseStatus == nil {
		message := strings.TrimSpace(a.responseError.Raw)
		if a.responseError.ProviderContract != nil {
			message = a.responseError.ProviderContract.Message
		}
		if message == "" {
			message = "unrecognized stream error"
		}
		return errors.New(message)
	}
	if a.responseError.ProviderContract != nil {
		return llmerrors.NewProviderContractError(providerID, responseStatus.Code, errors.New(a.responseError.ProviderContract.Message))
	}
	if err, ok := mapOpenAIStreamErrorPayload(providerID, []byte(a.responseError.Raw), nil, responseStatus.Code); ok {
		return err
	}
	message := strings.TrimSpace(a.responseError.Raw)
	if message == "" {
		message = "unrecognized stream error"
	}
	return &ProviderAPIError{
		ProviderID: providerID,
		StatusCode: responseStatus.Code,
		Code:       UnifiedErrorCodeUnknown,
		Message:    message,
		Raw:        message,
	}
}

func (a *responseStreamAccumulator) emitReasoningSummaryDelta(key string) {
	if a.callbacks.OnReasoningSummaryDelta == nil {
		return
	}
	a.callbacks.OnReasoningSummaryDelta(reasoningSummaryDeltaFromText(key, reasoningRoleSummary, a.reasoning.Current(reasoningRoleSummary, key)))
}

func (a *responseStreamAccumulator) Response() OpenAIResponse {
	usage := Usage{WindowTokens: a.windowTokens}
	streamText, streamPhase, streamOutputIndex, hasResolvedStream := a.assistantMessages.Resolve()
	rawDeltaText := a.assistantText.String()
	streamedDeltaText := ""
	if a.callbacks.OnAssistantDelta != nil {
		streamedDeltaText = rawDeltaText
	}
	finalText := rawDeltaText
	if assistantResponseTextExtendsStream(streamedDeltaText, streamText) {
		finalText = streamText
	}
	finalPhase := streamPhase
	finalCalls := a.toolCalls.ToToolCalls()
	finalReasoning := a.reasoning.Entries()
	finalReasoningItems := a.reasoning.Items()
	finalOutputItems := mergePassthroughOutputItems(buildOutputItemsFromStream(finalText, finalPhase, finalCalls, finalReasoning, finalReasoningItems), a.passthrough.Items())

	if a.completed == nil {
		return OpenAIResponse{
			AssistantText:  finalText,
			AssistantPhase: finalPhase,
			ToolCalls:      finalCalls,
			Reasoning:      normalizeReasoningEntries(finalReasoning),
			ReasoningItems: finalReasoningItems,
			OutputItems:    finalOutputItems,
			Usage:          usage,
		}
	}

	if a.completed.Usage.InputTokens > 0 || a.completed.Usage.OutputTokens > 0 {
		usage = usageFromSDK(a.completed.Usage, a.windowTokens)
	}
	parsedItems, parsedText, parsedPhase, parsedCalls, parsedReasoning, parsedReasoningItems := parseOutputItems(a.completed.Output)
	if assistantResponseTextExtendsStream(streamedDeltaText, parsedText) {
		finalText = parsedText
	}
	if parsedPhase != "" {
		finalPhase = parsedPhase
	}
	a.toolCalls.Merge(parsedCalls)
	finalCalls = a.toolCalls.ToToolCalls()
	finalReasoning = normalizeReasoningEntries(mergeReasoningEntries(parsedReasoning, finalReasoning))
	finalReasoningItems = mergeReasoningItems(parsedReasoningItems, finalReasoningItems)
	if len(parsedItems) > 0 {
		finalOutputItems = mergePassthroughOutputItems(repairAssistantOutputItems(parsedItems, finalText, finalPhase, streamOutputIndex, hasResolvedStream), a.passthrough.Items())
	}

	return OpenAIResponse{
		AssistantText:  finalText,
		AssistantPhase: finalPhase,
		ToolCalls:      finalCalls,
		Reasoning:      finalReasoning,
		ReasoningItems: finalReasoningItems,
		OutputItems:    finalOutputItems,
		Usage:          usage,
	}
}

func assistantResponseTextExtendsStream(streamed string, candidate string) bool {
	if candidate == "" {
		return false
	}
	if streamed == "" {
		return true
	}
	return strings.HasPrefix(candidate, streamed)
}

func repairAssistantOutputItems(items []ResponseItem, text string, phase MessagePhase, outputIndex int64, hasResolvedStream bool) []ResponseItem {
	if len(items) == 0 {
		return nil
	}
	repaired := CloneResponseItems(items)
	assistantIndexes := make([]int, 0, len(repaired))
	for idx := len(repaired) - 1; idx >= 0; idx-- {
		if repaired[idx].Type == ResponseItemTypeMessage && repaired[idx].Role == RoleAssistant {
			assistantIndexes = append(assistantIndexes, idx)
		}
	}
	if len(assistantIndexes) == 0 {
		if strings.TrimSpace(text) == "" {
			return repaired
		}
		assistant := ResponseItem{
			Type:        ResponseItemTypeMessage,
			OutputIndex: outputIndex,
			Role:        RoleAssistant,
			Phase:       phase,
			Content:     text,
		}
		if !hasResolvedStream {
			return append([]ResponseItem{assistant}, repaired...)
		}
		insertAt := len(repaired)
		for idx, item := range repaired {
			if item.OutputIndex > outputIndex {
				insertAt = idx
				break
			}
		}
		repaired = append(repaired, ResponseItem{})
		copy(repaired[insertAt+1:], repaired[insertAt:])
		repaired[insertAt] = assistant
		return repaired
	}
	targetAssistantIdx := assistantIndexes[0]
	if hasResolvedStream {
		for _, idx := range assistantIndexes {
			if repaired[idx].OutputIndex == outputIndex {
				targetAssistantIdx = idx
				break
			}
		}
	}
	if len(assistantIndexes) == 1 && text != "" && repaired[targetAssistantIdx].Content != text {
		repaired[targetAssistantIdx].Content = text
	}
	if repaired[targetAssistantIdx].Phase == "" && phase != "" {
		repaired[targetAssistantIdx].Phase = phase
	}
	return repaired
}

func mergeReasoningEntries(primary, secondary []ReasoningEntry) []ReasoningEntry {
	out := make([]ReasoningEntry, 0, len(primary)+len(secondary))
	seen := make(map[string]struct{}, len(primary)+len(secondary))
	appendEntries := func(entries []ReasoningEntry) {
		for _, entry := range entries {
			role := strings.TrimSpace(entry.Role)
			text := strings.TrimSpace(entry.Text)
			if role == "" || text == "" {
				continue
			}
			key := role + "\x00" + text
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, ReasoningEntry{Role: role, Text: text})
		}
	}
	appendEntries(primary)
	appendEntries(secondary)
	return out
}

func mergeReasoningItems(primary, secondary []ReasoningItem) []ReasoningItem {
	out := make([]ReasoningItem, 0, len(primary)+len(secondary))
	seen := make(map[string]struct{}, len(primary)+len(secondary))
	appendItems := func(items []ReasoningItem) {
		for _, item := range items {
			id := strings.TrimSpace(item.ID)
			encrypted := strings.TrimSpace(item.EncryptedContent)
			if id == "" || encrypted == "" {
				continue
			}
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, ReasoningItem{ID: id, EncryptedContent: encrypted})
		}
	}
	appendItems(primary)
	appendItems(secondary)
	return out
}

func reasoningEventKey(itemID string, outputIndex, partIndex int64) string {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return fmt.Sprintf("output:%d:part:%d", outputIndex, partIndex)
	}
	return fmt.Sprintf("%s:part:%d", itemID, partIndex)
}

type reasoningAccumulator struct {
	order         []string
	items         map[string]*ReasoningEntry
	reasoningIDs  []string
	reasoningByID map[string]ReasoningItem
}

type assistantMessageAccumulator struct {
	byIndex map[int64]ResponseItem
	order   []int64
}

func newAssistantMessageAccumulator() *assistantMessageAccumulator {
	return &assistantMessageAccumulator{
		byIndex: make(map[int64]ResponseItem),
		order:   make([]int64, 0, 4),
	}
}

func (a *assistantMessageAccumulator) Upsert(item responses.ResponseOutputItemUnion, outputIndex int64) {
	if a == nil || item.Type != "message" {
		return
	}
	parsedItems, _, _, _, _, _ := parseOutputItems([]responses.ResponseOutputItemUnion{item})
	if len(parsedItems) == 0 {
		return
	}
	assistant := parsedItems[0]
	if assistant.Type != ResponseItemTypeMessage || assistant.Role != RoleAssistant {
		return
	}
	if _, exists := a.byIndex[outputIndex]; !exists {
		a.order = append(a.order, outputIndex)
	}
	a.byIndex[outputIndex] = assistant
}

func (a *assistantMessageAccumulator) Resolve() (string, MessagePhase, int64, bool) {
	if a == nil {
		return "", "", 0, false
	}
	segments := make([]assistantOutputSegment, 0, len(a.order))
	for _, outputIndex := range a.order {
		item, ok := a.byIndex[outputIndex]
		if !ok || item.Type != ResponseItemTypeMessage || item.Role != RoleAssistant {
			continue
		}
		segments = append(segments, assistantOutputSegment{Text: item.Content, Phase: item.Phase, OutputIndex: outputIndex})
	}
	return resolveAssistantOutput(segments)
}

func (a *assistantMessageAccumulator) Phase(outputIndex int64) MessagePhase {
	if a == nil {
		return ""
	}
	item, ok := a.byIndex[outputIndex]
	if !ok || item.Type != ResponseItemTypeMessage || item.Role != RoleAssistant {
		return ""
	}
	return item.Phase
}

func newReasoningAccumulator() *reasoningAccumulator {
	return &reasoningAccumulator{
		order:         make([]string, 0, 8),
		items:         make(map[string]*ReasoningEntry, 8),
		reasoningIDs:  make([]string, 0, 4),
		reasoningByID: make(map[string]ReasoningItem, 4),
	}
}

func (a *reasoningAccumulator) ensure(role, key string) *ReasoningEntry {
	role = strings.TrimSpace(role)
	key = strings.TrimSpace(key)
	if role == "" || key == "" {
		return nil
	}
	composite := role + "\x00" + key
	if item, ok := a.items[composite]; ok {
		return item
	}
	entry := &ReasoningEntry{Role: role}
	a.items[composite] = entry
	a.order = append(a.order, composite)
	return entry
}

func (a *reasoningAccumulator) Append(role, key, delta string) {
	if delta == "" {
		return
	}
	entry := a.ensure(role, key)
	if entry == nil {
		return
	}
	entry.Text += delta
}

func (a *reasoningAccumulator) Set(role, key, text string) {
	if text == "" {
		return
	}
	entry := a.ensure(role, key)
	if entry == nil {
		return
	}
	entry.Text = text
}

func (a *reasoningAccumulator) Current(role, key string) string {
	entry := a.ensure(role, key)
	if entry == nil {
		return ""
	}
	return entry.Text
}

func (a *reasoningAccumulator) Entries() []ReasoningEntry {
	if a == nil {
		return nil
	}
	out := make([]ReasoningEntry, 0, len(a.order))
	for _, key := range a.order {
		entry, ok := a.items[key]
		if !ok {
			continue
		}
		text := strings.TrimSpace(entry.Text)
		if text == "" {
			continue
		}
		out = append(out, ReasoningEntry{Role: entry.Role, Text: text})
	}
	return out
}

func (a *reasoningAccumulator) UpsertReasoningItem(item responses.ResponseOutputItemUnion) {
	if item.Type != "reasoning" {
		return
	}
	reasoningItem := item.AsReasoning()
	id := strings.TrimSpace(reasoningItem.ID)
	if id == "" {
		return
	}
	for idx, summary := range reasoningItem.Summary {
		key := fmt.Sprintf("%s:summary:%d", id, idx)
		a.Set(reasoningRoleSummary, key, summary.Text)
	}
	encrypted := strings.TrimSpace(reasoningItem.EncryptedContent)
	if encrypted == "" {
		return
	}
	if _, exists := a.reasoningByID[id]; !exists {
		a.reasoningIDs = append(a.reasoningIDs, id)
	}
	a.reasoningByID[id] = ReasoningItem{ID: id, EncryptedContent: encrypted}
}

func (a *reasoningAccumulator) Items() []ReasoningItem {
	if a == nil {
		return nil
	}
	out := make([]ReasoningItem, 0, len(a.reasoningIDs))
	for _, id := range a.reasoningIDs {
		item, ok := a.reasoningByID[id]
		if !ok {
			continue
		}
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.EncryptedContent) == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}

type toolCallAccumulator struct {
	byKey     map[string]*toolCallState
	itemToKey map[string]string
	order     []string
}

type toolCallState struct {
	CallID   string
	Name     string
	Args     strings.Builder
	Custom   strings.Builder
	IsCustom bool
}

func newToolCallAccumulator() *toolCallAccumulator {
	return &toolCallAccumulator{
		byKey:     map[string]*toolCallState{},
		itemToKey: map[string]string{},
		order:     []string{},
	}
}

func (a *toolCallAccumulator) ensure(key string) *toolCallState {
	if key == "" {
		return nil
	}
	if state, ok := a.byKey[key]; ok {
		return state
	}
	state := &toolCallState{CallID: key}
	a.byKey[key] = state
	a.order = append(a.order, key)
	return state
}

func (a *toolCallAccumulator) UpsertFromOutput(item responses.ResponseOutputItemUnion) {
	if item.Type != "function_call" && item.Type != "custom_tool_call" {
		return
	}
	callID := ""
	id := ""
	name := ""
	args := ""
	isCustom := item.Type == "custom_tool_call"
	if isCustom {
		call := item.AsCustomToolCall()
		callID = call.CallID
		id = call.ID
		name = call.Name
		args = call.Input
	} else {
		call := item.AsFunctionCall()
		callID = call.CallID
		id = call.ID
		name = call.Name
		args = call.Arguments
	}
	key := textutil.FirstNonEmpty(strings.TrimSpace(callID), strings.TrimSpace(id))
	if key == "" {
		return
	}
	state := a.ensure(key)
	if state == nil {
		return
	}
	if v := strings.TrimSpace(callID); v != "" {
		state.CallID = v
	}
	if v := strings.TrimSpace(name); v != "" {
		state.Name = v
	}
	if id != "" {
		a.itemToKey[id] = key
	}
	if isCustom {
		state.IsCustom = true
		if strings.TrimSpace(args) != "" {
			state.Custom.Reset()
			state.Custom.WriteString(args)
		}
	} else if strings.TrimSpace(args) != "" {
		state.Args.Reset()
		state.Args.WriteString(args)
	}
}

func (a *toolCallAccumulator) AppendArguments(itemID, delta string) {
	key := textutil.FirstNonEmpty(strings.TrimSpace(a.itemToKey[itemID]), strings.TrimSpace(itemID))
	state := a.ensure(key)
	if state == nil || strings.TrimSpace(delta) == "" {
		return
	}
	state.Args.WriteString(delta)
}

func (a *toolCallAccumulator) SetArguments(itemID, arguments string) {
	key := textutil.FirstNonEmpty(strings.TrimSpace(a.itemToKey[itemID]), strings.TrimSpace(itemID))
	state := a.ensure(key)
	if state == nil {
		return
	}
	state.Args.Reset()
	state.Args.WriteString(arguments)
}

func (a *toolCallAccumulator) AppendCustomInput(itemID, delta string) {
	key := textutil.FirstNonEmpty(strings.TrimSpace(a.itemToKey[itemID]), strings.TrimSpace(itemID))
	state := a.ensure(key)
	if state == nil || delta == "" {
		return
	}
	state.IsCustom = true
	state.Custom.WriteString(delta)
}

func (a *toolCallAccumulator) SetCustomInput(itemID, input string) {
	key := textutil.FirstNonEmpty(strings.TrimSpace(a.itemToKey[itemID]), strings.TrimSpace(itemID))
	state := a.ensure(key)
	if state == nil {
		return
	}
	state.IsCustom = true
	state.Custom.Reset()
	state.Custom.WriteString(input)
}

func (a *toolCallAccumulator) Merge(calls []ToolCall) {
	for _, call := range calls {
		key := textutil.FirstNonEmpty(strings.TrimSpace(call.ID), strings.TrimSpace(call.Name))
		state := a.ensure(key)
		if state == nil {
			continue
		}
		if v := strings.TrimSpace(call.ID); v != "" {
			state.CallID = v
		}
		if v := strings.TrimSpace(call.Name); v != "" {
			state.Name = v
		}
		if call.Custom {
			state.IsCustom = true
			if call.CustomInput != "" {
				state.Custom.Reset()
				state.Custom.WriteString(call.CustomInput)
			}
		} else if len(call.Input) > 0 {
			state.Args.Reset()
			state.Args.WriteString(normalizeToolArguments(string(call.Input)))
		}
	}
}

func (a *toolCallAccumulator) ToToolCalls() []ToolCall {
	out := make([]ToolCall, 0, len(a.order))
	for _, key := range a.order {
		state, ok := a.byKey[key]
		if !ok {
			continue
		}
		callID := textutil.FirstNonEmpty(strings.TrimSpace(state.CallID), key)
		if callID == "" && strings.TrimSpace(state.Name) == "" {
			continue
		}
		input := normalizeToolInput(state.Args.String())
		if state.IsCustom {
			input = normalizeToolInput(state.Custom.String())
		}
		out = append(out, ToolCall{ID: callID, Name: state.Name, Input: input, Custom: state.IsCustom, CustomInput: state.Custom.String()})
	}
	return out
}

func buildOutputItemsFromStream(text string, phase MessagePhase, toolCalls []ToolCall, reasoning []ReasoningEntry, reasoningItems []ReasoningItem) []ResponseItem {
	items := make([]ResponseItem, 0, 1+len(toolCalls)+len(reasoningItems))
	if strings.TrimSpace(text) != "" {
		items = append(items, ResponseItem{Type: ResponseItemTypeMessage, Role: RoleAssistant, Phase: phase, Content: text})
	}
	for _, call := range toolCalls {
		callID := textutil.FirstNonEmpty(strings.TrimSpace(call.ID), strings.TrimSpace(call.Name))
		if callID == "" {
			continue
		}
		if call.Custom {
			items = append(items, ResponseItem{Type: ResponseItemTypeCustomToolCall, ID: callID, CallID: callID, Name: call.Name, CustomInput: call.CustomInput})
		} else {
			items = append(items, ResponseItem{Type: ResponseItemTypeFunctionCall, ID: callID, CallID: callID, Name: call.Name, Arguments: normalizeToolInput(string(call.Input))})
		}
	}
	summaries := make([]ReasoningEntry, 0, len(reasoning))
	for _, entry := range reasoning {
		text := strings.TrimSpace(entry.Text)
		if text == "" {
			continue
		}
		summaries = append(summaries, ReasoningEntry{Role: entry.Role, Text: text})
	}
	for _, item := range reasoningItems {
		id := strings.TrimSpace(item.ID)
		encrypted := strings.TrimSpace(item.EncryptedContent)
		if id == "" || encrypted == "" {
			continue
		}
		items = append(items, ResponseItem{
			Type:             ResponseItemTypeReasoning,
			ID:               id,
			EncryptedContent: encrypted,
			ReasoningSummary: append([]ReasoningEntry(nil), summaries...),
		})
	}
	return items
}

type passthroughOutputAccumulator struct {
	byIndex map[int64]ResponseItem
	order   []int64
}

func newPassthroughOutputAccumulator() *passthroughOutputAccumulator {
	return &passthroughOutputAccumulator{
		byIndex: make(map[int64]ResponseItem),
		order:   make([]int64, 0, 4),
	}
}

func (a *passthroughOutputAccumulator) Upsert(item responses.ResponseOutputItemUnion, outputIndex int64) {
	if a == nil || isKnownResponseOutputItemType(item.Type) {
		return
	}
	raw := json.RawMessage(item.RawJSON())
	if len(raw) == 0 || !json.Valid(raw) {
		return
	}
	if _, exists := a.byIndex[outputIndex]; !exists {
		a.order = append(a.order, outputIndex)
	}
	copyRaw := append(json.RawMessage(nil), raw...)
	a.byIndex[outputIndex] = ResponseItem{Type: ResponseItemTypeOther, OutputIndex: outputIndex, Raw: copyRaw}
}

func (a *passthroughOutputAccumulator) Items() []ResponseItem {
	if a == nil {
		return nil
	}
	out := make([]ResponseItem, 0, len(a.order))
	for _, outputIndex := range a.order {
		item, ok := a.byIndex[outputIndex]
		if !ok {
			continue
		}
		copyItem := item
		copyItem.Raw = append(json.RawMessage(nil), item.Raw...)
		out = append(out, copyItem)
	}
	return out
}

func mergePassthroughOutputItems(items []ResponseItem, passthrough []ResponseItem) []ResponseItem {
	if len(passthrough) == 0 {
		return items
	}
	out := CloneResponseItems(items)
	seen := make(map[string]struct{}, len(out))
	for _, item := range out {
		if item.Type != ResponseItemTypeOther || len(item.Raw) == 0 {
			continue
		}
		seen[fmt.Sprintf("%d\x00%s", item.OutputIndex, string(item.Raw))] = struct{}{}
	}
	for _, item := range passthrough {
		if item.Type != ResponseItemTypeOther || len(item.Raw) == 0 {
			continue
		}
		key := fmt.Sprintf("%d\x00%s", item.OutputIndex, string(item.Raw))
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		copyItem := item
		copyItem.Raw = append(json.RawMessage(nil), item.Raw...)
		out = append(out, copyItem)
	}
	return out
}

func isKnownResponseOutputItemType(itemType string) bool {
	switch strings.TrimSpace(itemType) {
	case "message", "function_call", "custom_tool_call", "reasoning", "compaction":
		return true
	default:
		return false
	}
}
