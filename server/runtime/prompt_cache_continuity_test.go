package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/prompts"
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	brand "core/shared/config"
	"core/shared/toolspec"
	"core/shared/transcript"
)

// This regression test guards prompt-cache continuity across restarts. It
// seeds a realistic live runtime conversation, relies on production persistence
// to write session.json/events.jsonl, replays the persisted event stream,
// reopens the runtime from disk, and finally proves that the cache-relevant
// request prefix is unchanged before vs after reload.
func TestBuildRequest_ReopenPreservesPromptCachePrefix(t *testing.T) {
	fixture := newPromptCacheContinuityFixture(t)
	fixture.assertPersistedProjectionParity(t)
	originalReq, err := fixture.engine.buildRequest(context.Background(), "", true)
	if err != nil {
		t.Fatalf("build original request: %v", err)
	}
	reloaded, reopenedStore := fixture.reopen(t)
	assertPersistedProjectionMatchesRuntime(t, capturePersistedProjectionFromStore(t, reopenedStore), captureRuntimeProjection(t, reloaded))
	reloadedReq, err := reloaded.buildRequest(context.Background(), "", true)
	if err != nil {
		t.Fatalf("build reloaded request: %v", err)
	}
	assertPromptCacheChunksEqual(t, originalReq, reloadedReq)
}

// Reviewer requests transform transcript state differently from normal runtime
// turns, so they need their own continuity check over the same events.jsonl
// persistence boundary.
func TestBuildReviewerRequest_ReopenPreservesPromptCachePrefix(t *testing.T) {
	fixture := newPromptCacheContinuityFixture(t)
	fixture.assertPersistedProjectionParity(t)
	originalReq, err := fixture.engine.buildReviewerRequest(context.Background(), fixture.reviewerClient)
	if err != nil {
		t.Fatalf("build original reviewer request: %v", err)
	}
	reloaded, reopenedStore := fixture.reopen(t)
	assertPersistedProjectionMatchesRuntime(t, capturePersistedProjectionFromStore(t, reopenedStore), captureRuntimeProjection(t, reloaded))
	reloadedReq, err := reloaded.buildReviewerRequest(context.Background(), fixture.reviewerClient)
	if err != nil {
		t.Fatalf("build reloaded reviewer request: %v", err)
	}
	assertPromptCacheChunksEqual(t, originalReq, reloadedReq)
}

func TestHeadlessToInteractiveReopenPreservesPromptCachePrefix(t *testing.T) {
	prevHeadlessPrompt := prompts.HeadlessModePrompt
	prevExitPrompt := prompts.HeadlessModeExitPrompt
	prompts.HeadlessModePrompt = "headless mode instructions"
	prompts.HeadlessModeExitPrompt = "interactive mode instructions"
	defer func() {
		prompts.HeadlessModePrompt = prevHeadlessPrompt
		prompts.HeadlessModeExitPrompt = prevExitPrompt
	}()

	store := mustCreateTestSession(t)
	registry := tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}})
	headlessResponse := finalOutputItemResponse("headless-ok")
	headlessResponse.Usage.HasCachedInputTokens = true
	headlessResponse.Usage.CachedInputTokens = 4096
	headlessClient := &fakeClient{responses: []llm.Response{headlessResponse}}
	headlessEngine := mustNewTestEngine(t, store, headlessClient, registry, Config{
		HeadlessMode:  true,
		EnabledTools:  []toolspec.ID{toolspec.ToolExecCommand},
		ToolPreambles: false,
	})
	if _, err := headlessEngine.SubmitUserMessage(context.Background(), "run headless"); err != nil {
		t.Fatalf("headless submit: %v", err)
	}
	assertModelCallCount(t, headlessClient, 1)
	lastHeadlessRequest := headlessClient.calls[0]
	if err := headlessEngine.Close(); err != nil {
		t.Fatalf("close headless engine: %v", err)
	}
	reopenedStore := mustOpenTestSession(t, store.Dir())
	interactiveResponse := finalOutputItemResponse("interactive-ok")
	interactiveResponse.Usage.HasCachedInputTokens = true
	interactiveResponse.Usage.CachedInputTokens = 4096
	interactiveClient := &fakeClient{responses: []llm.Response{interactiveResponse}}
	interactiveEngine := mustNewTestEngine(t, reopenedStore, interactiveClient, registry, Config{
		EnabledTools:  []toolspec.ID{toolspec.ToolExecCommand},
		ToolPreambles: false,
	})
	if _, err := interactiveEngine.SubmitUserMessage(context.Background(), "continue interactively"); err != nil {
		t.Fatalf("interactive submit: %v", err)
	}
	assertModelCallCount(t, interactiveClient, 1)

	assertPromptCacheChunkPrefix(t, lastHeadlessRequest, interactiveClient.calls[0])
}

func TestBuildRequest_ReopenPreservesShellStringToolOutputPayload(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Phase: llm.MessagePhaseCommentary, ToolCalls: []llm.ToolCall{
				{ID: "call-a", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"a"}`)},
				{ID: "call-b", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"b"}`)},
				{ID: "call-c", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"c && d"}`)},
			}},
			ReasoningItems: []llm.ReasoningItem{{ID: "rs-1", EncryptedContent: "encrypted"}},
			Usage:          llm.Usage{WindowTokens: 200000},
		},
		finalOutputItemResponse("done"),
	}}
	registry := tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: stringOutputTool{name: toolspec.ToolExecCommand}})
	engine := mustNewTestEngine(t, store, client, registry, Config{
		EnabledTools:  []toolspec.ID{toolspec.ToolExecCommand},
		ToolPreambles: false,
	})
	if _, err := engine.SubmitUserMessage(context.Background(), "run tools"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	assertModelCallCount(t, client, 2)
	liveFollowup := client.calls[1]
	if err := engine.Close(); err != nil {
		t.Fatalf("close engine: %v", err)
	}
	reopenedStore := mustOpenTestSession(t, store.Dir())
	reopened := mustNewTestEngine(t, reopenedStore, client, registry, Config{
		EnabledTools:  []toolspec.ID{toolspec.ToolExecCommand},
		ToolPreambles: false,
	})
	reopenedFollowup, err := reopened.buildRequest(context.Background(), "", true)
	if err != nil {
		t.Fatalf("build reopened request: %v", err)
	}

	assertPromptCacheChunkPrefix(t, liveFollowup, reopenedFollowup)
}

func TestPromptCacheReplayPreservesMultiToolHTMLUnescapeShape(t *testing.T) {
	liveReq := seq21To28ShapeRequest(json.RawMessage(`{"cmd":"git diff --cached && git diff","workdir":"/workspace","max_output_tokens":20000}`))
	replayedReq := seq21To28ShapeRequest(json.RawMessage(`{"cmd":"git diff --cached \u0026\u0026 git diff","workdir":"/workspace","max_output_tokens":20000}`))

	liveShape, err := summarizePromptCacheRequest(liveReq)
	if err != nil {
		t.Fatalf("live prompt cache summary: %v", err)
	}
	replayedShape, err := summarizePromptCacheRequest(replayedReq)
	if err != nil {
		t.Fatalf("replayed prompt cache summary: %v", err)
	}
	if liveShape.terminalHash != replayedShape.terminalHash {
		t.Fatalf("terminal hash differs\nlive=%s\nreplayed=%s", liveShape.terminalHash, replayedShape.terminalHash)
	}
	const wantTerminalHash = "c666799d9e7a7fbe0f3d8318de0fec51d286dbd93411bb10bfa1dc9203653700"
	if liveShape.terminalHash != wantTerminalHash {
		t.Fatalf("terminal hash = %s, want %s", liveShape.terminalHash, wantTerminalHash)
	}
}

// The fixture intentionally includes the transcript parts that are most likely
// to affect cache-prefix stability: meta injections, user messages, assistant
// commentary/final output, tool calls, tool results, developer entries, and
// persisted local transcript entries.
type promptCacheContinuityFixture struct {
	store          *session.Store
	engine         *Engine
	client         *fakeClient
	reviewerClient *fakeClient
	registry       *tools.Registry
	cfg            Config
}

func newPromptCacheContinuityFixture(t *testing.T) *promptCacheContinuityFixture {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	workspaceRoot := filepath.Join(root, "workspace")
	persistenceRoot := filepath.Join(root, "sessions")
	for _, dir := range []string{
		home,
		filepath.Join(home, brand.ConfigDirName),
		filepath.Join(home, brand.ConfigDirName, "skills", "global-cache-skill"),
		workspaceRoot,
		filepath.Join(workspaceRoot, brand.ConfigDirName, "skills", "workspace-cache-skill"),
		persistenceRoot,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	t.Chdir(workspaceRoot)
	writeTestFile(t, filepath.Join(home, brand.ConfigDirName, "AGENTS.md"), "global prompt cache rule")
	writeTestFile(t, filepath.Join(workspaceRoot, "AGENTS.md"), "workspace prompt cache rule")
	writeTestFile(t, filepath.Join(home, brand.ConfigDirName, "skills", "global-cache-skill", "SKILL.md"), skillFixtureMarkdown("global-cache-skill", "Global prompt-cache continuity skill."))
	writeTestFile(t, filepath.Join(workspaceRoot, brand.ConfigDirName, "skills", "workspace-cache-skill", "SKILL.md"), skillFixtureMarkdown("workspace-cache-skill", "Workspace prompt-cache continuity skill."))

	store := mustCreateNamedTestSessionAt(t, persistenceRoot, "ws", workspaceRoot)
	clientCaps := llm.ProviderCapabilities{
		ProviderID:                    "openai",
		SupportsResponsesAPI:          true,
		SupportsResponsesCompact:      true,
		SupportsPromptCacheKey:        true,
		SupportsReasoningEncrypted:    true,
		SupportsServerSideContextEdit: true,
		IsOpenAIFirstParty:            true,
	}
	client := &fakeClient{caps: clientCaps}
	reviewerClient := &fakeClient{caps: clientCaps}
	registry := tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}, tools.HandlerRegistration{ID: toolspec.ToolAskQuestion, Handler: fakeTool{name: toolspec.ToolAskQuestion}})
	cfg := Config{
		Model:         "gpt-5",
		ThinkingLevel: "medium",
		EnabledTools:  []toolspec.ID{toolspec.ToolExecCommand, toolspec.ToolAskQuestion},
		Reviewer: ReviewerConfig{
			Model:         "gpt-5",
			ThinkingLevel: "medium",
		},
	}
	engine := mustNewTestEngine(t, store, client, registry, cfg)
	seedPromptCacheContinuityConversation(t, engine)
	assertSessionPersistenceFilesPresent(t, store)
	return &promptCacheContinuityFixture{
		store:          store,
		engine:         engine,
		client:         client,
		reviewerClient: reviewerClient,
		registry:       registry,
		cfg:            cfg,
	}
}

func (f *promptCacheContinuityFixture) reopen(t *testing.T) (*Engine, *session.Store) {
	t.Helper()
	if err := f.engine.Close(); err != nil {
		t.Fatalf("close original engine: %v", err)
	}
	reopenedStore := mustOpenTestSession(t, f.store.Dir())
	reopened := mustNewTestEngine(t, reopenedStore, f.client, f.registry, f.cfg)
	return reopened, reopenedStore
}

// Compare live runtime state with the projection reconstructed from persisted
// events first, so failures tell us whether drift came from persistence/hydrate
// or later request building.
func (f *promptCacheContinuityFixture) assertPersistedProjectionParity(t *testing.T) {
	t.Helper()
	assertPersistedProjectionMatchesRuntime(t, capturePersistedProjectionFromStore(t, f.store), captureRuntimeProjection(t, f.engine))
}

func seedPromptCacheContinuityConversation(t *testing.T, engine *Engine) {
	t.Helper()
	if err := engine.steerBaseMetaContextIfNeeded("seed-meta"); err != nil {
		t.Fatalf("inject agents: %v", err)
	}
	if err := engine.steer("turn-1", steerMessagesWithPersistenceIntent(steeringPriorityUser, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "Need a prompt cache continuity test that survives a server restart."}})); err != nil {
		t.Fatalf("append first user message: %v", err)
	}
	if err := engine.steer("turn-1", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, Phase: llm.MessagePhaseCommentary, Content: "I am reconstructing the live runtime state before comparing serialized OpenAI payloads."}})); err != nil {
		t.Fatalf("append assistant commentary: %v", err)
	}
	toolCall := llm.ToolCall{
		ID:   "call-shell-1",
		Name: string(toolspec.ToolExecCommand),
		Input: mustJSON(map[string]any{
			"command": "git status --short",
			"workdir": ".",
		}),
	}
	if err := engine.steer("turn-1", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleAssistant, Phase: llm.MessagePhaseCommentary, ToolCalls: []llm.ToolCall{toolCall}}})); err != nil {
		t.Fatalf("append tool call: %v", err)
	}
	toolResult := tools.Result{
		CallID: toolCall.ID,
		Name:   toolspec.ToolExecCommand,
		Output: mustJSON(map[string]any{
			"stdout":    " M server/runtime/request_cache_lineage.go\n M server/runtime/reviewer_pipeline.go",
			"exit_code": 0,
		}),
	}
	if err := engine.steer("turn-1", steerToolCompletionIntent(toolResult)); err != nil {
		t.Fatalf("persist tool completion: %v", err)
	}
	if err := engine.steer("turn-1", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleTool, ToolCallID: toolResult.CallID, Name: string(toolResult.Name), Content: string(toolResult.Output)}})); err != nil {
		t.Fatalf("append tool result message: %v", err)
	}
	if err := engine.steer("turn-1", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, Content: "Keep the persisted transcript byte-stable across hydrate and restart before sending the next model request."}})); err != nil {
		t.Fatalf("append developer entry: %v", err)
	}
	if err := engine.steer("turn-1", steerLocalEntryIntent(storedLocalEntry{
		Visibility:    transcript.EntryVisibilityAuto,
		Role:          "warning",
		Text:          "Prompt cache continuity probe is still running.",
		CondensedText: "Prompt cache continuity probe is still running.",
	})); err != nil {
		t.Fatalf("append local entry: %v", err)
	}
	if err := engine.steer("turn-1", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "The runtime state is seeded. I only need the post-restart payload comparison now."}})); err != nil {
		t.Fatalf("append assistant final answer: %v", err)
	}
	if err := engine.steer("turn-2", steerMessagesWithPersistenceIntent(steeringPriorityUser, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "Continue after restart and compare the exact OpenAI payload bytes."}})); err != nil {
		t.Fatalf("append second user message: %v", err)
	}
}

func assertPromptCacheChunksEqual(t *testing.T, original llm.Request, reloaded llm.Request) {
	t.Helper()
	originalChunks, err := promptCacheChunks(original)
	if err != nil {
		t.Fatalf("original prompt cache chunks: %v", err)
	}
	reloadedChunks, err := promptCacheChunks(reloaded)
	if err != nil {
		t.Fatalf("reloaded prompt cache chunks: %v", err)
	}
	originalJSON := mustMarshalCanonicalJSON(t, originalChunks)
	reloadedJSON := mustMarshalCanonicalJSON(t, reloadedChunks)
	if !bytes.Equal(originalJSON, reloadedJSON) {
		t.Fatalf("prompt cache chunks mismatch after reopen\noriginal=%s\nreloaded=%s", originalJSON, reloadedJSON)
	}
}

func assertPromptCacheChunkPrefix(t *testing.T, previous llm.Request, next llm.Request) {
	t.Helper()
	previousChunks, err := promptCacheChunks(previous)
	if err != nil {
		t.Fatalf("previous prompt cache chunks: %v", err)
	}
	nextChunks, err := promptCacheChunks(next)
	if err != nil {
		t.Fatalf("next prompt cache chunks: %v", err)
	}
	if len(previousChunks) > len(nextChunks) {
		t.Fatalf("previous request has %d cache chunks, next request has %d", len(previousChunks), len(nextChunks))
	}
	for idx, previousChunk := range previousChunks {
		if bytes.Equal(previousChunk, nextChunks[idx]) {
			continue
		}
		t.Fatalf("prompt cache chunk %d differs after reopen\nprevious=%s\nnext=%s", idx, previousChunk, nextChunks[idx])
	}
}

type promptCacheComparableMainView struct {
	SessionID                      string                        `json:"session_id"`
	SessionName                    string                        `json:"session_name,omitempty"`
	ConversationFreshness          string                        `json:"conversation_freshness"`
	Revision                       int64                         `json:"revision"`
	CommittedEntryCount            int                           `json:"committed_entry_count"`
	ParentSessionID                string                        `json:"parent_session_id,omitempty"`
	LastCommittedAssistantResponse string                        `json:"last_committed_assistant_response,omitempty"`
	ActiveRun                      *promptCacheComparableRunView `json:"active_run,omitempty"`
}

type promptCacheComparableRunView struct {
	RunID      string `json:"run_id"`
	StepID     string `json:"step_id"`
	Status     string `json:"status"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

type promptCacheProjection struct {
	MainViewJSON   []byte
	TranscriptJSON []byte
}

// Capture the same runtime-owned state that request building reads from, so any
// mismatch vs persisted replay is a real cache-prefix risk rather than a test
// representation mismatch.
func captureRuntimeProjection(t *testing.T, engine *Engine) promptCacheProjection {
	t.Helper()
	return promptCacheProjection{
		MainViewJSON:   mustMarshalCanonicalJSON(t, runtimeMainViewComparable(engine)),
		TranscriptJSON: mustMarshalCanonicalJSON(t, engine.RecentTailTranscriptWindow(500)),
	}
}

// Rebuild the projection strictly from persisted session events. This is the
// production boundary that matters for restart cache continuity.
func capturePersistedProjectionFromStore(t *testing.T, store *session.Store) promptCacheProjection {
	t.Helper()
	scan := mustScanPersistedTranscript(t, store)
	return promptCacheProjection{
		MainViewJSON:   mustMarshalCanonicalJSON(t, persistedMainViewComparable(t, store, scan)),
		TranscriptJSON: mustMarshalCanonicalJSON(t, scan.RecentTailSnapshot()),
	}
}

func assertPersistedProjectionMatchesRuntime(t *testing.T, persisted promptCacheProjection, runtime promptCacheProjection) {
	t.Helper()
	if !bytes.Equal(runtime.MainViewJSON, persisted.MainViewJSON) {
		t.Fatalf("persisted main view mismatch\nruntime=%s\npersisted=%s", runtime.MainViewJSON, persisted.MainViewJSON)
	}
	if !bytes.Equal(runtime.TranscriptJSON, persisted.TranscriptJSON) {
		t.Fatalf("persisted transcript mismatch\nruntime=%s\npersisted=%s", runtime.TranscriptJSON, persisted.TranscriptJSON)
	}
}

func mustMarshalCanonicalJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal canonical json: %v", err)
	}
	var out bytes.Buffer
	if err := json.Indent(&out, data, "", "  "); err != nil {
		t.Fatalf("indent canonical json: %v", err)
	}
	return out.Bytes()
}

func runtimeMainViewComparable(engine *Engine) promptCacheComparableMainView {
	return promptCacheComparableMainView{
		SessionID:                      engine.SessionID(),
		SessionName:                    engine.SessionName(),
		ConversationFreshness:          conversationFreshnessLabel(engine.ConversationFreshness()),
		Revision:                       engine.TranscriptRevision(),
		CommittedEntryCount:            engine.CommittedTranscriptEntryCount(),
		ParentSessionID:                engine.ParentSessionID(),
		LastCommittedAssistantResponse: engine.LastCommittedAssistantFinalAnswer(),
		ActiveRun:                      comparableRuntimeRunView(engine.ActiveRun()),
	}
}

func persistedMainViewComparable(t *testing.T, store *session.Store, scan *PersistedTranscriptScan) promptCacheComparableMainView {
	t.Helper()
	meta := store.Meta()
	return promptCacheComparableMainView{
		SessionID:                      meta.SessionID,
		SessionName:                    meta.Name,
		ConversationFreshness:          conversationFreshnessLabel(store.ConversationFreshness()),
		Revision:                       meta.LastSequence,
		CommittedEntryCount:            scan.TotalEntries(),
		ParentSessionID:                meta.ParentSessionID,
		LastCommittedAssistantResponse: scan.LastCommittedAssistantFinalAnswer(),
		ActiveRun:                      nil,
	}
}

func mustScanPersistedTranscript(t *testing.T, store *session.Store) *PersistedTranscriptScan {
	t.Helper()
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{TrackRecentTail: true, TailLimit: 500})
	if err := store.WalkEvents(func(evt session.Event) error {
		return scan.ApplyPersistedEvent(evt)
	}); err != nil {
		t.Fatalf("scan persisted transcript: %v", err)
	}
	return scan
}

func comparableRuntimeRunView(run *RunSnapshot) *promptCacheComparableRunView {
	if run == nil {
		return nil
	}
	finishedAt := ""
	if !run.FinishedAt.IsZero() {
		finishedAt = run.FinishedAt.UTC().Format(time.RFC3339Nano)
	}
	return &promptCacheComparableRunView{
		RunID:      run.RunID,
		StepID:     run.StepID,
		Status:     string(run.Status),
		StartedAt:  run.StartedAt.UTC().Format(time.RFC3339Nano),
		FinishedAt: finishedAt,
	}
}

func conversationFreshnessLabel(f session.ConversationFreshness) string {
	if f.IsFresh() {
		return "fresh"
	}
	return "established"
}

func assertSessionPersistenceFilesPresent(t *testing.T, store *session.Store) {
	t.Helper()
	for _, name := range []string{"session.json", "events.jsonl"} {
		path := filepath.Join(store.Dir(), name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read persistence file %s: %v", path, err)
		}
		if len(data) == 0 {
			t.Fatalf("expected persistence file %s to be non-empty", path)
		}
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func skillFixtureMarkdown(name, description string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# " + name + "\n"
}

func seq21To28ShapeRequest(thirdCallInput json.RawMessage) llm.Request {
	return llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic,
		Model:        "gpt-5",
		SystemPrompt: "system",
		Items: llm.ItemsFromMessages([]llm.Message{
			{Role: llm.RoleUser, Content: "review docs migration"},
			{
				Role: llm.RoleAssistant,
				ToolCalls: []llm.ToolCall{
					{ID: "call-lines", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"wc -l docs/*.md","workdir":"/workspace","max_output_tokens":20000}`)},
					{ID: "call-search", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"rg -n \"decisions\\.md|TERMINOLOGY\\.md\" .","workdir":"/workspace","max_output_tokens":40000}`)},
					{ID: "call-status", Name: string(toolspec.ToolExecCommand), Input: thirdCallInput},
				},
				ReasoningItems: []llm.ReasoningItem{{ID: "rs-seq21", EncryptedContent: "encrypted-seq21"}},
			},
			{Role: llm.RoleTool, ToolCallID: "call-lines", Name: string(toolspec.ToolExecCommand), Content: `"42 docs/dev/specs/README.md"`},
			{Role: llm.RoleTool, ToolCallID: "call-search", Name: string(toolspec.ToolExecCommand), Content: `"docs/dev/specs/README.md:1:# Product Specs"`},
			{Role: llm.RoleTool, ToolCallID: "call-status", Name: string(toolspec.ToolExecCommand), Content: `"M\tdocs/dev/specs/README.md"`},
		}),
		Tools: []llm.Tool{{Name: string(toolspec.ToolExecCommand), Description: "execute command", Schema: json.RawMessage(`{"type":"object"}`)}},
	}
}

type stringOutputTool struct {
	name toolspec.ID
}

func (t stringOutputTool) Call(_ context.Context, c tools.Call) (tools.Result, error) {
	output, _ := json.Marshal("output for " + c.ID)
	return tools.Result{CallID: c.ID, Name: c.Name, Output: output}, nil
}
