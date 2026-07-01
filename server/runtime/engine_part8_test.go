package runtime

import (
	"context"
	"errors"

	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	shelltool "core/server/tools/shell"
	"core/shared/config"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestMultipleBackgroundShellNoticesFlushTogetherOnFirstAvailableSlot(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "working", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{{ID: "call_shell_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}

	started := make(chan struct{})
	release := make(chan struct{})
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: blockingTool{name: toolspec.ToolExecCommand, started: started, release: release}}), Config{Model: "gpt-5"})

	submitDone := make(chan struct {
		assistant llm.Message
		err       error
	}, 1)
	go func() {
		assistant, submitErr := eng.SubmitUserMessage(context.Background(), "run tools")
		submitDone <- struct {
			assistant llm.Message
			err       error
		}{assistant: assistant, err: submitErr}
	}()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for tool call to start")
	}

	eng.HandleBackgroundShellUpdate(BackgroundShellEvent{
		Type:       "completed",
		ID:         "1000",
		State:      "completed",
		NoticeText: "Background shell 1000 completed.\nExit code: 0\nOutput:\ndone-a",
	}, true)
	eng.HandleBackgroundShellUpdate(BackgroundShellEvent{
		Type:       "completed",
		ID:         "1001",
		State:      "completed",
		NoticeText: "Background shell 1001 completed.\nExit code: 0\nOutput:\ndone-b",
	}, true)

	close(release)
	result := <-submitDone
	if result.err != nil {
		t.Fatalf("submit: %v", result.err)
	}
	if result.assistant.Content != "done" {
		t.Fatalf("assistant content = %q, want done", result.assistant.Content)
	}

	client.mu.Lock()
	requests := append([]llm.Request(nil), client.calls...)
	client.mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("expected 2 model calls with both background notices injected into the next request, got %d", len(requests))
	}

	containsNotice := func(req llm.Request, shellID string) bool {
		for _, msg := range requestMessages(req) {
			if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeBackgroundNotice && strings.Contains(msg.Content, "Background shell "+shellID+" completed.") {
				return true
			}
		}
		return false
	}
	if !containsNotice(requests[1], "1000") || !containsNotice(requests[1], "1001") {
		t.Fatalf("expected both background notices in the same in-turn follow-up, messages=%+v", requestMessages(requests[1]))
	}

	time.Sleep(50 * time.Millisecond)
	client.mu.Lock()
	callCountAfterReturn := len(client.calls)
	client.mu.Unlock()
	if callCountAfterReturn != 2 {
		t.Fatalf("did not expect a later batched continuation after turn completion, got %d calls", callCountAfterReturn)
	}
}

func TestWriteStdinCompletionDoesNotQueueDuplicateBackgroundNotice(t *testing.T) {
	store := mustCreateTestSession(t)
	manager, err := shelltool.NewManager(shelltool.WithMinimumExecToBgTime(250 * time.Millisecond))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	defer func() {
		_ = manager.Close()
	}()

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "start background", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{{
				ID:    "call_exec_1",
				Name:  string(toolspec.ToolExecCommand),
				Input: json.RawMessage(`{"cmd":"sleep 0.3; echo done","shell":"/bin/sh","login":false,"yield_time_ms":250}`),
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "wait for it", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{{
				ID:    "call_poll_1",
				Name:  string(toolspec.ToolWriteStdin),
				Input: json.RawMessage(`{"session_id":1000,"yield_time_ms":800}`),
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "unexpected extra turn", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	registry := tools.NewRegistry(
		tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: shelltool.NewExecCommandTool(store.Meta().WorkspaceRoot, 16_000, manager, store.Meta().SessionID)},
		tools.HandlerRegistration{ID: toolspec.ToolWriteStdin, Handler: shelltool.NewWriteStdinTool(16_000, manager)},
	)
	eng := mustNewTestEngine(t, store, client, registry, Config{Model: "gpt-5"})
	manager.SetEventHandler(func(evt shelltool.Event) {
		eng.HandleBackgroundShellUpdate(BackgroundShellEvent{
			Type:    string(evt.Type),
			ID:      evt.Snapshot.ID,
			State:   evt.Snapshot.State,
			Command: evt.Snapshot.Command,
			Workdir: evt.Snapshot.Workdir,
			LogPath: evt.Snapshot.LogPath,
			Preview: evt.Preview,
			Removed: evt.Removed,
			ExitCode: func() *int {
				if evt.Snapshot.ExitCode == nil {
					return nil
				}
				out := *evt.Snapshot.ExitCode
				return &out
			}(),
			NoticeSuppressed: evt.NoticeSuppressed,
		}, strings.TrimSpace(evt.Snapshot.OwnerSessionID) == store.Meta().SessionID && !evt.NoticeSuppressed)
	})

	assistant, err := eng.SubmitUserMessage(context.Background(), "run and wait")
	if err != nil {
		t.Fatalf("submit user message: %v", err)
	}
	if assistant.Content != "done" {
		t.Fatalf("assistant content = %q, want done", assistant.Content)
	}
	time.Sleep(50 * time.Millisecond)

	client.mu.Lock()
	callCount := len(client.calls)
	client.mu.Unlock()
	if callCount != 3 {
		t.Fatalf("model call count = %d, want 3", callCount)
	}
	for _, msg := range eng.transcriptRuntimeState().SnapshotMessages() {
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeBackgroundNotice {
			t.Fatalf("did not expect background notice after write_stdin harvested completion: %+v", msg)
		}
	}
}

func TestSubmitUserMessageSurfacesInFlightClearFailure(t *testing.T) {
	store := mustCreateTestSession(t)
	sessionDir := store.Dir()
	defer func() {
		_ = os.Chmod(sessionDir, 0o755)
	}()

	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	var (
		mu         sync.Mutex
		events     []Event
		chmodDone  bool
		chmodError error
	)
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			mu.Lock()
			events = append(events, evt)
			shouldLockDir := evt.Kind == EventAssistantMessage && !chmodDone
			if shouldLockDir {
				chmodDone = true
			}
			mu.Unlock()
			if shouldLockDir {
				if chmodErr := os.Chmod(sessionDir, 0o555); chmodErr != nil {
					mu.Lock()
					if chmodError == nil {
						chmodError = chmodErr
					}
					mu.Unlock()
				}
			}
		},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "hi")
	if msg.Content != "done" {
		t.Fatalf("assistant content = %q, want done", msg.Content)
	}
	if !errors.Is(err, errMarkInFlightFalse) {
		t.Fatalf("expected errMarkInFlightFalse, got %v", err)
	}

	mu.Lock()
	gotChmodDone := chmodDone
	gotChmodErr := chmodError
	seenClearFailureEvent := false
	for _, evt := range events {
		if evt.Kind == EventInFlightClearFailed && strings.Contains(evt.Error, "mark in-flight false") {
			seenClearFailureEvent = true
			break
		}
	}
	mu.Unlock()

	if !gotChmodDone {
		t.Fatal("expected permission flip hook to run")
	}
	if gotChmodErr != nil {
		t.Fatalf("chmod hook failed: %v", gotChmodErr)
	}
	if !seenClearFailureEvent {
		t.Fatalf("expected %s event, got %+v", EventInFlightClearFailed, events)
	}
	if err := os.Chmod(sessionDir, 0o755); err != nil {
		t.Fatalf("restore session dir permissions: %v", err)
	}

	reopened, openErr := session.Open(sessionDir)
	if openErr != nil {
		t.Fatalf("re-open session store: %v", openErr)
	}
	if !reopened.Meta().InFlightStep {
		t.Fatalf("expected persisted in-flight flag to remain true after clear failure")
	}
	latest, err := reopened.LatestRun()
	if err != nil {
		t.Fatalf("read durable run after reopen: %v", err)
	}
	if latest == nil {
		t.Fatalf("expected durable run lifecycle to persist despite clear failure")
	}
	if latest.Status != session.RunStatusCompleted || latest.FinishedAt.IsZero() {
		t.Fatalf("expected terminal durable run after clear failure, got %+v", latest)
	}
}

func TestNewNormalizesPersistedInFlightStepOnReopen(t *testing.T) {
	store := mustCreateTestSession(t)
	if _, _, err := store.AppendEvent("legacy-step", "message", llm.Message{Role: llm.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if err := store.MarkInFlight(true); err != nil {
		t.Fatalf("mark in-flight true: %v", err)
	}

	reopenedStore, err := session.Open(store.Dir())
	if err != nil {
		t.Fatalf("re-open store: %v", err)
	}
	restored := mustNewTestEngine(t, reopenedStore, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	if reopenedStore.Meta().InFlightStep {
		t.Fatal("expected reopen path to clear persisted in-flight flag")
	}
	messages := restored.transcriptRuntimeState().SnapshotMessages()
	if len(messages) != 2 {
		t.Fatalf("expected original user message plus interruption marker, got %+v", messages)
	}
	last := messages[len(messages)-1]
	if last.Role != llm.RoleDeveloper || last.MessageType != llm.MessageTypeInterruption || last.Content != interruptMessage {
		t.Fatalf("expected interruption developer message, got %+v", last)
	}
	events, err := sessiontest.CollectEvents(reopenedStore)
	if err != nil {
		t.Fatalf("read reopened events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected persisted interruption event appended on reopen, got %+v", events)
	}
}

func TestReopenCarriesInterruptedAskQuestionToolAttemptIntoNextModelRequest(t *testing.T) {
	testReopenCarriesInterruptedToolAttemptIntoNextModelRequest(t, llm.ToolCall{
		ID:    "call_ask",
		Name:  string(toolspec.ToolAskQuestion),
		Input: json.RawMessage(`{"question":"Choose scope?","suggestions":["full","fast"],"recommended_option_index":1}`),
		Presentation: transcript.EncodeToolCallMeta(transcript.ToolCallMeta{
			ToolName:               string(toolspec.ToolAskQuestion),
			Presentation:           transcript.ToolPresentationAskQuestion,
			RenderBehavior:         transcript.ToolCallRenderBehaviorAskQuestion,
			Question:               "Choose scope?",
			Suggestions:            []string{"full", "fast"},
			RecommendedOptionIndex: 1,
			Command:                "Choose scope?",
		}),
	})
}

func TestReopenCarriesInterruptedShellToolAttemptIntoNextModelRequest(t *testing.T) {
	testReopenCarriesInterruptedToolAttemptIntoNextModelRequest(t, llm.ToolCall{
		ID:    "call_shell",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"command":"pwd"}`),
		Presentation: transcript.EncodeToolCallMeta(transcript.ToolCallMeta{
			ToolName:       string(toolspec.ToolExecCommand),
			Presentation:   transcript.ToolPresentationShell,
			RenderBehavior: transcript.ToolCallRenderBehaviorShell,
			IsShell:        true,
			Command:        "pwd",
			TimeoutLabel:   "",
		}),
	})
}

func TestReopenCarriesInterruptedApprovalBackedPatchToolAttemptIntoNextModelRequest(t *testing.T) {
	testReopenCarriesInterruptedToolAttemptIntoNextModelRequest(t, llm.ToolCall{
		ID:          "call_patch",
		Name:        string(toolspec.ToolPatch),
		Custom:      true,
		CustomInput: "*** Begin Patch\n*** Add File: ../outside.txt\n+hello\n*** End Patch\n",
	})
}

func testReopenCarriesInterruptedToolAttemptIntoNextModelRequest(t *testing.T, call llm.ToolCall) {
	t.Helper()

	store := mustCreateTestSession(t)
	if _, _, err := store.AppendEvent("legacy-step", "message", llm.Message{Role: llm.RoleUser, Content: "do the thing"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if _, _, err := store.AppendEvent("legacy-step", "message", llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}}); err != nil {
		t.Fatalf("append assistant tool call message: %v", err)
	}
	if err := store.MarkInFlight(true); err != nil {
		t.Fatalf("mark in-flight true: %v", err)
	}

	reopenedStore, err := session.Open(store.Dir())
	if err != nil {
		t.Fatalf("re-open store: %v", err)
	}
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "decided anew", Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	restored := mustNewTestEngine(t, reopenedStore, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	if reopenedStore.Meta().InFlightStep {
		t.Fatal("expected reopen path to clear persisted in-flight flag")
	}

	msg, err := restored.SubmitUserMessage(context.Background(), "continue")
	if err != nil {
		t.Fatalf("submit after reopen: %v", err)
	}
	if msg.Content != "decided anew" {
		t.Fatalf("assistant content = %q, want decided anew", msg.Content)
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected one resumed model call, got %d", len(client.calls))
	}

	var (
		foundPriorAttempt    bool
		foundUnexpectedReply bool
	)
	for _, item := range client.calls[0].Items {
		switch {
		case item.Type == llm.ResponseItemTypeFunctionCall && item.CallID == call.ID && item.Name == call.Name:
			foundPriorAttempt = true
		case item.Type == llm.ResponseItemTypeCustomToolCall && item.CallID == call.ID && item.Name == call.Name:
			foundPriorAttempt = true
		case item.Type == llm.ResponseItemTypeFunctionCallOutput && item.CallID == call.ID:
			foundUnexpectedReply = true
		case item.Type == llm.ResponseItemTypeCustomToolOutput && item.CallID == call.ID:
			foundUnexpectedReply = true
		}
	}
	if !foundPriorAttempt {
		t.Fatalf("expected resumed request to include prior interrupted tool call attempt, items=%+v", client.calls[0].Items)
	}
	if foundUnexpectedReply {
		t.Fatalf("did not expect resumed request to fabricate completed tool output for interrupted call, items=%+v", client.calls[0].Items)
	}

	seenInterruption := false
	for _, reqMsg := range requestMessages(client.calls[0]) {
		if reqMsg.Role == llm.RoleDeveloper && reqMsg.MessageType == llm.MessageTypeInterruption && reqMsg.Content == interruptMessage {
			seenInterruption = true
			break
		}
	}
	if !seenInterruption {
		t.Fatalf("expected resumed request to include interruption marker, messages=%+v", requestMessages(client.calls[0]))
	}
}

func TestSubmitUserShellCommandPersistsDeveloperNoticeAndToolEntries(t *testing.T) {
	store := mustCreateTestSession(t)

	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	result, err := eng.SubmitUserShellCommand(context.Background(), "pwd")
	if err != nil {
		t.Fatalf("submit user shell command: %v", err)
	}
	if result.Name != toolspec.ToolExecCommand {
		t.Fatalf("unexpected tool result name: %+v", result)
	}

	messages := eng.transcriptRuntimeState().SnapshotMessages()
	if len(messages) == 0 {
		t.Fatal("expected persisted messages")
	}
	foundAssistantToolCall := false
	foundToolOutput := false
	for _, msg := range messages {
		switch msg.Role {
		case llm.RoleDeveloper:
			if strings.Contains(msg.Content, "User ran shell command directly:") {
				t.Fatalf("unexpected duplicate developer notice for user shell command, msg=%+v", msg)
			}
		case llm.RoleAssistant:
			if len(msg.ToolCalls) == 1 && msg.ToolCalls[0].Name == string(toolspec.ToolExecCommand) {
				foundAssistantToolCall = true
			}
		case llm.RoleTool:
			if msg.Name == string(toolspec.ToolExecCommand) && strings.TrimSpace(msg.Content) != "" {
				foundToolOutput = true
			}
		}
	}
	if !foundAssistantToolCall {
		t.Fatalf("expected assistant shell tool call message, messages=%+v", messages)
	}
	if !foundToolOutput {
		t.Fatalf("expected shell tool output message, messages=%+v", messages)
	}

	snapshot := eng.ChatSnapshot()
	foundUserShellCall := false
	for _, entry := range snapshot.Entries {
		if entry.Role != "tool_call" {
			continue
		}
		if entry.ToolCall == nil || !entry.ToolCall.IsShell {
			continue
		}
		if entry.ToolCall.UserInitiated && strings.Contains(entry.Text, "pwd") {
			foundUserShellCall = true
			break
		}
	}
	if !foundUserShellCall {
		t.Fatalf("expected user-initiated shell tool call in transcript snapshot, entries=%+v", snapshot.Entries)
	}
}

func TestSubmitUserShellCommandReturnsUnknownToolErrorWhenShellNotRegistered(t *testing.T) {
	store := mustCreateTestSession(t)

	eng, err := New(store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})

	result, err := eng.SubmitUserShellCommand(context.Background(), "pwd")
	if !errors.Is(err, errUnknownTool) {
		t.Fatalf("expected errUnknownTool, got %v", err)
	}
	if result.Name != toolspec.ToolExecCommand || !result.IsError {
		t.Fatalf("expected shell error result, got %+v", result)
	}
	var payload struct {
		Error string `json:"error"`
	}
	if unmarshalErr := json.Unmarshal(result.Output, &payload); unmarshalErr != nil {
		t.Fatalf("decode result output: %v", unmarshalErr)
	}
	if strings.TrimSpace(payload.Error) != "unknown tool" {
		t.Fatalf("expected unknown tool output payload, got %v", payload)
	}

	messages := eng.transcriptRuntimeState().SnapshotMessages()
	foundToolOutput := false
	for _, msg := range messages {
		if msg.Role != llm.RoleTool {
			continue
		}
		if msg.Name != string(toolspec.ToolExecCommand) {
			continue
		}
		foundToolOutput = true
		break
	}
	if !foundToolOutput {
		t.Fatalf("expected persisted shell tool output message, messages=%+v", messages)
	}
}

func TestParallelToolsReturnDeclaredOrder(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "working"},
			ToolCalls: []llm.ToolCall{
				{ID: "a", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{}`)},
				{ID: "b", Name: string(toolspec.ToolPatch), Input: json.RawMessage(`{}`)},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done"},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand, delay: 40 * time.Millisecond}}, tools.HandlerRegistration{ID: toolspec.ToolPatch, Handler: fakeTool{name: toolspec.ToolPatch, delay: 1 * time.Millisecond}}), Config{Model: "gpt-5", Temperature: 1})

	if _, err := eng.SubmitUserMessage(context.Background(), "run tools"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}

	toolMessages := []llm.Message{}
	for _, evt := range events {
		if evt.Kind != "message" {
			continue
		}
		var msg llm.Message
		if err := json.Unmarshal(evt.Payload, &msg); err != nil {
			t.Fatalf("decode message: %v", err)
		}
		if msg.Role == llm.RoleTool {
			toolMessages = append(toolMessages, msg)
		}
	}

	if len(toolMessages) != 2 {
		t.Fatalf("tool message count = %d, want 2", len(toolMessages))
	}
	if toolMessages[0].ToolCallID != "a" || toolMessages[1].ToolCallID != "b" {
		t.Fatalf("tool order mismatch: first=%s second=%s", toolMessages[0].ToolCallID, toolMessages[1].ToolCallID)
	}

	if len(client.calls) < 2 {
		t.Fatalf("expected at least 2 model requests, got %d", len(client.calls))
	}
	secondReq := client.calls[1]
	foundAssistantWithCalls := false
	for _, msg := range requestMessages(secondReq) {
		if msg.Role == llm.RoleAssistant && len(msg.ToolCalls) == 2 {
			if msg.ToolCalls[0].ID == "a" && msg.ToolCalls[1].ID == "b" {
				foundAssistantWithCalls = true
				break
			}
		}
	}
	if !foundAssistantWithCalls {
		t.Fatalf("second request is missing assistant tool call metadata: %+v", requestMessages(secondReq))
	}

}

func TestParallelToolCompletionAppearsInChatSnapshotBeforeAllToolsFinish(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "working"},
			ToolCalls: []llm.ToolCall{
				{ID: "a", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{}`)},
				{ID: "b", Name: string(toolspec.ToolPatch), Input: json.RawMessage(`{}`)},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done"},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}

	slow := blockingTool{name: toolspec.ToolExecCommand, started: make(chan struct{}), release: make(chan struct{})}
	toolCompleted := make(chan tools.Result, 4)
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(
		tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: slow},
		tools.HandlerRegistration{ID: toolspec.ToolPatch, Handler: fakeTool{name: toolspec.ToolPatch, delay: 1 * time.Millisecond}},
	), Config{
		Model:       "gpt-5",
		Temperature: 1,
		OnEvent: func(evt Event) {
			if evt.Kind != EventToolCallCompleted || evt.ToolResult == nil {
				return
			}
			select {
			case toolCompleted <- *evt.ToolResult:
			default:
			}
		},
	})

	submitDone := make(chan error, 1)
	go func() {
		_, submitErr := eng.SubmitUserMessage(context.Background(), "run tools")
		submitDone <- submitErr
	}()

	select {
	case <-slow.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for slow tool to start")
	}

	var completed tools.Result
	select {
	case completed = <-toolCompleted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for fast tool completion")
	}
	if completed.CallID != "b" {
		t.Fatalf("expected fast patch tool to complete first, got %+v", completed)
	}

	snapshot := eng.ChatSnapshot()
	foundPendingA := false
	foundCompletedB := false
	for _, entry := range snapshot.Entries {
		switch {
		case entry.Role == "tool_call" && entry.ToolCallID == "a":
			foundPendingA = true
		case entry.Role == "tool_result_ok" && entry.ToolCallID == "b":
			foundCompletedB = true
		}
	}
	if !foundPendingA || !foundCompletedB {
		t.Fatalf("expected snapshot to expose pending a and completed b before slow tool finishes, got %+v", snapshot.Entries)
	}

	close(slow.release)
	select {
	case submitErr := <-submitDone:
		if submitErr != nil {
			t.Fatalf("submit: %v", submitErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for submit completion")
	}
}

func TestAskQuestionToolCallsExecuteSequentiallyInDeclaredOrder(t *testing.T) {
	store := mustCreateTestSession(t)
	sequencer := &serialPairProbeTool{
		firstID:       "call-ask-1",
		secondID:      "call-ask-2",
		firstStarted:  make(chan struct{}),
		secondStarted: make(chan struct{}),
		releaseFirst:  make(chan struct{}),
	}
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(
		tools.HandlerRegistration{ID: toolspec.ToolAskQuestion, Handler: sequencer},
	), Config{Model: "gpt-5", EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})

	done := make(chan struct {
		results []tools.Result
		err     error
	}, 1)
	go func() {
		results, err := eng.executeToolCalls(context.Background(), "step", []llm.ToolCall{
			{ID: "call-ask-1", Name: string(toolspec.ToolAskQuestion), Input: json.RawMessage(`{}`)},
			{ID: "call-ask-2", Name: string(toolspec.ToolAskQuestion), Input: json.RawMessage(`{}`)},
		})
		done <- struct {
			results []tools.Result
			err     error
		}{results: results, err: err}
	}()

	select {
	case <-sequencer.firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first ask_question call to start")
	}
	select {
	case <-sequencer.secondStarted:
		t.Fatal("second ask_question call started before first completed")
	case <-time.After(100 * time.Millisecond):
	}
	close(sequencer.releaseFirst)
	select {
	case <-sequencer.secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second ask_question call to start")
	}
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("execute tool calls: %v", result.err)
		}
		if len(result.results) != 2 || result.results[0].CallID != "call-ask-1" || result.results[1].CallID != "call-ask-2" {
			t.Fatalf("results = %+v, want declared ask order", result.results)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ask_question tool calls to finish")
	}
}

func TestWorkflowPromptCapableToolCallsSerializeWithAskQuestion(t *testing.T) {
	store := mustCreateTestSession(t)
	sequencer := &serialPairProbeTool{
		firstID:       "call-patch",
		secondID:      "call-ask",
		firstStarted:  make(chan struct{}),
		secondStarted: make(chan struct{}),
		releaseFirst:  make(chan struct{}),
	}
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(
		tools.HandlerRegistration{ID: toolspec.ToolPatch, Handler: sequencer},
		tools.HandlerRegistration{ID: toolspec.ToolAskQuestion, Handler: sequencer},
	), Config{
		Model:        "gpt-5",
		EnabledTools: []toolspec.ID{toolspec.ToolPatch, toolspec.ToolAskQuestion},
		WorkflowRun:  testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeTool),
	})

	done := make(chan error, 1)
	go func() {
		_, err := eng.executeToolCalls(context.Background(), "step", []llm.ToolCall{
			{ID: "call-patch", Name: string(toolspec.ToolPatch), Input: json.RawMessage(`{}`)},
			{ID: "call-ask", Name: string(toolspec.ToolAskQuestion), Input: json.RawMessage(`{}`)},
		})
		done <- err
	}()

	select {
	case <-sequencer.firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for workflow prompt-capable tool to start")
	}
	select {
	case <-sequencer.secondStarted:
		t.Fatal("ask_question started before earlier workflow prompt-capable tool completed")
	case <-time.After(100 * time.Millisecond):
	}
	close(sequencer.releaseFirst)
	select {
	case <-sequencer.secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ask_question to start")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("execute tool calls: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for workflow prompt-capable tool calls to finish")
	}
}

func TestPersistedAssistantToolCallsContainNoUIDisplayMarkers(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "working"},
			ToolCalls: []llm.ToolCall{
				{ID: "a", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done"},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	if _, err := eng.SubmitUserMessage(context.Background(), "run tool"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}

	foundAssistantWithCall := false
	for _, evt := range events {
		if evt.Kind != "message" {
			continue
		}
		var msg llm.Message
		if err := json.Unmarshal(evt.Payload, &msg); err != nil {
			t.Fatalf("decode message: %v", err)
		}
		if msg.Role != llm.RoleAssistant || len(msg.ToolCalls) == 0 {
			continue
		}
		foundAssistantWithCall = true
		for _, call := range msg.ToolCalls {
			if strings.Contains(call.Name, "shell_call") {
				t.Fatalf("assistant tool call name should not contain display marker: %+v", call)
			}
			if strings.Contains(string(call.Input), "shell_call") || strings.Contains(string(call.Input), "patch_payload") || strings.ContainsRune(string(call.Input), '\x1e') || strings.ContainsRune(string(call.Input), '\x1f') {
				t.Fatalf("assistant tool call input should not contain display markers: %+v", call)
			}
		}
	}
	if !foundAssistantWithCall {
		t.Fatal("expected persisted assistant message with tool_calls")
	}
}

func TestExecuteToolCallsFailsOnToolCompletionPersistence(t *testing.T) {
	tests := []struct {
		name     string
		registry *tools.Registry
		callName string
	}{
		{
			name:     "unknown tool name",
			registry: tools.NewRegistry(),
			callName: "not_a_tool",
		},
		{
			name:     "known tool without handler",
			registry: tools.NewRegistry(),
			callName: string(toolspec.ToolExecCommand),
		},
		{
			name:     "registered tool handler",
			registry: tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}),
			callName: string(toolspec.ToolExecCommand),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := mustCreateTestSession(t)

			eng := mustNewTestEngine(t, store, &fakeClient{}, tc.registry, Config{Model: "gpt-5"})

			sessionDir := store.Dir()
			if err := os.Chmod(sessionDir, 0o555); err != nil {
				t.Fatalf("chmod read-only session dir: %v", err)
			}
			defer func() {
				_ = os.Chmod(sessionDir, 0o755)
			}()

			_, err := eng.executeToolCalls(context.Background(), "step", []llm.ToolCall{
				{ID: "call-1", Name: tc.callName, Input: json.RawMessage(`{}`)},
			})
			if !errors.Is(err, errPersistToolCompletion) {
				t.Fatalf("expected errPersistToolCompletion, got %v", err)
			}

			if got := eng.transcriptRuntimeState().ToolCompletionCount(); got != 0 {
				t.Fatalf("expected no in-memory tool completions when persistence fails, got %d", got)
			}
		})
	}
}
