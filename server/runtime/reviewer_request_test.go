package runtime

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/config"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestBuildReviewerTranscriptMessagesSummarizesViewImagePayloads(t *testing.T) {
	t.Parallel()
	messages := []llm.Message{
		{
			Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{{
				ID:    "call-view-image-1",
				Name:  string(toolspec.ToolViewImage),
				Input: []byte(`{"path":"docs/page.pdf"}`),
			}},
		},
		{
			Role:       llm.RoleTool,
			ToolCallID: textutil.Value("call-view-image-1"),
			Name:       textutil.Value(string(toolspec.ToolViewImage)),
			Content:    textutil.Value(`[{"type":"input_file","filename":"page.pdf","file_data":"data:application/pdf;base64,JVBERi0xLjQKJUVPRg=="}]`),
		},
	}

	got := buildReviewerTranscriptMessages(messages)
	if len(got) != 2 {
		t.Fatalf("reviewer transcript messages = %d, want 2 (%+v)", len(got), got)
	}
	if !strings.Contains(messageContent(got[0]), "Tool call:") || !strings.Contains(messageContent(got[0]), "docs/page.pdf") {
		t.Fatalf("expected tool call entry with source path, got %q", messageContent(got[0]))
	}
	if !strings.Contains(messageContent(got[1]), "Tool result:") || !strings.Contains(messageContent(got[1]), "attached PDF: page.pdf") {
		t.Fatalf("expected summarized view_image tool result, got %q", messageContent(got[1]))
	}
	if strings.Contains(messageContent(got[1]), "base64") || strings.Contains(messageContent(got[1]), "data:application/pdf") {
		t.Fatalf("expected reviewer transcript to omit binary payloads, got %q", messageContent(got[1]))
	}
}

func TestReviewerSuggestions_ReusesStableMetaForPromptCachePrefix(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engineClient := &fakeClient{caps: llm.ProviderCapabilities{ProviderID: "openai-compatible", SupportsResponsesAPI: true}}
	reviewerClient := &fakeClient{
		caps: llm.ProviderCapabilities{ProviderID: "openai-compatible", SupportsResponsesAPI: true, SupportsPromptCacheKey: true},
		responses: []llm.Response{
			{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(`{"suggestions":[]}`)}, Usage: llm.Usage{InputTokens: 10}},
			{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(`{"suggestions":[]}`)}, Usage: llm.Usage{InputTokens: 10}},
		},
	}
	eng := mustNewTestEngine(t, store, engineClient, tools.NewRegistry(), Config{Model: "gpt-5", Reviewer: ReviewerConfig{Model: "gpt-5"}})

	if _, err := runReviewerSuggestionsTestActiveStep(context.Background(), eng, "step-1", reviewerClient); err != nil {
		t.Fatalf("first reviewer suggestions: %v", err)
	}
	if _, err := runReviewerSuggestionsTestActiveStep(context.Background(), eng, "step-2", reviewerClient); err != nil {
		t.Fatalf("second reviewer suggestions: %v", err)
	}

	if len(reviewerClient.calls) != 2 {
		t.Fatalf("reviewer client calls = %d, want 2", len(reviewerClient.calls))
	}
	firstMessages := requestMessages(reviewerClient.calls[0])
	secondMessages := requestMessages(reviewerClient.calls[1])
	if !hasReviewerMessagePrefix(secondMessages, firstMessages) {
		t.Fatalf("expected second reviewer request to reuse first as prefix\nfirst=%+v\nsecond=%+v", firstMessages, secondMessages)
	}
	if warnings := persistedCacheWarnings(t, store); len(warnings) != 0 {
		t.Fatalf("expected stable reviewer prompt cache lineage, got warnings %+v", warnings)
	}
}

func TestBuildReviewerRequestUsesReviewerModelCapabilities(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		Reviewer: ReviewerConfig{
			Model: "local-reviewer",
			ModelCapabilities: session.LockedModelCapabilities{
				SupportsReasoningEffort: true,
			},
		},
	})

	req, err := eng.buildReviewerRequest(context.Background(), newObservedModelClient(&fakeClient{}))
	if err != nil {
		t.Fatalf("build reviewer request: %v", err)
	}
	if !req.SupportsReasoningEffort {
		t.Fatal("expected reviewer request to use reviewer model capability override")
	}
}

func TestBuildReviewerRequestPreservesTranscriptBytes(t *testing.T) {
	t.Parallel()
	seedContent := "review raw \x1b[31mansi\x1b[0m"
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model:    "gpt-5",
		Reviewer: ReviewerConfig{Model: "gpt-5"},
	})
	if err := steerTestActiveStep(eng, "seed-step", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value(seedContent)}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}

	req, err := eng.buildReviewerRequest(context.Background(), newObservedModelClient(&fakeClient{}))
	if err != nil {
		t.Fatalf("build reviewer request: %v", err)
	}
	found := false
	for _, msg := range requestMessages(req) {
		if strings.Contains(messageContent(msg), seedContent) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected reviewer request to preserve exact ANSI transcript bytes %q, messages=%+v", seedContent, requestMessages(req))
	}
}

func TestReviewerRebuildRetainsGenerationSkillsWithoutMutatingMainTranscript(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	writeTestSkill(t, filepath.Join(workspace, config.ConfigDirName, "skills", "review-skill"), "review-skill", "review skill")

	persistedSkills := llm.Message{
		Role:        llm.RoleDeveloper,
		MessageType: textutil.Value(llm.MessageTypeSkills),
		Content:     textutil.Value("persisted skills context"),
	}
	messages := []llm.Message{
		persistedSkills,
		{Role: llm.RoleUser, Content: textutil.Value("request")},
	}
	original := append([]llm.Message(nil), messages...)
	disabledPolicy := config.ResolveSkillPolicy(config.Settings{SkillToggles: map[string]bool{"review-skill": false}})
	rebuilt, err := buildReviewerRequestMessagesWithBuilder(
		messages,
		newMetaContextBuilder(workspace, "gpt-5", "medium", disabledPolicy, time.Now()),
		false,
	)
	if err != nil {
		t.Fatalf("build reviewer request messages: %v", err)
	}
	content, found := skillMessageContent(rebuilt)
	if !found || content != messageContent(persistedSkills) {
		t.Fatalf("reviewer rebuild changed generation skills context: %+v", rebuilt)
	}
	if !reflect.DeepEqual(messages, original) {
		t.Fatalf("reviewer rebuild mutated main transcript\nbefore=%+v\nafter=%+v", original, messages)
	}
}

func TestReviewerSuggestions_ReopenKeepsPromptCachePrefixStable(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engineClient := &fakeClient{caps: llm.ProviderCapabilities{ProviderID: "openai-compatible", SupportsResponsesAPI: true}}
	reviewerClient := &fakeClient{
		caps: llm.ProviderCapabilities{ProviderID: "openai-compatible", SupportsResponsesAPI: true, SupportsPromptCacheKey: true},
		responses: []llm.Response{
			{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(`{"suggestions":[]}`)}, Usage: llm.Usage{InputTokens: 10}},
			{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(`{"suggestions":[]}`)}, Usage: llm.Usage{InputTokens: 10}},
		},
	}
	eng := mustNewTestEngine(t, store, engineClient, tools.NewRegistry(), Config{Model: "gpt-5", Reviewer: ReviewerConfig{Model: "gpt-5"}})
	t.Cleanup(func() { _ = eng.Close() })
	if err := steerTestActiveStep(eng, "prep-1", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("first request")}})); err != nil {
		t.Fatalf("append first message: %v", err)
	}
	if _, err := runReviewerSuggestionsTestActiveStep(context.Background(), eng, "step-1", reviewerClient); err != nil {
		t.Fatalf("first reviewer suggestions: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("close original engine: %v", err)
	}

	reopened, err := runtimeTestSessionPersistence.Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	reopenedEng := mustNewTestEngine(t, reopened, engineClient, tools.NewRegistry(), Config{Model: "gpt-5", Reviewer: ReviewerConfig{Model: "gpt-5"}})
	t.Cleanup(func() { _ = reopenedEng.Close() })
	if err := steerTestActiveStep(reopenedEng, "prep-2", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("second request")}})); err != nil {
		t.Fatalf("append second message: %v", err)
	}
	if _, err := runReviewerSuggestionsTestActiveStep(context.Background(), reopenedEng, "step-2", reviewerClient); err != nil {
		t.Fatalf("second reviewer suggestions: %v", err)
	}

	if len(reviewerClient.calls) != 2 {
		t.Fatalf("reviewer client calls = %d, want 2", len(reviewerClient.calls))
	}
	firstMessages := requestMessages(reviewerClient.calls[0])
	secondMessages := requestMessages(reviewerClient.calls[1])
	if !hasReviewerMessagePrefix(secondMessages, firstMessages) {
		t.Fatalf("expected reopened reviewer request to extend the original request\nfirst=%+v\nsecond=%+v", firstMessages, secondMessages)
	}
	if warnings := persistedCacheWarnings(t, reopened); len(warnings) != 0 {
		t.Fatalf("expected no reviewer cache warnings after reopen, got %+v", warnings)
	}
}

func hasReviewerMessagePrefix(messages []llm.Message, prefix []llm.Message) bool {
	if len(prefix) > len(messages) {
		return false
	}
	for i := range prefix {
		if !reflect.DeepEqual(messages[i], prefix[i]) {
			return false
		}
	}
	return true
}
