package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/runtimewirefixture"
	"core/server/auth"
	"core/server/llm"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/tools"
	askquestion "core/server/tools"
	triggerhandofftool "core/server/tools"
	patchtool "core/server/tools/patch"
	shelltool "core/server/tools/shell"
	"core/shared/config"
	"core/shared/toolspec"
)

type stubTriggerHandoffController struct{}

func (stubTriggerHandoffController) TriggerHandoff(_ context.Context, _ string, _ llm.ToolCall, _ string, _ string) (string, bool, error) {
	return "", false, nil
}

func TestBuildToolRegistry_AllowsHostedWebSearchWithoutLocalRuntimeBuilder(t *testing.T) {
	workspace := t.TempDir()

	registry, _, _, err := buildToolRegistry(
		workspace,
		"",
		[]toolspec.ID{toolspec.ToolExecCommand, toolspec.ToolWebSearch},
		15*time.Second,
		16_000,
		false,
		true,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("build tool registry: %v", err)
	}

	defs := registry.Definitions()
	if len(defs) != 1 {
		t.Fatalf("expected only local runtime tools in registry, got %d", len(defs))
	}
	if defs[0].ID != toolspec.ToolExecCommand {
		t.Fatalf("expected shell runtime tool definition, got %+v", defs[0])
	}
}

func TestBuildLocalRuntimeHandler_CoversAllLocalToolContracts(t *testing.T) {
	workspace := t.TempDir()
	background, err := shelltool.NewManager()
	if err != nil {
		t.Fatalf("new background manager: %v", err)
	}
	ctx := localToolRuntimeContext{
		workspaceRoot:          workspace,
		ownerSessionID:         "session-1",
		shellOutputMaxChars:    16_000,
		allowNonCwdEdits:       false,
		supportsVision:         true,
		askQuestionBroker:      askquestion.NewAskQuestionBroker(),
		backgroundShellManager: background,
		triggerHandoffController: func() triggerhandofftool.TriggerHandoffController {
			return stubTriggerHandoffController{}
		},
		outsideWorkspaceEditApprover: func(context.Context, patchtool.OutsideWorkspaceRequest) (patchtool.OutsideWorkspaceApproval, error) {
			return patchtool.OutsideWorkspaceApproval{Decision: patchtool.OutsideWorkspaceDecisionDeny}, nil
		},
		outsideWorkspaceReadApprover: func(context.Context, patchtool.OutsideWorkspaceRequest) (patchtool.OutsideWorkspaceApproval, error) {
			return patchtool.OutsideWorkspaceApproval{Decision: patchtool.OutsideWorkspaceDecisionDeny}, nil
		},
	}

	for _, id := range tools.CatalogIDs() {
		def, ok := tools.DefinitionFor(id)
		if !ok || !def.AvailableInLocalRuntime() {
			continue
		}
		_, err := buildLocalRuntimeHandler(def, ctx)
		if err != nil {
			t.Fatalf("build local runtime handler for %s: %v", id, err)
		}
	}
}

func TestBuildToolRegistry_IncludesViewImageWhenEnabled(t *testing.T) {
	workspace := t.TempDir()

	registry, _, _, err := buildToolRegistry(
		workspace,
		"",
		[]toolspec.ID{toolspec.ToolViewImage},
		15*time.Second,
		16_000,
		false,
		true,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("build tool registry: %v", err)
	}

	defs := registry.Definitions()
	if len(defs) != 1 {
		t.Fatalf("expected 1 local runtime tool in registry, got %d", len(defs))
	}
	if defs[0].ID != toolspec.ToolViewImage {
		t.Fatalf("unexpected runtime tool definition: %+v", defs[0])
	}
}

func TestBuildToolRegistry_ViewImageApprovedOutsidePathSucceeds(t *testing.T) {
	workspace := t.TempDir()
	outsideFile := filepath.Join(outsideNonTempDir(t), "doc.pdf")
	pdfBytes := []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\ntrailer\n<<>>\n%%EOF\n")
	if err := os.WriteFile(outsideFile, pdfBytes, 0o644); err != nil {
		t.Fatalf("write outside pdf: %v", err)
	}

	sessionDir := t.TempDir()
	logger, err := newRunLogger(sessionDir, nil)
	if err != nil {
		t.Fatalf("new run logger: %v", err)
	}

	registry, broker, _, err := buildToolRegistry(
		workspace,
		"",
		[]toolspec.ID{toolspec.ToolViewImage},
		15*time.Second,
		16_000,
		false,
		true,
		logger,
		nil,
	)
	if err != nil {
		t.Fatalf("build tool registry: %v", err)
	}
	broker.SetAskHandler(func(req askquestion.AskQuestionRequest) (askquestion.AskQuestionResponse, error) {
		if !strings.Contains(req.Question, "Allow reading") {
			t.Fatalf("expected read-focused approval question, got %q", req.Question)
		}
		return askquestion.AskQuestionResponse{Approval: &askquestion.AskQuestionApprovalPayload{Decision: askquestion.AskQuestionApprovalDecisionAllowOnce}}, nil
	})

	viewImageHandler, ok := registry.Get(toolspec.ToolViewImage)
	if !ok {
		t.Fatal("expected view_image handler")
	}
	input, err := json.Marshal(map[string]any{"path": outsideFile})
	if err != nil {
		t.Fatalf("marshal view_image input: %v", err)
	}
	result, err := viewImageHandler.Call(context.Background(), tools.Call{ID: "call-1", Name: toolspec.ToolViewImage, Input: input})
	if err != nil {
		t.Fatalf("view_image call: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success result, got %s", string(result.Output))
	}
	var contentItems []map[string]any
	if err := json.Unmarshal(result.Output, &contentItems); err != nil {
		t.Fatalf("decode view_image output: %v", err)
	}
	if len(contentItems) != 1 {
		t.Fatalf("expected one view_image content item, got %+v", contentItems)
	}
	if contentItems[0]["type"] != "input_file" {
		t.Fatalf("expected view_image success payload, got %+v", contentItems)
	}

	if err := logger.Close(); err != nil {
		t.Fatalf("close run logger: %v", err)
	}
}

func TestBuildToolRegistry_ViewImageConfiguredAllowBypassesApprovalForOutsidePath(t *testing.T) {
	workspace := t.TempDir()
	outsideDir := filepath.Join(outsideNonTempDir(t), "missing")
	outsideFile := filepath.Join(outsideDir, "doc.pdf")
	pdfBytes := []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\ntrailer\n<<>>\n%%EOF\n")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("mkdir outside dir: %v", err)
	}
	if err := os.WriteFile(outsideFile, pdfBytes, 0o644); err != nil {
		t.Fatalf("write outside pdf: %v", err)
	}

	registry, broker, _, err := buildToolRegistry(
		workspace,
		"",
		[]toolspec.ID{toolspec.ToolViewImage},
		15*time.Second,
		16_000,
		true,
		true,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("build tool registry: %v", err)
	}

	askCalls := 0
	broker.SetAskHandler(func(req askquestion.AskQuestionRequest) (askquestion.AskQuestionResponse, error) {
		askCalls++
		return askquestion.AskQuestionResponse{Approval: &askquestion.AskQuestionApprovalPayload{Decision: askquestion.AskQuestionApprovalDecisionAllowOnce}}, nil
	})

	viewImageHandler, ok := registry.Get(toolspec.ToolViewImage)
	if !ok {
		t.Fatal("expected view_image handler")
	}
	input, err := json.Marshal(map[string]any{"path": outsideFile})
	if err != nil {
		t.Fatalf("marshal view_image input: %v", err)
	}
	result, err := viewImageHandler.Call(context.Background(), tools.Call{ID: "call-config-allow", Name: toolspec.ToolViewImage, Input: input})
	if err != nil {
		t.Fatalf("view_image call: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success result, got %s", string(result.Output))
	}
	if askCalls != 0 {
		t.Fatalf("expected configured allow to bypass approval, got %d asks", askCalls)
	}
}

func TestNewRuntimeWiring_ProviderOverrideSupportsAliasModelsForMainAndReviewer(t *testing.T) {
	workspace := t.TempDir()
	store := createAppRuntimeSessionAt(t, t.TempDir(), "ws", workspace)

	home := t.TempDir()
	t.Setenv("HOME", home)
	activeCfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	active := activeCfg.Settings
	active.Model = "main-alias"
	active.ProviderOverride = "openai"
	active.Reviewer.Frequency = "all"
	active.Reviewer.Model = "reviewer-alias"

	authMgr := auth.NewManager(auth.NewMemoryStore(auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{
			Type: auth.MethodAPIKey,
			APIKey: &auth.APIKeyMethod{
				Key: "test-key",
			},
		},
	}), nil, time.Now)

	logger, err := newRunLogger(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("new run logger: %v", err)
	}
	defer logger.Close()

	wiring, err := runtimewire.NewRuntimeWiring(store, active, config.EnabledToolIDs(active), workspace, authMgr, logger, runtimewire.RuntimeWiringOptions{})
	if err != nil {
		t.Fatalf("new runtime wiring: %v", err)
	}
	if wiring == nil || wiring.Engine == nil {
		t.Fatal("expected runtime wiring with engine")
	}
}

func TestNewRuntimeWiring_ReviewerProviderCanUseLocalAnonymousModel(t *testing.T) {
	workspace := t.TempDir()
	store := createAppRuntimeSessionAt(t, t.TempDir(), "ws", workspace)

	home := t.TempDir()
	t.Setenv("HOME", home)
	activeCfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	active := activeCfg.Settings
	active.Model = "gpt-5"
	active.Reviewer.Frequency = "all"
	active.Reviewer.Model = "local-supervisor"
	active.Reviewer.ProviderOverride = "openai"
	active.Reviewer.OpenAIBaseURL = "http://127.0.0.1:11434/v1"

	authMgr := auth.NewManager(auth.NewMemoryStore(auth.EmptyState()), nil, time.Now)

	logger, err := newRunLogger(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("new run logger: %v", err)
	}
	defer logger.Close()

	wiring, err := runtimewire.NewRuntimeWiring(store, active, config.EnabledToolIDs(active), workspace, authMgr, logger, runtimewire.RuntimeWiringOptions{})
	if err != nil {
		t.Fatalf("new runtime wiring: %v", err)
	}
	if wiring == nil || wiring.Engine == nil {
		t.Fatal("expected runtime wiring with engine")
	}
}

func TestBackgroundEventRouterSkipsDeveloperNoticeForOrphanedShells(t *testing.T) {
	root := t.TempDir()
	storeA := createAppRuntimeSessionAt(t, root, "ws-a", root)
	storeB := createAppRuntimeSessionAt(t, root, "ws-b", root)

	clientA := &busyToggleFakeClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "a", Phase: llm.MessagePhaseFinal}, Usage: llm.Usage{WindowTokens: 200_000}}}}
	clientB := &busyToggleFakeClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "b", Phase: llm.MessagePhaseFinal}, Usage: llm.Usage{WindowTokens: 200_000}}}}
	var mu sync.Mutex
	backgroundUpdates := 0
	engA := newAppRuntimeEngineWithStore(t, storeA, clientA, runtime.Config{})
	t.Cleanup(func() { _ = engA.Close() })
	engB := newAppRuntimeEngineWithStore(t, storeB, clientB, runtime.Config{OnEvent: func(evt runtime.Event) {
		if evt.Kind == runtime.EventBackgroundUpdated {
			mu.Lock()
			backgroundUpdates++
			mu.Unlock()
		}
	}})
	t.Cleanup(func() { _ = engB.Close() })

	router := &backgroundEventRouter{}
	router.SetActiveSession(storeB.Meta().SessionID, engB)
	router.handle(runtimewirefixture.BackgroundCompletionEvent("1000", storeA.Meta().SessionID, root))

	time.Sleep(150 * time.Millisecond)
	if got := clientB.CallCount(); got != 0 {
		t.Fatalf("expected orphaned completion to skip model notice for active session, got %d client calls", got)
	}
	mu.Lock()
	updates := backgroundUpdates
	mu.Unlock()
	if updates != 0 {
		t.Fatalf("expected orphaned completion to stay isolated from foreign active sessions, got %d background updates", updates)
	}
	if got := clientA.CallCount(); got != 0 {
		t.Fatalf("did not expect inactive owner engine to be called, got %d", got)
	}
}

func TestBackgroundEventRouterRoutesCompletionToMatchingActiveOwnerSession(t *testing.T) {
	root := t.TempDir()
	storeA := createAppRuntimeSessionAt(t, root, "ws-a", root)
	storeB := createAppRuntimeSessionAt(t, root, "ws-b", root)

	clientA := &busyToggleFakeClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "a", Phase: llm.MessagePhaseFinal}, Usage: llm.Usage{WindowTokens: 200_000}}}}
	clientB := &busyToggleFakeClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "b", Phase: llm.MessagePhaseFinal}, Usage: llm.Usage{WindowTokens: 200_000}}}}
	engA := newAppRuntimeEngineWithStore(t, storeA, clientA, runtime.Config{})
	t.Cleanup(func() { _ = engA.Close() })
	engB := newAppRuntimeEngineWithStore(t, storeB, clientB, runtime.Config{})
	t.Cleanup(func() { _ = engB.Close() })

	router := &backgroundEventRouter{}
	router.SetActiveSession(storeA.Meta().SessionID, engA)
	router.SetActiveSession(storeB.Meta().SessionID, engB)
	router.handle(runtimewirefixture.BackgroundCompletionEvent("1002", storeA.Meta().SessionID, root))

	deadline := time.Now().Add(500 * time.Millisecond)
	for clientA.CallCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := clientA.CallCount(); got == 0 {
		t.Fatal("expected owner session completion to route to its active engine even when another session is also active")
	}
	if got := clientB.CallCount(); got != 0 {
		t.Fatalf("did not expect foreign active session to receive routed completion, got %d", got)
	}
}

func TestBackgroundEventRouterQueuesNoticeForActiveOwnerSession(t *testing.T) {
	root := t.TempDir()
	store := createAppRuntimeSessionAt(t, root, "ws", root)
	client := &busyToggleFakeClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "notice handled", Phase: llm.MessagePhaseFinal}, Usage: llm.Usage{WindowTokens: 200_000}}}}
	eng := newAppRuntimeEngineWithStore(t, store, client, runtime.Config{})
	t.Cleanup(func() { _ = eng.Close() })

	router := &backgroundEventRouter{}
	router.SetActiveSession(store.Meta().SessionID, eng)
	router.handle(runtimewirefixture.BackgroundCompletionEvent("1001", store.Meta().SessionID, root))

	deadline := time.Now().Add(2 * time.Second)
	for client.CallCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := client.CallCount(); got == 0 {
		t.Fatal("expected active owner completion to queue a model notice")
	}
}

func TestBackgroundEventRouterShapesBackgroundNoticeByOutputMode(t *testing.T) {
	tests := []struct {
		name              string
		mode              shelltool.BackgroundOutputMode
		exitCode          int
		maxChars          int
		content           string
		wantInlinePreview bool
		wantTruncated     bool
	}{
		{
			name:              "concise success suppresses inline preview",
			mode:              shelltool.BackgroundOutputConcise,
			exitCode:          0,
			maxChars:          16,
			content:           "alpha\nbeta\ngamma\n",
			wantInlinePreview: false,
		},
		{
			name:              "verbose success keeps inline preview",
			mode:              shelltool.BackgroundOutputVerbose,
			exitCode:          0,
			maxChars:          5,
			content:           "alpha\nbeta\ngamma\n",
			wantInlinePreview: true,
		},
		{
			name:              "concise non-zero uses default preview",
			mode:              shelltool.BackgroundOutputConcise,
			exitCode:          17,
			maxChars:          32,
			content:           "alpha line\n" + strings.Repeat("middle-noise-", 40) + "\nomega line\n",
			wantInlinePreview: true,
			wantTruncated:     true,
		},
		{
			name:              "verbose non-zero keeps inline preview",
			mode:              shelltool.BackgroundOutputVerbose,
			exitCode:          17,
			maxChars:          5,
			content:           "alpha\nbeta\ngamma\n",
			wantInlinePreview: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			store := createAppRuntimeSessionAt(t, root, "ws", root)
			client := &busyToggleFakeClient{}
			events := make(chan runtime.Event, 4)
			eng := newAppRuntimeEngineWithStore(t, store, client, runtime.Config{
				OnEvent: func(evt runtime.Event) {
					if evt.Kind == runtime.EventBackgroundUpdated {
						events <- evt
					}
				},
			})
			t.Cleanup(func() { _ = eng.Close() })

			router := newBackgroundEventRouter(nil, tt.maxChars, tt.mode)
			router.SetActiveSession(store.Meta().SessionID, eng)
			event := runtimewirefixture.BackgroundCompletionEventWithOutput("1000", store.Meta().SessionID, root, tt.content, tt.exitCode)
			event.NoticeSuppressed = true
			router.handle(event)

			select {
			case evt := <-events:
				if evt.Background == nil ||
					evt.Background.ExitCode == nil || *evt.Background.ExitCode != tt.exitCode ||
					evt.Background.NoticeText == "" || evt.Background.CompactText == "" {
					t.Fatalf("background update = %+v", evt.Background)
				}
				if got := evt.Background.Preview != ""; got != tt.wantInlinePreview {
					t.Fatalf("inline preview = %t, want %t", got, tt.wantInlinePreview)
				}
				if got := evt.Background.PreviewRemoved > 0; got != tt.wantTruncated {
					t.Fatalf("preview truncation = %t, want %t", got, tt.wantTruncated)
				}
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for background update event")
			}
		})
	}
}

func TestBackgroundEventRouterRoutesValidCompletion(t *testing.T) {
	root := t.TempDir()
	store := createAppRuntimeSessionAt(t, root, "ws", root)
	client := &busyToggleFakeClient{}
	events := make(chan runtime.Event, 4)
	eng := newAppRuntimeEngineWithStore(t, store, client, runtime.Config{
		OnEvent: func(evt runtime.Event) {
			if evt.Kind == runtime.EventBackgroundUpdated {
				events <- evt
			}
		},
	})
	t.Cleanup(func() { _ = eng.Close() })

	router := newBackgroundEventRouter(nil, 80, shelltool.BackgroundOutputDefault)
	router.SetActiveSession(store.Meta().SessionID, eng)
	exitCode := 0
	event := runtimewirefixture.BackgroundCompletionEvent("1000", store.Meta().SessionID, root)
	event.NoticeSuppressed = true
	router.handle(event)

	select {
	case evt := <-events:
		if evt.Background == nil || evt.Background.ExitCode == nil || *evt.Background.ExitCode != exitCode ||
			evt.Background.NoticeText == "" || evt.Background.CompactText == "" || evt.Background.Preview == "" {
			t.Fatalf("valid background completion update = %+v", evt.Background)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for background update event")
	}
}

func TestBuildToolRegistryExecCommandPropagatesOwnerSessionID(t *testing.T) {
	workspace := t.TempDir()
	registry, _, manager, err := buildToolRegistry(
		workspace,
		"session-owner-1",
		[]toolspec.ID{toolspec.ToolExecCommand},
		250*time.Millisecond,
		16_000,
		false,
		true,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("build tool registry: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	handler, ok := registry.Get(toolspec.ToolExecCommand)
	if !ok {
		t.Fatal("expected exec_command handler")
	}
	input, err := json.Marshal(map[string]any{
		"cmd":           "printf owner-check\\n; sleep 30",
		"yield_time_ms": 250,
	})
	if err != nil {
		t.Fatalf("marshal exec_command input: %v", err)
	}
	if _, err := handler.Call(context.Background(), tools.Call{ID: "call-1", Name: toolspec.ToolExecCommand, Input: input}); err != nil {
		t.Fatalf("exec_command call: %v", err)
	}
	entries := manager.List()
	if len(entries) != 1 {
		t.Fatalf("expected one background process, got %d", len(entries))
	}
	if entries[0].OwnerSessionID != "session-owner-1" {
		t.Fatalf("expected owner session id propagation, got %q", entries[0].OwnerSessionID)
	}
}

func TestBackgroundEventRouterDoesNotRetroactivelyQueueNoticeAfterOwnerSessionResume(t *testing.T) {
	root := t.TempDir()
	manager, err := shelltool.NewManager(shelltool.WithMinimumExecToBgTime(20 * time.Millisecond))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	router := newBackgroundEventRouter(manager, 16_000, shelltool.BackgroundOutputDefault)

	storeA := createAppRuntimeSessionAt(t, root, "ws-a", root)
	storeB := createAppRuntimeSessionAt(t, root, "ws-b", root)
	clientA := &busyToggleFakeClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "a", Phase: llm.MessagePhaseFinal}, Usage: llm.Usage{WindowTokens: 200_000}}}}
	clientB := &busyToggleFakeClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "b", Phase: llm.MessagePhaseFinal}, Usage: llm.Usage{WindowTokens: 200_000}}}}
	engA := newAppRuntimeEngineWithStore(t, storeA, clientA, runtime.Config{})
	engB := newAppRuntimeEngineWithStore(t, storeB, clientB, runtime.Config{})

	router.SetActiveSession(storeA.Meta().SessionID, engA)
	workdir := t.TempDir()
	res, err := manager.Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"sh", "-c", "printf resume-check\\n; sleep 0.1"},
		DisplayCommand: "resume-check",
		OwnerSessionID: storeA.Meta().SessionID,
		Workdir:        workdir,
		YieldTime:      20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if !res.Backgrounded {
		t.Fatal("expected process to background")
	}
	router.ClearActiveSession(storeA.Meta().SessionID, engA)
	router.SetActiveSession(storeB.Meta().SessionID, engB)

	deadline := time.Now().Add(2 * time.Second)
	for {
		entries := manager.List()
		if len(entries) == 1 && !entries[0].Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for background process completion")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := clientA.CallCount(); got != 0 {
		t.Fatalf("expected owner session to receive no notice while orphaned, got %d", got)
	}
	if got := clientB.CallCount(); got != 0 {
		t.Fatalf("expected active foreign session to receive no notice, got %d", got)
	}

	router.SetActiveSession(storeA.Meta().SessionID, engA)
	time.Sleep(50 * time.Millisecond)
	if got := clientA.CallCount(); got != 0 {
		t.Fatalf("expected no retroactive notice on owner session resume, got %d", got)
	}
	entries := manager.List()
	if len(entries) != 1 || entries[0].ID != res.SessionID || entries[0].Running {
		t.Fatalf("expected finished process to remain visible in manager state after resume, got %+v", entries)
	}
}

func TestBackgroundEventRouterDropsNoticeWhenNoSessionIsActive(t *testing.T) {
	root := t.TempDir()
	manager := newFastBackgroundTestManager(t)
	router := newBackgroundEventRouter(manager, 16_000, shelltool.BackgroundOutputDefault)

	store := createAppRuntimeSessionAt(t, root, "ws", root)
	client := &busyToggleFakeClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal}, Usage: llm.Usage{WindowTokens: 200_000}}}}
	eng := newAppRuntimeEngineWithStore(t, store, client, runtime.Config{})
	router.SetActiveSession(store.Meta().SessionID, eng)
	router.ClearActiveSession(store.Meta().SessionID, eng)
	router.handle(runtimewirefixture.BackgroundCompletionEvent("1002", store.Meta().SessionID, root))
	time.Sleep(50 * time.Millisecond)
	if got := client.CallCount(); got != 0 {
		t.Fatalf("expected no notice delivery while no session is active, got %d", got)
	}
}

func outsideNonTempDir(t *testing.T) string {
	t.Helper()
	bases := make([]string, 0, 2)
	if wd, err := os.Getwd(); err == nil {
		bases = append(bases, wd)
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		bases = append(bases, home)
	}
	for _, base := range bases {
		dir, err := os.MkdirTemp(base, "kent-app-outside-*")
		if err != nil {
			continue
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			_ = os.RemoveAll(dir)
			continue
		}
		if patchtool.IsPathInTemporaryDir(abs) {
			_ = os.RemoveAll(dir)
			continue
		}
		t.Cleanup(func() {
			_ = os.RemoveAll(dir)
		})
		return abs
	}
	t.Skip("unable to create non-temporary outside directory for test")
	return ""
}
