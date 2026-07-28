package runtime

import (
	"context"
	"errors"

	"encoding/json"
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
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestMultipleBackgroundShellNoticesFlushTogetherOnFirstAvailableSlot(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("working"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
			ToolCalls: []llm.ToolCall{{ID: "call_shell_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}

	started := make(chan struct{})
	release := make(chan struct{})
	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: blockingTool{name: toolspec.ToolExecCommand, started: started, release: release}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			mu.Lock()
			events = append(events, evt)
			mu.Unlock()
		},
	})

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
		Type:       BackgroundShellEventCompleted,
		ID:         "1000",
		State:      "completed",
		NoticeText: "Background shell 1000 completed.\nExit code: 0\nOutput:\ndone-a",
	}, true)
	eng.HandleBackgroundShellUpdate(BackgroundShellEvent{
		Type:       BackgroundShellEventCompleted,
		ID:         "1001",
		State:      "completed",
		NoticeText: "Background shell 1001 completed.\nExit code: 0\nOutput:\ndone-b",
	}, true)

	client.mu.Lock()
	callCountWhileBusy := len(client.calls)
	client.mu.Unlock()
	if callCountWhileBusy != 1 {
		t.Fatalf("expected queued notices to avoid immediate model calls while busy, got %d calls", callCountWhileBusy)
	}

	close(release)
	result := <-submitDone
	if result.err != nil {
		t.Fatalf("submit: %v", result.err)
	}
	if messageContent(result.assistant) != "done" {
		t.Fatalf("assistant content = %q, want done", messageContent(result.assistant))
	}

	client.mu.Lock()
	requests := append([]llm.Request(nil), client.calls...)
	client.mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("expected 2 model calls with both background notices injected into the next request, got %d", len(requests))
	}

	containsNotice := func(req llm.Request, shellID string) bool {
		for _, msg := range requestMessages(req) {
			if msg.Role == llm.RoleDeveloper && msg.MessageType != nil && *msg.MessageType == llm.MessageTypeBackgroundNotice && strings.Contains(messageContent(msg), "Background shell "+shellID+" completed.") {
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

	mu.Lock()
	defer mu.Unlock()
	immediateUpdates := map[string]bool{"1000": false, "1001": false}
	for _, evt := range events {
		if evt.Kind != EventBackgroundUpdated || evt.Background == nil {
			continue
		}
		if _, ok := immediateUpdates[evt.Background.ID]; !ok {
			continue
		}
		if evt.CommittedEntryCount != 0 || evt.CommittedEntryStartSet {
			t.Fatalf("background update should not claim committed transcript range, got %+v", evt)
		}
		immediateUpdates[evt.Background.ID] = true
	}
	for shellID, found := range immediateUpdates {
		if !found {
			t.Fatalf("expected immediate background_updated event for %s, got %+v", shellID, events)
		}
	}
}

func TestWriteStdinCompletionDoesNotQueueDuplicateBackgroundNotice(t *testing.T) {
	store := mustCreateTestSession(t)
	manager, err := shelltool.NewManager(shelltool.WithMinimumExecToBgTime(time.Millisecond))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	defer func() {
		_ = manager.Close()
	}()

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("start background"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
			ToolCalls: []llm.ToolCall{{
				ID:    "call_exec_1",
				Name:  string(toolspec.ToolExecCommand),
				Input: json.RawMessage(`{"cmd":"read line; echo done","shell":"/bin/sh","login":false,"tty":true,"yield_time_ms":1}`),
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("wait for it"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
			ToolCalls: []llm.ToolCall{{
				ID:    "call_poll_1",
				Name:  string(toolspec.ToolWriteStdin),
				Input: json.RawMessage(`{"session_id":1000,"chars":"\n","yield_time_ms":15000}`),
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("unexpected extra turn"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	registry := tools.NewRegistry(
		tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: shelltool.NewExecCommandTool(store.Meta().WorkspaceRoot, 16_000, manager, store.Meta().SessionID)},
		tools.HandlerRegistration{ID: toolspec.ToolWriteStdin, Handler: shelltool.NewWriteStdinTool(16_000, manager)},
	)
	eng := mustNewTestEngine(t, store, client, registry, Config{Model: "gpt-5"})
	manager.SetEventHandler(func(evt shelltool.Event) {
		summary, summaryErr := shelltool.SummarizeBackgroundEvent(evt, shelltool.BackgroundNoticeOptions{MaxChars: 16_000, SuccessOutputMode: shelltool.BackgroundOutputDefault})
		if summaryErr != nil {
			t.Errorf("SummarizeBackgroundEvent: %v", summaryErr)
			return
		}
		preview, previewRemoved := summary.RuntimePreview()
		eng.HandleBackgroundShellUpdate(BackgroundShellEvent{
			Type:           backgroundShellEventTypeForTest(evt.Type),
			ID:             evt.Snapshot.ID,
			State:          evt.Snapshot.State,
			Command:        evt.Snapshot.Command,
			Workdir:        evt.Snapshot.Workdir,
			LogPath:        evt.Snapshot.LogPath,
			Preview:        preview,
			PreviewRemoved: previewRemoved,
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
	if messageContent(assistant) != "done" {
		t.Fatalf("assistant content = %q, want done", messageContent(assistant))
	}
	client.mu.Lock()
	callCount := len(client.calls)
	client.mu.Unlock()
	if callCount != 3 {
		t.Fatalf("model call count = %d, want 3", callCount)
	}
	for _, msg := range eng.transcriptRuntimeState().SnapshotMessages() {
		if msg.Role == llm.RoleDeveloper && msg.MessageType != nil && *msg.MessageType == llm.MessageTypeBackgroundNotice {
			t.Fatalf("did not expect background notice after write_stdin harvested completion: %+v", msg)
		}
	}
}

func TestNewConsumesPendingModelRecoveryWithoutMarkerWhenStepCompleted(t *testing.T) {
	store := mustCreateTestSession(t)
	if _, _, err := appendTestEvent(t, store, "completed-step", llm.Message{Role: llm.RoleUser, Content: textutil.Value("hello")}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if _, _, err := appendTestEvent(t, store, "completed-step", llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)}); err != nil {
		t.Fatalf("append terminal assistant message: %v", err)
	}
	if err := store.SetPendingModelRecovery(session.PendingModelRecovery{RecoveryID: "recovery-completed", StepID: "completed-step", Reason: "test", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("set pending recovery: %v", err)
	}

	reopenedStore, err := runtimeTestSessionPersistence.Open(store.Dir())
	if err != nil {
		t.Fatalf("re-open store: %v", err)
	}
	restored := mustNewTestEngine(t, reopenedStore, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	if reopenedStore.Meta().PendingModelRecovery != nil {
		t.Fatal("expected reopen path to clear pending model recovery")
	}
	for _, msg := range restored.transcriptRuntimeState().SnapshotMessages() {
		if msg.MessageType != nil && *msg.MessageType == llm.MessageTypeInterruption {
			t.Fatalf("did not expect interruption marker for completed step, messages=%+v", restored.transcriptRuntimeState().SnapshotMessages())
		}
	}
}

func TestNewDiscardsPendingModelRecoveryWithoutConcreteStep(t *testing.T) {
	store := mustCreateTestSession(t)
	if err := store.SetPendingModelRecovery(session.PendingModelRecovery{RecoveryID: "recovery", Reason: "missing_step", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("set pending recovery: %v", err)
	}

	reopenedStore, err := runtimeTestSessionPersistence.Open(store.Dir())
	if err != nil {
		t.Fatalf("re-open store: %v", err)
	}
	restored := mustNewTestEngine(t, reopenedStore, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	if reopenedStore.Meta().PendingModelRecovery != nil {
		t.Fatal("expected reopen path to clear pending model recovery")
	}
	for _, msg := range restored.transcriptRuntimeState().SnapshotMessages() {
		if msg.MessageType != nil && *msg.MessageType == llm.MessageTypeInterruption {
			t.Fatalf("did not expect interruption marker without concrete step, messages=%+v", restored.transcriptRuntimeState().SnapshotMessages())
		}
	}
	events, err := collectTestEventRecords(reopenedStore)
	if err != nil {
		t.Fatalf("read reopened events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("metadata-only recovery discard emitted events: %+v", events)
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
			if strings.Contains(messageContent(msg), "User ran shell command directly:") {
				t.Fatalf("unexpected duplicate developer notice for user shell command, msg=%+v", msg)
			}
		case llm.RoleAssistant:
			if len(msg.ToolCalls) == 1 && msg.ToolCalls[0].Name == string(toolspec.ToolExecCommand) {
				foundAssistantToolCall = true
			}
		case llm.RoleTool:
			if msg.Name != nil && *msg.Name == string(toolspec.ToolExecCommand) && strings.TrimSpace(messageContent(msg)) != "" {
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

	eng, err := New(store, mustMaterializeTestEventLog(t, store), &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})

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
		if msg.Name == nil || *msg.Name != string(toolspec.ToolExecCommand) {
			continue
		}
		foundToolOutput = true
		break
	}
	if !foundToolOutput {
		t.Fatalf("expected persisted shell tool output message, messages=%+v", messages)
	}
	completion, ok := eng.transcriptRuntimeState().ToolCompletionSnapshot(result.CallID)
	if !ok {
		t.Fatal("expected persisted shell tool completion")
	}
	if completion.Presentation == nil || completion.Presentation.Command != "pwd" || !completion.Presentation.IsShell {
		t.Fatalf("persisted shell presentation = %+v, want typed command input", completion.Presentation)
	}
}

func TestParallelToolsReturnDeclaredOrder(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("working")},
			ToolCalls: []llm.ToolCall{
				{ID: "a", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{}`)},
				{ID: "b", Name: string(toolspec.ToolPatch), Input: json.RawMessage(`{}`)},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done")},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand, delay: 40 * time.Millisecond}}, tools.HandlerRegistration{ID: toolspec.ToolPatch, Handler: fakeTool{name: toolspec.ToolPatch, delay: 1 * time.Millisecond}}), Config{Model: "gpt-5", Temperature: 1})

	if _, err := eng.SubmitUserMessage(context.Background(), "run tools"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	events, err := collectTestEventRecords(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}

	toolMessages := []llm.Message{}
	for _, evt := range events {
		if evt.Kind != "message" {
			continue
		}
		msg := persistedMessageForTest(t, evt)
		if msg.Role == llm.RoleTool {
			toolMessages = append(toolMessages, msg)
		}
	}

	if len(toolMessages) != 2 {
		t.Fatalf("tool message count = %d, want 2", len(toolMessages))
	}
	if toolMessages[0].ToolCallID == nil || *toolMessages[0].ToolCallID != "a" ||
		toolMessages[1].ToolCallID == nil || *toolMessages[1].ToolCallID != "b" {
		t.Fatalf("tool order mismatch: first=%v second=%v", toolMessages[0].ToolCallID, toolMessages[1].ToolCallID)
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
	watchdog, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("working")},
			ToolCalls: []llm.ToolCall{
				{ID: "a", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{}`)},
				{ID: "b", Name: string(toolspec.ToolPatch), Input: json.RawMessage(`{}`)},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done")},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}

	slow := blockingTool{name: toolspec.ToolExecCommand, started: make(chan struct{}), release: make(chan struct{})}
	var releaseSlow sync.Once
	release := func() {
		releaseSlow.Do(func() {
			close(slow.release)
		})
	}
	t.Cleanup(release)
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
		_, submitErr := eng.SubmitUserMessage(watchdog, "run tools")
		submitDone <- submitErr
	}()

	select {
	case <-slow.started:
	case submitErr := <-submitDone:
		t.Fatalf("submit completed before slow tool started: %v", submitErr)
	case <-watchdog.Done():
		t.Fatalf("timed out waiting for slow tool to start: %v", watchdog.Err())
	}

	var completed tools.Result
	select {
	case completed = <-toolCompleted:
	case submitErr := <-submitDone:
		t.Fatalf("submit completed before fast tool result: %v", submitErr)
	case <-watchdog.Done():
		t.Fatalf("timed out waiting for fast tool completion: %v", watchdog.Err())
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

	release()
	select {
	case submitErr := <-submitDone:
		if submitErr != nil {
			t.Fatalf("submit: %v", submitErr)
		}
	case <-watchdog.Done():
		t.Fatalf("timed out waiting for submit completion: %v", watchdog.Err())
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
		Model:                "gpt-5",
		EnabledTools:         []toolspec.ID{toolspec.ToolPatch, toolspec.ToolAskQuestion},
		CurrentNodeExecution: testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeTool),
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
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("working")},
			ToolCalls: []llm.ToolCall{
				{ID: "a", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done")},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	if _, err := eng.SubmitUserMessage(context.Background(), "run tool"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	events, err := collectTestEventRecords(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}

	foundAssistantWithCall := false
	for _, evt := range events {
		if evt.Kind != "message" {
			continue
		}
		msg := persistedMessageForTest(t, evt)
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

func TestExecuteToolCallsAppliesToolCompletionByCommitReceipt(t *testing.T) {
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
		t.Run(tc.name+"/uncommitted", func(t *testing.T) {
			store := mustCreateTestSession(t)
			eng := mustNewTestEngine(t, store, &fakeClient{}, tc.registry, Config{Model: "gpt-5"})
			mustBlockTestEventLogAppends(t, store)

			_, err := eng.executeToolCalls(context.Background(), "step", []llm.ToolCall{{
				ID: "call-1", Name: tc.callName, Input: json.RawMessage(`{}`),
			}})
			if !errors.Is(err, errPersistToolCompletion) {
				t.Fatalf("expected errPersistToolCompletion, got %v", err)
			}
			if got := eng.transcriptRuntimeState().ToolCompletionCount(); got != 0 {
				t.Fatalf("uncommitted tool completions = %d, want 0", got)
			}
		})

		t.Run(tc.name+"/committed_observer_error", func(t *testing.T) {
			observerErr := errors.New("tool completion observer failed")
			gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
			store := mustCreateNamedTestSession(t, "ws", t.TempDir(), session.WithPersistenceObserver(gate))
			eng := mustNewTestEngine(t, store, &fakeClient{}, tc.registry, Config{Model: "gpt-5"})
			gate.FailNext(observerErr)

			_, err := eng.executeToolCalls(context.Background(), "step", []llm.ToolCall{{
				ID: "call-1", Name: tc.callName, Input: json.RawMessage(`{}`),
			}})
			if !errors.Is(err, errPersistToolCompletion) || !errors.Is(err, observerErr) {
				t.Fatalf("tool completion error = %v, want persistence wrapper and observer error", err)
			}
			if got := eng.transcriptRuntimeState().ToolCompletionCount(); got != 1 {
				t.Fatalf("committed tool completions = %d, want 1", got)
			}
		})
	}
}
