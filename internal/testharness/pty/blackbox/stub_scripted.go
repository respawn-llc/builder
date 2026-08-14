package blackbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"strings"

	"core/internal/testharness/scriptedllm"
	"core/server/llm"
	"core/shared/textutil"
)

type Script = scriptedllm.Script
type ScriptStep = scriptedllm.Step

func FinalAnswer(content string) ScriptStep { return scriptedllm.FinalAnswer(content) }
func ToolBatch(content string, calls ...llm.ToolCall) ScriptStep {
	return scriptedllm.ToolBatch(content, calls...)
}

type scriptedResponsesProgram struct {
	client   *scriptedllm.Client
	lineages map[scriptedLineage]*scriptedLineageState
	active   map[scriptedLineage]struct{}
}

type scriptedLineage struct {
	sessionID  string
	supervisor bool
}

type scriptedLineageState struct {
	input     []llm.ResponseItem
	pending   map[string]scriptedCall
	delivered map[string]struct{}
}

type scriptedCall struct {
	name string
	kind llm.ResponseItemType
}

type scriptedResponseRequest struct {
	Model string            `json:"model"`
	Input []json.RawMessage `json:"input"`
}

func (s *ResponsesStub) serveScripted(
	ctx context.Context,
	writer http.ResponseWriter,
	request *http.Request,
	route Route,
	body []byte,
) (scriptedllm.RequestAdmission, error) {
	switch route {
	case RouteCompact:
		http.Error(writer, "scripted Responses compaction is unsupported", http.StatusBadRequest)
		return scriptedllm.RequestNotAdmitted, nil
	case RouteModel:
		window, err := s.scripted.client.ResolveModelContextWindow(ctx, request.PathValue("model"))
		if err != nil {
			return scriptedllm.RequestNotAdmitted, err
		}
		return scriptedllm.RequestNotAdmitted, writeJSON(writer, http.StatusOK, map[string]any{
			"id": request.PathValue("model"), "object": "model", "created": 0,
			"owned_by": "kent", "context_window": window,
		})
	case RouteResponses:
	default:
		return scriptedllm.RequestNotAdmitted, fmt.Errorf("unsupported scripted Responses route %q", route)
	}

	lineage, err := parseScriptedLineage(request.Header.Get("session-id"))
	if err != nil {
		return scriptedllm.RequestNotAdmitted, err
	}
	llmRequest, canonical, err := decodeScriptedRequest(body)
	if err != nil {
		return scriptedllm.RequestNotAdmitted, err
	}
	llmRequest.SessionID = textutil.Value(lineage.sessionID)
	enriched, state, err := s.beginScriptedLineage(lineage, canonical)
	if err != nil {
		return scriptedllm.RequestNotAdmitted, err
	}
	llmRequest.Items = enriched
	assistantStarted := false
	streamStarted := false
	startStream := func() {
		if streamStarted {
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		streamStarted = true
	}
	outcome, err := s.scripted.client.GenerateStreamWithEventsOutcome(ctx, llmRequest, llm.StreamCallbacks{
		OnAssistantDelta: func(delta llm.AssistantDelta) {
			startStream()
			if !assistantStarted {
				s.writeResponseOutputItem(writer, 0, assistantMessageOutputItem(nil, ResponsePhase(delta.Phase)))
			}
			s.writeResponseAssistantDelta(writer, 0, delta)
			assistantStarted = true
		},
	})
	if commitErr := s.finishScriptedLineage(lineage, canonical, state, outcome); err == nil {
		err = commitErr
	}
	if err != nil {
		return outcome.Admission, err
	}
	output := scriptedResponseOutput(outcome.Response)
	startStream()
	for index, item := range output {
		typed, ok := item.(map[string]any)
		if ok && (typed["type"] == "function_call" || typed["type"] == "custom_tool_call") {
			s.writeResponseOutputItem(writer, index, item)
		}
	}
	servedModel := llmRequest.Model
	if outcome.Response.ServedModel != nil && strings.TrimSpace(*outcome.Response.ServedModel) != "" {
		servedModel = strings.TrimSpace(*outcome.Response.ServedModel)
	}
	if !s.writeResponseCompleted(writer, servedModel, output, outcome.Response.Usage) || !s.writeResponseDone(writer) {
		return outcome.Admission, errors.New("write scripted Responses stream")
	}
	return outcome.Admission, nil
}

func parseScriptedLineage(raw string) (scriptedLineage, error) {
	key, err := parseSessionCacheKey(raw)
	if err != nil {
		return scriptedLineage{}, errors.New("scripted Responses requires a stable Session cache identity")
	}
	return scriptedLineage{sessionID: key.SessionID.String(), supervisor: key.Supervisor}, nil
}

func (s *ResponsesStub) beginScriptedLineage(
	lineage scriptedLineage,
	current []llm.ResponseItem,
) ([]llm.ResponseItem, *scriptedLineageState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, active := s.scripted.active[lineage]; active {
		return nil, nil, scriptedllm.ErrConcurrentCall
	}
	previous := s.scripted.lineages[lineage]
	if previous == nil {
		state := &scriptedLineageState{
			pending: make(map[string]scriptedCall), delivered: make(map[string]struct{}),
		}
		if err := state.seedHistoricalDeliveries(current); err != nil {
			return nil, nil, err
		}
		s.scripted.active[lineage] = struct{}{}
		return llm.CloneResponseItems(current), state, nil
	}
	state := &scriptedLineageState{input: previous.input, pending: maps.Clone(previous.pending), delivered: maps.Clone(previous.delivered)}
	if len(current) < len(state.input) {
		return nil, nil, fmt.Errorf("scripted Responses input prefix shortened: prior=%d current=%d", len(state.input), len(current))
	}
	for index := range state.input {
		if !reflect.DeepEqual(state.input[index], current[index]) {
			return nil, nil, fmt.Errorf("scripted Responses input prefix changed at index %d", index)
		}
	}
	enriched := llm.CloneResponseItems(current)
	seen := make(map[string]struct{})
	for index := len(state.input); index < len(current); index++ {
		item := current[index]
		if item.Type != llm.ResponseItemTypeFunctionCallOutput && item.Type != llm.ResponseItemTypeCustomToolOutput {
			continue
		}
		if item.CallID == nil || strings.TrimSpace(*item.CallID) == "" {
			return nil, nil, fmt.Errorf("scripted Responses tool output at index %d has no call ID", index)
		}
		callID := strings.TrimSpace(*item.CallID)
		if _, duplicate := seen[callID]; duplicate {
			return nil, nil, fmt.Errorf("scripted Responses duplicate appended delivery %q", callID)
		}
		seen[callID] = struct{}{}
		if _, delivered := state.delivered[callID]; delivered {
			return nil, nil, fmt.Errorf("scripted Responses call ID %q was already delivered", callID)
		}
		call, pending := state.pending[callID]
		if !pending {
			return nil, nil, fmt.Errorf("scripted Responses call ID %q is unknown", callID)
		}
		if item.Type != call.kind {
			return nil, nil, fmt.Errorf("scripted Responses call ID %q output kind mismatch: got=%s want=%s", callID, item.Type, call.kind)
		}
		enriched[index].Name = textutil.Value(call.name)
		delete(state.pending, callID)
		state.delivered[callID] = struct{}{}
	}
	s.scripted.active[lineage] = struct{}{}
	return enriched, state, nil
}

func (s *ResponsesStub) finishScriptedLineage(
	lineage scriptedLineage,
	canonical []llm.ResponseItem,
	state *scriptedLineageState,
	outcome scriptedllm.GenerationOutcome,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.scripted.active, lineage)
	if outcome.Admission != scriptedllm.RequestAdmitted {
		return nil
	}
	state.input = llm.CloneResponseItems(canonical)
	registerErr := state.register(outcome.Response.ToolCalls)
	s.scripted.lineages[lineage] = state
	return registerErr
}

func (state *scriptedLineageState) seedHistoricalDeliveries(items []llm.ResponseItem) error {
	for index, item := range items {
		if item.Type != llm.ResponseItemTypeFunctionCallOutput && item.Type != llm.ResponseItemTypeCustomToolOutput {
			continue
		}
		if item.CallID == nil || strings.TrimSpace(*item.CallID) == "" {
			return fmt.Errorf("scripted Responses historical tool output at index %d has no call ID", index)
		}
		callID := strings.TrimSpace(*item.CallID)
		if _, duplicate := state.delivered[callID]; duplicate {
			return fmt.Errorf("scripted Responses duplicate historical call ID %q", callID)
		}
		state.delivered[callID] = struct{}{}
	}
	return nil
}

func (state *scriptedLineageState) register(calls []llm.ToolCall) error {
	for _, call := range calls {
		callID := strings.TrimSpace(call.ID)
		name := strings.TrimSpace(call.Name)
		if callID == "" || name == "" {
			return errors.New("scripted response emitted a tool call without ID or name")
		}
		if _, pending := state.pending[callID]; pending {
			return fmt.Errorf("scripted response emitted duplicate pending call ID %q", callID)
		}
		if _, delivered := state.delivered[callID]; delivered {
			return fmt.Errorf("scripted response reused delivered call ID %q", callID)
		}
		state.pending[callID] = scriptedCall{name: name, kind: llm.ToolOutputItemType(call.Custom)}
	}
	return nil
}

func decodeScriptedRequest(body []byte) (llm.Request, []llm.ResponseItem, error) {
	var request scriptedResponseRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return llm.Request{}, nil, fmt.Errorf("decode scripted Responses request: %w", err)
	}
	items := make([]llm.ResponseItem, 0, len(request.Input))
	for index, raw := range request.Input {
		item, err := decodeProviderResponseItem(raw)
		if err != nil {
			return llm.Request{}, nil, fmt.Errorf("decode scripted Responses input item %d: %w", index, err)
		}
		items = append(items, item)
	}
	return llm.Request{
		Model: request.Model, Items: llm.CloneResponseItems(items), ToolChoiceMode: llm.ToolChoiceModeAutomatic,
	}, items, nil
}

func decodeProviderResponseItem(raw json.RawMessage) (llm.ResponseItem, error) {
	var envelope struct {
		Type   llm.ResponseItemType `json:"type"`
		Name   string               `json:"name"`
		CallID string               `json:"call_id"`
		Output json.RawMessage      `json:"output"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return llm.ResponseItem{}, err
	}
	item := llm.ResponseItem{
		Type:   envelope.Type,
		Name:   textutil.OptionalTrimmedString(envelope.Name),
		CallID: textutil.OptionalTrimmedString(envelope.CallID),
		Raw:    json.RawMessage(textutil.CompactNoHTMLEscape(raw)),
	}
	switch envelope.Type {
	case llm.ResponseItemTypeFunctionCallOutput, llm.ResponseItemTypeCustomToolOutput:
		var outputText string
		if json.Unmarshal(envelope.Output, &outputText) == nil {
			if json.Valid([]byte(outputText)) {
				item.Output = json.RawMessage(outputText)
			} else {
				item.Output, _ = json.Marshal(outputText)
			}
		} else {
			item.Output = append(json.RawMessage(nil), envelope.Output...)
		}
	}
	return item, nil
}

func scriptedResponseOutput(response llm.Response) []any {
	output := make([]any, 0, 1+len(response.ToolCalls))
	if response.Assistant.Content != nil {
		phase := ResponsePhaseAbsent
		if response.Assistant.Phase != nil {
			phase = ResponsePhase(*response.Assistant.Phase)
		}
		output = append(output, assistantMessageOutputItem(response.Assistant.Content, phase))
	}
	for _, call := range response.ToolCalls {
		item := map[string]any{"id": call.ID, "call_id": call.ID, "name": call.Name}
		if call.Custom {
			input := ""
			if call.CustomInput != nil {
				input = *call.CustomInput
			}
			item["type"], item["input"] = "custom_tool_call", input
		} else {
			item["type"], item["arguments"] = "function_call", string(call.Input)
		}
		output = append(output, item)
	}
	return output
}
