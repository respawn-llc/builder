package runprompt

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	modelstub "core/internal/testharness/pty/blackbox"
	"core/server/auth"
	"core/server/launch"
	"core/server/metadata"
	"core/server/runtime"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionlaunch"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/toolspec"
)

func TestRunPromptSenderProvenance(t *testing.T) {
	tests := map[string]struct {
		agent  bool
		create bool
	}{
		"agent-created":   {agent: true, create: true},
		"agent-continued": {agent: true},
		"human-created":   {create: true},
		"human-continued": {},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			runPromptSenderProvenanceCase(t, test.agent, test.create)
		})
	}
}

func runPromptSenderProvenanceCase(t *testing.T, agent bool, create bool) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	workspace := t.TempDir()
	meta, err := metadata.Open(root)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	binding, err := meta.RegisterWorkspaceBinding(ctx, workspace)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	containerDir := filepath.Join(root, "projects", binding.ProjectID, "sessions")
	storeOptions := meta.AuthoritativeSessionStoreOptions()
	createSession := func(category sessioncontract.SessionCategory) *session.Store {
		store, err := session.Create(containerDir, filepath.Base(containerDir), workspace, category, storeOptions...)
		if err != nil {
			t.Fatalf("session.Create: %v", err)
		}
		if err := store.EnsureDurable(); err != nil {
			t.Fatalf("EnsureDurable: %v", err)
		}
		return store
	}

	var callerID *string
	var callerRuntimeID *runtimeids.SessionID
	if agent {
		caller := createSession(sessioncontract.SessionCategoryMain)
		id := caller.Meta().SessionID
		callerID = &id
		parsedID := mustRunPromptSessionID(t, id)
		callerRuntimeID = &parsedID
	}
	var targetID string
	if !create {
		target := createSession(sessioncontract.SessionCategoryMain)
		targetID = target.Meta().SessionID
	}

	prompt := "delegate this task"
	wantContent := prompt
	wantRole := session.MessageRoleUser
	if agent {
		steer, err := runtime.NewAgentSteer(*callerRuntimeID, prompt)
		if err != nil {
			t.Fatalf("NewAgentSteer: %v", err)
		}
		message := steer.Message()
		if message.Content == nil {
			t.Fatal("NewAgentSteer returned absent content")
		}
		wantContent = *message.Content
		wantRole = session.MessageRoleDeveloper
	}

	var providerPayload map[string]any
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&providerPayload); err != nil {
			http.Error(w, "invalid provider request", http.StatusBadRequest)
			return
		}
		modelstub.WriteCompletedResponseStream(w, "done", 1, 1)
	}))
	defer provider.Close()

	authManager := auth.NewManager(auth.NewMemoryStore(auth.State{Method: auth.Method{
		Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "test-key"},
	}}), nil, time.Now)
	cfg := config.App{
		WorkspaceRoot:   workspace,
		PersistenceRoot: root,
		Settings: config.Settings{
			Model:            "gpt-5",
			OpenAIBaseURL:    provider.URL,
			EnabledTools:     map[toolspec.ID]bool{},
			MaxSubagentDepth: 2,
			Shell:            config.ShellSettings{PostprocessingMode: config.ShellPostprocessingModeBuiltin},
		},
	}
	history := &recordingPromptHistoryStore{}
	authority := newTestHeadlessRuntimeAuthority(root, authManager, nil, storeOptions...)
	client := NewInProcessRunPromptClient(HeadlessBootstrap{
		SessionLaunch: sessionlaunch.NewService(launch.Planner{
			Config:                   cfg,
			ContainerDir:             containerDir,
			StoreOptions:             storeOptions,
			PersistedSessions:        meta,
			ProjectWorkspaceBoundary: fixedProjectWorkspaceBoundaryResolver{root: workspace},
			ExecutionTargets: fixedSessionExecutionTargetResolver{target: clientui.SessionExecutionTarget{
				WorkspaceRoot:    workspace,
				CwdRelpath:       ".",
				EffectiveWorkdir: workspace,
			}},
		}).WithAuthStateReader(authManager),
		RuntimeAuthority: authority,
		PromptHistory:    history,
	})

	var intent serverapi.SessionLaunchIntent
	if create {
		if agent {
			intent = serverapi.CreateNewSessionLaunchIntent(
				serverapi.ParentAgentSessionCreateOrigin(*callerRuntimeID),
			)
		} else {
			intent = serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())
		}
	} else {
		intent = serverapi.OpenExistingSessionLaunchIntent(mustRunPromptSessionID(t, targetID))
	}
	response, err := client.RunPrompt(ctx, serverapi.RunPromptRequest{
		Intent:          intent,
		CallerSessionID: callerID,
		Prompt:          prompt,
	}, nil)
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if !create && response.SessionID != targetID {
		t.Fatalf("continued session id = %q, want %q", response.SessionID, targetID)
	}
	if len(history.entries) != 1 || history.entries[0].Text != wantContent {
		t.Fatalf("prompt history = %+v, want one entry with submitted representation", history.entries)
	}

	target, err := session.OpenByID(root, response.SessionID, storeOptions...)
	if err != nil {
		t.Fatalf("OpenByID target: %v", err)
	}
	records, err := sessiontest.CollectRecords(target)
	if err != nil {
		t.Fatalf("CollectRecords target: %v", err)
	}
	var submitted *session.MessageRecord
	for _, record := range records {
		payload, err := record.Payload()
		if err != nil {
			t.Fatalf("read target event payload: %v", err)
		}
		message, ok := payload.(session.MessageRecord)
		if !ok || message.Content == nil || (*message.Content != prompt && *message.Content != wantContent) {
			continue
		}
		submitted = &message
		break
	}
	if submitted == nil {
		t.Fatalf("target transcript does not contain the submitted assignment")
	}
	if submitted.Role != wantRole {
		t.Fatalf("submitted assignment role = %q, want %q", submitted.Role, wantRole)
	}
	if submitted.Content == nil || *submitted.Content != wantContent {
		t.Fatalf("submitted assignment content = %v, want %q", submitted.Content, wantContent)
	}
	if agent {
		if submitted.MessageType == nil || *submitted.MessageType != session.MessageTypeAgentSteer {
			t.Fatalf("submitted assignment message type = %v, want agent_steer", submitted.MessageType)
		}
	} else if submitted.MessageType != nil {
		t.Fatalf("human assignment message type = %q, want ordinary user message", *submitted.MessageType)
	}

	input, ok := providerPayload["input"].([]any)
	if !ok {
		t.Fatalf("provider input = %#v, want structured input items", providerPayload["input"])
	}
	providerRole := "user"
	if agent {
		providerRole = "developer"
	}
	if !providerContainsMessage(input, providerRole, wantContent) {
		t.Fatalf("provider input does not contain the submitted assignment: %#v", input)
	}
}

func providerContainsMessage(items []any, wantRole string, wantContent string) bool {
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok || item["type"] != "message" || item["role"] != wantRole {
			continue
		}
		content, ok := item["content"].([]any)
		if !ok {
			continue
		}
		for _, rawPart := range content {
			part, ok := rawPart.(map[string]any)
			if ok && part["text"] == wantContent {
				return true
			}
		}
	}
	return false
}
