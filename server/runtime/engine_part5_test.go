package runtime

import (
	"context"
	"errors"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func reviewerPromptConfig(path string) Config {
	return Config{Reviewer: ReviewerConfig{
		Model:            "gpt-5",
		SystemPromptFile: path,
	}}
}

func runReviewerPrompt(t *testing.T, eng *Engine) llm.Request {
	t.Helper()
	if _, err := eng.ensureLocked(); err != nil {
		t.Fatalf("ensure locked: %v", err)
	}
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(`{"suggestions":[]}`)},
	}}}
	if _, err := eng.runReviewerSuggestions(context.Background(), "review", client); err != nil {
		t.Fatalf("run reviewer suggestions: %v", err)
	}
	assertModelCallCount(t, client, 1)
	return client.calls[0]
}

func TestReviewerSystemPromptFileIsLazyLockedAndReused(t *testing.T) {
	dir := t.TempDir()
	reviewerPromptPath := filepath.Join(dir, "reviewer-prompt.md")
	writeTestFile(t, reviewerPromptPath, "custom reviewer prompt")

	store := mustCreateTestSessionAt(t, dir)
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), reviewerPromptConfig(reviewerPromptPath))
	if got := runReviewerPrompt(t, eng).SystemPrompt; got != "custom reviewer prompt" {
		t.Fatalf("reviewer system prompt = %q, want custom reviewer prompt", got)
	}
	if locked := store.Meta().Locked; locked == nil || !locked.HasReviewerPrompt || locked.ReviewerPrompt != "custom reviewer prompt" {
		t.Fatalf("locked reviewer prompt = %+v, want custom reviewer prompt snapshot", locked)
	}

	writeTestFile(t, reviewerPromptPath, "changed reviewer prompt")
	if err := eng.Close(); err != nil {
		t.Fatalf("close engine: %v", err)
	}
	reopened := mustOpenTestSession(t, store.Dir())
	reopenedEngine := mustNewTestEngine(t, reopened, &fakeClient{}, newTestToolRegistry(t), reviewerPromptConfig(reviewerPromptPath))
	if got := runReviewerPrompt(t, reopenedEngine).SystemPrompt; got != "custom reviewer prompt" {
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
		{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("ok")}, Usage: llm.Usage{WindowTokens: 200000}},
		{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("summary")}, Usage: llm.Usage{WindowTokens: 200000}},
	}}
	reviewerClient := &fakeClient{}
	cfg := reviewerPromptConfig(reviewerPromptPath)
	cfg.CompactionMode = "local"
	cfg.AutoCompactionEnabled = &autoCompactionEnabled
	cfg.Reviewer.Frequency = "all"
	cfg.Reviewer.Client = reviewerClient
	eng := mustNewExecTestEngine(t, store, mainClient, cfg)
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
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), reviewerPromptConfig("~/reviewer-prompt.md"))
	if got := runReviewerPrompt(t, eng).SystemPrompt; got != "tilde reviewer prompt" {
		t.Fatalf("reviewer system prompt = %q, want tilde reviewer prompt", got)
	}
}

func TestReviewerSystemPromptFileMissingFailsWithoutSnapshot(t *testing.T) {
	dir := t.TempDir()
	missingPromptPath := filepath.Join(dir, "missing-reviewer-prompt.md")
	store := mustCreateTestSessionAt(t, dir)
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), reviewerPromptConfig(missingPromptPath))
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
	reviewerClient := &fakeClient{}
	cfg := reviewerPromptConfig(missingPromptPath)
	cfg.Reviewer.Frequency = "off"
	cfg.Reviewer.Client = reviewerClient
	eng := mustNewExecTestEngine(t, store, &fakeClient{responses: []llm.Response{finalTextResponse("done")}}, cfg)
	if _, err := eng.SubmitUserMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	assertModelCallCount(t, reviewerClient, 0)
	if locked := store.Meta().Locked; locked == nil || locked.HasReviewerPrompt || locked.ReviewerPrompt != "" {
		t.Fatalf("locked reviewer prompt = %+v, want no reviewer prompt snapshot", locked)
	}
}

func TestReviewerSuggestionsRequestInheritsFastMode(t *testing.T) {
	store := mustCreateTestSession(t)

	mainClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(`{"suggestions":[]}`)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	eng := mustNewTestEngine(t, store, mainClient, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
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

func TestBlankFinalProjectionIsInvisibleAndSkipsReviewer(t *testing.T) {
	store := mustCreateTestSession(t)

	mainClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(""), Phase: textutil.Value(llm.MessagePhaseFinal)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(`{"suggestions":["x"]}`)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, mainClient, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
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
	if msg.Content != nil {
		t.Fatalf("assistant content = %q, want absent", *msg.Content)
	}
	if len(mainClient.calls) != 1 {
		t.Fatalf("expected one main model call, got %d", len(mainClient.calls))
	}
	if len(reviewerClient.calls) != 0 {
		t.Fatalf("expected reviewer not to run for former-marker final, got %d calls", len(reviewerClient.calls))
	}

	finalAssistantContents := make([]string, 0)
	noopFinalCount := 0
	for _, persisted := range eng.transcriptRuntimeState().SnapshotMessages() {
		if persisted.Role == llm.RoleAssistant && persisted.Phase != nil && *persisted.Phase == llm.MessagePhaseFinal {
			finalAssistantContents = append(finalAssistantContents, messageContent(persisted))
		}
		if isBlankFinalAnswer(persisted) {
			noopFinalCount++
		}
	}
	if noopFinalCount != 1 {
		t.Fatalf("noop final count = %d, want 1; messages=%+v", noopFinalCount, eng.transcriptRuntimeState().SnapshotMessages())
	}
	if len(finalAssistantContents) != 1 || finalAssistantContents[0] != "" {
		t.Fatalf("expected hidden persisted blank final assistant message, got %q", finalAssistantContents)
	}

	snapshot := eng.ChatSnapshot()
	visibleFinalRows := 0
	for _, fact := range TranscriptCommittedRowFactsFromSnapshot(snapshot) {
		if fact.Kind == TranscriptCommittedRowFactAssistant &&
			fact.Assistant != nil &&
			fact.Assistant.Phase == llm.MessagePhaseFinal {
			visibleFinalRows++
		}
	}
	if visibleFinalRows != 0 {
		t.Fatalf("blank final projected as %d visible assistant final rows: %+v", visibleFinalRows, snapshot.Entries)
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
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("working"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
			ToolCalls: []llm.ToolCall{{ID: "call_patch_1", Name: string(toolspec.ToolPatch), Custom: true, CustomInput: textutil.Value("*** Begin Patch\n*** Add File: a.txt\n+hello\n*** End Patch")}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("final"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(`{"suggestions":[]}`)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	eng := mustNewTestEngine(t, store, mainClient, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolPatch, Handler: fakeTool{name: toolspec.ToolPatch}}), Config{
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
	if messageContent(msg) != "final" {
		t.Fatalf("assistant content = %q, want final", messageContent(msg))
	}
	if len(reviewerClient.calls) != 1 {
		t.Fatalf("expected reviewer to be called once after patch edit, got %d", len(reviewerClient.calls))
	}
}

func TestReviewerBlankFinalKeepsOriginalAnswerAndReportsNoChanges(t *testing.T) {
	store := mustCreateTestSession(t)
	mainClient := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("working"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
			ToolCalls: []llm.ToolCall{
				{ID: "call_shell_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		finalTextResponse("original final"),
		finalTextResponse(""),
	}}
	reviewerClient := &streamRequiredClient{response: llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(`{"suggestions":["Double-check test output before final handoff."]}`)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}
	eng := mustNewExecTestEngine(t, store, mainClient, Config{
		Model: "gpt-5",
		Reviewer: ReviewerConfig{
			Frequency:     "all",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			Client:        reviewerClient,
		},
	})

	_, err := eng.SubmitUserMessage(context.Background(), "do task")
	if err != nil {
		t.Fatalf("submit with blank Reviewer follow-up: %v", err)
	}
	if reviewerClient.StreamCalls() != 1 {
		t.Fatalf("reviewer stream calls = %d, want 1", reviewerClient.StreamCalls())
	}
	assertModelCallCount(t, mainClient, 3)

	feedback := 0
	for _, message := range requestMessages(mainClient.calls[2]) {
		if message.Role == llm.RoleDeveloper && message.MessageType != nil && *message.MessageType == llm.MessageTypeReviewerFeedback {
			feedback++
			if strings.TrimSpace(messageContent(message)) == "" {
				t.Fatal("reviewer feedback content is blank")
			}
		}
	}
	if feedback != 1 {
		t.Fatalf("reviewer feedback messages = %d, want 1; messages=%+v", feedback, requestMessages(mainClient.calls[2]))
	}

	feedbackRows := 0
	statusRows := 0
	snapshot := eng.ChatSnapshot()
	for _, entry := range snapshot.Entries {
		if entry.ReviewerFeedback != nil {
			feedbackRows++
		}
		if entry.Role == string(transcript.EntryRoleReviewerStatus) {
			statusRows++
			if entry.Text != reviewerStatusText(ReviewerStatus{Outcome: "noop", SuggestionsCount: 1}, nil) {
				t.Fatalf("reviewer no-change status = %+v", entry)
			}
		}
	}
	if feedbackRows != 1 {
		t.Fatalf("reviewer feedback rows = %d, want one; entries=%+v", feedbackRows, snapshot.Entries)
	}
	if statusRows != 1 {
		t.Fatalf("reviewer status rows = %d, want one; entries=%+v", statusRows, snapshot.Entries)
	}
	restored := mustNewExecTestEngine(t, store, &fakeClient{}, Config{Model: "gpt-5"})
	if len(restored.ChatSnapshot().Entries) == 0 {
		t.Fatal("restored chat snapshot is empty")
	}
	finals := 0
	for _, entry := range restored.ChatSnapshot().Entries {
		if entry.Role == "assistant" && entry.Phase == llm.MessagePhaseFinal {
			finals++
		}
	}
	if finals != 1 {
		t.Fatalf("restored visible final rows = %d, want original only", finals)
	}
}

func TestReviewerSuggestionsRemainVisibleWhenFollowUpReturnsNoAnswer(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), Config{
		Model:    "gpt-5",
		Reviewer: ReviewerConfig{Model: "gpt-5"},
	})
	pipeline := &defaultReviewerPipeline{
		engine:     engine,
		stepRunner: missingReviewerFollowUpRunner{},
	}
	_, err := pipeline.RunFollowUp(
		context.Background(),
		"11111111-1111-4111-8111-111111111111",
		llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseFinal), Content: textutil.Value("original")},
		0,
		false,
		&fakeClient{responses: []llm.Response{{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value(`{"suggestions":["fix"]}`),
			},
		}}},
	)
	if err == nil {
		t.Fatal("missing Reviewer follow-up answer unexpectedly succeeded")
	}
	feedbackRows := 0
	for _, entry := range engine.ChatSnapshot().Entries {
		if entry.ReviewerFeedback != nil {
			feedbackRows++
		}
	}
	if feedbackRows != 1 {
		t.Fatalf("missing follow-up feedback rows = %d, want issued suggestions preserved", feedbackRows)
	}
}

type missingReviewerFollowUpRunner struct{}

func (missingReviewerFollowUpRunner) RunStepLoopWithOptions(context.Context, string, stepLoopOptions) (stepLoopResult, error) {
	return stepLoopResult{}, nil
}

func TestSubmitUserMessageRejectedAfterClose(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), Config{})
	if err := engine.Close(); err != nil {
		t.Fatalf("close engine: %v", err)
	}
	if _, err := engine.SubmitUserMessage(context.Background(), "stale turn"); !errors.Is(err, ErrEngineClosed) {
		t.Fatalf("SubmitUserMessage after close err=%v, want ErrEngineClosed", err)
	}
	if err := engine.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("stale append")}})); !errors.Is(err, ErrEngineClosed) {
		t.Fatalf("steer after close err=%v, want ErrEngineClosed", err)
	}
}
