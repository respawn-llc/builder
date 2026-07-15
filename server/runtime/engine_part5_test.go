package runtime

import (
	"context"
	"errors"

	"core/prompts"
	"core/server/llm"
	"core/server/tools"
	"core/shared/toolspec"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestReviewerRunsOnAllFrequencyWithoutToolCalls(t *testing.T) {
	store := mustCreateTestSession(t)

	mainClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":[]}`},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	eng := mustNewTestEngine(t, store, mainClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		Reviewer: ReviewerConfig{
			Frequency:     "all",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			VerboseOutput: true,
			Client:        reviewerClient,
		},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "hello")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != "done" {
		t.Fatalf("assistant content = %q, want done", msg.Content)
	}
	if len(reviewerClient.calls) != 1 {
		t.Fatalf("expected reviewer to be called once for frequency=all, got %d", len(reviewerClient.calls))
	}
}

func TestReviewerSystemPromptFileIsLazyLockedAndReused(t *testing.T) {
	dir := t.TempDir()
	reviewerPromptPath := filepath.Join(dir, "reviewer-prompt.md")
	writeTestFile(t, reviewerPromptPath, "custom reviewer prompt")

	store := mustCreateTestSessionAt(t, dir)
	mainClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":[]}`},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	eng := mustNewTestEngine(t, store, mainClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		Reviewer: ReviewerConfig{
			Frequency:        "all",
			Model:            "gpt-5",
			SystemPromptFile: reviewerPromptPath,
			Client:           reviewerClient,
		},
	})
	if _, err := eng.SubmitUserMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := reviewerClient.calls[0].SystemPrompt; got != "custom reviewer prompt" {
		t.Fatalf("reviewer system prompt = %q, want custom reviewer prompt", got)
	}
	if locked := store.Meta().Locked; locked == nil || !locked.HasReviewerPrompt || locked.ReviewerPrompt != "custom reviewer prompt" {
		t.Fatalf("locked reviewer prompt = %+v, want custom reviewer prompt snapshot", locked)
	}

	writeTestFile(t, reviewerPromptPath, "changed reviewer prompt")
	if err := eng.Close(); err != nil {
		t.Fatalf("close engine: %v", err)
	}
	reopened, err := runtimeTestSessionPersistence.Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	reopenedReviewer := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":[]}`},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	reopenedEngine := mustNewTestEngine(t, reopened, mainClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		Reviewer: ReviewerConfig{
			Model:            "gpt-5",
			SystemPromptFile: reviewerPromptPath,
		},
	})
	if err != nil {
		t.Fatalf("new reopened engine: %v", err)
	}
	if _, err := reopenedEngine.runReviewerSuggestions(context.Background(), "step-2", reopenedReviewer); err != nil {
		t.Fatalf("run reviewer suggestions: %v", err)
	}
	if got := reopenedReviewer.calls[0].SystemPrompt; got != "custom reviewer prompt" {
		t.Fatalf("reopened reviewer system prompt = %q, want locked custom reviewer prompt", got)
	}
}

func TestReviewerSystemPromptRefreshesIndependentlyAfterCompaction(t *testing.T) {
	workspace := t.TempDir()
	reviewerPromptPath := filepath.Join(workspace, "reviewer.md")
	writeTestFile(t, reviewerPromptPath, "reviewer A")
	autoCompactionEnabled := false
	store := mustCreateTestSession(t, workspace)
	mainClient := &fakeClient{responses: []llm.Response{
		{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "ok"}, Usage: llm.Usage{WindowTokens: 200000}},
		{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "summary"}, Usage: llm.Usage{WindowTokens: 200000}},
	}}
	reviewerClient := &fakeClient{}
	eng := mustNewExecTestEngine(t, store, mainClient, Config{
		CompactionMode:        "local",
		AutoCompactionEnabled: &autoCompactionEnabled,
		Reviewer: ReviewerConfig{
			Frequency:        "all",
			Model:            "gpt-5",
			SystemPromptFile: reviewerPromptPath,
			Client:           reviewerClient,
		},
	})
	if _, err := eng.SubmitUserMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	reviewerReq, err := eng.buildReviewerRequest(context.Background(), reviewerClient)
	if err != nil {
		t.Fatalf("build reviewer before compaction: %v", err)
	}
	if reviewerReq.SystemPrompt != "reviewer A" {
		t.Fatalf("reviewer before compaction = %q, want reviewer A", reviewerReq.SystemPrompt)
	}
	writeTestFile(t, reviewerPromptPath, "reviewer B")
	if err := eng.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact: %v", err)
	}
	mainLocked := store.Meta().Locked
	if mainLocked == nil || mainLocked.HasSystemPrompt || mainLocked.HasReviewerPrompt {
		t.Fatalf("locked prompts after compaction = %+v, want both stale", mainLocked)
	}
	reviewerReq, err = eng.buildReviewerRequest(context.Background(), reviewerClient)
	if err != nil {
		t.Fatalf("build reviewer after compaction: %v", err)
	}
	if reviewerReq.SystemPrompt != "reviewer B" {
		t.Fatalf("reviewer after compaction = %q, want reviewer B", reviewerReq.SystemPrompt)
	}
	if locked := store.Meta().Locked; locked == nil || locked.SystemPrompt != "" || !locked.HasReviewerPrompt || locked.ReviewerPrompt != "reviewer B" {
		t.Fatalf("locked prompts after reviewer refresh = %+v", locked)
	}
}

func TestReviewerSystemPromptFileResolvesTilde(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	t.Setenv("HOME", home)
	reviewerPromptPath := filepath.Join(home, "reviewer-prompt.md")
	writeTestFile(t, reviewerPromptPath, "tilde reviewer prompt")

	store := mustCreateTestSessionAt(t, dir)
	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":[]}`},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		Reviewer: ReviewerConfig{
			Model:            "gpt-5",
			SystemPromptFile: "~/reviewer-prompt.md",
		},
	})
	if _, err := eng.runReviewerSuggestions(context.Background(), "step-1", reviewerClient); err != nil {
		t.Fatalf("run reviewer suggestions: %v", err)
	}
	if got := reviewerClient.calls[0].SystemPrompt; got != "tilde reviewer prompt" {
		t.Fatalf("reviewer system prompt = %q, want tilde reviewer prompt", got)
	}
}

func TestReviewerSystemPromptFileMissingFailsWithoutSnapshot(t *testing.T) {
	dir := t.TempDir()
	missingPromptPath := filepath.Join(dir, "missing-reviewer-prompt.md")
	store := mustCreateTestSessionAt(t, dir)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		Reviewer: ReviewerConfig{
			Model:            "gpt-5",
			SystemPromptFile: missingPromptPath,
		},
	})
	if _, err := eng.ensureLocked(); err != nil {
		t.Fatalf("ensure locked: %v", err)
	}
	_, err := eng.runReviewerSuggestions(context.Background(), "step-1", &fakeClient{})
	if !errors.Is(err, errReadReviewerSystemPromptFile) {
		t.Fatalf("expected errReadReviewerSystemPromptFile, got %v", err)
	}
	if locked := store.Meta().Locked; locked == nil || locked.HasReviewerPrompt || locked.ReviewerPrompt != "" {
		t.Fatalf("locked reviewer prompt = %+v, want no reviewer prompt snapshot", locked)
	}
}

func TestReviewerFrequencyOffDoesNotReadSystemPromptFile(t *testing.T) {
	dir := t.TempDir()
	missingPromptPath := filepath.Join(dir, "missing-reviewer-prompt.md")
	store := mustCreateTestSessionAt(t, dir)
	mainClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":[]}`},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	eng := mustNewTestEngine(t, store, mainClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		Reviewer: ReviewerConfig{
			Frequency:        "off",
			Model:            "gpt-5",
			SystemPromptFile: missingPromptPath,
			Client:           reviewerClient,
		},
	})
	if _, err := eng.SubmitUserMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(reviewerClient.calls) != 0 {
		t.Fatalf("expected reviewer not to run, got %d calls", len(reviewerClient.calls))
	}
	if locked := store.Meta().Locked; locked == nil || locked.HasReviewerPrompt || locked.ReviewerPrompt != "" {
		t.Fatalf("locked reviewer prompt = %+v, want no reviewer prompt snapshot", locked)
	}
}

func TestReviewerSuggestionsRequestInheritsFastMode(t *testing.T) {
	store := mustCreateTestSession(t)

	mainClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":[]}`},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	eng := mustNewTestEngine(t, store, mainClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:           "gpt-5",
		FastModeEnabled: true,
		Reviewer: ReviewerConfig{
			Frequency:     "all",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			Client:        reviewerClient,
		},
	})

	if _, err := eng.SubmitUserMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(reviewerClient.calls) != 1 {
		t.Fatalf("expected reviewer to be called once, got %d", len(reviewerClient.calls))
	}
	if !reviewerClient.calls[0].FastMode {
		t.Fatal("expected reviewer request to inherit fast mode")
	}
}

func TestFinalNoopAnswerIsInvisibleAndSkipsReviewer(t *testing.T) {
	store := mustCreateTestSession(t)

	mainClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: reviewerNoopToken, Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":["x"]}`},
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

	msg, err := eng.SubmitUserMessage(context.Background(), "hello")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != "" {
		t.Fatalf("assistant content = %q, want empty", msg.Content)
	}
	if len(mainClient.calls) != 1 {
		t.Fatalf("expected one main model call, got %d", len(mainClient.calls))
	}
	if len(reviewerClient.calls) != 0 {
		t.Fatalf("expected reviewer not to run for NO_OP final, got %d calls", len(reviewerClient.calls))
	}

	finalAssistantContents := make([]string, 0)
	noopFinalCount := 0
	for _, persisted := range eng.transcriptRuntimeState().SnapshotMessages() {
		if persisted.Role == llm.RoleAssistant && persisted.Phase == llm.MessagePhaseFinal {
			finalAssistantContents = append(finalAssistantContents, persisted.Content)
		}
		if isNoopFinalAnswer(persisted) {
			noopFinalCount++
		}
	}
	if noopFinalCount != 1 {
		t.Fatalf("noop final count = %d, want 1; messages=%+v", noopFinalCount, eng.transcriptRuntimeState().SnapshotMessages())
	}
	if len(finalAssistantContents) != 1 || finalAssistantContents[0] != reviewerNoopToken {
		t.Fatalf("expected hidden persisted noop final assistant message, got %q", finalAssistantContents)
	}

	snapshot := eng.ChatSnapshot()
	for _, entry := range snapshot.Entries {
		if strings.Contains(entry.Text, reviewerNoopToken) {
			t.Fatalf("noop token leaked into chat snapshot: %+v", snapshot.Entries)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	assistantEvents := 0
	modelResponseEvents := 0
	for _, evt := range events {
		if evt.Kind == EventAssistantMessage {
			assistantEvents++
		}
		if evt.Kind == EventModelResponse {
			modelResponseEvents++
		}
	}
	if assistantEvents != 0 {
		t.Fatalf("expected no assistant_message events for NO_OP final, got %d", assistantEvents)
	}
	if modelResponseEvents != 0 {
		t.Fatalf("expected no model_response_received events for NO_OP final, got %d", modelResponseEvents)
	}
}

func TestReviewerRunsOnEditsFrequencyOnlyWhenPatchApplied(t *testing.T) {
	store := mustCreateTestSession(t)

	mainClient := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "working", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{{ID: "call_patch_1", Name: string(toolspec.ToolPatch), Custom: true, CustomInput: "*** Begin Patch\n*** Add File: a.txt\n+hello\n*** End Patch"}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "final", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":[]}`},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	eng := mustNewTestEngine(t, store, mainClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolPatch, Handler: fakeTool{name: toolspec.ToolPatch}}), Config{
		Model: "gpt-5",
		Reviewer: ReviewerConfig{
			Frequency:     "edits",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			Client:        reviewerClient,
		},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "edit file")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != "final" {
		t.Fatalf("assistant content = %q, want final", msg.Content)
	}
	if len(reviewerClient.calls) != 1 {
		t.Fatalf("expected reviewer to be called once after patch edit, got %d", len(reviewerClient.calls))
	}
}

func TestReviewerSuggestionsTriggerFollowUpAndNoopKeepsOriginalAnswer(t *testing.T) {
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

	store := mustCreateTestSession(t)

	mainClient := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "working", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{
				{ID: "call_shell_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "original final", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: reviewerNoopToken, Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}

	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":["Double-check test output before final handoff."]}`},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	var (
		eventsMu sync.Mutex
		events   []Event
	)
	eng := mustNewTestEngine(t, store, mainClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			eventsMu.Lock()
			defer eventsMu.Unlock()
			events = append(events, evt)
		},
		Reviewer: ReviewerConfig{
			Frequency:     "all",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			VerboseOutput: true,
			Client:        reviewerClient,
		},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "do task")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != "original final" {
		t.Fatalf("assistant content = %q, want original final", msg.Content)
	}
	if len(reviewerClient.calls) != 1 {
		t.Fatalf("expected one reviewer call, got %d", len(reviewerClient.calls))
	}
	if len(mainClient.calls) != 3 {
		t.Fatalf("expected 3 main calls (tool loop + final + follow-up), got %d", len(mainClient.calls))
	}

	req := mainClient.calls[2]
	foundReviewInstruction := false
	for _, message := range requestMessages(req) {
		if message.Role == llm.RoleDeveloper && strings.Contains(message.Content, "Supervisor agent gave you suggestions") {
			if message.MessageType != llm.MessageTypeReviewerFeedback {
				t.Fatalf("expected reviewer feedback message type, got %+v", message)
			}
			foundReviewInstruction = true
			break
		}
	}
	if !foundReviewInstruction {
		t.Fatalf("expected reviewer suggestions developer message in follow-up request")
	}

	reviewerReq := reviewerClient.calls[0]
	if reviewerReq.SystemPrompt != prompts.ReviewerSystemPrompt {
		t.Fatalf("unexpected reviewer prompt")
	}
	if reviewerReq.SessionID != reviewerSessionID(store.Meta().SessionID) {
		t.Fatalf("expected reviewer session id suffix, got %q", reviewerReq.SessionID)
	}
	if len(requestMessages(reviewerReq)) == 0 {
		t.Fatalf("expected reviewer request to include transcript entry messages")
	}
	if requestMessages(reviewerReq)[0].Role != llm.RoleDeveloper || requestMessages(reviewerReq)[0].MessageType != llm.MessageTypeEnvironment {
		t.Fatalf("expected reviewer message[0] to be environment meta developer message, got %+v", requestMessages(reviewerReq)[0])
	}
	agentsIdx := -1
	environmentIdx := -1
	boundaryIdx := -1
	skillsMetaIdx := -1
	for idx, message := range requestMessages(reviewerReq) {
		if message.Role == llm.RoleDeveloper && message.MessageType == llm.MessageTypeAgentsMD && strings.Contains(message.Content, "source: "+globalPath) {
			agentsIdx = idx
		}
		if message.Role == llm.RoleDeveloper && message.MessageType == llm.MessageTypeEnvironment {
			environmentIdx = idx
		}
		if message.Role == llm.RoleDeveloper && message.MessageType == llm.MessageTypeSkills {
			skillsMetaIdx = idx
		}
		if message.Role == llm.RoleDeveloper && message.Content == reviewerMetaBoundaryMessage {
			boundaryIdx = idx
			break
		}
	}
	if environmentIdx < 0 {
		t.Fatalf("expected reviewer metadata to include environment context, got %+v", requestMessages(reviewerReq))
	}
	if boundaryIdx < 0 {
		t.Fatalf("expected reviewer metadata to include transcript boundary message, got %+v", requestMessages(reviewerReq))
	}
	if agentsIdx < 0 {
		t.Fatalf("expected reviewer metadata to include AGENTS context, got %+v", requestMessages(reviewerReq))
	}
	if environmentIdx >= boundaryIdx {
		t.Fatalf("expected environment metadata before boundary, env=%d boundary=%d", environmentIdx, boundaryIdx)
	}
	if agentsIdx <= environmentIdx {
		t.Fatalf("expected AGENTS metadata after environment, agents=%d env=%d", agentsIdx, environmentIdx)
	}
	if skillsMetaIdx >= 0 && (skillsMetaIdx <= environmentIdx || skillsMetaIdx >= agentsIdx) {
		t.Fatalf("expected skills metadata between environment and AGENTS when present, skills=%d env=%d agents=%d", skillsMetaIdx, environmentIdx, agentsIdx)
	}
	foundAgentLabel := false
	foundToolCallEntry := false
	foundToolResultEntry := false
	for _, message := range requestMessages(reviewerReq)[boundaryIdx+1:] {
		if message.Role != llm.RoleUser {
			t.Fatalf("expected reviewer transcript entries after metadata to be user role messages, got %q", message.Role)
		}
		if strings.Contains(message.Content, "Agent:") {
			foundAgentLabel = true
		}
		if strings.Contains(message.Content, "Tool call:") && strings.Contains(message.Content, "pwd") {
			foundToolCallEntry = true
		}
		if strings.Contains(message.Content, "Tool result:") && strings.Contains(message.Content, "{\"tool\":\"exec_command\"}") {
			foundToolResultEntry = true
		}
	}
	if !foundAgentLabel {
		t.Fatalf("expected reviewer request to include agent labels, messages=%+v", requestMessages(reviewerReq))
	}
	if !foundToolCallEntry {
		t.Fatalf("expected reviewer request to include tool call transcript entries, messages=%+v", requestMessages(reviewerReq))
	}
	if !foundToolResultEntry {
		t.Fatalf("expected reviewer request to include tool result transcript entries, messages=%+v", requestMessages(reviewerReq))
	}
	if len(reviewerReq.Items) == 0 {
		t.Fatalf("expected reviewer request items to carry canonical transcript history")
	}
	if len(reviewerReq.Tools) != 0 {
		t.Fatalf("expected reviewer request with no tools")
	}
	if reviewerReq.StructuredOutput == nil {
		t.Fatalf("expected reviewer request structured output")
	}
	if reviewerReq.StructuredOutput.Name != "reviewer_suggestions" {
		t.Fatalf("unexpected reviewer structured output name: %+v", reviewerReq.StructuredOutput)
	}

	snapshot := eng.ChatSnapshot()
	foundReviewerStatus := false
	for _, entry := range snapshot.Entries {
		if strings.Contains(entry.Text, reviewerNoopToken) {
			t.Fatalf("noop token leaked into chat snapshot: %+v", snapshot.Entries)
		}
		if entry.Role == "reviewer_status" && strings.Contains(entry.Text, "Supervisor ran") {
			foundReviewerStatus = true
		}
	}
	if !foundReviewerStatus {
		t.Fatalf("expected reviewer status entry in snapshot, got %+v", snapshot.Entries)
	}

	eventsMu.Lock()
	recordedEvents := append([]Event(nil), events...)
	eventsMu.Unlock()
	originalFinalEventIdx := -1
	reviewerSuggestionsEventIdx := -1
	reviewerStatusEventIdx := -1
	for idx, evt := range recordedEvents {
		if evt.Kind == EventAssistantMessage && evt.Message.Content == "original final" {
			originalFinalEventIdx = idx
		}
		if evt.Kind == EventLocalEntryAdded && evt.LocalEntry != nil && evt.LocalEntry.Role == "reviewer_suggestions" {
			reviewerSuggestionsEventIdx = idx
		}
		if evt.Kind == EventLocalEntryAdded && evt.LocalEntry != nil && evt.LocalEntry.Role == "reviewer_status" {
			reviewerStatusEventIdx = idx
		}
	}
	if originalFinalEventIdx < 0 {
		t.Fatalf("expected original final assistant event before reviewer events, got %+v", recordedEvents)
	}
	if reviewerSuggestionsEventIdx < 0 {
		t.Fatalf("expected reviewer suggestions local entry event, got %+v", recordedEvents)
	}
	if reviewerStatusEventIdx < 0 {
		t.Fatalf("expected reviewer status local entry event, got %+v", recordedEvents)
	}
	if originalFinalEventIdx > reviewerSuggestionsEventIdx || reviewerSuggestionsEventIdx > reviewerStatusEventIdx {
		t.Fatalf("expected original final -> reviewer suggestions -> reviewer status event order, got %+v", recordedEvents)
	}
}

func TestReviewerNoSuggestionsPersistsStatusEntry(t *testing.T) {
	store := mustCreateTestSession(t)

	mainClient := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "working", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{
				{ID: "call_shell_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "final", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}

	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":[]}`},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	eng := mustNewTestEngine(t, store, mainClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		Reviewer: ReviewerConfig{
			Frequency:     "all",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			VerboseOutput: true,
			Client:        reviewerClient,
		},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "do task")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != "final" {
		t.Fatalf("assistant content = %q, want final", msg.Content)
	}

	snapshot := eng.ChatSnapshot()
	foundNoSuggestionsStatus := false
	for _, entry := range snapshot.Entries {
		if entry.Role == "reviewer_status" && strings.Contains(entry.Text, "no suggestions") {
			foundNoSuggestionsStatus = true
			break
		}
	}
	if !foundNoSuggestionsStatus {
		t.Fatalf("expected no-suggestions reviewer status entry, got %+v", snapshot.Entries)
	}
}

func TestReviewerArrayPayloadIsIgnoredAsNoSuggestions(t *testing.T) {
	store := mustCreateTestSession(t)

	mainClient := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "working", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{
				{ID: "call_shell_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "final", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}

	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `["should","be","ignored"]`},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	eng := mustNewTestEngine(t, store, mainClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		Reviewer: ReviewerConfig{
			Frequency:     "all",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			VerboseOutput: true,
			Client:        reviewerClient,
		},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "do task")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != "final" {
		t.Fatalf("assistant content = %q, want final", msg.Content)
	}

	snapshot := eng.ChatSnapshot()
	foundNoSuggestionsStatus := false
	for _, entry := range snapshot.Entries {
		if entry.Role == "reviewer_status" && strings.Contains(entry.Text, "no suggestions") {
			foundNoSuggestionsStatus = true
			break
		}
	}
	if !foundNoSuggestionsStatus {
		t.Fatalf("expected no-suggestions reviewer status entry for array payload, got %+v", snapshot.Entries)
	}
}

func TestReviewerUsesStreamingClientWhenAvailable(t *testing.T) {
	store := mustCreateTestSession(t)

	mainClient := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "working", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{
				{ID: "call_shell_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "original final", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: reviewerNoopToken, Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}

	reviewerClient := &streamRequiredClient{response: llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":["Check output formatting."]}`},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}

	eng := mustNewTestEngine(t, store, mainClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		Reviewer: ReviewerConfig{
			Frequency:     "all",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			Client:        reviewerClient,
		},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "do task")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != "original final" {
		t.Fatalf("assistant content = %q, want original final", msg.Content)
	}
	if reviewerClient.StreamCalls() != 1 {
		t.Fatalf("expected one reviewer stream call, got %d", reviewerClient.StreamCalls())
	}
}

func TestReviewerAppliedFollowUpRemainsVisibleInTranscript(t *testing.T) {
	store := mustCreateTestSession(t)

	var (
		eventsMu sync.Mutex
		events   []Event
	)

	mainClient := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "working", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{
				{ID: "call_shell_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "original final", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "updated final after review", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}

	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":["Add final verification notes."]}`},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	eng := mustNewTestEngine(t, store, mainClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			eventsMu.Lock()
			defer eventsMu.Unlock()
			events = append(events, evt)
		},
		Reviewer: ReviewerConfig{
			Frequency:     "all",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			VerboseOutput: true,
			Client:        reviewerClient,
		},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "do task")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != "updated final after review" {
		t.Fatalf("assistant content = %q, want updated final after review", msg.Content)
	}

	snapshot := eng.ChatSnapshot()
	foundFollowUpAssistant := false
	foundAppliedStatus := false
	suggestionsIdx := -1
	followUpIdx := -1
	wantSuggestionsCondensedText := "Supervisor suggested:\n1. Add final verification notes."
	for idx, entry := range snapshot.Entries {
		if entry.Role == "reviewer_suggestions" && strings.Contains(entry.Text, "Supervisor suggested:") {
			suggestionsIdx = idx
			if entry.CondensedText != wantSuggestionsCondensedText {
				t.Fatalf("expected verbose reviewer suggestions ongoing text, got %+v", entry)
			}
		}
		if entry.Role == "assistant" && strings.Contains(entry.Text, "updated final after review") {
			foundFollowUpAssistant = true
			if followUpIdx < 0 {
				followUpIdx = idx
			}
		}
		if entry.Role == "reviewer_status" && strings.Contains(entry.Text, "Supervisor ran: 1 suggestion, applied.") {
			foundAppliedStatus = true
		}
	}
	if suggestionsIdx < 0 {
		t.Fatalf("expected reviewer suggestions status entry in snapshot, got %+v", snapshot.Entries)
	}
	if !foundFollowUpAssistant {
		t.Fatalf("expected follow-up assistant message in snapshot, got %+v", snapshot.Entries)
	}
	if followUpIdx >= 0 && suggestionsIdx > followUpIdx {
		t.Fatalf("expected reviewer suggestions to appear before follow-up assistant output, got %+v", snapshot.Entries)
	}
	if !foundAppliedStatus {
		t.Fatalf("expected applied reviewer status entry in snapshot, got %+v", snapshot.Entries)
	}

	eventsMu.Lock()
	deferredEvents := append([]Event(nil), events...)
	eventsMu.Unlock()
	originalFinalEventIdx := -1
	reviewerSuggestionsEventIdx := -1
	assistantEventIdx := -1
	reviewerStatusIdx := -1
	reviewerEventIdx := -1
	for idx, evt := range deferredEvents {
		if evt.Kind == EventAssistantMessage && evt.Message.Content == "original final" {
			originalFinalEventIdx = idx
		}
		if evt.Kind == EventLocalEntryAdded && evt.LocalEntry != nil && evt.LocalEntry.Role == "reviewer_suggestions" {
			reviewerSuggestionsEventIdx = idx
		}
		if evt.Kind == EventAssistantMessage && evt.Message.Content == "updated final after review" {
			assistantEventIdx = idx
		}
		if evt.Kind == EventLocalEntryAdded && evt.LocalEntry != nil && evt.LocalEntry.Role == "reviewer_status" && strings.Contains(evt.LocalEntry.Text, "applied") {
			reviewerStatusIdx = idx
		}
		if evt.Kind == EventReviewerCompleted && evt.Reviewer != nil && evt.Reviewer.Outcome == "applied" {
			reviewerEventIdx = idx
			if evt.CommittedTranscriptChanged {
				t.Fatalf("expected reviewer completion to avoid committed transcript advancement, got %+v", evt)
			}
		}
	}
	if assistantEventIdx < 0 {
		t.Fatalf("expected follow-up assistant event, got %+v", deferredEvents)
	}
	if originalFinalEventIdx < 0 {
		t.Fatalf("expected original final assistant event before reviewer follow-up, got %+v", deferredEvents)
	}
	if reviewerSuggestionsEventIdx < 0 {
		t.Fatalf("expected reviewer suggestions local entry event before reviewer follow-up, got %+v", deferredEvents)
	}
	if reviewerStatusIdx < 0 {
		t.Fatalf("expected reviewer status local entry event, got %+v", deferredEvents)
	}
	if reviewerEventIdx < 0 {
		t.Fatalf("expected reviewer completed event, got %+v", deferredEvents)
	}
	if originalFinalEventIdx > reviewerSuggestionsEventIdx || reviewerSuggestionsEventIdx > assistantEventIdx || assistantEventIdx > reviewerStatusIdx || reviewerStatusIdx > reviewerEventIdx {
		t.Fatalf("expected original final -> reviewer suggestions -> updated final -> reviewer_status -> reviewer_completed event order, got %+v", deferredEvents)
	}

	restored := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	restoredSnapshot := restored.ChatSnapshot()
	foundRestoredSuggestions := false
	for _, entry := range restoredSnapshot.Entries {
		if entry.Role != "reviewer_suggestions" || !strings.Contains(entry.Text, "Supervisor suggested:") {
			continue
		}
		foundRestoredSuggestions = true
		if entry.CondensedText != wantSuggestionsCondensedText {
			t.Fatalf("expected restored verbose reviewer suggestions ongoing text, got %+v", entry)
		}
	}
	if !foundRestoredSuggestions {
		t.Fatalf("expected restored reviewer suggestions entry, got %+v", restoredSnapshot.Entries)
	}
}

func TestSubmitUserMessageRejectedAfterClose(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{})
	if err := engine.Close(); err != nil {
		t.Fatalf("close engine: %v", err)
	}
	if _, err := engine.SubmitUserMessage(context.Background(), "stale turn"); !errors.Is(err, ErrEngineClosed) {
		t.Fatalf("SubmitUserMessage after close err=%v, want ErrEngineClosed", err)
	}
	if err := engine.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "stale append"}})); !errors.Is(err, ErrEngineClosed) {
		t.Fatalf("steer after close err=%v, want ErrEngineClosed", err)
	}
}
