package runprompt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	modelstub "core/internal/testharness/pty/blackbox"
	"core/internal/testharness/testsetup"
	"core/server/auth"
	"core/server/launch"
	"core/server/llm"
	"core/server/metadata"
	"core/server/requestmemo"
	"core/server/runtime"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionlaunch"
	"core/server/sessionruntime"
	shelltool "core/server/tools/shell"
	"core/server/tools/shell/postprocess"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/sessionenv"
	"core/shared/textutil"
	"core/shared/toolspec"
)

type recordingPromptHistoryStore struct {
	entries []metadata.PromptHistoryEntry
}

func (s *recordingPromptHistoryStore) RecordPromptHistoryEntry(_ context.Context, entry metadata.PromptHistoryEntry) (metadata.PromptHistoryRecord, bool, error) {
	s.entries = append(s.entries, entry)
	return metadata.PromptHistoryRecord{}, true, nil
}

type blockingPromptHistoryStore struct{}

func (s *blockingPromptHistoryStore) RecordPromptHistoryEntry(ctx context.Context, _ metadata.PromptHistoryEntry) (metadata.PromptHistoryRecord, bool, error) {
	<-ctx.Done()
	return metadata.PromptHistoryRecord{}, false, ctx.Err()
}

type fixedSessionExecutionTargetResolver struct {
	target clientui.SessionExecutionTarget
}

func (r fixedSessionExecutionTargetResolver) ResolveSessionExecutionTarget(context.Context, string) (clientui.SessionExecutionTarget, error) {
	return r.target, nil
}

type headlessLaunchArtifactSnapshot struct {
	SessionIDs       []string
	SessionArtifacts []string
	PersistenceFiles []string
	WorktreeFiles    []string
	WorktreeRecords  []metadata.WorktreeRecord
}

func snapshotHeadlessLaunchArtifacts(t *testing.T, ctx context.Context, meta *metadata.Store, projectID string, workspaceID string, containerDir string, persistenceRoot string, worktreeRoot string) headlessLaunchArtifactSnapshot {
	t.Helper()
	sessionIDs, err := meta.ListProjectSessionIDs(ctx, projectID)
	if err != nil {
		t.Fatalf("ListProjectSessionIDs snapshot: %v", err)
	}
	worktreeRecords, err := meta.ListWorktreeRecordsByWorkspaceID(ctx, workspaceID)
	if err != nil {
		t.Fatalf("ListWorktreeRecordsByWorkspaceID snapshot: %v", err)
	}
	return headlessLaunchArtifactSnapshot{
		SessionIDs:       sessionIDs,
		SessionArtifacts: snapshotArtifactPaths(t, containerDir),
		PersistenceFiles: snapshotArtifactPaths(t, persistenceRoot),
		WorktreeFiles:    snapshotArtifactPaths(t, worktreeRoot),
		WorktreeRecords:  worktreeRecords,
	}
}

func snapshotArtifactPaths(t *testing.T, root string) []string {
	t.Helper()
	paths := make([]string, 0)
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return paths
	} else if err != nil {
		t.Fatalf("Stat %s: %v", root, err)
	}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		kind := "file"
		if entry.IsDir() {
			kind = "dir"
		}
		paths = append(paths, kind+":"+filepath.ToSlash(relative))
		return nil
	}); err != nil {
		t.Fatalf("WalkDir %s: %v", root, err)
	}
	sort.Strings(paths)
	return paths
}

func TestRunPromptProgressFromRuntimeEventPublishesUserVisibleEvents(t *testing.T) {
	tests := []struct {
		name      string
		event     runtime.Event
		wantKind  serverapi.RunPromptProgressKind
		wantText  string
		wantPhase clientui.MessagePhase
	}{
		{
			name: "assistant commentary",
			event: runtime.Event{
				Kind: runtime.EventAssistantMessage,
				Message: llm.Message{
					Role:    llm.RoleAssistant,
					Phase:   textutil.Value(llm.MessagePhaseCommentary),
					Content: textutil.Value("I am checking the runtime."),
				},
			},
			wantKind:  serverapi.RunPromptProgressKindAssistantMessage,
			wantText:  "I am checking the runtime.",
			wantPhase: clientui.MessagePhaseCommentary,
		},
		{
			name:     "compaction started",
			event:    runtime.Event{Kind: runtime.EventCompactionStarted},
			wantKind: serverapi.RunPromptProgressKindCompactionStarted,
		},
		{
			name: "compaction failed",
			event: runtime.Event{
				Kind:       runtime.EventCompactionFailed,
				Compaction: &runtime.CompactionStatus{Error: "provider rejected compaction"},
			},
			wantKind: serverapi.RunPromptProgressKindCompactionFailed,
			wantText: "provider rejected compaction",
		},
		{
			name: "steering accepted",
			event: runtime.Event{
				Kind: runtime.EventQueuedUserMessageStatus,
				QueuedUserMessageStatus: &runtime.QueuedUserMessageStatusEvent{
					Status:      runtime.QueuedUserMessageAccepted,
					RestoreText: "use the safer migration",
				},
			},
			wantKind: serverapi.RunPromptProgressKindSteeredMessage,
			wantText: "use the safer migration",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := RunPromptProgressFromRuntimeEvent(test.event)
			if !ok {
				t.Fatal("expected user-visible progress event")
			}
			if got.Kind != test.wantKind {
				t.Fatalf("kind = %q, want %q", got.Kind, test.wantKind)
			}
			switch test.wantKind {
			case serverapi.RunPromptProgressKindAssistantMessage:
				if got.AssistantMessage == nil {
					t.Fatal("assistant message payload is absent")
				}
				if got.AssistantMessage.Content != test.wantText || got.AssistantMessage.Phase != test.wantPhase {
					t.Fatalf("assistant message = %+v", got.AssistantMessage)
				}
			case serverapi.RunPromptProgressKindCompactionFailed:
				if got.Failure == nil || got.Failure.Error == nil || *got.Failure.Error != test.wantText {
					t.Fatalf("failure = %+v", got.Failure)
				}
			case serverapi.RunPromptProgressKindSteeredMessage:
				if got.SteeredMessage == nil || got.SteeredMessage.Content != test.wantText {
					t.Fatalf("steered message = %+v", got.SteeredMessage)
				}
			}
		})
	}
}

func TestRunPromptProgressFromRuntimeEventDropsOperationalSpam(t *testing.T) {
	for _, event := range []runtime.Event{
		{Kind: runtime.EventToolCallStarted},
		{Kind: runtime.EventToolCallCompleted},
		{Kind: runtime.EventReviewerCompleted},
		{
			Kind: runtime.EventQueuedUserMessageStatus,
			QueuedUserMessageStatus: &runtime.QueuedUserMessageStatusEvent{
				Status: runtime.QueuedUserMessageSubmitted,
			},
		},
	} {
		if got, ok := RunPromptProgressFromRuntimeEvent(event); ok {
			t.Fatalf("unexpected progress event for %q: %+v", event.Kind, got)
		}
	}
}

func TestInProcessRunPromptClientRejectsInvalidRequestsBeforeLaunch(t *testing.T) {
	reservedRole := "self"
	validIntent := serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())
	tests := []struct {
		name string
		req  serverapi.RunPromptRequest
	}{
		{name: "missing request id", req: serverapi.RunPromptRequest{Intent: validIntent, Prompt: "work"}},
		{name: "blank prompt", req: serverapi.RunPromptRequest{ClientRequestID: "request-1", Intent: validIntent, Prompt: " \n "}},
		{name: "invalid intent", req: serverapi.RunPromptRequest{ClientRequestID: "request-1", Prompt: "work"}},
		{name: "reserved role", req: serverapi.RunPromptRequest{
			ClientRequestID: "request-1",
			Intent:          validIntent,
			Prompt:          "work",
			Overrides:       serverapi.RunPromptOverrides{AgentRole: &reservedRole},
		}},
	}

	client := NewInProcessRunPromptClient(HeadlessBootstrap{})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := client.RunPrompt(context.Background(), test.req, nil); err == nil {
				t.Fatal("RunPrompt succeeded before validating the request")
			}
		})
	}
}

func newTestHeadlessSessionLaunch(
	cfg config.App,
	containerDir string,
	authManager *auth.Manager,
	authority *sessionruntime.Authority,
	persistences ...*sessiontest.Persistence,
) *sessionlaunch.Service {
	persistence := sessiontest.NewPersistence()
	if len(persistences) > 0 && persistences[0] != nil {
		persistence = persistences[0]
	}
	return sessionlaunch.NewService(launch.Planner{
		Config:                   cfg,
		ContainerDir:             containerDir,
		StoreOptions:             persistence.Options(),
		PersistedSessions:        persistence,
		ProjectWorkspaceBoundary: fixedProjectWorkspaceBoundaryResolver{root: cfg.WorkspaceRoot},
		ExecutionTargets: fixedSessionExecutionTargetResolver{target: clientui.SessionExecutionTarget{
			WorkspaceRoot:    cfg.WorkspaceRoot,
			CwdRelpath:       ".",
			EffectiveWorkdir: cfg.WorkspaceRoot,
		}},
	}).WithAuthStateReader(authManager).WithRuntimeAuthority(authority)
}

type fixedProjectWorkspaceBoundaryResolver struct{ root string }

func (r fixedProjectWorkspaceBoundaryResolver) ResolveSessionProjectWorkspaceBoundary(context.Context, string) (metadata.ProjectWorkspaceBoundary, error) {
	return metadata.ProjectWorkspaceBoundary{
		ProjectID:  "test-project",
		Workspaces: []metadata.ProjectWorkspace{{CanonicalRoot: r.root}},
	}, nil
}

func (r fixedProjectWorkspaceBoundaryResolver) ListManagedWorktreeRoots(context.Context) ([]string, error) {
	return nil, nil
}

func newTestHeadlessRuntimeAuthority(root string, authManager *auth.Manager, background *shelltool.Manager, storeOptions ...session.StoreOption) *sessionruntime.Authority {
	return sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: root,
		AuthManager:     authManager,
		Background:      background,
		StoreOptions:    storeOptions,
	})
}

func TestHeadlessRuntimeUsesServerManagedWorktreeNamespace(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	serverManagedBase := filepath.Join(root, "server-worktrees")
	projectManagedBase := filepath.Join(root, "project-worktrees")
	currentWorktree := filepath.Join(serverManagedBase, "current")
	workdir := filepath.Join(currentWorktree, "pkg")
	for _, dir := range []string{workspace, workdir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", dir, err)
		}
	}
	persistence := sessiontest.NewPersistence()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	store, err := session.Create(containerDir, "workspace-a", workspace, sessioncontract.SessionCategorySubagent, persistence.Options()...)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	descriptor, err := session.NewScopedOpenSessionDescriptor(sessionID, containerDir)
	if err != nil {
		t.Fatalf("NewScopedOpenSessionDescriptor: %v", err)
	}
	authManager := auth.NewManager(auth.NewMemoryStore(auth.State{
		Method: auth.Method{
			Type:   auth.MethodAPIKey,
			APIKey: &auth.APIKeyMethod{Key: "test-key"},
		},
	}), nil, time.Now)
	authority := newTestHeadlessRuntimeAuthority(root, authManager, nil, persistence.Options()...)
	launcher := &headlessPromptLauncher{boot: HeadlessBootstrap{
		RuntimeAuthority:       authority,
		ManagedWorktreeBaseDir: serverManagedBase,
	}}
	runtimePlan, err := launcher.prepareRuntime(context.Background(), launch.SessionPlan{
		Descriptor: descriptor,
		ActiveSettings: config.Settings{
			Model:         "gpt-5",
			OpenAIBaseURL: "http://127.0.0.1:1",
			Shell:         config.ShellSettings{PostprocessingMode: config.ShellPostprocessingModeBuiltin},
		},
		BaseSettings: config.Settings{
			Worktrees: config.WorktreeSettings{BaseDir: projectManagedBase},
		},
		ExecutionTarget: clientui.SessionExecutionTarget{
			WorkspaceRoot:    workspace,
			EffectiveWorkdir: workdir,
			Worktree: &clientui.SessionExecutionWorktreeTarget{
				Root: currentWorktree,
			},
		},
		ProjectWorkspaceBoundary: metadata.ProjectWorkspaceBoundary{
			ProjectID:  "project-a",
			Workspaces: []metadata.ProjectWorkspace{{CanonicalRoot: workspace}},
		},
		ManagedWorktreeRoots: []string{currentWorktree},
	}, nil, nil)
	if err != nil {
		t.Fatalf("prepareRuntime: %v", err)
	}
	if err := runtimePlan.CloseWithFailure(true); err != nil {
		t.Fatalf("CloseWithFailure: %v", err)
	}
}

type selectedRunPromptFixture struct {
	store     *session.Store
	authority *sessionruntime.Authority
	client    apicontract.RunPromptService
}

type retainedSelectedRunPromptFixture struct {
	metadata    *metadata.Store
	store       *workflowstore.Store
	authority   *sessionruntime.Authority
	controller  *workflowexecution.CurrentNodeController
	client      apicontract.RunPromptService
	sessionID   runtimeids.SessionID
	taskID      workflow.TaskID
	currentNode workflow.CurrentNodeReference
}

type retainedRunPromptAssignmentSteerer struct{}

func (retainedRunPromptAssignmentSteerer) SteerCurrentNodeAssignment(
	context.Context,
	workflow.CurrentNodeReference,
) (workflowexecution.CurrentNodeAssignmentSteer, error) {
	return retainedRunPromptAssignmentSteer{}, nil
}

type retainedRunPromptAssignmentSteer struct{}

func (retainedRunPromptAssignmentSteer) Wait(context.Context) (session.CommitReceipt, error) {
	return session.CommitReceipt{Committed: true}, nil
}

type retainedRunPromptBlockingRunner struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *retainedRunPromptBlockingRunner) StartCurrentNode(
	context.Context,
	workflow.CurrentNodeReference,
	workflowruntime.TaskPromptDelivery,
	workflowexecution.CurrentNodeAssignmentSteer,
	sessionruntime.WorkflowExecutionLease,
	workflowruntime.Controller,
) error {
	r.once.Do(func() { close(r.entered) })
	<-r.release
	return errors.New("release retained Task Resume preparation")
}

type retainedRunPromptFailingRunner struct{}

func (retainedRunPromptFailingRunner) StartCurrentNode(
	context.Context,
	workflow.CurrentNodeReference,
	workflowruntime.TaskPromptDelivery,
	workflowexecution.CurrentNodeAssignmentSteer,
	sessionruntime.WorkflowExecutionLease,
	workflowruntime.Controller,
) error {
	return errors.New("unexpected retained Current Node launch")
}

func newRetainedSelectedRunPromptFixture(
	t *testing.T,
	providerURL string,
	runner workflowexecution.CurrentNodeRunner,
	lifecycle sessionruntime.AgentResourceLifecycle,
) retainedSelectedRunPromptFixture {
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
	store, err := workflowstore.New(meta, workflowstore.WithRoleResolver(testsetup.QuestionsEnabled("coder")))
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	workflowID := createRetainedRunPromptWorkflow(t, store)
	if _, err := store.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := store.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID:         binding.ProjectID,
		WorkflowID:        &workflowID,
		Title:             "Retained RunPrompt",
		Body:              "Exercise selected retained Session authority.",
		SourceWorkspaceID: binding.WorkspaceID,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started, err := store.StartTaskWithExecutionTarget(ctx, task.ID, &workflowstore.ExecutionTargetCandidate{
		Snapshot: workflowstore.ExecutionTargetSnapshot{
			Mode:       workflow.ExecutionTargetModeNone,
			Provenance: workflowstore.ExecutionTargetProvenanceResolved,
		},
		Root: workflowstore.ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
		},
	})
	if err != nil {
		t.Fatalf("StartTaskWithExecutionTarget: %v", err)
	}
	if len(started.Mutation.Created) != 1 {
		t.Fatalf("started Current Nodes = %+v, want one", started.Mutation.Created)
	}
	currentNode := started.Mutation.Created[0].Reference
	containerDir := filepath.Join(root, "projects", binding.ProjectID, "sessions")
	sessionStore, err := session.Create(
		containerDir,
		binding.WorkspaceID,
		binding.CanonicalRoot,
		sessioncontract.SessionCategorySubagent,
		meta.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(sessionStore.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	if err := sessionStore.SetContinuationContext(session.ContinuationContext{OpenAIBaseURL: providerURL}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	if _, err := store.BindSessionToCurrentNode(ctx, workflowstore.CurrentNodeSessionBindingRequest{
		Association: workflowstore.TaskSessionAssociationRequest{
			SessionID:    sessionID,
			CurrentNode:  currentNode,
			AssociatedAt: time.Now().UTC(),
		},
	}); err != nil {
		t.Fatalf("BindSessionToCurrentNode: %v", err)
	}
	if err := store.InterruptCurrentNode(
		ctx,
		currentNode,
		"test_retained_runprompt",
		workflow.NewCurrentNodeInterruptionDetail("test_retained_runprompt", nil),
	); err != nil {
		t.Fatalf("InterruptCurrentNode: %v", err)
	}
	authManager := auth.NewManager(auth.NewMemoryStore(auth.State{
		Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "test-key"}},
	}), nil, time.Now)
	var controller *workflowexecution.CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot:   root,
		AuthManager:       authManager,
		StoreOptions:      meta.AuthoritativeSessionStoreOptions(),
		ResourceLifecycle: lifecycle,
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	if runner == nil {
		runner = retainedRunPromptFailingRunner{}
	}
	controller, err = workflowexecution.NewCurrentNodeController(
		store,
		runner,
		authority,
		workflowexecution.NewTaskMutationCoordinator(),
		workflowexecution.CurrentNodeControllerConfig{
			AgentConcurrency:  1,
			AssignmentSteerer: retainedRunPromptAssignmentSteerer{},
		},
	)
	if err != nil {
		t.Fatalf("NewCurrentNodeController: %v", err)
	}
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})
	cfg := config.App{
		WorkspaceRoot:   binding.CanonicalRoot,
		PersistenceRoot: root,
		Settings: config.Settings{
			Model:         "gpt-5",
			ThinkingLevel: "medium",
			OpenAIBaseURL: providerURL,
			Shell:         config.ShellSettings{PostprocessingMode: config.ShellPostprocessingModeBuiltin},
		},
	}
	sessionLaunch := sessionlaunch.NewService(launch.Planner{
		Config:                   cfg,
		ContainerDir:             containerDir,
		StoreOptions:             meta.AuthoritativeSessionStoreOptions(),
		PersistedSessions:        meta,
		ExecutionTargets:         meta,
		ProjectWorkspaceBoundary: meta,
	}).WithAuthStateReader(authManager).WithRuntimeAuthority(authority)
	return retainedSelectedRunPromptFixture{
		metadata:   meta,
		store:      store,
		authority:  authority,
		controller: controller,
		client: NewInProcessRunPromptClient(HeadlessBootstrap{
			SessionLaunch:    sessionLaunch,
			RuntimeAuthority: authority,
		}),
		sessionID:   sessionID,
		taskID:      task.ID,
		currentNode: currentNode,
	}
}

func TestSelectedRetainedRunPromptUsesWorkflowAuthorityForKentTaskComplete(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if modelstub.HandleInputTokenCount(w, r, 1) {
			return
		}
		close(started)
		<-release
		modelstub.WriteCompletedResponseStream(w, "done", 1, 1)
	}))
	t.Cleanup(server.Close)
	fixture := newRetainedSelectedRunPromptFixture(t, server.URL, nil, nil)
	runDone := make(chan error, 1)
	go func() {
		_, err := fixture.client.RunPrompt(context.Background(), serverapi.RunPromptRequest{
			ClientRequestID: "retained-runprompt-complete",
			Intent:          serverapi.OpenExistingSessionLaunchIntent(fixture.sessionID),
			Prompt:          "continue",
		}, nil)
		runDone <- err
	}()
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
	})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("selected retained RunPrompt did not reach its model turn")
	}

	if _, err := fixture.controller.CompleteSessionCurrentNode(
		context.Background(),
		fixture.sessionID,
		"done",
		nil,
		"completed from kent task complete",
	); err != nil {
		t.Fatalf("kent task complete could not resolve selected retained RunPrompt Workflow authority: %v", err)
	}
}

func TestRetainedActivationRejectsExpectedExactReplacementInsteadOfOpeningOrdinaryRuntime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if modelstub.HandleInputTokenCount(w, r, 1) {
			return
		}
		modelstub.WriteCompletedResponseStream(w, "unused", 1, 1)
	}))
	t.Cleanup(server.Close)
	fixture := newRetainedSelectedRunPromptFixture(t, server.URL, nil, nil)
	api := sessionruntime.NewAPI(fixture.metadata, nil, fixture.authority, sessionruntime.APIOptions{})
	initial, err := api.ActivateSessionRuntime(
		context.Background(),
		retainedRunPromptActivationRequest(t, fixture.sessionID, server.URL, "user_activation"),
	)
	if err != nil {
		t.Fatalf("open retained Session resource: %v", err)
	}
	t.Cleanup(func() {
		_, _ = api.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
			ClientRequestID: "release-expected-exact-resource",
			Attachment:      initial.Attachment,
			OwnerID:         "tui-owner",
			DropOwner:       true,
			ClosePolicy:     serverapi.SessionRuntimeReleaseClosePolicyDetachOnly,
		})
	})
	scope, err := fixture.store.TaskExecutionScope(context.Background(), fixture.taskID)
	if err != nil {
		t.Fatalf("TaskExecutionScope: %v", err)
	}
	lease, err := fixture.authority.NewWorkflowExecutionLease(sessionruntime.WorkflowExecutionRef{
		ProjectID:   scope.ProjectID,
		WorkflowID:  scope.WorkflowID,
		CurrentNode: fixture.currentNode,
	})
	if err != nil {
		t.Fatalf("NewWorkflowExecutionLease: %v", err)
	}
	lease.Release()
	descriptor, err := session.NewOpenSessionDescriptor(fixture.sessionID)
	if err != nil {
		t.Fatalf("NewOpenSessionDescriptor: %v", err)
	}
	execution, err := fixture.authority.StartAgentExecution(context.Background(), sessionruntime.AgentExecutionRequest{
		Descriptor: descriptor,
		Workflow:   &lease,
		Resource:   sessionruntime.CurrentAgentResource{},
		Runner: func(context.Context, sessionruntime.ExecutionScope, sessionruntime.AgentRuntimeBridge) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("start expected Workflow Exact: %v", err)
	}
	expectedScope := execution.Scope()
	expectedResource, ok := expectedScope.Resource()
	if !ok {
		t.Fatal("expected Workflow Exact has no Session Resource Generation")
	}
	if _, err := execution.Wait(context.Background()); err != nil {
		t.Fatalf("wait expected Workflow Exact retirement: %v", err)
	}

	request := retainedRunPromptActivationRequest(
		t,
		fixture.sessionID,
		server.URL,
		"user_activation",
	)
	request.OwnerID = "delayed-activation-owner"
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal retained activation: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode retained activation: %v", err)
	}
	expectedAttachment := map[string]any{
		"session_id":          fixture.sessionID.String(),
		"execution_scope_id":  expectedScope.ID().String(),
		"task_id":             string(fixture.currentNode.TaskID),
		"node_id":             string(fixture.currentNode.NodeID),
		"resource_generation": uint64(expectedResource.Generation()),
	}
	if branchKey, present := fixture.currentNode.TransitionBranchKey(); present {
		expectedAttachment["branch_key"] = string(branchKey)
	}
	payload["expected_exact_attachment"] = expectedAttachment
	encoded, err = json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal expected Exact activation: %v", err)
	}
	if err := json.Unmarshal(encoded, &request); err != nil {
		t.Fatalf("decode expected Exact activation: %v", err)
	}
	activation, err := api.ActivateSessionRuntime(context.Background(), request)
	if err == nil {
		_, _ = api.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
			ClientRequestID: "release-unexpected-replacement",
			Attachment:      activation.Attachment,
			OwnerID:         "tui-owner",
			DropOwner:       true,
			ClosePolicy:     serverapi.SessionRuntimeReleaseClosePolicyDetachOnly,
		})
		t.Fatalf(
			"retained activation expected Session %s, Exact %s, Current Node %v, Resource Generation %d but opened an ordinary replacement",
			fixture.sessionID,
			expectedScope.ID(),
			fixture.currentNode,
			expectedResource.Generation(),
		)
	}
}

func TestTaskResumeFirstMakesSelectedRetainedRunPromptFailIdleCheck(t *testing.T) {
	runner := &retainedRunPromptBlockingRunner{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(runner.release)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if modelstub.HandleInputTokenCount(w, r, 1) {
			return
		}
		modelstub.WriteCompletedResponseStream(w, "ordinary split", 1, 1)
	}))
	t.Cleanup(server.Close)
	fixture := newRetainedSelectedRunPromptFixture(t, server.URL, runner, nil)
	if _, err := fixture.controller.ResumeTask(context.Background(), fixture.taskID); err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}
	select {
	case <-runner.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("Task Resume did not enter pre-Exact preparation")
	}

	_, err := fixture.client.RunPrompt(context.Background(), serverapi.RunPromptRequest{
		ClientRequestID: "retained-runprompt-task-first",
		Intent:          serverapi.OpenExistingSessionLaunchIntent(fixture.sessionID),
		Prompt:          "continue",
	}, nil)
	if !errors.Is(err, ErrSessionRunning) {
		t.Fatalf("Task Resume-first RunPrompt error = %v, want ErrSessionRunning before planning/input", err)
	}
}

func TestFreshUserActivationRacingTaskResumeWaitsForTheWinningWorkflowExact(t *testing.T) {
	runner := &retainedRunPromptBlockingRunner{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(runner.release)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if modelstub.HandleInputTokenCount(w, r, 1) {
			return
		}
		modelstub.WriteCompletedResponseStream(w, "done", 1, 1)
	}))
	t.Cleanup(server.Close)
	fixture := newRetainedSelectedRunPromptFixture(t, server.URL, runner, nil)
	api := sessionruntime.NewAPI(fixture.metadata, nil, fixture.authority, sessionruntime.APIOptions{})
	activationDone := make(chan error, 1)
	resumeDone := make(chan error, 1)
	go func() {
		_, err := fixture.controller.ResumeTask(context.Background(), fixture.taskID)
		resumeDone <- err
	}()
	select {
	case err := <-resumeDone:
		if err != nil {
			t.Fatalf("Task Resume: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Task Resume did not return after queueing its winning Run")
	}
	select {
	case <-runner.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("Task Resume did not reach pre-Exact preparation")
	}
	go func() {
		_, err := api.ActivateSessionRuntime(
			context.Background(),
			retainedRunPromptActivationRequest(t, fixture.sessionID, server.URL, "user_activation"),
		)
		activationDone <- err
	}()

	select {
	case err := <-activationDone:
		t.Fatalf("fresh activation returned before the racing Task Resume registered its Exact: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
}

type retainedRunPromptLifecycleProbe struct {
	ready        chan io.Closer
	draining     chan struct{}
	drainingOnce sync.Once
}

func (p *retainedRunPromptLifecycleProbe) ResourceReady(
	_ context.Context,
	_ sessionruntime.AgentResourceDescriptor,
	_ *runtime.Engine,
	retain sessionruntime.AgentResourceRetainer,
) error {
	closer, err := retain()
	if err != nil {
		return err
	}
	p.ready <- closer
	return nil
}

func (p *retainedRunPromptLifecycleProbe) ResourceDraining(
	context.Context,
	sessionruntime.AgentResourceDescriptor,
) error {
	p.drainingOnce.Do(func() { close(p.draining) })
	return nil
}

func TestRetiringPredecessorMakesSelectedRetainedRunPromptFailIdleCheck(t *testing.T) {
	probe := &retainedRunPromptLifecycleProbe{
		ready:    make(chan io.Closer, 1),
		draining: make(chan struct{}),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if modelstub.HandleInputTokenCount(w, r, 1) {
			return
		}
		modelstub.WriteCompletedResponseStream(w, "ordinary split", 1, 1)
	}))
	t.Cleanup(server.Close)
	fixture := newRetainedSelectedRunPromptFixture(t, server.URL, nil, probe)
	api := sessionruntime.NewAPI(fixture.metadata, nil, fixture.authority, sessionruntime.APIOptions{})
	activation, err := api.ActivateSessionRuntime(
		context.Background(),
		retainedRunPromptActivationRequest(t, fixture.sessionID, server.URL, "user_activation"),
	)
	if err != nil {
		t.Fatalf("open predecessor Runtime: %v", err)
	}
	retention := <-probe.ready
	scope, err := fixture.store.TaskExecutionScope(context.Background(), fixture.taskID)
	if err != nil {
		t.Fatalf("TaskExecutionScope: %v", err)
	}
	lease, err := fixture.authority.NewWorkflowExecutionLease(sessionruntime.WorkflowExecutionRef{
		ProjectID:   scope.ProjectID,
		WorkflowID:  scope.WorkflowID,
		CurrentNode: fixture.currentNode,
	})
	if err != nil {
		t.Fatalf("NewWorkflowExecutionLease: %v", err)
	}
	lease.Release()
	descriptor, err := session.NewOpenSessionDescriptor(fixture.sessionID)
	if err != nil {
		t.Fatalf("NewOpenSessionDescriptor: %v", err)
	}
	predecessor, err := fixture.authority.StartAgentExecution(context.Background(), sessionruntime.AgentExecutionRequest{
		Descriptor: descriptor,
		Workflow:   &lease,
		Resource:   sessionruntime.CurrentAgentResource{},
		Runner: func(context.Context, sessionruntime.ExecutionScope, sessionruntime.AgentRuntimeBridge) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("start predecessor Exact: %v", err)
	}
	if _, err := predecessor.Wait(context.Background()); err != nil {
		t.Fatalf("wait predecessor Exact: %v", err)
	}
	resource, err := runtimeids.NewSessionResourceRef(
		fixture.sessionID,
		runtimeids.ResourceGeneration(activation.Attachment.Generation),
	)
	if err != nil {
		t.Fatalf("NewSessionResourceRef: %v", err)
	}
	releaseDone := make(chan error, 1)
	go func() {
		_, releaseErr := fixture.authority.ReleaseRuntime(context.Background(), sessionruntime.RuntimeReleaseRequest{
			Resource:  resource,
			OwnerID:   "tui-owner",
			DropOwner: true,
			Policy:    sessionruntime.RuntimeReleaseClose,
		})
		releaseDone <- releaseErr
	}()
	select {
	case <-probe.draining:
	case <-time.After(3 * time.Second):
		t.Fatal("predecessor resource did not enter retiring")
	}
	runDone := make(chan error, 1)
	runCtx, cancelRun := context.WithCancel(context.Background())
	go func() {
		_, runErr := fixture.client.RunPrompt(runCtx, serverapi.RunPromptRequest{
			ClientRequestID: "retained-runprompt-retiring-first",
			Intent:          serverapi.OpenExistingSessionLaunchIntent(fixture.sessionID),
			Prompt:          "continue",
		}, nil)
		runDone <- runErr
	}()
	t.Cleanup(func() {
		cancelRun()
		_ = retention.Close()
		select {
		case <-releaseDone:
		case <-time.After(3 * time.Second):
			t.Errorf("retiring predecessor release did not finish")
		}
		select {
		case <-runDone:
		case <-time.After(3 * time.Second):
			t.Errorf("retiring predecessor RunPrompt did not finish")
		}
	})

	select {
	case err := <-runDone:
		runDone <- err
		if !errors.Is(err, ErrSessionRunning) {
			t.Fatalf("retiring predecessor RunPrompt error = %v, want ErrSessionRunning", err)
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("selected retained RunPrompt waited behind a retiring predecessor instead of failing the idle check")
	}
}

func TestSelectedRetainedRunPromptOutcomeLessFinalizationDurablyInterruptsBeforeCleanup(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if modelstub.HandleInputTokenCount(w, r, 1) {
			return
		}
		close(started)
		<-release
		modelstub.WriteCompletedResponseStream(w, "done without task completion", 1, 1)
	}))
	t.Cleanup(server.Close)
	fixture := newRetainedSelectedRunPromptFixture(t, server.URL, nil, nil)
	if _, err := fixture.metadata.DB().ExecContext(
		context.Background(),
		`CREATE TRIGGER fail_retained_runprompt_outcome_less
BEFORE UPDATE OF scheduling_state ON task_current_nodes
WHEN OLD.scheduling_state = 'admitted' AND NEW.scheduling_state = 'interrupted'
BEGIN
    SELECT RAISE(ABORT, 'injected outcome-less interruption failure');
END`,
	); err != nil {
		t.Fatalf("install outcome-less persistence failure: %v", err)
	}
	runDone := make(chan error, 1)
	go func() {
		_, err := fixture.client.RunPrompt(context.Background(), serverapi.RunPromptRequest{
			ClientRequestID: "retained-runprompt-outcome-less",
			Intent:          serverapi.OpenExistingSessionLaunchIntent(fixture.sessionID),
			Prompt:          "continue",
		}, nil)
		runDone <- err
	}()
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		<-runDone
	})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("selected retained RunPrompt did not reach its model turn")
	}
	execution, live := fixture.authority.SessionExecution(fixture.sessionID)
	if !live {
		t.Fatal("selected retained RunPrompt reached its model turn without Exact authority")
	}
	workflowRef, workflowOwned := execution.Scope().Workflow()
	if !workflowOwned || !workflowRef.CurrentNode.Equal(fixture.currentNode) {
		t.Fatalf(
			"selected retained RunPrompt Exact = %+v, want Workflow-owned Current Node %v before outcome-less finalization",
			execution.Scope(),
			fixture.currentNode,
		)
	}
	releaseOnce.Do(func() { close(release) })
	<-runDone
	nodes, err := fixture.store.ListCurrentNodes(context.Background(), fixture.taskID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(nodes) != 1 ||
		nodes[0].Scheduling == nil ||
		nodes[0].Scheduling.State != workflow.CurrentNodeSchedulingAdmitted {
		t.Fatalf("outcome-less retained RunPrompt cleaned up before its failed durable disposition: %+v", nodes)
	}
	if _, err := fixture.controller.ObserveWorkflowTaskExecutions(
		[]workflow.TaskID{fixture.taskID},
	); err == nil {
		t.Fatal("outcome-less retained RunPrompt persistence failure still allowed lifecycle reads")
	}
	if _, live := fixture.authority.SessionExecution(fixture.sessionID); !live {
		t.Fatal("outcome-less retained RunPrompt persistence failure released Exact authority")
	}
}

func retainedRunPromptActivationRequest(
	t *testing.T,
	sessionID runtimeids.SessionID,
	providerURL string,
	operation string,
) serverapi.SessionRuntimeActivateRequest {
	t.Helper()
	request := serverapi.SessionRuntimeActivateRequest{
		ClientRequestID: "retained-runprompt-activation",
		SessionID:       sessionID.String(),
		OwnerID:         "tui-owner",
		ActiveSettings: config.Settings{
			Model:              "gpt-5",
			ModelContextWindow: 200000,
			OpenAIBaseURL:      providerURL,
			Reviewer:           config.ReviewerSettings{Frequency: "off"},
			Timeouts:           config.Timeouts{ModelRequestSeconds: 1},
			Shell:              config.ShellSettings{PostprocessingMode: config.ShellPostprocessingModeBuiltin},
		},
		Source: config.SourceReport{Sources: map[string]string{}},
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal activation request: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode activation request: %v", err)
	}
	payload["operation"] = operation
	encoded, err = json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal typed activation request: %v", err)
	}
	if err := json.Unmarshal(encoded, &request); err != nil {
		t.Fatalf("decode typed activation request: %v", err)
	}
	return request
}

func createRetainedRunPromptWorkflow(
	t *testing.T,
	store *workflowstore.Store,
) runtimeids.WorkflowID {
	t.Helper()
	ctx := context.Background()
	created, err := store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Retained RunPrompt"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	definition, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	var startID, terminalID workflow.NodeID
	for _, node := range definition.Nodes {
		switch node.Kind() {
		case workflow.NodeKindStart:
			startID = workflow.NodeIDOf(node)
		case workflow.NodeKindTerminal:
			terminalID = workflow.NodeIDOf(node)
		}
	}
	agentID := workflow.NodeID("node-agent-" + created.ID.String())
	if _, err := store.AddNode(ctx, workflowstore.NodeRecord{
		ID:           agentID,
		WorkflowID:   created.ID,
		Key:          "agent",
		Kind:         workflow.NodeKindAgent,
		DisplayName:  "Agent",
		SubagentRole: "coder",
	}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	startGroup := workflow.TransitionGroupID("group-start-" + created.ID.String())
	doneGroup := workflow.TransitionGroupID("group-done-" + created.ID.String())
	for _, group := range []workflowstore.TransitionGroupRecord{
		{ID: startGroup, WorkflowID: created.ID, SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"},
		{ID: doneGroup, WorkflowID: created.ID, SourceNodeID: agentID, TransitionID: "done", DisplayName: "Done"},
	} {
		if _, err := store.AddTransitionGroup(ctx, group); err != nil {
			t.Fatalf("AddTransitionGroup: %v", err)
		}
	}
	for _, edge := range []workflowstore.EdgeRecord{
		{
			ID:                workflow.EdgeID("edge-start-" + created.ID.String()),
			WorkflowID:        created.ID,
			TransitionGroupID: startGroup,
			Key:               "start",
			TargetNodeID:      agentID,
			ContextMode:       workflow.ContextModeNewSession,
			PromptTemplate:    "Continue the task.",
			AssigneeSelection: workflow.AssigneeSelectionConfigured,
			ThinkingSelection: workflow.ThinkingSelectionConfigured,
		},
		{
			ID:                workflow.EdgeID("edge-done-" + created.ID.String()),
			WorkflowID:        created.ID,
			TransitionGroupID: doneGroup,
			Key:               "done",
			TargetNodeID:      terminalID,
			ContextMode:       workflow.ContextModeNewSession,
			AssigneeSelection: workflow.AssigneeSelectionConfigured,
			ThinkingSelection: workflow.ThinkingSelectionConfigured,
		},
	} {
		if _, err := store.AddEdge(ctx, edge); err != nil {
			t.Fatalf("AddEdge: %v", err)
		}
	}
	return created.ID
}

func newSelectedRunPromptFixture(t *testing.T, providerURL string, history promptHistoryStore) selectedRunPromptFixture {
	t.Helper()
	root := t.TempDir()
	workspace := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	persistence := sessiontest.NewPersistence()
	store, err := session.Create(containerDir, "workspace-a", workspace, sessioncontract.SessionCategorySubagent, persistence.Options()...)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	authManager := auth.NewManager(auth.NewMemoryStore(auth.State{
		Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "test-key"}},
	}), nil, time.Now)
	cfg := config.App{
		WorkspaceRoot:   store.Meta().WorkspaceRoot,
		PersistenceRoot: root,
		Settings: config.Settings{
			Model:         "gpt-5",
			ThinkingLevel: "medium",
			OpenAIBaseURL: providerURL,
			EnabledTools:  map[toolspec.ID]bool{toolspec.ToolAskQuestion: true},
			Shell:         config.ShellSettings{PostprocessingMode: config.ShellPostprocessingModeBuiltin},
		},
	}
	authority := newTestHeadlessRuntimeAuthority(root, authManager, nil, persistence.Options()...)
	return selectedRunPromptFixture{
		store:     store,
		authority: authority,
		client: NewInProcessRunPromptClient(HeadlessBootstrap{
			SessionLaunch:    newTestHeadlessSessionLaunch(cfg, containerDir, authManager, authority, persistence),
			RuntimeAuthority: authority,
			PromptHistory:    history,
		}),
	}
}

func TestHeadlessSiblingWorkspacePatchUsesProjectBoundary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	root := t.TempDir()
	workspace := t.TempDir()
	sibling := t.TempDir()
	meta, err := metadata.Open(root)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	binding, err := meta.RegisterWorkspaceBinding(ctx, workspace)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	if _, err := meta.AttachWorkspaceToProject(ctx, binding.ProjectID, sibling); err != nil {
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}
	containerDir := filepath.Join(root, "projects", binding.ProjectID, "sessions")
	store, err := session.Create(containerDir, filepath.Base(containerDir), workspace, sessioncontract.SessionCategoryMain, meta.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}

	target := filepath.Join(sibling, "headless.txt")
	patchArgs, err := json.Marshal(map[string]any{"patch": strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: " + target,
		"+headless sibling",
		"*** End Patch",
		"",
	}, "\n")})
	if err != nil {
		t.Fatalf("marshal patch arguments: %v", err)
	}
	var providerCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if modelstub.HandleInputTokenCount(w, r, 1) {
			return
		}
		switch providerCalls.Add(1) {
		case 1:
			writeRunPromptFunctionCallResponse(w, "fc-sibling-patch", "call-sibling-patch", toolspec.ToolPatch, patchArgs)
		case 2:
			modelstub.WriteCompletedResponseStream(w, "done", 1, 1)
		default:
			t.Errorf("unexpected provider request %d", providerCalls.Load())
		}
	}))
	defer provider.Close()

	authManager := auth.NewManager(auth.NewMemoryStore(auth.State{
		Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "test-key"}},
	}), nil, time.Now)
	cfg := config.App{
		WorkspaceRoot:   workspace,
		PersistenceRoot: root,
		Settings: config.Settings{
			Model:         "gpt-5",
			OpenAIBaseURL: provider.URL,
			EnabledTools:  map[toolspec.ID]bool{toolspec.ToolPatch: true},
			Shell:         config.ShellSettings{PostprocessingMode: config.ShellPostprocessingModeBuiltin},
		},
	}
	authority := newTestHeadlessRuntimeAuthority(root, authManager, nil, meta.AuthoritativeSessionStoreOptions()...)
	client := NewInProcessRunPromptClient(HeadlessBootstrap{
		SessionLaunch: sessionlaunch.NewService(launch.Planner{
			Config:                   cfg,
			ContainerDir:             containerDir,
			StoreOptions:             meta.AuthoritativeSessionStoreOptions(),
			PersistedSessions:        meta,
			ProjectWorkspaceBoundary: meta,
		}).WithAuthStateReader(authManager).WithRuntimeAuthority(authority),
		RuntimeAuthority: authority,
	})
	sessionID := mustRunPromptSessionID(t, store.Meta().SessionID)
	response, err := client.RunPrompt(ctx, serverapi.RunPromptRequest{
		ClientRequestID: "headless-sibling-workspace",
		Intent:          serverapi.OpenExistingSessionLaunchIntent(sessionID),
		Prompt:          "write to the sibling Workspace",
	}, nil)
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if response.Result != "done" {
		t.Fatalf("RunPrompt result = %q, want done", response.Result)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "headless sibling\n" {
		t.Fatalf("sibling patch data = %q, error = %v", data, err)
	}
}

func TestHeadlessChildUsesInheritedExecutionTargetAfterWorktreeReminderWasConsumed(t *testing.T) {
	t.Setenv("KENT_REVIEWER_FREQUENCY", "off")
	ctx := context.Background()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	managedBase := filepath.Join(root, "worktrees")
	worktreeRoot := filepath.Join(managedBase, "task")
	worktreeSubdir := filepath.Join(worktreeRoot, "pkg")
	for _, dir := range []string{workspace, worktreeSubdir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", dir, err)
		}
	}
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
	parent, err := session.Create(
		containerDir,
		filepath.Base(containerDir),
		workspace,
		sessioncontract.SessionCategoryMain,
		meta.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create parent: %v", err)
	}
	if err := parent.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable parent: %v", err)
	}
	canonicalWorktreeRoot, err := config.CanonicalWorkspaceRoot(worktreeRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot worktree: %v", err)
	}
	canonicalWorktreeSubdir, err := config.CanonicalWorkspaceRoot(worktreeSubdir)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot worktree subdir: %v", err)
	}
	if err := meta.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
		ID:              "worktree-task",
		WorkspaceID:     binding.WorkspaceID,
		CanonicalRoot:   canonicalWorktreeRoot,
		DisplayName:     "task",
		Availability:    "available",
		Managed:         true,
		GitMetadataJSON: `{}`,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	if err := meta.UpdateSessionExecutionTarget(ctx, metadata.SessionExecutionTargetUpdate{
		SessionID:  parent.Meta().SessionID,
		Workspace:  &metadata.SessionExecutionTargetUpdateWorkspace{ID: binding.WorkspaceID},
		Worktree:   &metadata.SessionExecutionTargetUpdateWorktree{ID: "worktree-task"},
		CwdRelpath: "pkg",
	}); err != nil {
		t.Fatalf("UpdateSessionExecutionTarget parent: %v", err)
	}
	if err := parent.SetWorktreeReminderState(&session.WorktreeReminderState{
		Mode: session.WorktreeReminderModeEnter,
		WorktreeContext: session.WorktreeContext{
			Branch:        session.OptionalWorktreeBranch("task"),
			WorktreePath:  canonicalWorktreeRoot,
			WorkspaceRoot: workspace,
			EffectiveCwd:  canonicalWorktreeSubdir,
		},
	}); err != nil {
		t.Fatalf("SetWorktreeReminderState parent: %v", err)
	}
	if err := parent.SetWorktreeReminderState(nil); err != nil {
		t.Fatalf("consume parent worktree reminder: %v", err)
	}
	if parent.Meta().WorktreeReminder != nil {
		t.Fatalf("parent worktree reminder = %+v, want consumed reminder", parent.Meta().WorktreeReminder)
	}

	patchTarget := filepath.Join(canonicalWorktreeSubdir, "probe.txt")
	patchArgs, err := json.Marshal(map[string]any{"patch": strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: " + patchTarget,
		"+inherited target",
		"*** End Patch",
		"",
	}, "\n")})
	if err != nil {
		t.Fatalf("marshal patch arguments: %v", err)
	}
	patchOutput := make(chan string, 1)
	var providerCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if modelstub.HandleInputTokenCount(w, r, 1) {
			return
		}
		switch providerCalls.Add(1) {
		case 1:
			writeRunPromptFunctionCallResponse(w, "fc-patch-target", "call-patch-target", toolspec.ToolPatch, patchArgs)
		case 2:
			defer r.Body.Close()
			var payload map[string]any
			if decodeErr := json.NewDecoder(r.Body).Decode(&payload); decodeErr != nil {
				t.Errorf("decode patch follow-up request: %v", decodeErr)
			}
			output, ok := findRunPromptFunctionCallOutput(payload)
			if !ok {
				t.Errorf("patch follow-up request has no function_call_output")
			} else {
				patchOutput <- output
			}
			modelstub.WriteCompletedResponseStream(w, "done", 1, 1)
		default:
			t.Errorf("unexpected provider request %d", providerCalls.Load())
		}
	}))
	defer provider.Close()

	authManager := auth.NewManager(auth.NewMemoryStore(auth.State{
		Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "test-key"}},
	}), nil, time.Now)
	cfg := config.App{
		WorkspaceRoot:   workspace,
		PersistenceRoot: root,
		Settings: config.Settings{
			Model:               "gpt-5",
			ThinkingLevel:       "medium",
			OpenAIBaseURL:       provider.URL,
			EnabledTools:        map[toolspec.ID]bool{toolspec.ToolPatch: true},
			AllowNonCwdEdits:    true,
			MaxSubagentDepth:    2,
			Worktrees:           config.WorktreeSettings{BaseDir: managedBase},
			ShellOutputMaxChars: 16_000,
			Shell:               config.ShellSettings{PostprocessingMode: config.ShellPostprocessingModeBuiltin},
		},
	}
	authority := newTestHeadlessRuntimeAuthority(root, authManager, nil, meta.AuthoritativeSessionStoreOptions()...)
	client := NewInProcessRunPromptClient(HeadlessBootstrap{
		SessionLaunch: sessionlaunch.NewService(launch.Planner{
			Config:                   cfg,
			ContainerDir:             containerDir,
			StoreOptions:             meta.AuthoritativeSessionStoreOptions(),
			PersistedSessions:        meta,
			ProjectWorkspaceBoundary: meta,
		}).WithAuthStateReader(authManager).WithRuntimeAuthority(authority),
		RuntimeAuthority:       authority,
		PromptHistory:          meta,
		ManagedWorktreeBaseDir: managedBase,
	})
	parentID := parent.Meta().SessionID
	response, err := client.RunPrompt(ctx, serverapi.RunPromptRequest{
		ClientRequestID: "inherited-target-without-reminder",
		Intent:          serverapi.CreateNewSessionLaunchIntent(serverapi.ParentAgentSessionCreateOrigin(mustRunPromptSessionID(t, parentID))),
		CallerSessionID: &parentID,
		Prompt:          "verify the inherited execution target",
	}, nil)
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if response.Result != "done" {
		t.Fatalf("RunPrompt result = %q, want done", response.Result)
	}
	select {
	case output := <-patchOutput:
		var result map[string]json.RawMessage
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatalf("decode patch output %q: %v", output, err)
		}
		if warning, exists := result["warning"]; exists {
			t.Fatalf("patch inside inherited current worktree emitted warning %s", warning)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for patch output")
	}
	if data, err := os.ReadFile(patchTarget); err != nil || string(data) != "inherited target\n" {
		t.Fatalf("patch target data = %q, error = %v", data, err)
	}

	child, err := session.OpenByID(root, response.SessionID, meta.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("OpenByID child: %v", err)
	}
	if child.Meta().WorktreeReminder != nil {
		t.Fatalf("child worktree reminder = %+v, want no reminder", child.Meta().WorktreeReminder)
	}
	target, err := meta.ResolveSessionExecutionTarget(ctx, response.SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget child: %v", err)
	}
	if target.Worktree == nil || target.Worktree.ID != "worktree-task" || target.EffectiveWorkdir != canonicalWorktreeSubdir {
		t.Fatalf("child execution target = %+v, want worktree-task at %q", target, canonicalWorktreeSubdir)
	}
	records, err := sessiontest.CollectRecords(child)
	if err != nil {
		t.Fatalf("CollectRecords child: %v", err)
	}
	wantCWD := "\nCWD: " + canonicalWorktreeSubdir + "\n"
	for _, record := range records {
		payload, payloadErr := record.Payload()
		if payloadErr != nil {
			t.Fatalf("read child event payload: %v", payloadErr)
		}
		message, ok := payload.(session.MessageRecord)
		if ok &&
			message.MessageType != nil &&
			*message.MessageType == session.MessageTypeEnvironment &&
			message.Content != nil &&
			strings.Contains(*message.Content, wantCWD) {
			return
		}
	}
	t.Fatalf("child environment did not use inherited CWD %q", canonicalWorktreeSubdir)
}

func TestWorkflowCallerDeniedTargetLeavesNoHeadlessLaunchArtifacts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(config.PersistenceRootEnvName, home)
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
	parent, err := session.Create(containerDir, filepath.Base(containerDir), workspace, sessioncontract.SessionCategoryMain, meta.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create parent: %v", err)
	}
	if err := parent.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable parent: %v", err)
	}
	testsetup.BindSessionToWorkflowTask(t, meta, binding.ProjectID, parent.Meta().SessionID)
	ordinaryCaller, err := session.Create(containerDir, filepath.Base(containerDir), workspace, sessioncontract.SessionCategoryMain, meta.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create ordinary caller: %v", err)
	}
	callerRole := "caller"
	if err := ordinaryCaller.SetContinuationContext(session.ContinuationContext{AgentRole: &callerRole}); err != nil {
		t.Fatalf("SetContinuationContext ordinary caller: %v", err)
	}
	if err := ordinaryCaller.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable ordinary caller: %v", err)
	}

	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.PersistenceRoot = root
	cfg.Settings.Model = "gpt-5.6-sol"
	cfg.Settings.Workflow = config.WorkflowSettings{Subagents: false}
	hiddenSettings := cfg.Settings
	cfg.Settings.Subagents = map[string]config.SubagentRole{
		"caller": {
			Settings:         hiddenSettings,
			Sources:          map[string]string{"thinking_level": "file"},
			AgentCallableSet: true,
		},
		"hidden": {
			Settings:         hiddenSettings,
			Sources:          map[string]string{"thinking_level": "file"},
			AgentCallable:    true,
			AgentCallableSet: true,
		},
		"blocked": {
			Settings:         hiddenSettings,
			Sources:          map[string]string{"thinking_level": "file"},
			AgentCallableSet: true,
		},
	}
	worktreeRoot := filepath.Join(root, "worktrees")
	if err := os.MkdirAll(worktreeRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll worktree root: %v", err)
	}
	worktreeRoot, err = config.CanonicalWorkspaceRoot(worktreeRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot worktree root: %v", err)
	}
	if err := meta.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
		ID:              "workflow-parent-worktree",
		WorkspaceID:     binding.WorkspaceID,
		CanonicalRoot:   worktreeRoot,
		DisplayName:     filepath.Base(worktreeRoot),
		Availability:    "available",
		Managed:         true,
		GitMetadataJSON: `{}`,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	authority := newTestHeadlessRuntimeAuthority(root, nil, nil, meta.AuthoritativeSessionStoreOptions()...)
	sessionLauncher := sessionlaunch.NewService(launch.Planner{
		Config:                   cfg,
		ContainerDir:             containerDir,
		StoreOptions:             meta.AuthoritativeSessionStoreOptions(),
		PersistedSessions:        meta,
		ProjectWorkspaceBoundary: meta,
	}).WithRuntimeAuthority(authority)
	client := NewInProcessRunPromptClient(HeadlessBootstrap{
		SessionLaunch:    sessionLauncher,
		RuntimeAuthority: authority,
	})
	before := snapshotHeadlessLaunchArtifacts(t, ctx, meta, binding.ProjectID, binding.WorkspaceID, containerDir, root, worktreeRoot)
	role := "hidden"
	parentID := parent.Meta().SessionID
	_, err = client.RunPrompt(ctx, serverapi.RunPromptRequest{
		ClientRequestID: "workflow-denial-1",
		Intent:          serverapi.CreateNewSessionLaunchIntent(serverapi.ParentAgentSessionCreateOrigin(mustRunPromptSessionID(t, parentID))),
		CallerSessionID: &parentID,
		Prompt:          "delegate this",
		Overrides:       serverapi.RunPromptOverrides{AgentRole: &role},
	}, nil)
	var denied *serverapi.SubagentLaunchDeniedError
	if !errors.As(err, &denied) || denied.Kind != serverapi.SubagentLaunchDenialNotCallable {
		t.Fatalf("RunPrompt error = %T %v, want workflow policy denial", err, err)
	}
	after := snapshotHeadlessLaunchArtifacts(t, ctx, meta, binding.ProjectID, binding.WorkspaceID, containerDir, root, worktreeRoot)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("denied new-child launch changed artifacts: before=%+v after=%+v", before, after)
	}
	ordinaryCallerID := ordinaryCaller.Meta().SessionID
	blockedRole := "blocked"
	beforeOrdinaryTargetDenial := snapshotHeadlessLaunchArtifacts(t, ctx, meta, binding.ProjectID, binding.WorkspaceID, containerDir, root, worktreeRoot)
	_, err = client.RunPrompt(ctx, serverapi.RunPromptRequest{
		ClientRequestID: "ordinary-blocked-target-denial",
		Intent:          serverapi.CreateNewSessionLaunchIntent(serverapi.ParentAgentSessionCreateOrigin(mustRunPromptSessionID(t, ordinaryCallerID))),
		CallerSessionID: &ordinaryCallerID,
		Prompt:          "delegate this",
		Overrides:       serverapi.RunPromptOverrides{AgentRole: &blockedRole},
	}, nil)
	if !errors.As(err, &denied) || denied.Kind != serverapi.SubagentLaunchDenialNotCallable {
		t.Fatalf("ordinary blocked-target error = %T %v, want not-callable denial", err, err)
	}
	if afterOrdinaryTargetDenial := snapshotHeadlessLaunchArtifacts(t, ctx, meta, binding.ProjectID, binding.WorkspaceID, containerDir, root, worktreeRoot); !reflect.DeepEqual(afterOrdinaryTargetDenial, beforeOrdinaryTargetDenial) {
		t.Fatalf("ordinary blocked-target denial changed artifacts: before=%+v after=%+v", beforeOrdinaryTargetDenial, afterOrdinaryTargetDenial)
	}
	selected, err := session.Create(containerDir, filepath.Base(containerDir), workspace, sessioncontract.SessionCategoryMain, meta.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create selected: %v", err)
	}
	if err := selected.SetName("selected session"); err != nil {
		t.Fatalf("SetName selected: %v", err)
	}
	if err := selected.SetContinuationContext(session.ContinuationContext{AgentRole: &role}); err != nil {
		t.Fatalf("SetContinuationContext selected: %v", err)
	}
	selectedBefore := selected.Meta()
	selectedEventLog, err := selected.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize selected event log: %v", err)
	}
	selectedRevisionBefore, err := selectedEventLog.Revision()
	if err != nil {
		t.Fatalf("read selected event log revision: %v", err)
	}
	beforeSelectedDenial := snapshotHeadlessLaunchArtifacts(t, ctx, meta, binding.ProjectID, binding.WorkspaceID, containerDir, root, worktreeRoot)
	_, err = client.RunPrompt(ctx, serverapi.RunPromptRequest{
		ClientRequestID: "workflow-selected-denial-1",
		Intent:          serverapi.OpenExistingSessionLaunchIntent(mustRunPromptSessionID(t, selectedBefore.SessionID)),
		CallerSessionID: &parentID,
		Prompt:          "continue selected",
	}, nil)
	if !errors.As(err, &denied) || denied.Kind != serverapi.SubagentLaunchDenialNotCallable {
		t.Fatalf("selected RunPrompt error = %T %v, want workflow policy denial", err, err)
	}
	reopenedSelected, err := session.OpenByID(root, selectedBefore.SessionID, meta.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("OpenByID selected: %v", err)
	}
	reopenedEventLog, err := reopenedSelected.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize reopened selected event log: %v", err)
	}
	reopenedRevision, err := reopenedEventLog.Revision()
	if err != nil {
		t.Fatalf("read reopened selected event log revision: %v", err)
	}
	if got := reopenedSelected.Meta(); got.Name != selectedBefore.Name ||
		!reflect.DeepEqual(got.PreviousSessionID, selectedBefore.PreviousSessionID) ||
		!reflect.DeepEqual(got.ParentAgentSessionID, selectedBefore.ParentAgentSessionID) ||
		got.Continuation == nil ||
		got.Continuation.AgentRole == nil ||
		*got.Continuation.AgentRole != role ||
		got.ModelRequestCount != selectedBefore.ModelRequestCount ||
		reopenedRevision != selectedRevisionBefore {
		t.Fatalf("selected session changed on denied launch: before=%+v after=%+v", selectedBefore, got)
	}
	afterSelectedDenial := snapshotHeadlessLaunchArtifacts(t, ctx, meta, binding.ProjectID, binding.WorkspaceID, containerDir, root, worktreeRoot)
	if !reflect.DeepEqual(afterSelectedDenial, beforeSelectedDenial) {
		t.Fatalf("denied selected-session launch changed artifacts: before=%+v after=%+v", beforeSelectedDenial, afterSelectedDenial)
	}
	substitutedCaller := selectedBefore.SessionID
	substitutedCallerID := mustRunPromptSessionID(t, substitutedCaller)
	substitutedPlan, err := sessionLauncher.PlanLaunchSession(ctx, serverapi.SessionPlanRequest{
		ClientRequestID: "substituted-ordinary-caller",
		Mode:            serverapi.SessionLaunchModeHeadless,
		Intent:          serverapi.CreateNewSessionLaunchIntent(serverapi.ParentAgentSessionCreateOrigin(substitutedCallerID)),
		CallerSessionID: &substitutedCaller,
		Overrides:       serverapi.RunPromptOverrides{AgentRole: &role},
	})
	if err != nil {
		t.Fatalf("substituted ordinary caller PlanLaunchSession: %v", err)
	}
	substitutedStore, err := session.MaterializeSessionDescriptor(root, substitutedPlan.Plan.Descriptor, meta.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("materialize substituted caller plan: %v", err)
	}
	if got := substitutedStore.Meta().ParentAgentSessionID; got == nil || *got != substitutedCallerID {
		t.Fatalf("substituted caller child parent-agent = %v, want %q", got, substitutedCaller)
	}
	if got := substitutedStore.Meta().Continuation; got == nil || got.AgentRole == nil || *got.AgentRole != role {
		t.Fatalf("substituted caller continuation = %+v, want hidden role", got)
	}
}

func TestWorkflowCallerLaunchesDefaultAndCustomHeadlessSubagents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(config.PersistenceRootEnvName, home)
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
	parent, err := session.Create(containerDir, filepath.Base(containerDir), workspace, sessioncontract.SessionCategoryMain, meta.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create parent: %v", err)
	}
	if err := parent.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable parent: %v", err)
	}
	testsetup.BindSessionToWorkflowTask(t, meta, binding.ProjectID, parent.Meta().SessionID)
	parentID := parent.Meta().SessionID
	ordinaryParent, err := session.Create(containerDir, filepath.Base(containerDir), workspace, sessioncontract.SessionCategoryMain, meta.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create ordinary parent: %v", err)
	}
	currentRole := "current"
	if err := ordinaryParent.SetContinuationContext(session.ContinuationContext{AgentRole: &currentRole}); err != nil {
		t.Fatalf("SetContinuationContext ordinary parent: %v", err)
	}
	ordinaryParentID := ordinaryParent.Meta().SessionID

	var responseCount int
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if modelstub.HandleInputTokenCount(w, r, 1) {
			return
		}
		responseCount++
		modelstub.WriteCompletedResponseStream(w, "workflow response", 1, 1)
	}))
	defer provider.Close()
	authManager := auth.NewManager(auth.NewMemoryStore(auth.State{
		Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "test-key"}},
	}), nil, time.Now)
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.PersistenceRoot = root
	cfg.Settings.Model = "gpt-5.6-sol"
	cfg.Settings.OpenAIBaseURL = provider.URL
	cfg.Settings.Workflow = config.WorkflowSettings{Subagents: true}
	workerSettings := cfg.Settings
	cfg.Settings.Subagents = map[string]config.SubagentRole{
		"current": {
			Settings:         workerSettings,
			Sources:          map[string]string{"model": "file"},
			AgentCallableSet: true,
		},
		"worker": {
			Settings:         workerSettings,
			Sources:          map[string]string{"model": "file"},
			AgentCallable:    true,
			AgentCallableSet: true,
		},
	}
	authority := newTestHeadlessRuntimeAuthority(root, authManager, nil, meta.AuthoritativeSessionStoreOptions()...)
	client := NewInProcessRunPromptClient(HeadlessBootstrap{
		SessionLaunch: sessionlaunch.NewService(launch.Planner{
			Config:                   cfg,
			ContainerDir:             containerDir,
			StoreOptions:             meta.AuthoritativeSessionStoreOptions(),
			PersistedSessions:        meta,
			ProjectWorkspaceBoundary: meta,
		}).WithAuthStateReader(authManager).WithRuntimeAuthority(authority),
		RuntimeAuthority: authority,
		PromptHistory:    meta,
	})

	worker := "worker"
	var sessionStarted int
	response, err := client.RunPrompt(ctx, serverapi.RunPromptRequest{
		ClientRequestID: "workflow-allowed-custom",
		Intent:          serverapi.CreateNewSessionLaunchIntent(serverapi.ParentAgentSessionCreateOrigin(mustRunPromptSessionID(t, parentID))),
		CallerSessionID: &parentID,
		Prompt:          "delegate this",
		Overrides:       serverapi.RunPromptOverrides{AgentRole: &worker},
	}, serverapi.RunPromptProgressFunc(func(progress serverapi.RunPromptProgress) {
		if progress.Kind != serverapi.RunPromptProgressKindSessionStarted {
			return
		}
		sessionStarted++
		if progress.SessionStarted == nil {
			t.Errorf("session_started published before the new runtime became active: %+v", progress)
			return
		}
		sessionID, parseErr := runtimeids.ParseSessionID(progress.SessionStarted.SessionID.String())
		if parseErr != nil {
			t.Errorf("session_started session id: %v", parseErr)
			return
		}
		if _, active := authority.SessionExecution(sessionID); !active {
			t.Errorf("session_started published before the new execution became active: %+v", progress)
		}
	}))
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if response.Result != "workflow response" {
		t.Fatalf("result = %q, want provider response", response.Result)
	}
	if sessionStarted != 1 {
		t.Fatalf("session_started events = %d, want 1", sessionStarted)
	}
	child, err := session.OpenByID(root, response.SessionID, meta.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("OpenByID child: %v", err)
	}
	childMeta := child.Meta()
	parentSessionID := mustRunPromptSessionID(t, parentID)
	if childMeta.ParentAgentSessionID == nil || *childMeta.ParentAgentSessionID != parentSessionID {
		t.Fatalf("parent-agent id = %v, want %q", childMeta.ParentAgentSessionID, parentID)
	}
	if got := childMeta.Continuation; got == nil || got.AgentRole == nil || *got.AgentRole != worker {
		t.Fatalf("continuation = %+v, want worker role", got)
	}
	responseID, err := runtimeids.ParseSessionID(response.SessionID)
	if err != nil {
		t.Fatalf("parse response session id: %v", err)
	}
	if _, active := authority.SessionExecution(responseID); active {
		t.Fatalf("completed headless launch left runtime active for %q", response.SessionID)
	}

	fast := config.BuiltInSubagentRoleFast
	ordinaryTests := []struct {
		name string
		role string
	}{
		{name: "fast", role: fast},
		{name: "custom", role: worker},
	}
	for _, test := range ordinaryTests {
		t.Run("ordinary non-callable caller "+test.name, func(t *testing.T) {
			response, err := client.RunPrompt(ctx, serverapi.RunPromptRequest{
				ClientRequestID: "ordinary-non-callable-" + test.name,
				Intent:          serverapi.CreateNewSessionLaunchIntent(serverapi.ParentAgentSessionCreateOrigin(mustRunPromptSessionID(t, ordinaryParentID))),
				CallerSessionID: &ordinaryParentID,
				Prompt:          "delegate this",
				Overrides:       serverapi.RunPromptOverrides{AgentRole: &test.role},
			}, nil)
			if err != nil {
				t.Fatalf("RunPrompt: %v", err)
			}
			if response.Result != "workflow response" {
				t.Fatalf("result = %q, want provider response", response.Result)
			}
			child, err := session.OpenByID(root, response.SessionID, meta.AuthoritativeSessionStoreOptions()...)
			if err != nil {
				t.Fatalf("OpenByID child: %v", err)
			}
			childMeta := child.Meta()
			ordinaryParentSessionID := mustRunPromptSessionID(t, ordinaryParentID)
			if childMeta.ParentAgentSessionID == nil || *childMeta.ParentAgentSessionID != ordinaryParentSessionID {
				t.Fatalf("parent-agent id = %v, want %q", childMeta.ParentAgentSessionID, ordinaryParentID)
			}
			if childMeta.Continuation == nil || childMeta.Continuation.AgentRole == nil || *childMeta.Continuation.AgentRole != test.role {
				t.Fatalf("continuation = %+v, want role %q", childMeta.Continuation, test.role)
			}
			responseID, err := runtimeids.ParseSessionID(response.SessionID)
			if err != nil {
				t.Fatalf("parse response session id: %v", err)
			}
			if _, active := authority.SessionExecution(responseID); active {
				t.Fatalf("completed headless launch left runtime active for %q", response.SessionID)
			}
		})
	}
	if responseCount != 3 {
		t.Fatalf("provider responses = %d, want 3", responseCount)
	}
}

func TestInProcessRunPromptClientUsesSelectedSessionContinuationContext(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	persistence := sessiontest.NewPersistence()
	store, err := session.Create(containerDir, "workspace-a", workspace, sessioncontract.SessionCategorySubagent, persistence.Options()...)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}

	var providerCalls atomic.Int32
	concurrentStarted := make(chan struct{})
	concurrentRelease := make(chan struct{})
	var concurrentReleaseOnce sync.Once
	releaseConcurrent := func() {
		concurrentReleaseOnce.Do(func() { close(concurrentRelease) })
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if modelstub.HandleInputTokenCount(w, r, 1) {
			return
		}
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got == "" {
			t.Fatal("expected authorization header")
		}
		call := providerCalls.Add(1)
		if call == 2 {
			close(concurrentStarted)
			<-concurrentRelease
			modelstub.WriteCompletedResponseStream(w, "concurrent replay", 1, 1)
			return
		}
		modelstub.WriteCompletedResponseStream(w, "from persisted continuation", 1, 1)
	}))
	defer func() {
		releaseConcurrent()
		server.Close()
	}()

	if err := store.SetContinuationContext(session.ContinuationContext{OpenAIBaseURL: server.URL}); err != nil {
		t.Fatalf("set continuation context: %v", err)
	}

	authManager := auth.NewManager(auth.NewMemoryStore(auth.State{
		Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "test-key"}},
	}), nil, time.Now)

	cfg := config.App{
		WorkspaceRoot:   workspace,
		PersistenceRoot: root,
		Settings: config.Settings{
			Model:         "gpt-5",
			ThinkingLevel: "medium",
			OpenAIBaseURL: "http://wrong.invalid",
			Shell:         config.ShellSettings{PostprocessingMode: config.ShellPostprocessingModeBuiltin},
		},
	}
	history := &recordingPromptHistoryStore{}
	authority := newTestHeadlessRuntimeAuthority(root, authManager, nil, persistence.Options()...)
	client := NewInProcessRunPromptClient(HeadlessBootstrap{
		SessionLaunch:    newTestHeadlessSessionLaunch(cfg, containerDir, authManager, authority, persistence),
		RuntimeAuthority: authority,
		PromptHistory:    history,
	})

	var progresses []serverapi.RunPromptProgress
	request := serverapi.RunPromptRequest{
		ClientRequestID: "  continuation-direct-1  ",
		Intent:          serverapi.OpenExistingSessionLaunchIntent(mustRunPromptSessionID(t, store.Meta().SessionID)),
		Prompt:          "  hello  ",
	}
	response, err := client.RunPrompt(context.Background(), request, serverapi.RunPromptProgressFunc(func(progress serverapi.RunPromptProgress) {
		progresses = append(progresses, progress)
	}))
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if response.SessionID != store.Meta().SessionID {
		t.Fatalf("session id = %q, want %q", response.SessionID, store.Meta().SessionID)
	}
	if response.Result != "from persisted continuation" {
		t.Fatalf("result = %q, want from persisted continuation", response.Result)
	}
	for _, progress := range progresses {
		if progress.Kind == serverapi.RunPromptProgressKindSessionStarted {
			t.Fatalf("continued run announced a new session: %+v", progress)
		}
	}
	if got := store.Meta().Continuation; got == nil || got.OpenAIBaseURL != server.URL {
		t.Fatalf("expected persisted continuation preserved, got %+v", got)
	}
	replayed, err := client.RunPrompt(context.Background(), request, nil)
	if err != nil {
		t.Fatalf("replayed RunPrompt: %v", err)
	}
	if replayed.SessionID != response.SessionID || replayed.Result != response.Result || providerCalls.Load() != 1 {
		t.Fatalf("replayed response=%+v provider calls=%d, want memoized %+v/1", replayed, providerCalls.Load(), response)
	}
	if len(history.entries) != 1 || history.entries[0].SourceID != "continuation-direct-1" || history.entries[0].Text != "hello" {
		t.Fatalf("prompt history = %+v, want one normalized entry", history.entries)
	}
	mismatch := request
	mismatch.Prompt = "different"
	if _, err := client.RunPrompt(context.Background(), mismatch, nil); !errors.Is(err, requestmemo.ErrClientRequestIDReused) {
		t.Fatalf("mismatched replay error = %v, want request-id conflict", err)
	}

	concurrent := request
	concurrent.ClientRequestID = "concurrent-request"
	concurrent.Prompt = "concurrent"
	results := make(chan serverapi.RunPromptResponse, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			got, err := client.RunPrompt(context.Background(), concurrent, nil)
			results <- got
			errs <- err
		}()
	}
	select {
	case <-concurrentStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for concurrent provider request")
	}
	releaseConcurrent()
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent replay: %v", err)
		}
		if got := <-results; got.Result != "concurrent replay" {
			t.Fatalf("concurrent response = %+v", got)
		}
	}
	if providerCalls.Load() != 2 {
		t.Fatalf("provider calls = %d, want one initial and one concurrent call", providerCalls.Load())
	}
}

func TestInProcessRunPromptTimeoutCoversHistoryAndRunCleanup(t *testing.T) {
	t.Run("prompt history", func(t *testing.T) {
		providerCalls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if modelstub.HandleInputTokenCount(w, r, 1) {
				return
			}
			providerCalls++
			modelstub.WriteCompletedResponseStream(w, "unexpected", 1, 1)
		}))
		defer server.Close()

		fixture := newSelectedRunPromptFixture(t, server.URL, &blockingPromptHistoryStore{})
		_, err := fixture.client.RunPrompt(context.Background(), serverapi.RunPromptRequest{
			ClientRequestID: "history-timeout",
			Intent:          serverapi.OpenExistingSessionLaunchIntent(mustRunPromptSessionID(t, fixture.store.Meta().SessionID)),
			Prompt:          "hello",
			Timeout:         100 * time.Millisecond,
		}, nil)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("RunPrompt error = %v, want deadline exceeded", err)
		}
		sessionID := mustRunPromptSessionID(t, fixture.store.Meta().SessionID)
		_, active := fixture.authority.SessionExecution(sessionID)
		if providerCalls != 0 || active {
			t.Fatalf("provider calls=%d runtime active=%t, want 0/false", providerCalls, active)
		}
	})

	t.Run("runtime", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		var startedOnce sync.Once
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if modelstub.HandleInputTokenCount(w, r, 1) {
				return
			}
			startedOnce.Do(func() { close(started) })
			<-release
		}))
		defer func() {
			close(release)
			server.Close()
		}()

		fixture := newSelectedRunPromptFixture(t, server.URL, nil)
		type result struct {
			response serverapi.RunPromptResponse
			err      error
		}
		done := make(chan result, 1)
		go func() {
			response, err := fixture.client.RunPrompt(context.Background(), serverapi.RunPromptRequest{
				ClientRequestID: "run-timeout",
				Intent:          serverapi.OpenExistingSessionLaunchIntent(mustRunPromptSessionID(t, fixture.store.Meta().SessionID)),
				Prompt:          "hello",
				Timeout:         5 * time.Second,
			}, nil)
			done <- result{response: response, err: err}
		}()

		select {
		case <-started:
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for provider request")
		}
		select {
		case got := <-done:
			if !errors.Is(got.err, context.DeadlineExceeded) {
				t.Fatalf("RunPrompt error = %v, want deadline exceeded", got.err)
			}
			if got.response.SessionID != fixture.store.Meta().SessionID {
				t.Fatalf("partial response session = %q, want %q", got.response.SessionID, fixture.store.Meta().SessionID)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("RunPrompt did not finish after timeout")
		}
		sessionID := mustRunPromptSessionID(t, fixture.store.Meta().SessionID)
		if _, active := fixture.authority.SessionExecution(sessionID); active {
			t.Fatal("timed out headless execution remained active")
		}
	})
}

func TestInProcessRunPromptPublishesCommentaryBeforeHeadlessAskFollowupFails(t *testing.T) {
	var calls atomic.Int32
	var sawAskResult atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if modelstub.HandleInputTokenCount(w, r, 1) {
			return
		}
		switch calls.Add(1) {
		case 1:
			writeRunPromptAskQuestionResponse(w, "partial progress")
		case 2:
			defer r.Body.Close()
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode ask follow-up request: %v", err)
			}
			if _, ok := findRunPromptFunctionCallOutput(payload); !ok {
				t.Errorf("ask follow-up request has no function_call_output")
			} else {
				sawAskResult.Store(true)
			}
			writeSSEJSON(w, map[string]any{
				"type": "response.incomplete",
				"response": map[string]any{
					"incomplete_details": map[string]any{"reason": "max_output_tokens"},
					"output":             []any{},
				},
			})
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		default:
			t.Errorf("unexpected provider request %d", calls.Load())
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	fixture := newSelectedRunPromptFixture(t, server.URL, nil)
	var progresses []serverapi.RunPromptProgress
	response, err := fixture.client.RunPrompt(context.Background(), serverapi.RunPromptRequest{
		ClientRequestID: "ask-followup-failure",
		Intent:          serverapi.OpenExistingSessionLaunchIntent(mustRunPromptSessionID(t, fixture.store.Meta().SessionID)),
		Prompt:          "ask before failing",
	}, serverapi.RunPromptProgressFunc(func(progress serverapi.RunPromptProgress) {
		progresses = append(progresses, progress)
	}))
	if err == nil || !llm.IsNonRetriableModelError(err) {
		t.Fatalf("RunPrompt error = %v, want non-retriable provider failure", err)
	}
	if response.SessionID != fixture.store.Meta().SessionID || calls.Load() != 2 || !sawAskResult.Load() {
		t.Fatalf("response=%+v calls=%d saw ask result=%t", response, calls.Load(), sawAskResult.Load())
	}
	foundCommentary := false
	for _, progress := range progresses {
		if progress.AssistantMessage != nil &&
			progress.AssistantMessage.Phase == clientui.MessagePhaseCommentary &&
			progress.AssistantMessage.Content == "partial progress" {
			foundCommentary = true
		}
	}
	sessionID := mustRunPromptSessionID(t, fixture.store.Meta().SessionID)
	_, active := fixture.authority.SessionExecution(sessionID)
	if !foundCommentary || active {
		t.Fatalf("commentary found=%t runtime active=%t", foundCommentary, active)
	}
}

func TestInProcessRunPromptClientUsesActiveShellPostprocessorWithSuppliedBackgroundManager(t *testing.T) {
	root := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	persistence := sessiontest.NewPersistence()
	store, err := session.Create(containerDir, "workspace-a", t.TempDir(), sessioncontract.SessionCategorySubagent, persistence.Options()...)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}

	bootstrapHook := writeRunPromptHook(t, `printf '{"processed":true,"replaced_output":"BOOTSTRAP"}'`)
	effectiveHook := writeRunPromptHook(t, fmt.Sprintf(
		`printf '{"processed":true,"replaced_output":"EFFECTIVE:%%s"}' "$%s"`,
		sessionenv.SessionIDEnv,
	))
	bootstrapRunner := mustRunPromptPostprocessor(t, config.ShellPostprocessingModeUser, &bootstrapHook)
	background, err := shelltool.NewManager(shelltool.WithPostprocessor(bootstrapRunner))
	if err != nil {
		t.Fatalf("new supplied background manager: %v", err)
	}
	t.Cleanup(func() { _ = background.Close() })

	toolOutput := make(chan string, 1)
	var responseCount int
	var responseMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if modelstub.HandleInputTokenCount(w, r, 1) {
			return
		}
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}

		responseMu.Lock()
		responseCount++
		currentResponse := responseCount
		responseMu.Unlock()

		switch currentResponse {
		case 1:
			args, marshalErr := json.Marshal(map[string]any{
				"cmd":           "printf raw",
				"shell":         "/bin/sh",
				"login":         false,
				"yield_time_ms": 1000,
			})
			if marshalErr != nil {
				t.Fatalf("marshal exec_command arguments: %v", marshalErr)
			}
			writeRunPromptExecCommandResponse(w, args)
		case 2:
			defer r.Body.Close()
			var payload map[string]any
			if decodeErr := json.NewDecoder(r.Body).Decode(&payload); decodeErr != nil {
				t.Fatalf("decode second provider request: %v", decodeErr)
			}
			output, ok := findRunPromptFunctionCallOutput(payload)
			if !ok {
				t.Fatalf("second provider request has no function_call_output: %#v", payload)
			}
			toolOutput <- output
			modelstub.WriteCompletedResponseStream(w, "done", 1, 1)
		default:
			t.Fatalf("unexpected provider response request %d", currentResponse)
		}
	}))
	defer server.Close()

	authManager := auth.NewManager(auth.NewMemoryStore(auth.State{
		Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "test-key"}},
	}), nil, time.Now)
	cfg := config.App{
		WorkspaceRoot:   store.Meta().WorkspaceRoot,
		PersistenceRoot: root,
		Settings: config.Settings{
			Model:               "gpt-5",
			ThinkingLevel:       "medium",
			OpenAIBaseURL:       server.URL,
			ShellOutputMaxChars: 16_000,
			EnabledTools:        map[toolspec.ID]bool{toolspec.ToolExecCommand: true},
			Shell: config.ShellSettings{
				PostprocessingMode: config.ShellPostprocessingModeUser,
				PostprocessHook:    &effectiveHook,
			},
		},
	}
	authority := newTestHeadlessRuntimeAuthority(root, authManager, background, persistence.Options()...)
	client := NewInProcessRunPromptClient(HeadlessBootstrap{
		SessionLaunch:    newTestHeadlessSessionLaunch(cfg, containerDir, authManager, authority, persistence),
		RuntimeAuthority: authority,
	})

	response, err := client.RunPrompt(context.Background(), serverapi.RunPromptRequest{
		ClientRequestID: "active-shell-policy-1",
		Intent:          serverapi.OpenExistingSessionLaunchIntent(mustRunPromptSessionID(t, store.Meta().SessionID)),
		Prompt:          "run the shell probe",
	}, nil)
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if response.Result != "done" {
		t.Fatalf("result = %q, want done", response.Result)
	}
	select {
	case output := <-toolOutput:
		want := "EFFECTIVE:" + store.Meta().SessionID
		if output != want {
			t.Fatalf("exec_command output = %q, want %q", output, want)
		}
		if strings.Contains(output, "BOOTSTRAP") {
			t.Fatalf("exec_command used bootstrap shell postprocessing: %q", output)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for exec_command output")
	}
}

func writeRunPromptHook(t *testing.T, body string) string {
	t.Helper()
	script := "#!/bin/sh\n" + body + "\n"
	return testsetup.WriteExecutable(t, "postprocess-hook.sh", script)
}

func mustRunPromptPostprocessor(t *testing.T, mode config.ShellPostprocessingMode, hookPath *string) *postprocess.Runner {
	t.Helper()
	runner, err := postprocess.NewRunner(postprocess.Settings{
		Mode:     mode,
		HookPath: hookPath,
	})
	if err != nil {
		t.Fatalf("new run prompt postprocessor: %v", err)
	}
	return runner
}

func writeRunPromptExecCommandResponse(w http.ResponseWriter, args json.RawMessage) {
	writeRunPromptFunctionCallResponse(w, "fc-runprompt-1", "call-runprompt-1", toolspec.ToolExecCommand, args)
}

func writeRunPromptFunctionCallResponse(w http.ResponseWriter, itemID string, callID string, toolID toolspec.ID, args json.RawMessage) {
	writeSSEJSON(w, map[string]any{
		"type": "response.output_item.added",
		"item": map[string]any{
			"id":        itemID,
			"type":      "function_call",
			"name":      string(toolID),
			"call_id":   callID,
			"arguments": "",
		},
	})
	writeSSEJSON(w, map[string]any{
		"type":    "response.function_call_arguments.delta",
		"item_id": itemID,
		"delta":   string(args),
	})
	writeSSEJSON(w, map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"usage": map[string]any{
				"input_tokens":  1,
				"output_tokens": 1,
				"total_tokens":  2,
			},
			"output": []any{
				map[string]any{
					"type":      "function_call",
					"id":        itemID,
					"name":      string(toolID),
					"call_id":   callID,
					"arguments": string(args),
				},
			},
		},
	})
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeRunPromptAskQuestionResponse(w http.ResponseWriter, commentary string) {
	args := json.RawMessage(`{"question":"Need input?"}`)
	writeSSEJSON(w, map[string]any{
		"type":         "response.output_item.added",
		"output_index": 0,
		"item":         map[string]any{"type": "message", "role": "assistant", "phase": "commentary", "content": []any{}},
	})
	writeSSEJSON(w, map[string]any{"type": "response.output_text.delta", "output_index": 0, "delta": commentary})
	writeSSEJSON(w, map[string]any{
		"type":         "response.output_item.added",
		"output_index": 1,
		"item":         map[string]any{"id": "fc-ask", "type": "function_call", "name": string(toolspec.ToolAskQuestion), "call_id": "call-ask", "arguments": ""},
	})
	writeSSEJSON(w, map[string]any{"type": "response.function_call_arguments.delta", "item_id": "fc-ask", "delta": string(args)})
	writeSSEJSON(w, map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2},
			"output": []any{
				map[string]any{"type": "message", "role": "assistant", "phase": "commentary", "content": []any{map[string]any{"type": "output_text", "text": commentary}}},
				map[string]any{"id": "fc-ask", "type": "function_call", "name": string(toolspec.ToolAskQuestion), "call_id": "call-ask", "arguments": string(args)},
			},
		},
	})
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func writeSSEJSON(w http.ResponseWriter, value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("marshal run prompt SSE event: %v", err))
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
}

func findRunPromptFunctionCallOutput(payload map[string]any) (string, bool) {
	items, ok := payload["input"].([]any)
	if !ok {
		return "", false
	}
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok || item["type"] != "function_call_output" {
			continue
		}
		output, ok := item["output"].(string)
		if !ok {
			return "", false
		}
		return output, true
	}
	return "", false
}

func mustRunPromptSessionID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q): %v", raw, err)
	}
	return id
}

func TestInProcessRunPromptClientRejectsSelectedSessionWithGoal(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	persistence := sessiontest.NewPersistence()
	store, err := session.Create(containerDir, "workspace-a", workspace, sessioncontract.SessionCategorySubagent, persistence.Options()...)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, _, err := store.SetGoal("ship feature", session.GoalActorUser); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}

	cfg := config.App{
		WorkspaceRoot:   workspace,
		PersistenceRoot: root,
		Settings:        config.Settings{Model: "gpt-5"},
	}
	authority := newTestHeadlessRuntimeAuthority(root, nil, nil, persistence.Options()...)
	client := NewInProcessRunPromptClient(HeadlessBootstrap{
		SessionLaunch:    newTestHeadlessSessionLaunch(cfg, containerDir, nil, authority, persistence),
		RuntimeAuthority: authority,
	})

	_, err = client.RunPrompt(context.Background(), serverapi.RunPromptRequest{
		ClientRequestID: "goal-reject-1",
		Intent:          serverapi.OpenExistingSessionLaunchIntent(mustRunPromptSessionID(t, store.Meta().SessionID)),
		Prompt:          "continue",
	}, nil)
	if !errors.Is(err, ErrHeadlessGoalSession) {
		t.Fatalf("RunPrompt error = %v, want ErrHeadlessGoalSession", err)
	}
}

func TestInProcessRunPromptClientUnregistersRuntimeAfterCompletion(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	persistence := sessiontest.NewPersistence()
	store, err := session.Create(containerDir, "workspace-a", workspace, sessioncontract.SessionCategorySubagent, persistence.Options()...)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if modelstub.HandleInputTokenCount(w, r, 1) {
			return
		}
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		startedOnce.Do(func() { close(started) })
		<-release
		modelstub.WriteCompletedResponseStream(w, "done", 1, 1)
	}))
	defer server.Close()

	authManager := auth.NewManager(auth.NewMemoryStore(auth.State{
		Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "test-key"}},
	}), nil, time.Now)
	cfg := config.App{
		WorkspaceRoot:   workspace,
		PersistenceRoot: root,
		Settings: config.Settings{
			Model:         "gpt-5",
			ThinkingLevel: "medium",
			OpenAIBaseURL: server.URL,
			Shell:         config.ShellSettings{PostprocessingMode: config.ShellPostprocessingModeBuiltin},
		},
	}
	authority := newTestHeadlessRuntimeAuthority(root, authManager, nil, persistence.Options()...)
	client := NewInProcessRunPromptClient(HeadlessBootstrap{
		SessionLaunch:    newTestHeadlessSessionLaunch(cfg, containerDir, authManager, authority, persistence),
		RuntimeAuthority: authority,
	})

	done := make(chan error, 1)
	go func() {
		_, err := client.RunPrompt(context.Background(), serverapi.RunPromptRequest{
			ClientRequestID: "runtime-cleanup-1",
			Intent:          serverapi.OpenExistingSessionLaunchIntent(mustRunPromptSessionID(t, store.Meta().SessionID)),
			Prompt:          "hello",
		}, nil)
		done <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for /responses request")
	}
	sessionID := mustRunPromptSessionID(t, store.Meta().SessionID)
	if _, active := authority.SessionExecution(sessionID); !active {
		t.Fatal("expected headless execution active while request is in flight")
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunPrompt: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunPrompt did not finish")
	}
	if _, active := authority.SessionExecution(sessionID); active {
		t.Fatal("expected headless execution to finalize after completion")
	}
}

func TestHeadlessRunPromptOverridesRespectLockedModelContract(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(config.PersistenceRootEnvName, home)

	root := t.TempDir()
	workspace := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	persistence := sessiontest.NewPersistence()
	store, err := session.Create(containerDir, "workspace-a", workspace, sessioncontract.SessionCategorySubagent, persistence.Options()...)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.MarkModelDispatchLocked(session.LockedContract{Model: "locked-model", EnabledTools: []string{string(toolspec.ToolExecCommand)}}); err != nil {
		t.Fatalf("mark model dispatch locked: %v", err)
	}

	requestBodies := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if modelstub.HandleInputTokenCount(w, r, 1) {
			return
		}
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		defer r.Body.Close()
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		requestBodies <- payload
		modelstub.WriteCompletedResponseStream(w, "locked response", 1, 1)
	}))
	defer server.Close()

	authManager := auth.NewManager(auth.NewMemoryStore(auth.State{
		Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "test-key"}},
	}), nil, time.Now)

	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.PersistenceRoot = root
	cfg.Settings.Model = "base-model"
	cfg.Settings.OpenAIBaseURL = server.URL
	cfg.Settings.EnabledTools = map[toolspec.ID]bool{toolspec.ToolPatch: true}
	authority := newTestHeadlessRuntimeAuthority(root, authManager, nil, persistence.Options()...)
	client := NewInProcessRunPromptClient(HeadlessBootstrap{
		SessionLaunch:    newTestHeadlessSessionLaunch(cfg, containerDir, authManager, authority, persistence),
		RuntimeAuthority: authority,
	})

	response, err := client.RunPrompt(context.Background(), serverapi.RunPromptRequest{
		ClientRequestID: "locked-direct-1",
		Intent:          serverapi.OpenExistingSessionLaunchIntent(mustRunPromptSessionID(t, store.Meta().SessionID)),
		Prompt:          "hello",
		Overrides: serverapi.RunPromptOverrides{
			Model: "override-model",
			Tools: "patch",
		},
	}, nil)
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if response.Result != "locked response" {
		t.Fatalf("result = %q, want locked response", response.Result)
	}
	select {
	case payload := <-requestBodies:
		if got := payload["model"]; got != "locked-model" {
			t.Fatalf("provider model = %#v, want locked-model", got)
		}
		toolsPayload, ok := payload["tools"].([]any)
		if !ok || len(toolsPayload) != 1 {
			t.Fatalf("expected one locked tool in request payload, got %#v", payload["tools"])
		}
		toolPayload, ok := toolsPayload[0].(map[string]any)
		if !ok {
			t.Fatalf("unexpected tool payload: %#v", toolsPayload[0])
		}
		if got := toolPayload["name"]; got != string(toolspec.ToolExecCommand) {
			t.Fatalf("expected locked shell tool, got %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for provider request payload")
	}
}
