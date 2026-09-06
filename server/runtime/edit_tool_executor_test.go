package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestExecuteToolCallsCanonicalizesEditAliases(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	var events []Event
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolEdit, Handler: &capturingTool{name: toolspec.ToolEdit}}), Config{
		Model:        "claude",
		EnabledTools: []toolspec.ID{toolspec.ToolEdit},
		OnEvent: func(evt Event) {
			events = append(events, evt)
		},
	})
	stepID := runtimeTestStepID("step")
	restoreStep := setTestActiveStep(eng, stepID)
	defer restoreStep()

	results, err := eng.executeToolCalls(context.Background(), stepID, []llm.ToolCall{{
		ID:    "call-replace",
		Name:  "replace",
		Input: json.RawMessage(`{"path":"a.go","old_string":"old","new_string":"new"}`),
	}})
	if err != nil {
		t.Fatalf("execute tool calls: %v", err)
	}
	if len(results) != 1 || results[0].Name != toolspec.ToolEdit {
		t.Fatalf("results = %+v, want canonical edit", results)
	}
	snapshot := mustTranscriptHydrationSnapshot(t, eng)
	var persistedSummary string
	for _, row := range snapshot.CommittedRows {
		if row.Tool != nil && row.Tool.ToolCallID == "call-replace" {
			persistedSummary = row.Tool.ResultSummary
		}
	}
	if persistedSummary != "edited file" {
		t.Fatalf("persisted edit summary = %q, want %q", persistedSummary, "edited file")
	}
	var started *llm.ToolCall
	for _, evt := range events {
		if evt.Kind == EventToolCallStarted && evt.ToolCall != nil && evt.ToolCall.ID == "call-replace" {
			started = evt.ToolCall
		}
	}
	if started == nil {
		t.Fatalf("events = %+v, want started event", events)
	}
	meta := transcriptToolCallMeta(*started, store.Meta().WorkspaceRoot)
	if got := meta.ToolName; got != string(toolspec.ToolEdit) {
		t.Fatalf("started tool name = %q, want edit", got)
	}
	if meta.PatchPresentation == nil ||
		meta.PatchPresentation.Changes == nil ||
		len(meta.PatchPresentation.Changes.Files) != 1 {
		t.Fatalf("started edit presentation = %+v, want one structured file", meta)
	}
	file := meta.PatchPresentation.Changes.Files[0]
	if file.Added != 1 || file.Removed == nil || *file.Removed != 1 {
		t.Fatalf("started edit file = %+v, want one addition and one removal", file)
	}
}

func TestExecuteToolCallsCanonicalizesHiddenToolAndParameterAliases(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	var events []Event
	handler := &capturingTool{name: toolspec.ToolExecCommand}
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{
		ID:      toolspec.ToolExecCommand,
		Handler: handler,
	}), Config{
		Model:        "claude",
		EnabledTools: []toolspec.ID{toolspec.ToolExecCommand},
		OnEvent: func(evt Event) {
			events = append(events, evt)
		},
	})
	stepID := runtimeTestStepID("step-hidden-exec-alias")
	restoreStep := setTestActiveStep(eng, stepID)
	defer restoreStep()

	results, err := eng.executeToolCalls(context.Background(), stepID, []llm.ToolCall{{
		ID:    "call-run-command",
		Name:  "run-command",
		Input: json.RawMessage(`{"script":"echo hi","working-directory":"."}`),
	}})
	if err != nil {
		t.Fatalf("execute tool calls: %v", err)
	}
	if len(results) != 1 || results[0].Name != toolspec.ToolExecCommand || results[0].IsError {
		t.Fatalf("results = %+v, want canonical successful exec_command", results)
	}
	if got := string(handler.input); got != `{"cmd":"echo hi","workdir":"."}` {
		t.Fatalf("handler input = %s, want canonical input", got)
	}
	var started *llm.ToolCall
	for _, evt := range events {
		if evt.Kind == EventToolCallStarted && evt.ToolCall != nil && evt.ToolCall.ID == "call-run-command" {
			started = evt.ToolCall
		}
	}
	if started == nil {
		t.Fatalf("events = %+v, want started event", events)
	}
	if started.Name != string(toolspec.ToolExecCommand) || string(started.Input) != `{"cmd":"echo hi","workdir":"."}` {
		t.Fatalf("started call = %+v, want canonical name and input", *started)
	}
	snapshot := mustTranscriptHydrationSnapshot(t, eng)
	found := false
	for _, row := range snapshot.CommittedRows {
		if row.Tool != nil && row.Tool.ToolCallID == "call-run-command" {
			found = true
			if row.Tool.ToolName != string(toolspec.ToolExecCommand) {
				t.Fatalf("persisted tool name = %q, want exec_command", row.Tool.ToolName)
			}
		}
	}
	if !found {
		t.Fatalf("snapshot = %+v, want persisted tool call", snapshot.CommittedRows)
	}
}

func TestExecuteToolCallsRetainsCanonicalInvalidInputAndSkipsHandler(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	handler := &capturingTool{name: toolspec.ToolAskQuestion}
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{
		ID:      toolspec.ToolAskQuestion,
		Handler: handler,
	}), Config{
		Model:        "claude",
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})
	stepID := runtimeTestStepID("step-invalid-ask-alias")
	restoreStep := setTestActiveStep(eng, stepID)
	defer restoreStep()

	results, err := eng.executeToolCalls(context.Background(), stepID, []llm.ToolCall{{
		ID:    "call-ask-invalid",
		Name:  "ask",
		Input: json.RawMessage(`{"text":"Continue?","choices":"not-an-array"}`),
	}})
	if err != nil {
		t.Fatalf("execute tool calls: %v", err)
	}
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("results = %+v, want validation error", results)
	}
	if handler.input != nil {
		t.Fatalf("handler input = %s, want handler not invoked", handler.input)
	}
	snapshot := mustTranscriptHydrationSnapshot(t, eng)
	for _, row := range snapshot.CommittedRows {
		if row.Tool != nil && row.Tool.ToolCallID == "call-ask-invalid" {
			if row.Tool.ToolName != string(toolspec.ToolAskQuestion) {
				t.Fatalf("persisted tool name = %q, want ask_question", row.Tool.ToolName)
			}
		}
	}
}

func TestExecuteToolCallsRetainsCanonicalInvalidExecInputAndSkipsHandler(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	handler := &capturingTool{name: toolspec.ToolExecCommand}
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{
		ID:      toolspec.ToolExecCommand,
		Handler: handler,
	}), Config{
		Model:        "claude",
		EnabledTools: []toolspec.ID{toolspec.ToolExecCommand},
	})
	stepID := runtimeTestStepID("step-invalid-exec-alias")
	restoreStep := setTestActiveStep(eng, stepID)
	defer restoreStep()

	results, err := eng.executeToolCalls(context.Background(), stepID, []llm.ToolCall{{
		ID:    "call-exec-invalid",
		Name:  "run-command",
		Input: json.RawMessage(`{"script":"echo hi","pty":"not-a-bool"}`),
	}})
	if err != nil {
		t.Fatalf("execute tool calls: %v", err)
	}
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("results = %+v, want validation error", results)
	}
	if handler.input != nil {
		t.Fatalf("handler input = %s, want handler not invoked", handler.input)
	}
}

func TestInvalidAliasedCallsPersistCanonicalHistoryAndProviderContext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		id        toolspec.ID
		callName  string
		input     string
		wantInput string
		callID    string
	}{
		{
			name:      "ask question",
			id:        toolspec.ToolAskQuestion,
			callName:  "ask",
			input:     `{"text":"Continue?","choices":"not-an-array"}`,
			wantInput: `{"question":"Continue?","suggestions":"not-an-array"}`,
			callID:    "call-invalid-ask-persisted",
		},
		{
			name:      "exec command",
			id:        toolspec.ToolExecCommand,
			callName:  "run-command",
			input:     `{"script":"echo hi","pty":"not-a-bool"}`,
			wantInput: `{"cmd":"echo hi","tty":"not-a-bool"}`,
			callID:    "call-invalid-exec-persisted",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			handler := &capturingTool{name: test.id}
			client := &fakeClient{responses: []llm.Response{
				{
					Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("working"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
					ToolCalls: []llm.ToolCall{{
						ID:    test.callID,
						Name:  test.callName,
						Input: json.RawMessage(test.input),
					}},
				},
				{
					Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("final"), Phase: textutil.Value(llm.MessagePhaseFinal)},
				},
			}}
			eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{
				ID:      test.id,
				Handler: handler,
			}), Config{Model: "claude", EnabledTools: []toolspec.ID{test.id}})
			if _, err := eng.SubmitUserMessage(context.Background(), "run aliased tool"); err != nil {
				t.Fatalf("submit: %v", err)
			}
			if handler.input != nil {
				t.Fatalf("handler input = %s, want handler not invoked", handler.input)
			}
			if len(client.calls) < 2 {
				t.Fatalf("provider calls = %d, want continuation request", len(client.calls))
			}
			var continued *llm.ToolCall
			for _, message := range requestMessages(client.calls[1]) {
				for index := range message.ToolCalls {
					if message.ToolCalls[index].ID == test.callID {
						call := message.ToolCalls[index]
						continued = &call
					}
				}
			}
			if continued == nil || continued.Name != string(test.id) || string(continued.Input) != test.wantInput {
				t.Fatalf("continued call = %+v, want canonical name/input %s/%s", continued, test.id, test.wantInput)
			}
			records, err := collectTestEventRecords(store)
			if err != nil {
				t.Fatalf("collect persisted events: %v", err)
			}
			var persisted *llm.ToolCall
			for _, event := range records {
				message, ok := mustSessionEventPayload(event.Record).(session.MessageRecord)
				if !ok {
					continue
				}
				restored, err := llmMessageFromSessionRecord(message)
				if err != nil {
					t.Fatalf("restore persisted message: %v", err)
				}
				for index := range restored.ToolCalls {
					if restored.ToolCalls[index].ID == test.callID {
						call := restored.ToolCalls[index]
						persisted = &call
					}
				}
			}
			if persisted == nil || persisted.Name != string(test.id) || string(persisted.Input) != test.wantInput {
				t.Fatalf("persisted call = %+v, want canonical name/input %s/%s", persisted, test.id, test.wantInput)
			}
			foundSnapshot := false
			for _, entry := range eng.ChatSnapshot().Entries {
				if entry.ToolCallID == test.callID && entry.ToolCall != nil {
					foundSnapshot = true
					if entry.ToolCall.ToolName != string(test.id) {
						t.Fatalf("snapshot tool name = %q, want %s", entry.ToolCall.ToolName, test.id)
					}
				}
			}
			if !foundSnapshot {
				t.Fatalf("snapshot omitted %s", test.callID)
			}
		})
	}
}

func TestExecuteToolCallsAcceptsCustomEditJSONAndRejectsPlainText(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolEdit, Handler: &capturingTool{name: toolspec.ToolEdit}}), Config{Model: "claude", EnabledTools: []toolspec.ID{toolspec.ToolEdit}})

	jsonStepID := runtimeTestStepID("step-json")
	restoreStep := setTestActiveStep(eng, jsonStepID)
	okResults, err := eng.executeToolCalls(context.Background(), jsonStepID, []llm.ToolCall{{
		ID:          "call-json",
		Name:        "edit",
		Custom:      true,
		CustomInput: textutil.Value(`{"path":"a.go","old_string":"old","new_string":"new"}`),
	}})
	if err != nil {
		t.Fatalf("execute json custom tool call: %v", err)
	}
	if len(okResults) != 1 || okResults[0].IsError {
		t.Fatalf("json custom results = %+v, want success", okResults)
	}
	restoreStep()

	textStepID := runtimeTestStepID("step-text")
	restoreStep = setTestActiveStep(eng, textStepID)
	defer restoreStep()
	badResults, err := eng.executeToolCalls(context.Background(), textStepID, []llm.ToolCall{{
		ID:          "call-text",
		Name:        "edit",
		Custom:      true,
		CustomInput: textutil.Value("not json"),
	}})
	if err != nil {
		t.Fatalf("execute text custom tool call: %v", err)
	}
	if len(badResults) != 1 || !badResults[0].IsError {
		t.Fatalf("plain custom results = %+v, want error", badResults)
	}
}

type capturingTool struct {
	name  toolspec.ID
	input json.RawMessage
}

func (t *capturingTool) Call(_ context.Context, c tools.Call) (tools.Result, error) {
	t.input = append(t.input[:0], c.Input...)
	var payload map[string]any
	validJSON := json.Unmarshal(c.Input, &payload) == nil && payload != nil
	if !validJSON {
		out, _ := json.Marshal("Edit failed: expected JSON object input.")
		// Tool-logic failures are returned in Result.IsError, not as a Go error.
		return tools.Result{CallID: c.ID, Name: c.Name, Output: out, IsError: true, Summary: textutil.Value("Edit failed: expected JSON object input.")}, nil
	}
	out, _ := json.Marshal("ok")
	return tools.Result{
		CallID:  c.ID,
		Name:    c.Name,
		Output:  out,
		Summary: textutil.Value("edited file"),
	}, nil
}
