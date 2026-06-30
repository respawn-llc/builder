package runtime

import (
	"context"
	"core/server/llm"
	"core/server/tools"
	shelltool "core/server/tools/shell"
	brand "core/shared/config"
	"core/shared/toolspec"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAppendMissingReviewerMetaContextKeepsExistingMetaMessages(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	existing := llm.Message{
		Role:        llm.RoleDeveloper,
		MessageType: llm.MessageTypeAgentsMD,
		Content:     agentsInjectedHeader + "\nsource: /tmp/AGENTS.md\n\n```md\nrule\n```",
	}
	existingEnv := llm.Message{
		Role:        llm.RoleDeveloper,
		MessageType: llm.MessageTypeEnvironment,
		Content:     environmentInjectedHeader + "\nOS: darwin",
	}
	in := []llm.Message{
		existing,
		existingEnv,
		{Role: llm.RoleUser, Content: "request"},
	}
	got, err := appendMissingReviewerMetaContext(in, workspace, "gpt-5", "high", "", false, nil)
	if err != nil {
		t.Fatalf("appendMissingReviewerMetaContext: %v", err)
	}
	if len(got) != len(in) {
		t.Fatalf("expected no extra messages when AGENTS+environment already present, got %d", len(got))
	}
}

func TestAppendMissingReviewerMetaContextBackfillsSkillsBetweenAgentsAndEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	writeTestSkill(t, filepath.Join(workspace, brand.ConfigDirName, "skills", "workspace-skill"), "workspace-skill", "from workspace")

	existingGlobalAgents := llm.Message{
		Role:        llm.RoleDeveloper,
		MessageType: llm.MessageTypeAgentsMD,
		SourcePath:  "/tmp/global/AGENTS.md",
		Content:     agentsInjectedHeader + "\nsource: /tmp/global/AGENTS.md\n\n```md\nglobal\n```",
	}
	existingWorkspaceAgents := llm.Message{
		Role:        llm.RoleDeveloper,
		MessageType: llm.MessageTypeAgentsMD,
		SourcePath:  "/tmp/workspace/AGENTS.md",
		Content:     agentsInjectedHeader + "\nsource: /tmp/workspace/AGENTS.md\n\n```md\nworkspace\n```",
	}
	existingEnv := llm.Message{
		Role:        llm.RoleDeveloper,
		MessageType: llm.MessageTypeEnvironment,
		Content:     environmentInjectedHeader + "\nOS: darwin",
	}
	in := []llm.Message{
		existingGlobalAgents,
		existingWorkspaceAgents,
		existingEnv,
		{Role: llm.RoleUser, Content: "request"},
	}

	got, err := appendMissingReviewerMetaContext(in, workspace, "gpt-5", "high", "", false, nil)
	if err != nil {
		t.Fatalf("appendMissingReviewerMetaContext: %v", err)
	}
	if len(got) != len(in)+1 {
		t.Fatalf("expected one skills message to be inserted, got len=%d", len(got))
	}
	if got[0].MessageType != llm.MessageTypeEnvironment {
		t.Fatalf("expected environment metadata first, got %+v", got[0])
	}
	if got[1].MessageType != llm.MessageTypeSkills {
		t.Fatalf("expected skills metadata after environment, got %+v", got[1])
	}
	if got[2].MessageType != llm.MessageTypeAgentsMD || got[3].MessageType != llm.MessageTypeAgentsMD {
		t.Fatalf("expected AGENTS metadata after environment+skills, got %+v %+v", got[2], got[3])
	}
	if got[4].Role != llm.RoleUser || got[4].Content != "request" {
		t.Fatalf("expected transcript content to stay at tail, got %+v", got[4])
	}
}

func TestAppendMissingReviewerMetaContextBackfillsSkillsBeforeEnvironmentWhenNoAgents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	writeTestSkill(t, filepath.Join(workspace, brand.ConfigDirName, "skills", "workspace-skill"), "workspace-skill", "from workspace")

	existingEnv := llm.Message{
		Role:        llm.RoleDeveloper,
		MessageType: llm.MessageTypeEnvironment,
		Content:     environmentInjectedHeader + "\nOS: darwin",
	}
	in := []llm.Message{
		existingEnv,
		{Role: llm.RoleUser, Content: "request"},
	}

	got, err := appendMissingReviewerMetaContext(in, workspace, "gpt-5", "high", "", false, nil)
	if err != nil {
		t.Fatalf("appendMissingReviewerMetaContext: %v", err)
	}
	if len(got) != len(in)+1 {
		t.Fatalf("expected one skills message to be inserted, got len=%d", len(got))
	}
	if got[0].MessageType != llm.MessageTypeEnvironment {
		t.Fatalf("expected environment metadata first when already present, got %+v", got[0])
	}
	if got[1].MessageType != llm.MessageTypeSkills {
		t.Fatalf("expected skills metadata after environment, got %+v", got[1])
	}
	if got[2].Role != llm.RoleUser || got[2].Content != "request" {
		t.Fatalf("expected transcript content to stay at tail, got %+v", got[2])
	}
}

func TestAppendMissingReviewerMetaContextBackfillsMissingWorkspaceAgentsSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	globalDir := filepath.Join(home, agentsGlobalDirName)
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatalf("mkdir global agents dir: %v", err)
	}
	globalPath := filepath.Join(globalDir, agentsFileName)
	if err := os.WriteFile(globalPath, []byte("global rule"), 0o644); err != nil {
		t.Fatalf("write global AGENTS: %v", err)
	}

	workspace := t.TempDir()
	workspacePath := filepath.Join(workspace, agentsFileName)
	if err := os.WriteFile(workspacePath, []byte("workspace rule"), 0o644); err != nil {
		t.Fatalf("write workspace AGENTS: %v", err)
	}

	in := []llm.Message{
		{
			Role:        llm.RoleDeveloper,
			MessageType: llm.MessageTypeAgentsMD,
			SourcePath:  globalPath,
			Content:     agentsInjectedHeader + "\nsource: " + globalPath + "\n\n```md\nglobal rule\n```",
		},
		{Role: llm.RoleUser, Content: "request"},
	}
	got, err := appendMissingReviewerMetaContext(in, workspace, "gpt-5", "high", "", false, nil)
	if err != nil {
		t.Fatalf("appendMissingReviewerMetaContext: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("expected global+workspace agents, environment, and transcript, got %d", len(got))
	}
	if got[0].MessageType != llm.MessageTypeEnvironment {
		t.Fatalf("expected environment first, got %+v", got[0])
	}
	if got[1].MessageType != llm.MessageTypeAgentsMD || !strings.Contains(got[1].Content, "source: "+globalPath) {
		t.Fatalf("expected global AGENTS after environment, got %+v", got[1])
	}
	if got[2].MessageType != llm.MessageTypeAgentsMD || !strings.Contains(got[2].Content, "source: "+workspacePath) {
		t.Fatalf("expected missing workspace AGENTS to be backfilled last in base context, got %+v", got[2])
	}
	if got[3].Role != llm.RoleUser || got[3].Content != "request" {
		t.Fatalf("expected transcript content at tail, got %+v", got[3])
	}
}

func TestAppendMissingReviewerMetaContextLeavesUntypedLegacyMetaInTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	globalDir := filepath.Join(home, agentsGlobalDirName)
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatalf("mkdir global agents dir: %v", err)
	}
	globalPath := filepath.Join(globalDir, agentsFileName)
	if err := os.WriteFile(globalPath, []byte("global rule"), 0o644); err != nil {
		t.Fatalf("write global AGENTS: %v", err)
	}

	workspace := t.TempDir()
	legacyWorkspacePath := filepath.Join(workspace, agentsFileName)
	in := []llm.Message{
		{
			Role:    llm.RoleDeveloper,
			Content: agentsInjectedHeader + "\nsource: " + legacyWorkspacePath + "\n\n```md\nlegacy workspace rule\n```",
		},
		{
			Role:    llm.RoleDeveloper,
			Content: "## Skills\n" + skillsAvailableHeader + "\n- legacy-skill: legacy description (file: /tmp/legacy/SKILL.md)",
		},
		{
			Role:    llm.RoleDeveloper,
			Content: environmentInjectedHeader + "\nOS: darwin",
		},
		{Role: llm.RoleUser, Content: "request"},
	}

	got, err := appendMissingReviewerMetaContext(in, workspace, "gpt-5", "high", "", false, nil)
	if err != nil {
		t.Fatalf("appendMissingReviewerMetaContext: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("expected live metadata plus preserved legacy transcript entries, got %d", len(got))
	}
	if got[0].MessageType != llm.MessageTypeEnvironment || !strings.Contains(got[0].Content, environmentInjectedHeader) {
		t.Fatalf("expected live environment metadata first, got %+v", got[0])
	}
	if got[1].MessageType != llm.MessageTypeAgentsMD || !strings.Contains(got[1].Content, "source: "+globalPath) {
		t.Fatalf("expected live global AGENTS after environment, got %+v", got[1])
	}
	if got[2].Role != llm.RoleDeveloper || !strings.Contains(got[2].Content, legacyWorkspacePath) {
		t.Fatalf("expected untyped legacy AGENTS text to remain transcript content, got %+v", got[2])
	}
	if got[3].Role != llm.RoleDeveloper || !strings.Contains(got[3].Content, "legacy-skill") {
		t.Fatalf("expected untyped legacy skills text to remain transcript content, got %+v", got[3])
	}
	if got[4].Role != llm.RoleDeveloper || !strings.Contains(got[4].Content, environmentInjectedHeader) {
		t.Fatalf("expected untyped legacy environment text to remain transcript content, got %+v", got[4])
	}
	if got[5].Role != llm.RoleUser || got[5].Content != "request" {
		t.Fatalf("expected transcript content at tail, got %+v", got[5])
	}
}

func TestFastExecCommandCompletionDoesNotQueueBackgroundNotice(t *testing.T) {
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
				Content: "running fast command",
			},
			ToolCalls: []llm.ToolCall{{
				ID:    "call_exec_1",
				Name:  string(toolspec.ToolExecCommand),
				Input: json.RawMessage(`{"cmd":"echo hi","shell":"/bin/sh","login":false,"yield_time_ms":1000}`),
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done"},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "unexpected extra turn"},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	registry := tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: shelltool.NewExecCommandTool(dir, 16_000, manager, "")})
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
		}, true)
	})

	assistant, err := eng.SubmitUserMessage(context.Background(), "run fast command")
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
	if callCount != 2 {
		t.Fatalf("model call count = %d, want 2", callCount)
	}
	for _, msg := range eng.transcriptRuntimeState().SnapshotMessages() {
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeBackgroundNotice {
			t.Fatalf("did not expect background notice for foreground exec_command completion: %+v", msg)
		}
	}
}

func TestBackgroundShellNoticeFlushesOnFirstAvailableSlot(t *testing.T) {
	dir := t.TempDir()
	store := mustCreateTestSessionAt(t, dir)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "working", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{{ID: "call_shell_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "foreground done", Phase: llm.MessagePhaseFinal},
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
		Type:       "completed",
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
	if result.assistant.Content != "foreground done" {
		t.Fatalf("assistant content = %q, want foreground done", result.assistant.Content)
	}

	client.mu.Lock()
	requests := append([]llm.Request(nil), client.calls...)
	client.mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("expected 2 model calls with background notice injected into the next request, got %d", len(requests))
	}

	containsNotice := func(req llm.Request) bool {
		for _, msg := range requestMessages(req) {
			if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeBackgroundNotice && strings.Contains(msg.Content, "Background shell 1000 completed.") {
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
			break
		}
	}
	if !hasImmediateBackgroundUpdate {
		t.Fatalf("expected immediate background_updated event, got %+v", events)
	}
}

func TestDeferredFinalWithBackgroundNoticeStillRunsReviewerAndEmitsAssistantEvent(t *testing.T) {
	dir := t.TempDir()
	store := mustCreateTestSessionAt(t, dir)

	mainClient := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "working", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{{ID: "call_shell_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "foreground done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: reviewerNoopToken, Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":[]}`},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	started := make(chan struct{})
	release := make(chan struct{})
	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, mainClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: blockingTool{name: toolspec.ToolExecCommand, started: started, release: release}}), Config{
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
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for tool call to start")
	}

	eng.HandleBackgroundShellUpdate(BackgroundShellEvent{
		Type:       "completed",
		ID:         "1000",
		State:      "completed",
		NoticeText: "Background shell 1000 completed.\nExit code: 0\nOutput:\ndone",
	}, true)

	close(release)
	result := <-submitDone
	if result.err != nil {
		t.Fatalf("submit: %v", result.err)
	}
	if result.assistant.Content != "foreground done" {
		t.Fatalf("assistant content = %q, want foreground done", result.assistant.Content)
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
		assistantContents = append(assistantContents, evt.Message.Content)
	}
	if len(assistantContents) != 2 || assistantContents[0] != "working" || assistantContents[1] != "foreground done" {
		t.Fatalf("assistant message contents = %+v, want [working foreground done] events=%+v", assistantContents, events)
	}
}

func TestDeferredFinalWithQueuedUserInjectionStillRunsReviewerAndEmitsAssistantEvent(t *testing.T) {
	dir := t.TempDir()
	store := mustCreateTestSessionAt(t, dir)

	mainClient := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "foreground done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: reviewerNoopToken, Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":[]}`},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, mainClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
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

	eng.QueueUserMessage("steer now")
	result, err := eng.SubmitUserMessage(context.Background(), "run task")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if result.Content != "foreground done" {
		t.Fatalf("assistant content = %q, want foreground done", result.Content)
	}
	if len(reviewerClient.calls) != 1 {
		t.Fatalf("expected reviewer to run once for deferred final, got %d", len(reviewerClient.calls))
	}
	if len(mainClient.calls) != 2 {
		t.Fatalf("expected two main model calls for deferred final path, got %d", len(mainClient.calls))
	}
	snapshot := eng.ChatSnapshot()

	mu.Lock()
	defer mu.Unlock()
	assistantMessages := 0
	flushedQueuedUser := false
	assistantCommittedStart := -1
	assistantCommittedStartSet := false
	for i, evt := range events {
		_ = i
		if evt.Kind == EventAssistantMessage {
			assistantMessages++
			if evt.Message.Content != "foreground done" {
				t.Fatalf("assistant message content = %q, want foreground done", evt.Message.Content)
			}
			assistantCommittedStart = evt.CommittedEntryStart
			assistantCommittedStartSet = evt.CommittedEntryStartSet
		}
		if evt.Kind == EventUserMessageFlushed && evt.UserMessage == "steer now" {
			flushedQueuedUser = true
		}
	}
	if assistantMessages != 1 {
		t.Fatalf("expected one assistant_message event for deferred final, got %d events=%+v", assistantMessages, events)
	}
	if !flushedQueuedUser {
		t.Fatalf("expected queued user injection flush event, got %+v", events)
	}
	if !assistantCommittedStartSet {
		t.Fatalf("expected deferred final assistant event committed start metadata, got %+v", events)
	}
	if assistantCommittedStart < 0 || assistantCommittedStart >= len(snapshot.Entries) {
		t.Fatalf("deferred final assistant committed start = %d, snapshot=%+v", assistantCommittedStart, snapshot.Entries)
	}
	assistantEntry := snapshot.Entries[assistantCommittedStart]
	if assistantEntry.Role != "assistant" || assistantEntry.Text != "foreground done" {
		t.Fatalf("expected deferred final assistant event to point at committed assistant row, got %+v", assistantEntry)
	}
}

func TestDeferredFinalWithQueuedUserInjectionAndTrailingNoopStillUsesDeferredFinal(t *testing.T) {
	dir := t.TempDir()
	store := mustCreateTestSessionAt(t, dir)

	mainClient := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "foreground done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: reviewerNoopToken, Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":[]}`},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, mainClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
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

	eng.QueueUserMessage("steer now")
	result, err := eng.SubmitUserMessage(context.Background(), "run task")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if result.Content != "foreground done" {
		t.Fatalf("assistant content = %q, want foreground done", result.Content)
	}
	if len(reviewerClient.calls) != 1 {
		t.Fatalf("expected reviewer to run once for deferred final, got %d", len(reviewerClient.calls))
	}
	snapshot := eng.ChatSnapshot()

	mu.Lock()
	defer mu.Unlock()
	assistantMessages := 0
	assistantCommittedStart := -1
	assistantCommittedStartSet := false
	for _, evt := range events {
		if evt.Kind != EventAssistantMessage {
			continue
		}
		assistantMessages++
		if evt.Message.Content != "foreground done" {
			t.Fatalf("assistant message content = %q, want foreground done", evt.Message.Content)
		}
		assistantCommittedStart = evt.CommittedEntryStart
		assistantCommittedStartSet = evt.CommittedEntryStartSet
	}
	if assistantMessages != 1 {
		t.Fatalf("expected one assistant_message event for deferred final, got %d events=%+v", assistantMessages, events)
	}
	if !assistantCommittedStartSet {
		t.Fatalf("expected deferred final assistant event committed start metadata, got %+v", events)
	}
	if assistantCommittedStart < 0 || assistantCommittedStart >= len(snapshot.Entries) {
		t.Fatalf("deferred final assistant committed start = %d, snapshot=%+v", assistantCommittedStart, snapshot.Entries)
	}
	assistantEntry := snapshot.Entries[assistantCommittedStart]
	if assistantEntry.Role != "assistant" || assistantEntry.Text != "foreground done" {
		t.Fatalf("expected deferred final assistant event to point at committed assistant row, got %+v", assistantEntry)
	}
}

func TestBackgroundShellNoticeSameTurnNoopAddsNoAssistantMessage(t *testing.T) {
	dir := t.TempDir()
	store := mustCreateTestSessionAt(t, dir)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "working", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{{ID: "call_shell_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: reviewerNoopToken, Phase: llm.MessagePhaseFinal},
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
		Type:       "completed",
		ID:         "1000",
		State:      "completed",
		NoticeText: "Background shell 1000 completed.\nExit code: 0\nOutput:\ndone",
	}, true)

	close(release)
	result := <-submitDone
	if result.err != nil {
		t.Fatalf("submit: %v", result.err)
	}
	if strings.TrimSpace(result.assistant.Content) != "" {
		t.Fatalf("assistant content = %q, want empty", result.assistant.Content)
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
			if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeBackgroundNotice && strings.Contains(msg.Content, "Background shell 1000 completed.") {
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
		if persisted.Role == llm.RoleAssistant && persisted.Phase == llm.MessagePhaseFinal {
			finalAssistantContents = append(finalAssistantContents, persisted.Content)
		}
		if persisted.Role == llm.RoleDeveloper && persisted.MessageType == llm.MessageTypeBackgroundNotice && strings.Contains(persisted.Content, "Background shell 1000 completed.") {
			foundBackgroundNotice = true
		}
		if isNoopFinalAnswer(persisted) {
			noopFinalCount++
		}
	}
	if !foundBackgroundNotice {
		t.Fatalf("expected persisted background notice, got %+v", eng.transcriptRuntimeState().SnapshotMessages())
	}
	if noopFinalCount != 1 {
		t.Fatalf("noop final count = %d, want 1; messages=%+v", noopFinalCount, eng.transcriptRuntimeState().SnapshotMessages())
	}
	if len(finalAssistantContents) != 1 || finalAssistantContents[0] != reviewerNoopToken {
		t.Fatalf("expected hidden persisted noop final assistant message, got %q", finalAssistantContents)
	}

	mu.Lock()
	defer mu.Unlock()
	assistantContents := make([]string, 0, 1)
	for _, evt := range events {
		if evt.Kind == EventAssistantMessage {
			assistantContents = append(assistantContents, evt.Message.Content)
		}
	}
	if len(assistantContents) != 1 || assistantContents[0] != "working" {
		t.Fatalf("assistant message contents = %+v, want [working] events=%+v", assistantContents, events)
	}
}
