package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"core/shared/llmerrors"
	"core/shared/textutil"
	"github.com/openai/openai-go/v3/responses"
)

type responseStreamAccumulator struct {
	callbacks                StreamCallbacks
	windowTokens             int
	assistantText            strings.Builder
	assistantStartedByOutput map[int64]bool
	assistantMessages        *assistantMessageAccumulator
	toolCalls                *toolCallAccumulator
	reasoning                *reasoningAccumulator
	passthrough              *passthroughOutputAccumulator
	standardServedModel      *string
	completed                *responses.Response
	responseError            *responseStreamError
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
	if err := validateReasoningStreamEvent(evt); err != nil {
		a.recordReasoningProviderError(err)
		return
	}
	switch evt.Type {
	case "response.created":
		created := evt.AsResponseCreated()
		observeStandardServedModel(&a.standardServedModel, string(created.Response.Model))
	case "response.output_text.delta":
		if evt.Delta == "" {
			return
		}
		a.consumeAssistantDelta(evt.OutputIndex, evt.Delta)
	case "response.output_text.done":
		a.assistantMessages.SetFinalizedText(evt.OutputIndex, evt.Text)
	case "response.output_item.added", "response.output_item.done":
		if err := a.assistantMessages.Upsert(evt.Item, evt.OutputIndex); err != nil {
			a.responseError = &responseStreamError{
				ProviderContract: &responseStreamProviderContract{Message: err.Error()},
			}
			return
		}
		a.toolCalls.UpsertFromOutput(evt.Item)
		a.reasoning.UpsertReasoningItem(evt.Item, evt.OutputIndex)
		a.recordReasoningAccumulatorError()
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
		coordinate, coordinateErr := reasoningSourceCoordinate(evt.OutputIndex, evt.SummaryIndex)
		if coordinateErr != nil {
			a.recordReasoningProviderError(coordinateErr)
			return
		}
		identity, identityErr := reasoningItemIdentity(evt.ItemID, evt.SummaryIndex)
		if identityErr != nil {
			a.recordReasoningProviderError(identityErr)
			return
		}
		a.reasoning.Append(reasoningRoleSummary, coordinate, identity, evt.Delta)
		a.recordReasoningAccumulatorError()
		a.emitReasoningSummaryDelta(coordinate)
	case "response.reasoning_summary_text.done":
		coordinate, coordinateErr := reasoningSourceCoordinate(evt.OutputIndex, evt.SummaryIndex)
		if coordinateErr != nil {
			a.recordReasoningProviderError(coordinateErr)
			return
		}
		identity, identityErr := reasoningItemIdentity(evt.ItemID, evt.SummaryIndex)
		if identityErr != nil {
			a.recordReasoningProviderError(identityErr)
			return
		}
		a.reasoning.Set(reasoningRoleSummary, coordinate, identity, evt.Text)
		a.recordReasoningAccumulatorError()
		a.emitReasoningSummaryDelta(coordinate)
	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		if evt.Part.Type != "summary_text" {
			return
		}
		coordinate, coordinateErr := reasoningSourceCoordinate(evt.OutputIndex, evt.SummaryIndex)
		if coordinateErr != nil {
			a.recordReasoningProviderError(coordinateErr)
			return
		}
		identity, identityErr := reasoningItemIdentity(evt.ItemID, evt.SummaryIndex)
		if identityErr != nil {
			a.recordReasoningProviderError(identityErr)
			return
		}
		a.reasoning.Set(reasoningRoleSummary, coordinate, identity, evt.Part.Text)
		a.recordReasoningAccumulatorError()
		a.emitReasoningSummaryDelta(coordinate)
	case "response.completed":
		completedEvent := evt.AsResponseCompleted()
		if !responseCompletedEventHasValidPayload(completedEvent) {
			a.responseError = &responseStreamError{
				Raw: completedEvent.RawJSON(),
				ProviderContract: &responseStreamProviderContract{
					Message: "OpenAI-compatible Responses stream emitted response.completed without a valid response payload",
				},
			}
			return
		}
		completed := completedEvent.Response
		observeStandardServedModel(&a.standardServedModel, string(completed.Model))
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

func (a *responseStreamAccumulator) consumeAssistantDelta(outputIndex int64, text string) {
	phase := a.assistantMessages.Phase(outputIndex)
	if a.assistantStartedByOutput == nil {
		a.assistantStartedByOutput = make(map[int64]bool)
	}
	if !a.assistantStartedByOutput[outputIndex] {
		// Some OpenAI-compatible streams emit provisional leading whitespace
		// before the assistant's durable content is known.
		text = strings.TrimLeftFunc(text, unicode.IsSpace)
	}
	if text == "" {
		return
	}
	a.assistantMessages.AppendDelta(outputIndex, text)
	a.emitAssistantDelta(AssistantDelta{Text: text, Phase: phase})
	a.assistantStartedByOutput[outputIndex] = true
}

func (a *responseStreamAccumulator) emitAssistantDelta(delta AssistantDelta) {
	a.assistantText.WriteString(delta.Text)
	if a.callbacks.OnAssistantDelta != nil {
		a.callbacks.OnAssistantDelta(delta)
	}
}

func responseCompletedEventHasValidPayload(evt responses.ResponseCompletedEvent) bool {
	if !evt.JSON.Response.Valid() || !evt.Response.JSON.Output.Valid() {
		return false
	}
	var output []json.RawMessage
	if err := json.Unmarshal([]byte(evt.Response.JSON.Output.Raw()), &output); err != nil {
		return false
	}
	return output != nil
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

func (a *responseStreamAccumulator) emitReasoningSummaryDelta(coordinate reasoningCoordinate) {
	if a.callbacks.OnReasoningSummaryDelta == nil || a.reasoning == nil || a.reasoning.err != nil {
		return
	}
	entry := a.reasoning.Current(reasoningRoleSummary, coordinate)
	if entry == nil {
		return
	}
	a.callbacks.OnReasoningSummaryDelta(reasoningSummaryDeltaFromText(
		entry.SourceCoordinate,
		entry.ItemIdentity,
		reasoningRoleSummary,
		entry.Text,
	))
}

func (a *responseStreamAccumulator) recordReasoningProviderError(err error) {
	if a == nil || err == nil || a.responseError != nil {
		return
	}
	a.responseError = &responseStreamError{
		ProviderContract: &responseStreamProviderContract{Message: err.Error()},
	}
}

func (a *responseStreamAccumulator) recordReasoningAccumulatorError() {
	if a == nil || a.reasoning == nil || a.reasoning.err == nil || a.responseError != nil {
		return
	}
	a.responseError = &responseStreamError{
		ProviderContract: &responseStreamProviderContract{
			Message: a.reasoning.err.Error(),
		},
	}
}

func (a *responseStreamAccumulator) Response() (OpenAIResponse, error) {
	usage := Usage{WindowTokens: a.windowTokens}
	streamText, streamPhase, streamProviderPhase, streamOutputIndex, streamDeltaText, hasResolvedStream := a.assistantMessages.Resolve()
	rawDeltaText := a.assistantText.String()
	streamedDeltaText := ""
	if a.callbacks.OnAssistantDelta != nil {
		streamedDeltaText = rawDeltaText
	}
	finalText := textutil.Pointer(streamText)
	finalTextPresent := hasResolvedStream && streamText != nil
	if hasResolvedStream && streamText == nil && streamDeltaText != "" {
		finalText = textutil.Value(streamDeltaText)
		finalTextPresent = true
	}
	if !hasResolvedStream && rawDeltaText != "" {
		finalText = textutil.Value(rawDeltaText)
		finalTextPresent = true
	}
	finalPhase := streamPhase
	finalProviderPhase := streamProviderPhase
	finalCalls := a.toolCalls.ToToolCalls()
	finalReasoning := a.reasoning.Entries()
	finalReasoningItems := a.reasoning.Items()
	finalOutputItems := mergePassthroughOutputItems(buildOutputItemsFromStream(finalText, finalTextPresent, finalPhase, finalCalls, finalReasoning, finalReasoningItems), a.passthrough.Items())

	if a.completed == nil {
		return OpenAIResponse{
			AssistantText:  finalText,
			ProviderPhase:  finalProviderPhase,
			ToolCalls:      finalCalls,
			Reasoning:      normalizeReasoningEntries(finalReasoning),
			ReasoningItems: finalReasoningItems,
			OutputItems:    finalOutputItems,
			Usage:          usage,
		}, nil
	}

	if a.completed.Usage.InputTokens > 0 || a.completed.Usage.OutputTokens > 0 {
		usage = usageFromSDK(a.completed.Usage, a.windowTokens)
	}
	parsedItems, parsedText, parsedPhase, parsedProviderPhase, parsedCalls, parsedReasoning, parsedReasoningItems, err := parseOutputItems(a.completed.Output)
	if err != nil {
		return OpenAIResponse{}, err
	}
	parsedTextValue := ""
	if parsedText != nil {
		parsedTextValue = *parsedText
	}
	reconciliationText := streamDeltaText
	if !hasResolvedStream {
		reconciliationText = streamedDeltaText
	}
	if a.callbacks.OnAssistantDelta == nil && parsedText != nil {
		reconciliationText = ""
	}
	// A done-only stream can resolve an explicit empty text item before the
	// completed payload supplies its authoritative final phase. Preserve that
	// presence only for a fully resolved stream; pending unmatched deltas must
	// continue to fail reconciliation.
	reconciled := parsedText != nil &&
		(completedAssistantTextReconcilesStream(reconciliationText, parsedTextValue) ||
			(hasResolvedStream && reconciliationText == "" && parsedTextValue == ""))
	if reconciled {
		finalText = textutil.Value(reconciledCompletedAssistantText(reconciliationText, parsedTextValue))
		finalTextPresent = true
	} else if parsedText != nil && !hasResolvedStream && rawDeltaText == "" {
		finalText = textutil.Pointer(parsedText)
		finalTextPresent = true
	}
	streamDeltaConflict := streamDeltaText != "" &&
		(parsedText == nil || !completedAssistantTextReconcilesStream(streamDeltaText, parsedTextValue))
	if responseItemsContainAssistantMessage(parsedItems) && !reconciled &&
		(optionalStringsDiffer(finalText, parsedText) || streamDeltaConflict) {
		return OpenAIResponse{}, fmt.Errorf(
			"completed assistant content conflicts with streamed assistant content: streamed bytes=%d completed bytes=%d",
			lenOptionalString(finalText),
			lenOptionalString(parsedText),
		)
	}
	if parsedPhase != "" {
		finalPhase = parsedPhase
		finalProviderPhase = parsedProviderPhase
	}
	a.toolCalls.Merge(parsedCalls)
	finalCalls = a.toolCalls.ToToolCalls()
	mergedReasoning, mergeErr := mergeReasoningEntries(parsedReasoning, finalReasoning)
	if mergeErr != nil {
		return OpenAIResponse{}, mergeErr
	}
	finalReasoning = normalizeReasoningEntries(mergedReasoning)
	finalReasoningItems = mergeReasoningItems(parsedReasoningItems, finalReasoningItems)
	if len(parsedItems) > 0 {
		finalOutputItems = mergePassthroughOutputItems(repairAssistantOutputItems(parsedItems, finalText, finalTextPresent, finalPhase, streamOutputIndex, hasResolvedStream), a.passthrough.Items())
	}

	return OpenAIResponse{
		AssistantText:  finalText,
		ProviderPhase:  finalProviderPhase,
		ServedModel:    textutil.Pointer(a.standardServedModel),
		ToolCalls:      finalCalls,
		Reasoning:      finalReasoning,
		ReasoningItems: finalReasoningItems,
		OutputItems:    finalOutputItems,
		Usage:          usage,
	}, nil
}

func responseItemsContainAssistantMessage(items []ResponseItem) bool {
	for _, item := range items {
		if item.Type == ResponseItemTypeMessage && item.Role != nil && *item.Role == RoleAssistant {
			return true
		}
	}
	return false
}

func lenOptionalString(value *string) int {
	if value == nil {
		return 0
	}
	return len(*value)
}

func optionalStringsDiffer(left *string, right *string) bool {
	if left == nil || right == nil {
		return left != right
	}
	return *left != *right
}

func AssistantResponseTextExtendsStream(streamed string, candidate string) bool {
	if candidate == "" {
		return false
	}
	if streamed == "" {
		return true
	}
	return strings.HasPrefix(candidate, streamed)
}

func completedAssistantTextReconcilesStream(streamed string, completed string) bool {
	return AssistantResponseTextExtendsStream(streamed, completed) ||
		(streamed != "" &&
			completed != "" &&
			(strings.TrimRightFunc(streamed, unicode.IsSpace) == completed ||
				streamed == strings.TrimLeftFunc(completed, unicode.IsSpace)))
}

func reconciledCompletedAssistantText(streamed string, completed string) string {
	// Streamed bytes are already visible to clients. A completed payload may
	// normalize provisional whitespace, but it must never retract that prefix.
	if streamed != "" &&
		(streamed == strings.TrimLeftFunc(completed, unicode.IsSpace) ||
			strings.TrimRightFunc(streamed, unicode.IsSpace) == completed) {
		return streamed
	}
	return completed
}

func repairAssistantOutputItems(items []ResponseItem, text *string, textPresent bool, phase MessagePhase, outputIndex int64, hasResolvedStream bool) []ResponseItem {
	if len(items) == 0 {
		return nil
	}
	repaired := CloneResponseItems(items)
	assistantIndexes := make([]int, 0, len(repaired))
	for idx := len(repaired) - 1; idx >= 0; idx-- {
		if repaired[idx].Type == ResponseItemTypeMessage &&
			repaired[idx].Role != nil &&
			*repaired[idx].Role == RoleAssistant {
			assistantIndexes = append(assistantIndexes, idx)
		}
	}
	if len(assistantIndexes) == 0 {
		if !textPresent {
			return repaired
		}
		assistant := ResponseItem{
			Type:        ResponseItemTypeMessage,
			OutputIndex: outputIndex,
			Role:        textutil.Value(RoleAssistant),
			Phase:       optionalMessagePhase(phase),
			Content:     textutil.Pointer(text),
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
	if len(assistantIndexes) == 1 && textPresent && text != nil &&
		(repaired[targetAssistantIdx].Content == nil ||
			*repaired[targetAssistantIdx].Content != *text) {
		repaired[targetAssistantIdx].Content = textutil.Pointer(text)
	}
	if repaired[targetAssistantIdx].Phase == nil && phase != "" {
		repaired[targetAssistantIdx].Phase = textutil.Value(phase)
	}
	return repaired
}

type assistantMessageAccumulator struct {
	byIndex       map[int64]assistantAccumulatorItem
	order         []int64
	pendingDeltas map[int64]*strings.Builder
}

type assistantAccumulatorItem struct {
	message       ResponseItem
	providerPhase *ProviderPhase
	finalizedText *string
	deltaText     *strings.Builder
}

func newAssistantMessageAccumulator() *assistantMessageAccumulator {
	return &assistantMessageAccumulator{
		byIndex:       make(map[int64]assistantAccumulatorItem),
		order:         make([]int64, 0, 4),
		pendingDeltas: make(map[int64]*strings.Builder),
	}
}

func (a *assistantMessageAccumulator) Upsert(item responses.ResponseOutputItemUnion, outputIndex int64) error {
	if a == nil || item.Type != "message" {
		return nil
	}
	parsedItems, _, _, providerPhase, _, _, _, err := parseOutputItems([]responses.ResponseOutputItemUnion{item})
	if err != nil {
		return err
	}
	if len(parsedItems) == 0 {
		return nil
	}
	assistant := parsedItems[0]
	if assistant.Type != ResponseItemTypeMessage ||
		assistant.Role == nil ||
		*assistant.Role != RoleAssistant {
		return nil
	}
	current, exists := a.byIndex[outputIndex]
	if !exists {
		a.order = append(a.order, outputIndex)
	}
	deltaText := current.deltaText
	if pending := a.pendingDeltas[outputIndex]; pending != nil {
		deltaText = pending
		delete(a.pendingDeltas, outputIndex)
	}
	a.byIndex[outputIndex] = assistantAccumulatorItem{
		message:       assistant,
		providerPhase: providerPhase,
		finalizedText: current.finalizedText,
		deltaText:     deltaText,
	}
	return nil
}

func (a *assistantMessageAccumulator) AppendDelta(outputIndex int64, text string) {
	if a == nil || text == "" {
		return
	}
	item, exists := a.byIndex[outputIndex]
	if !exists {
		buffer := a.pendingDeltas[outputIndex]
		if buffer == nil {
			buffer = &strings.Builder{}
			a.pendingDeltas[outputIndex] = buffer
		}
		buffer.WriteString(text)
		return
	}
	if item.deltaText == nil {
		item.deltaText = &strings.Builder{}
	}
	item.deltaText.WriteString(text)
	a.byIndex[outputIndex] = item
}

func (a *assistantMessageAccumulator) SetFinalizedText(outputIndex int64, text string) {
	if a == nil {
		return
	}
	item, exists := a.byIndex[outputIndex]
	if !exists {
		a.order = append(a.order, outputIndex)
		item = assistantAccumulatorItem{
			message: ResponseItem{
				Type:        ResponseItemTypeMessage,
				OutputIndex: outputIndex,
				Role:        textutil.Value(RoleAssistant),
			},
			providerPhase: AbsentProviderPhase(),
			deltaText:     a.pendingDeltas[outputIndex],
		}
		delete(a.pendingDeltas, outputIndex)
	}
	item.finalizedText = textutil.Value(text)
	a.byIndex[outputIndex] = item
}

func (a *assistantMessageAccumulator) Resolve() (*string, MessagePhase, *ProviderPhase, int64, string, bool) {
	if a == nil {
		return nil, "", AbsentProviderPhase(), 0, "", false
	}
	segments := make([]assistantOutputSegment, 0, len(a.order))
	for _, outputIndex := range a.order {
		item, ok := a.byIndex[outputIndex]
		if !ok ||
			item.message.Type != ResponseItemTypeMessage ||
			item.message.Role == nil ||
			*item.message.Role != RoleAssistant {
			continue
		}
		phase := MessagePhase("")
		if item.message.Phase != nil {
			phase = *item.message.Phase
		}
		text := item.message.Content
		if (text == nil || strings.TrimSpace(*text) == "") && item.finalizedText != nil {
			text = item.finalizedText
		}
		deltaText := ""
		if item.deltaText != nil {
			deltaText = item.deltaText.String()
		}
		if (text == nil || strings.TrimSpace(*text) == "") && deltaText != "" {
			text = textutil.Value(deltaText)
		}
		text = resolveAssistantContent(RoleAssistant, phase, text)
		segments = append(segments, assistantOutputSegment{
			Text:          optionalStringValue(text),
			Content:       textutil.Pointer(text),
			DeltaText:     deltaText,
			Phase:         phase,
			ProviderPhase: item.providerPhase,
			OutputIndex:   outputIndex,
		})
	}
	text, phase, providerPhase, outputIndex, deltaText, resolved := resolveAssistantOutput(segments)
	if len(a.pendingDeltas) > 0 {
		return nil, "", AbsentProviderPhase(), 0, "", false
	}
	return text, phase, providerPhase, outputIndex, deltaText, resolved
}

func (a *assistantMessageAccumulator) Phase(outputIndex int64) MessagePhase {
	if a == nil {
		return ""
	}
	item, ok := a.byIndex[outputIndex]
	if !ok ||
		item.message.Type != ResponseItemTypeMessage ||
		item.message.Role == nil ||
		*item.message.Role != RoleAssistant {
		return ""
	}
	return providerPhaseProjection(item.providerPhase)
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
	if state == nil || delta == "" {
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
			if call.CustomInput != nil {
				state.Custom.Reset()
				state.Custom.WriteString(*call.CustomInput)
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
		out = append(out, ToolCall{
			ID: callID, Name: state.Name, Input: input, Custom: state.IsCustom,
			CustomInput: textutil.OptionalExactString(state.Custom.String()),
		})
	}
	return out
}

func buildOutputItemsFromStream(text *string, textPresent bool, phase MessagePhase, toolCalls []ToolCall, reasoning []ReasoningEntry, reasoningItems []ReasoningItem) []ResponseItem {
	items := make([]ResponseItem, 0, 1+len(toolCalls)+len(reasoningItems))
	if textPresent {
		items = append(items, ResponseItem{
			Type: ResponseItemTypeMessage, Role: textutil.Value(RoleAssistant),
			Phase: optionalMessagePhase(phase), Content: textutil.Pointer(text),
		})
	}
	for _, call := range toolCalls {
		callID := textutil.FirstNonEmpty(strings.TrimSpace(call.ID), strings.TrimSpace(call.Name))
		if callID == "" {
			continue
		}
		if call.Custom {
			items = append(items, ResponseItem{
				Type: ResponseItemTypeCustomToolCall, ID: textutil.Value(callID),
				CallID: textutil.Value(callID), Name: textutil.Value(call.Name),
				CustomInput: textutil.Pointer(call.CustomInput),
			})
		} else {
			items = append(items, ResponseItem{
				Type: ResponseItemTypeFunctionCall, ID: textutil.Value(callID),
				CallID: textutil.Value(callID), Name: textutil.Value(call.Name),
				Arguments: normalizeToolInput(string(call.Input)),
			})
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
			ID:               textutil.Value(id),
			EncryptedContent: textutil.Value(encrypted),
			ReasoningSummary: append([]ReasoningEntry(nil), summaries...),
		})
	}
	return items
}

func optionalMessagePhase(phase MessagePhase) *MessagePhase {
	if strings.TrimSpace(string(phase)) == "" {
		return nil
	}
	return textutil.Value(phase)
}

func optionalStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
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
	if a == nil {
		return
	}
	if item.Type == "compaction" {
		parsed, err := (compactionOutputItemParser{}).Parse(item, nil)
		if err != nil || len(parsed.CanonicalItems) != 1 {
			return
		}
		checkpoint := parsed.CanonicalItems[0]
		checkpoint.OutputIndex = outputIndex
		if _, exists := a.byIndex[outputIndex]; !exists {
			a.order = append(a.order, outputIndex)
		}
		a.byIndex[outputIndex] = checkpoint
		return
	}
	if isKnownResponseOutputItemType(item.Type) {
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
	a.byIndex[outputIndex] = ResponseItem{Type: ResponseItemTypeOther, ID: textutil.OptionalExactString(item.ID), OutputIndex: outputIndex, Raw: copyRaw}
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
	seenIDs := make(map[string]struct{}, len(out))
	seenUnidentifiedIndexes := make(map[int64]struct{})
	register := func(item ResponseItem) bool {
		if item.ID != nil {
			_, exists := seenIDs[*item.ID]
			seenIDs[*item.ID] = struct{}{}
			return exists
		}
		_, exists := seenUnidentifiedIndexes[item.OutputIndex]
		seenUnidentifiedIndexes[item.OutputIndex] = struct{}{}
		return exists
	}
	// Completed output owns the final payload. JSON bytes can change between
	// output_item.done and response.completed without changing item identity.
	for _, item := range out {
		register(item)
	}
	for _, item := range passthrough {
		if (item.Type != ResponseItemTypeOther && item.Type != ResponseItemTypeCompaction) || len(item.Raw) == 0 {
			continue
		}
		if register(item) {
			continue
		}
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
