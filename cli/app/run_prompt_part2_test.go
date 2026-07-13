package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"core/server/llm"
	"core/server/metadata"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/shared/config"
	"core/shared/serverapi"
)

func TestRunPromptCreatesSessionAndPersistsDurableTranscript(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)
	saveReadyAppAuthState(t, workspace)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleTestOpenAIInputTokenCount(w, r, 11) {
			return
		}
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := strings.TrimSpace(r.Header.Get("Authorization")); got == "" {
			t.Fatal("expected authorization header")
		}
		writeTestOpenAICompletedResponseStream(w, "hello from fake", 11, 7)
	}))
	defer server.Close()

	stopServer := startStandingRunPromptServer(t, workspace, server.URL)
	defer stopServer()

	var progresses []serverapi.RunPromptProgress
	result, err := RunPrompt(context.Background(), Options{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
		OpenAIBaseURL:         server.URL,
		OpenAIBaseURLExplicit: true,
	}, "hello from user", 0, serverapi.RunPromptProgressFunc(func(progress serverapi.RunPromptProgress) {
		progresses = append(progresses, progress)
	}))
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if result.Result != "hello from fake" {
		t.Fatalf("result = %q, want %q", result.Result, "hello from fake")
	}
	if strings.TrimSpace(result.SessionID) == "" {
		t.Fatal("expected session id")
	}
	if len(progresses) < 2 {
		t.Fatalf("progress events = %+v, want session start and assistant response", progresses)
	}
	started := progresses[0].SessionStarted
	if progresses[0].Kind != serverapi.RunPromptProgressKindSessionStarted || started == nil || started.SessionID.String() != result.SessionID {
		t.Fatalf("first progress event = %+v, want new session %q", progresses[0], result.SessionID)
	}
	last := progresses[len(progresses)-1].AssistantMessage
	if progresses[len(progresses)-1].Kind != serverapi.RunPromptProgressKindAssistantMessage || last == nil || last.Content != result.Result {
		t.Fatalf("last progress event = %+v, want assistant result", progresses[len(progresses)-1])
	}
	if !strings.HasSuffix(result.SessionName, " "+subagentSessionSuffix) {
		t.Fatalf("expected subagent session name, got %q", result.SessionName)
	}

	cfg := loadAppTestConfig(t, workspace, config.LoadOptions{OpenAIBaseURL: server.URL})
	store := openAuthoritativeAppSession(t, cfg.PersistenceRoot, result.SessionID)
	meta := store.Meta()
	wantWorkspaceRoot, err := config.CanonicalWorkspaceRoot(cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	if meta.WorkspaceRoot != wantWorkspaceRoot {
		t.Fatalf("workspace root = %q, want %q", meta.WorkspaceRoot, wantWorkspaceRoot)
	}
	if meta.FirstPromptPreview != "hello from user" {
		t.Fatalf("first prompt preview = %q, want %q", meta.FirstPromptPreview, "hello from user")
	}
	if meta.Continuation == nil || meta.Continuation.OpenAIBaseURL != server.URL {
		t.Fatalf("unexpected continuation context: %+v", meta.Continuation)
	}

	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	var (
		sawUser      bool
		sawAssistant bool
	)
	for _, evt := range events {
		if evt.Kind != "message" {
			continue
		}
		var msg llm.Message
		if err := json.Unmarshal(evt.Payload, &msg); err != nil {
			t.Fatalf("unmarshal message payload: %v", err)
		}
		if msg.Role == llm.RoleUser && msg.Content == "hello from user" {
			sawUser = true
		}
		if msg.Role == llm.RoleAssistant && msg.Content == "hello from fake" && msg.Phase == llm.MessagePhaseFinal {
			sawAssistant = true
		}
	}
	if !sawUser {
		t.Fatal("expected persisted user message in event log")
	}
	if !sawAssistant {
		t.Fatal("expected persisted final assistant message in event log")
	}
}

func TestRunPromptWorkspaceContextCreatesChildWithParentWorktreeContext(t *testing.T) {
	ctx := context.Background()
	home := newAppTestHome(t)
	workspace := t.TempDir()
	worktree := filepath.Join(home, config.ConfigDirName, "worktrees", "project", "feature")
	worktreeSubdir := filepath.Join(worktree, "pkg")
	if err := os.MkdirAll(worktreeSubdir, 0o755); err != nil {
		t.Fatalf("mkdir worktree subdir: %v", err)
	}
	configureAppTestServerPort(t)
	cfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	parent := createAuthoritativeAppSession(t, cfg.PersistenceRoot, cfg.WorkspaceRoot)
	metadataStore, err := metadata.Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	defer func() { _ = metadataStore.Close() }()
	binding, err := metadata.ResolveBinding(ctx, cfg.PersistenceRoot, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("ResolveBinding: %v", err)
	}
	canonicalWorktreeRoot, err := config.CanonicalWorkspaceRoot(worktree)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot worktree: %v", err)
	}
	if err := metadataStore.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
		ID:              "worktree-feature",
		WorkspaceID:     binding.WorkspaceID,
		CanonicalRoot:   canonicalWorktreeRoot,
		DisplayName:     "feature",
		Availability:    "available",
		GitMetadataJSON: `{}`,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	if err := metadataStore.UpdateSessionExecutionTarget(ctx, metadata.SessionExecutionTargetUpdate{SessionID: parent.Meta().SessionID, Workspace: &metadata.SessionExecutionTargetUpdateWorkspace{ID: binding.WorkspaceID}, Worktree: &metadata.SessionExecutionTargetUpdateWorktree{ID: "worktree-feature"}, CwdRelpath: "pkg"}); err != nil {
		t.Fatalf("UpdateSessionExecutionTarget parent: %v", err)
	}
	if err := parent.SetWorktreeReminderState(&session.WorktreeReminderState{
		Mode: session.WorktreeReminderModeEnter,
		WorktreeContext: session.WorktreeContext{
			Branch:        session.OptionalWorktreeBranch("feature/worktree"),
			WorktreePath:  canonicalWorktreeRoot,
			WorkspaceRoot: cfg.WorkspaceRoot,
			EffectiveCwd:  worktreeSubdir,
		},
	}); err != nil {
		t.Fatalf("SetWorktreeReminderState parent: %v", err)
	}
	saveReadyAppAuthState(t, workspace)
	fakeResponses, _ := newFakeResponsesServer(t, []string{"child reply"})
	defer fakeResponses.Close()
	stopServer := startStandingRunPromptServer(t, workspace, fakeResponses.URL)
	defer stopServer()

	result, err := RunPrompt(ctx, Options{
		WorkspaceRoot:             worktreeSubdir,
		WorkspaceContextSessionID: parent.Meta().SessionID,
		Model:                     "gpt-5",
		OpenAIBaseURL:             fakeResponses.URL,
		OpenAIBaseURLExplicit:     true,
	}, "hello from worktree", 0, nil)
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	child := openAuthoritativeAppSession(t, cfg.PersistenceRoot, result.SessionID)
	childMeta := child.Meta()
	if childMeta.ParentSessionID != parent.Meta().SessionID {
		t.Fatalf("child parent session id = %q, want %q", childMeta.ParentSessionID, parent.Meta().SessionID)
	}
	if childMeta.WorktreeReminder == nil {
		t.Fatal("expected child worktree reminder")
	}
	if childMeta.WorktreeReminder.Branch == nil ||
		*childMeta.WorktreeReminder.Branch != "feature/worktree" ||
		childMeta.WorktreeReminder.WorktreePath != canonicalWorktreeRoot {
		t.Fatalf("child worktree reminder = %+v", childMeta.WorktreeReminder)
	}
	messages, err := readStoredMessages(child)
	if err != nil {
		t.Fatalf("read messages: %v", err)
	}
	assertEnvironmentCWD(t, messages, worktreeSubdir)
	assertWorktreeReminderMessage(t, messages, "feature/worktree", worktreeSubdir, cfg.WorkspaceRoot)
}

func TestRunPromptFastRoleUsesRoleLevelProviderSettingsForHeuristics(t *testing.T) {
	home, workspace := newRegisteredAppWorkspace(t)
	saveReadyAppAuthState(t, workspace)

	configPath := filepath.Join(home, config.ConfigDirName, "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	contents := strings.Join([]string{
		"model = \"gpt-5.4\"",
		"",
		"[subagents.fast]",
		"provider_override = \"openai\"",
	}, "\n")

	requestBodies := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleTestOpenAIInputTokenCount(w, r, 11) {
			return
		}
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := strings.TrimSpace(r.Header.Get("Authorization")); got == "" {
			t.Fatal("expected authorization header")
		}
		defer r.Body.Close()
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		requestBodies <- payload
		writeTestOpenAICompletedResponseStream(w, "fast via role provider", 11, 7)
	}))
	defer server.Close()

	contents = "openai_base_url = \"" + server.URL + "\"\n" + contents + "\n"
	if err := os.WriteFile(configPath, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	stopServer := startStandingRunPromptServer(t, workspace, server.URL)
	defer stopServer()

	result, err := RunPrompt(context.Background(), Options{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		AgentRole:             config.BuiltInSubagentRoleFast,
	}, "hello from user", 0, nil)
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if result.Result != "fast via role provider" {
		t.Fatalf("result = %q, want %q", result.Result, "fast via role provider")
	}
	payload := <-requestBodies
	if got := payload["model"]; got != "gpt-5.6-terra" {
		t.Fatalf("model payload = %#v, want gpt-5.6-terra", got)
	}
	store := openAuthoritativeWorkspaceSessionStore(t, workspace, server.URL, result.SessionID)
	if store.Meta().Continuation == nil || store.Meta().Continuation.OpenAIBaseURL != server.URL {
		t.Fatalf("unexpected continuation context: %+v", store.Meta().Continuation)
	}
}

// runHeadlessPromptViaEmbedded opens an in-process embedded server, runs a
// single prompt through its run client, then closes the server (releasing the
// root lock) so a subsequent embedded server can resume the persisted session.
// kent run no longer starts servers, so tests drive the run client directly.
func runHeadlessPromptViaEmbedded(t *testing.T, opts Options, clientRequestID, prompt string) serverapi.RunPromptResponse {
	t.Helper()
	boot, err := startEmbeddedServer(context.Background(), opts, newHeadlessAuthInteractor(), false)
	if err != nil {
		t.Fatalf("bootstrap app: %v", err)
	}
	defer func() { _ = boot.Close() }()
	resp, err := newHeadlessRunPromptClient(boot).RunPrompt(context.Background(), serverapi.RunPromptRequest{
		ClientRequestID: clientRequestID,
		Prompt:          prompt,
	}, nil)
	if err != nil {
		t.Fatalf("embedded RunPrompt: %v", err)
	}
	return resp
}

func TestHeadlessRunPromptClientResumesExistingSessionByID(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)
	saveReadyAppAuthState(t, workspace)

	server, hits := newFakeResponsesServer(t, []string{"first response", "second response"})
	defer server.Close()

	created := runHeadlessPromptViaEmbedded(t, Options{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
		OpenAIBaseURL:         server.URL,
		OpenAIBaseURLExplicit: true,
	}, "req-create-1", "first prompt")

	boot, err := startEmbeddedServer(context.Background(), Options{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		SessionID:             created.SessionID,
		Model:                 "gpt-5",
		OpenAIBaseURL:         server.URL,
		OpenAIBaseURLExplicit: true,
	}, newHeadlessAuthInteractor(), false)
	if err != nil {
		t.Fatalf("bootstrap app: %v", err)
	}
	defer func() { _ = boot.Close() }()

	runClient := newHeadlessRunPromptClient(boot)
	resumed, err := runClient.RunPrompt(context.Background(), serverapi.RunPromptRequest{
		ClientRequestID:   "req-resume-1",
		SelectedSessionID: created.SessionID,
		Prompt:            "second prompt",
	}, nil)
	if err != nil {
		t.Fatalf("resumed client RunPrompt: %v", err)
	}
	if resumed.SessionID != created.SessionID {
		t.Fatalf("resumed session id = %q, want %q", resumed.SessionID, created.SessionID)
	}
	if resumed.Result != "second response" {
		t.Fatalf("resumed result = %q, want %q", resumed.Result, "second response")
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("fake response server hit count = %d, want 2", got)
	}

	store := openAuthoritativeWorkspaceSessionStore(t, workspace, server.URL, created.SessionID)
	messages, err := readStoredMessages(store)
	if err != nil {
		t.Fatalf("read stored messages: %v", err)
	}
	assertMessagePresent(t, messages, llm.RoleUser, "first prompt")
	assertMessagePresent(t, messages, llm.RoleAssistant, "first response")
	assertMessagePresent(t, messages, llm.RoleUser, "second prompt")
	assertMessagePresent(t, messages, llm.RoleAssistant, "second response")
	if got := store.Meta().FirstPromptPreview; got != "first prompt" {
		t.Fatalf("first prompt preview = %q, want %q", got, "first prompt")
	}
}

func TestHeadlessRunPromptClientRestoresContinuationContextFromSelectedSession(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)
	saveReadyAppAuthState(t, workspace)

	server, hits := newFakeResponsesServer(t, []string{"created via explicit base url", "resumed via continuation"})
	defer server.Close()

	created := runHeadlessPromptViaEmbedded(t, Options{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
		OpenAIBaseURL:         server.URL,
		OpenAIBaseURLExplicit: true,
	}, "req-create-2", "first prompt")

	boot, err := startEmbeddedServer(context.Background(), Options{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		SessionID:             created.SessionID,
		Model:                 "gpt-5",
	}, newHeadlessAuthInteractor(), false)
	if err != nil {
		t.Fatalf("bootstrap app: %v", err)
	}
	defer func() { _ = boot.Close() }()

	runClient := newHeadlessRunPromptClient(boot)
	resumed, err := runClient.RunPrompt(context.Background(), serverapi.RunPromptRequest{
		ClientRequestID:   "req-resume-2",
		SelectedSessionID: created.SessionID,
		Prompt:            "second prompt",
	}, nil)
	if err != nil {
		t.Fatalf("resumed client RunPrompt: %v", err)
	}
	if resumed.Result != "resumed via continuation" {
		t.Fatalf("resumed result = %q, want %q", resumed.Result, "resumed via continuation")
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("fake response server hit count = %d, want 2", got)
	}

	store := openAuthoritativeWorkspaceSessionStore(t, workspace, server.URL, created.SessionID)
	if store.Meta().Continuation == nil || store.Meta().Continuation.OpenAIBaseURL != server.URL {
		t.Fatalf("unexpected continuation context: %+v", store.Meta().Continuation)
	}
}

func newFakeResponsesServer(t *testing.T, assistantReplies []string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleTestOpenAIInputTokenCount(w, r, 11) {
			return
		}
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := strings.TrimSpace(r.Header.Get("Authorization")); got == "" {
			t.Fatal("expected authorization header")
		}
		index := int(hits.Add(1)) - 1
		if index >= len(assistantReplies) {
			t.Fatalf("unexpected response request index %d", index)
		}
		writeTestOpenAICompletedResponseStream(w, assistantReplies[index], 11, 7)
	}))
	return server, &hits
}

func newNoAuthFakeResponsesServer(t *testing.T, assistantReplies []string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleTestOpenAIInputTokenCount(w, r, 11) {
			return
		}
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := strings.TrimSpace(r.Header.Get("Authorization")); got != "" {
			t.Fatalf("unexpected authorization header %q", got)
		}
		index := int(hits.Add(1)) - 1
		if index >= len(assistantReplies) {
			t.Fatalf("unexpected response request index %d", index)
		}
		writeTestOpenAICompletedResponseStream(w, assistantReplies[index], 11, 7)
	}))
	return server, &hits
}

func openAuthoritativeWorkspaceSessionStore(t *testing.T, workspaceRoot, openAIBaseURL, sessionID string) *session.Store {
	t.Helper()
	loadOpts := config.LoadOptions{}
	if strings.TrimSpace(openAIBaseURL) != "" {
		loadOpts.OpenAIBaseURL = openAIBaseURL
	}
	cfg := loadAppTestConfig(t, workspaceRoot, loadOpts)
	return openAuthoritativeAppSession(t, cfg.PersistenceRoot, sessionID)
}

func readStoredMessages(store *session.Store) ([]llm.Message, error) {
	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		return nil, err
	}
	messages := make([]llm.Message, 0, len(events))
	for _, evt := range events {
		if evt.Kind != "message" {
			continue
		}
		var msg llm.Message
		if err := json.Unmarshal(evt.Payload, &msg); err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}
	return messages, nil
}

func assertEnvironmentCWD(t *testing.T, messages []llm.Message, cwd string) {
	t.Helper()
	want := "\nCWD: " + cwd + "\n"
	for _, msg := range messages {
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeEnvironment && strings.Contains(msg.Content, want) {
			return
		}
	}
	t.Fatalf("expected environment CWD %q in messages %+v", cwd, messages)
}

func assertWorktreeReminderMessage(t *testing.T, messages []llm.Message, branch string, cwd string, workspaceRoot string) {
	t.Helper()
	sawWorktreeReminder := false
	for _, msg := range messages {
		if msg.Role != llm.RoleDeveloper || msg.MessageType != llm.MessageTypeWorktreeMode {
			continue
		}
		sawWorktreeReminder = true
		if strings.Contains(msg.Content, branch) && strings.Contains(msg.Content, cwd) && strings.Contains(msg.Content, workspaceRoot) {
			return
		}
	}
	if sawWorktreeReminder {
		t.Fatalf("no matching worktree reminder found for branch %q cwd %q workspace %q", branch, cwd, workspaceRoot)
	}
	t.Fatalf("expected worktree reminder message in %+v", messages)
}

func assertMessagePresent(t *testing.T, messages []llm.Message, role llm.Role, content string) {
	t.Helper()
	for _, msg := range messages {
		if msg.Role == role && msg.Content == content {
			return
		}
	}
	t.Fatalf("expected message role=%s content=%q in %+v", role, content, messages)
}
