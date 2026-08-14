package runtime

import (
	"context"
	"core/server/llm"
	"core/server/tools"
	shelltool "core/server/tools/shell"
	"core/shared/textutil"
	"core/shared/toolspec"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// The readiness-gated background/reviewer tests below intentionally run
// serially. Their channels establish the product event under test; allowing
// package-level contention to delay the engine goroutine makes the short
// readiness deadline report a false timeout.

func TestFastExecCommandCompletionDoesNotQueueBackgroundNotice(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := mustCreateTestSessionAt(t, dir)
	manager, err := shelltool.NewManager(shelltool.WithMinimumExecToBgTime(250 * time.Millisecond))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	defer func() {
		_ = manager.Close()
	}()
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("running fast command"),
			},
			ToolCalls: []llm.ToolCall{{
				ID:    "call_exec_1",
				Name:  string(toolspec.ToolExecCommand),
				Input: json.RawMessage(`{"cmd":"echo hi","shell":"/bin/sh","login":false,"yield_time_ms":1000}`),
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done")},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("unexpected extra turn")},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	registry := newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: shelltool.NewExecCommandTool(dir, 16_000, manager, "")})
	eng := mustNewTestEngine(t, store, client, registry, Config{Model: "gpt-5"})
	manager.SetEventHandler(func(evt shelltool.Event) bool {
		summary, summaryErr := shelltool.SummarizeBackgroundEvent(evt, shelltool.BackgroundNoticeOptions{MaxChars: 16_000, SuccessOutputMode: shelltool.BackgroundOutputDefault})
		if summaryErr != nil {
			t.Errorf("SummarizeBackgroundEvent: %v", summaryErr)
			return false
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
		}, true)
		return true
	})

	assistant, err := eng.SubmitUserMessage(context.Background(), "run fast command")
	if err != nil {
		t.Fatalf("submit user message: %v", err)
	}
	if messageContent(assistant) != "done" {
		t.Fatalf("assistant content = %q, want done", messageContent(assistant))
	}
	time.Sleep(50 * time.Millisecond)
	client.mu.Lock()
	callCount := len(client.calls)
	client.mu.Unlock()
	if callCount != 2 {
		t.Fatalf("model call count = %d, want 2", callCount)
	}
	for _, msg := range eng.transcriptRuntimeState().SnapshotMessages() {
		if msg.Role == llm.RoleDeveloper && msg.MessageType != nil && *msg.MessageType == llm.MessageTypeBackgroundNotice {
			t.Fatalf("did not expect background notice for foreground exec_command completion: %+v", msg)
		}
	}
}

func TestBackgroundShellNoticeFlushesOnFirstAvailableSlot(t *testing.T) {
	dir := t.TempDir()
	store := mustCreateTestSessionAt(t, dir)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("working"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
			ToolCalls: []llm.ToolCall{{ID: "call_shell_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("foreground done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}

	started := make(chan struct{})
	release := make(chan struct{})
	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: blockingTool{name: toolspec.ToolExecCommand, started: started, release: release}}), Config{
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
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for tool call to start")
	}

	eng.HandleBackgroundShellUpdate(BackgroundShellEvent{
		Type:       BackgroundShellEventCompleted,
		ID:         "1000",
		State:      "completed",
		NoticeText: "Background shell 1000 completed.\nExit code: 0\nOutput:\ndone",
	}, true)

	client.mu.Lock()
	callCountWhileBusy := len(client.calls)
	client.mu.Unlock()
	if callCountWhileBusy != 1 {
		t.Fatalf("expected queued notice to avoid immediate model call while busy, got %d calls", callCountWhileBusy)
	}

	close(release)
	result := <-submitDone
	if result.err != nil {
		t.Fatalf("submit: %v", result.err)
	}
	if messageContent(result.assistant) != "foreground done" {
		t.Fatalf("assistant content = %q, want foreground done", messageContent(result.assistant))
	}

	client.mu.Lock()
	requests := append([]llm.Request(nil), client.calls...)
	client.mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("expected 2 model calls with background notice injected into the next request, got %d", len(requests))
	}

	containsNotice := func(req llm.Request) bool {
		for _, msg := range requestMessages(req) {
			if msg.Role == llm.RoleDeveloper && msg.MessageType != nil && *msg.MessageType == llm.MessageTypeBackgroundNotice && strings.Contains(messageContent(msg), "Background shell 1000 completed.") {
				return true
			}
		}
		return false
	}
	if !containsNotice(requests[1]) {
		t.Fatalf("expected background notice in first available in-turn follow-up, messages=%+v", requestMessages(requests[1]))
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
	hasImmediateBackgroundUpdate := false
	for _, evt := range events {
		if evt.Kind == EventBackgroundUpdated && evt.Background != nil && evt.Background.ID == "1000" {
			hasImmediateBackgroundUpdate = true
			if evt.CommittedEntryCount != 0 || evt.CommittedEntryStartSet {
				t.Fatalf("background update should not claim committed transcript range, got %+v", evt)
			}
			break
		}
	}
	if !hasImmediateBackgroundUpdate {
		t.Fatalf("expected immediate background_updated event, got %+v", events)
	}
}

func TestSteerAcceptedDuringReviewerAppearsInMainAgentFollowUp(t *testing.T) {
	mainClient := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("foreground done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("reviewed done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	reviewerResponse := llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(`{"suggestions":["apply the requested correction"]}`)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}
	reviewerClient, reviewerStarted, releaseReviewer := newGatedHookClient(reviewerResponse, reviewerResponse)
	eng := mustNewTestEngine(t, mustCreateTestSession(t), mainClient, tools.NewRegistry(), Config{
		Model: "gpt-5",
		Reviewer: ReviewerConfig{
			Frequency:     "all",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			Client:        reviewerClient,
		},
	})
	t.Cleanup(func() {
		eng.FailQueuedUserMessages(QueuedUserMessageFailureClosing)
		waitEngineLifecycleTasks(t, eng)
	})

	submitDone := make(chan struct {
		message llm.Message
		err     error
	}, 1)
	go func() {
		message, err := eng.SubmitUserMessage(context.Background(), "run task")
		submitDone <- struct {
			message llm.Message
			err     error
		}{message: message, err: err}
	}()
	select {
	case <-reviewerStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for reviewer request")
	}
	if _, err := eng.AcceptHumanSteering("steer reviewer follow-up", nil); err != nil {
		t.Fatalf("AcceptHumanSteering: %v", err)
	}
	releaseReviewer()
	result := <-submitDone
	if result.err != nil {
		t.Fatalf("submit: %v", result.err)
	}
	if messageContent(result.message) != "reviewed done" {
		t.Fatalf("assistant content = %q, want reviewed done", messageContent(result.message))
	}
	mainClient.mu.Lock()
	requests := append([]llm.Request(nil), mainClient.calls...)
	mainClient.mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("main-agent requests = %d, want initial and reviewer follow-up", len(requests))
	}
	assertRequestHasUserMessage(t, requests[1], "steer reviewer follow-up", true)
}

func TestEmitRawClearsCommittedRangeForBackgroundUpdated(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	var events []Event
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			events = append(events, evt)
		},
	})

	eng.emitRaw(Event{
		Kind:                   EventBackgroundUpdated,
		CommittedEntryCount:    99,
		CommittedEntryStart:    42,
		CommittedEntryStartSet: true,
		Background: &BackgroundShellEvent{
			Type:       BackgroundShellEventCompleted,
			ID:         "bg-1",
			State:      "completed",
			NoticeText: "Background shell bg-1 completed (exit 0)",
		},
	})

	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if got := events[0]; got.CommittedEntryCount != 0 || got.CommittedEntryStart != 0 || got.CommittedEntryStartSet {
		t.Fatalf("background update committed range = count:%d start:%d set:%t, want zero unset", got.CommittedEntryCount, got.CommittedEntryStart, got.CommittedEntryStartSet)
	}
}

func TestDeferredFinalWithBackgroundNoticeStillRunsReviewerAndEmitsAssistantEvent(t *testing.T) {
	dir := t.TempDir()
	store := mustCreateTestSessionAt(t, dir)

	mainClient := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("working"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
			ToolCalls: []llm.ToolCall{{ID: "call_shell_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("foreground done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(""), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(`{"suggestions":[]}`)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	started := make(chan struct{})
	release := make(chan struct{})
	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, mainClient, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: blockingTool{name: toolspec.ToolExecCommand, started: started, release: release}}), Config{
		Model: "gpt-5",
		Reviewer: ReviewerConfig{
			Frequency:     "all",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			Client:        reviewerClient,
		},
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
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for tool call to start")
	}

	eng.HandleBackgroundShellUpdate(BackgroundShellEvent{
		Type:       BackgroundShellEventCompleted,
		ID:         "1000",
		State:      "completed",
		NoticeText: "Background shell 1000 completed.\nExit code: 0\nOutput:\ndone",
	}, true)

	close(release)
	result := <-submitDone
	if result.err != nil {
		t.Fatalf("submit: %v", result.err)
	}
	if messageContent(result.assistant) != "foreground done" {
		t.Fatalf("assistant content = %q, want foreground done", messageContent(result.assistant))
	}
	if len(reviewerClient.calls) != 1 {
		t.Fatalf("expected reviewer to run once for deferred final, got %d", len(reviewerClient.calls))
	}

	mu.Lock()
	defer mu.Unlock()
	assistantContents := make([]string, 0, 2)
	for _, evt := range events {
		if evt.Kind != EventAssistantMessage {
			continue
		}
		assistantContents = append(assistantContents, messageContent(evt.Message))
	}
	if len(assistantContents) != 2 || assistantContents[0] != "working" || assistantContents[1] != "foreground done" {
		t.Fatalf("assistant message contents = %+v, want [working foreground done] events=%+v", assistantContents, events)
	}
}

func TestFinalAssistantBeforeSameTurnBackgroundNoticeKeepsCommittedFrontierContiguous(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := mustCreateTestSessionAt(t, dir)

	var (
		mu           sync.Mutex
		events       []Event
		queueOnce    sync.Once
		eng          *Engine
		backgroundID = "1000"
	)
	var client *hookClient
	client = &hookClient{
		response: llm.Response{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("foreground done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		beforeReturn: func() error {
			queueOnce.Do(func() {
				eng.HandleBackgroundShellUpdate(BackgroundShellEvent{
					Type:       BackgroundShellEventCompleted,
					ID:         backgroundID,
					State:      "completed",
					NoticeText: "Background shell 1000 completed.\nExit code: 0\nOutput:\ndone",
				}, true)
				client.mu.Lock()
				client.response = llm.Response{
					Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(""), Phase: textutil.Value(llm.MessagePhaseFinal)},
					Usage:     llm.Usage{WindowTokens: 200000},
				}
				client.mu.Unlock()
			})
			return nil
		},
	}
	eng = mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			mu.Lock()
			events = append(events, evt)
			mu.Unlock()
		},
	})
	if _, err := eng.SubmitUserMessage(context.Background(), "run task"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	committedEvents := committedTranscriptEventsWithEntries(events)
	assertRuntimeEventsAdvanceCommittedFrontierContiguously(t, committedEvents)
	assistantIdx := -1
	backgroundIdx := -1
	for idx, evt := range committedEvents {
		if assistantIdx < 0 && evt.Kind == EventAssistantMessage && messageContent(evt.Message) == "foreground done" {
			assistantIdx = idx
		}
		if evt.Kind == EventConversationUpdated && evt.Message.MessageType != nil && *evt.Message.MessageType == llm.MessageTypeBackgroundNotice {
			backgroundIdx = idx
		}
	}
	if assistantIdx < 0 {
		t.Fatalf("expected foreground final assistant event, got %+v", committedEvents)
	}
	if backgroundIdx < 0 {
		t.Fatalf("expected background notice committed event, got %+v", committedEvents)
	}
	if assistantIdx > backgroundIdx {
		t.Fatalf("foreground final assistant must publish before background notice, assistant_idx=%d background_idx=%d events=%+v", assistantIdx, backgroundIdx, committedEvents)
	}
}

func TestBackgroundShellNoticeSameTurnNoopAddsNoAssistantMessage(t *testing.T) {
	dir := t.TempDir()
	store := mustCreateTestSessionAt(t, dir)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("working"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
			ToolCalls: []llm.ToolCall{{ID: "call_shell_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(""), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}

	started := make(chan struct{})
	release := make(chan struct{})
	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: blockingTool{name: toolspec.ToolExecCommand, started: started, release: release}}), Config{
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
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for tool call to start")
	}

	eng.HandleBackgroundShellUpdate(BackgroundShellEvent{
		Type:       BackgroundShellEventCompleted,
		ID:         "1000",
		State:      "completed",
		NoticeText: "Background shell 1000 completed.\nExit code: 0\nOutput:\ndone",
	}, true)

	close(release)
	result := <-submitDone
	if result.err != nil {
		t.Fatalf("submit: %v", result.err)
	}
	if result.assistant.Content != nil {
		t.Fatalf("assistant content = %q, want absent", *result.assistant.Content)
	}

	client.mu.Lock()
	callCount := len(client.calls)
	requests := append([]llm.Request(nil), client.calls...)
	client.mu.Unlock()
	if callCount != 2 {
		t.Fatalf("expected 2 model calls within the same turn, got %d", callCount)
	}

	containsNotice := func(req llm.Request) bool {
		for _, msg := range requestMessages(req) {
			if msg.Role == llm.RoleDeveloper && msg.MessageType != nil && *msg.MessageType == llm.MessageTypeBackgroundNotice && strings.Contains(messageContent(msg), "Background shell 1000 completed.") {
				return true
			}
		}
		return false
	}
	if !containsNotice(requests[1]) {
		t.Fatalf("expected background notice in same-turn follow-up, messages=%+v", requestMessages(requests[1]))
	}
	time.Sleep(50 * time.Millisecond)
	client.mu.Lock()
	callCountAfterReturn := len(client.calls)
	client.mu.Unlock()
	if callCountAfterReturn != 2 {
		t.Fatalf("did not expect a later batched continuation after turn completion, got %d calls", callCountAfterReturn)
	}

	finalAssistantContents := make([]string, 0)
	foundBackgroundNotice := false
	noopFinalCount := 0
	for _, persisted := range eng.transcriptRuntimeState().SnapshotMessages() {
		if persisted.Role == llm.RoleAssistant && persisted.Phase != nil && *persisted.Phase == llm.MessagePhaseFinal {
			finalAssistantContents = append(finalAssistantContents, messageContent(persisted))
		}
		if persisted.Role == llm.RoleDeveloper && persisted.MessageType != nil && *persisted.MessageType == llm.MessageTypeBackgroundNotice && strings.Contains(messageContent(persisted), "Background shell 1000 completed.") {
			foundBackgroundNotice = true
		}
		if isBlankFinalAnswer(persisted) {
			noopFinalCount++
		}
	}
	if !foundBackgroundNotice {
		t.Fatalf("expected persisted background notice, got %+v", eng.transcriptRuntimeState().SnapshotMessages())
	}
	if noopFinalCount != 1 {
		t.Fatalf("noop final count = %d, want 1; messages=%+v", noopFinalCount, eng.transcriptRuntimeState().SnapshotMessages())
	}
	if len(finalAssistantContents) != 1 || finalAssistantContents[0] != "" {
		t.Fatalf("expected hidden persisted noop final assistant message, got %q", finalAssistantContents)
	}

	mu.Lock()
	defer mu.Unlock()
	assistantContents := make([]string, 0, 1)
	for _, evt := range events {
		if evt.Kind == EventAssistantMessage {
			assistantContents = append(assistantContents, messageContent(evt.Message))
		}
	}
	if len(assistantContents) != 1 || assistantContents[0] != "working" {
		t.Fatalf("assistant message contents = %+v, want [working] events=%+v", assistantContents, events)
	}
}
