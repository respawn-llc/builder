package startup

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/auth"
	"core/server/authservice"
	serverbootstrap "core/server/bootstrap"
	corepkg "core/server/core"
	"core/server/metadata"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	"core/shared/client"
	"core/shared/config"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

type envAuthHandler struct {
	lookupEnv func(string) string
}

type blockingAuthStore struct {
	base        auth.Store
	saveStarted chan auth.State
	releaseSave <-chan struct{}
}

func (s *blockingAuthStore) Load(ctx context.Context) (auth.State, error) {
	return s.base.Load(ctx)
}

func (s *blockingAuthStore) LoadPersisted(ctx context.Context) (auth.State, error) {
	if persisted, ok := s.base.(auth.PersistedStateLoader); ok {
		return persisted.LoadPersisted(ctx)
	}
	return s.base.Load(ctx)
}

func (s *blockingAuthStore) Save(ctx context.Context, state auth.State) error {
	select {
	case s.saveStarted <- state:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-s.releaseSave:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.base.Save(ctx, state)
}

type blockingAuthHandler struct {
	envAuthHandler
	saveStarted chan auth.State
	releaseSave <-chan struct{}
}

func (h blockingAuthHandler) WrapStore(base auth.Store) auth.Store {
	return &blockingAuthStore{
		base:        h.envAuthHandler.WrapStore(base),
		saveStarted: h.saveStarted,
		releaseSave: h.releaseSave,
	}
}

func (h envAuthHandler) WrapStore(base auth.Store) auth.Store {
	return authservice.WrapStoreWithEnvAPIKeyOverride(base, h.LookupEnv)
}

func (envAuthHandler) NeedsInteraction(req authservice.FlowInteractionRequest) bool {
	return !req.Gate.Ready
}

func (envAuthHandler) Interact(context.Context, authservice.FlowInteractionRequest) (authservice.FlowInteractionOutcome, error) {
	return authservice.FlowInteractionOutcome{}, auth.ErrAuthNotConfigured
}

func (h envAuthHandler) LookupEnv(key string) string {
	if h.lookupEnv != nil {
		return h.lookupEnv(key)
	}
	return testAuthLookupEnv(key)
}

func testAuthLookupEnv(key string) string {
	if key == "OPENAI_API_KEY" {
		return "in-memory-test-key"
	}
	return ""
}

var noopOnboarding = OnboardingHandler(func(_ context.Context, req OnboardingRequest) (config.App, error) {
	path, created, err := config.WriteDefaultSettingsFile()
	if err != nil {
		return config.App{}, err
	}
	reloaded, err := req.ReloadConfig()
	if err != nil {
		return config.App{}, err
	}
	reloaded.Source.CreatedDefaultConfig = created
	reloaded.Source.SettingsPath = path
	reloaded.Source.SettingsFileExists = true
	return reloaded, nil
})

func releaseServeTestPortForConfig(cfg config.App) {
	testsetup.ReleaseLoopbackPort(cfg.Settings.ServerHost, cfg.Settings.ServerPort)
}

func registerServeWorkspace(t *testing.T, workspace string) {
	t.Helper()
	configureServeTestServerPort(t)
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if _, _, err := config.WriteDefaultSettingsFileAt(cfg.Source.HomeSettingsPath); err != nil {
		t.Fatalf("write test settings: %v", err)
	}
	if _, err := metadata.RegisterBinding(context.Background(), cfg.PersistenceRoot, cfg.WorkspaceRoot); err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
}

func newServeWorkspace(t *testing.T) string {
	t.Helper()
	workspace := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	registerServeWorkspace(t, workspace)
	return workspace
}

func startServeTestServer(t *testing.T, request Request, authHandler AuthHandler, onboarding OnboardingHandler) *ServeServer {
	t.Helper()
	server, err := StartServeServer(context.Background(), request, authHandler, onboarding)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func TestStartServeServerRejectsSecondPersistenceRootOwner(t *testing.T) {
	workspace := newServeWorkspace(t)
	request := Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}
	first := startServeTestServer(t, request, envAuthHandler{}, noopOnboarding)

	if _, err := StartServeServer(context.Background(), request, envAuthHandler{}, noopOnboarding); !errors.Is(err, corepkg.ErrPersistenceRootBusy) {
		t.Fatalf("second StartServeServer error = %v, want ErrPersistenceRootBusy", err)
	}
	if first.Core == nil {
		t.Fatal("first server lost its core after rejected second owner")
	}
}

func startServingTestServer(t *testing.T, server *ServeServer) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		releaseServeTestPortForConfig(server.Config())
		errCh <- server.Serve(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		if serveErr := <-errCh; !errors.Is(serveErr, context.Canceled) {
			t.Errorf("Serve error = %v, want context canceled", serveErr)
		}
	})
}

func waitForServeResponse(t *testing.T, httpClient *http.Client, url string) *http.Response {
	t.Helper()
	var response *http.Response
	var err error
	if !testsetup.Until(time.Now().Add(5*time.Second), 10*time.Millisecond, func() bool {
		response, err = httpClient.Get(url)
		return err == nil
	}) {
		t.Fatalf("GET %s: %v", url, err)
	}
	return response
}

func requireServeResponse(t *testing.T, httpClient *http.Client, url string, status int) *http.Response {
	t.Helper()
	response := waitForServeResponse(t, httpClient, url)
	if response.StatusCode != status {
		_ = response.Body.Close()
		t.Fatalf("%s status = %d, want %d", url, response.StatusCode, status)
	}
	return response
}

func configureServeTestServerPort(t *testing.T) {
	t.Helper()
	reservation := testsetup.ReserveLoopbackPort(t)
	t.Setenv("KENT_SERVER_HOST", "127.0.0.1")
	t.Setenv("KENT_SERVER_PORT", strconv.Itoa(reservation.Port))
}

func TestServeWaitsForContextCancellation(t *testing.T) {
	server := &ServeServer{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := server.Serve(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Serve error = %v, want context canceled", err)
	}
}

func TestStartServeServerRecoversAdmittedCurrentNodeOnRestart(t *testing.T) {
	workspace := newServeWorkspace(t)
	request := Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}
	server, err := StartServeServer(context.Background(), request, envAuthHandler{}, noopOnboarding)
	if err != nil {
		t.Fatalf("StartServeServer: %v", err)
	}
	taskID, currentNode := createAdmittedCurrentNodeForRecovery(t, server)
	if err := server.Close(); err != nil {
		t.Fatalf("close initial server: %v", err)
	}

	restarted, err := StartServeServer(context.Background(), request, envAuthHandler{}, noopOnboarding)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	detail, err := restarted.WorkflowClient().GetWorkflowTask(context.Background(), serverapi.WorkflowTaskGetRequest{TaskID: string(taskID)})
	if err != nil {
		t.Fatalf("GetWorkflowTask after restart: %v", err)
	}
	if !detail.Task.Actions.CanResume || detail.Task.Actions.CanInterrupt {
		t.Fatalf("task actions after restart reconciliation = %+v, want resumable and not interruptible", detail.Task.Actions)
	}
	store, err := workflowstore.New(restarted.MetadataStore(), workflowstore.WithRoleResolver(testsetup.QuestionsEnabled("coder")))
	if err != nil {
		t.Fatalf("workflowstore.New after restart: %v", err)
	}
	currentNodes, err := store.ListCurrentNodes(context.Background(), taskID)
	if err != nil {
		t.Fatalf("ListCurrentNodes after restart: %v", err)
	}
	if len(currentNodes) != 1 ||
		!currentNodes[0].Reference.Equal(currentNode) ||
		currentNodes[0].Scheduling == nil ||
		currentNodes[0].Scheduling.State != workflow.CurrentNodeSchedulingInterrupted ||
		currentNodes[0].Scheduling.Interruption == nil ||
		currentNodes[0].Scheduling.Interruption.Reason != workflowexecution.ReasonCurrentNodeStartupRecovery ||
		currentNodes[0].Scheduling.Interruption.Detail.Code != string(workflowexecution.ReasonCurrentNodeStartupRecovery) ||
		currentNodes[0].Scheduling.Interruption.Detail.Fields == nil ||
		len(currentNodes[0].Scheduling.Interruption.Detail.Fields) != 0 {
		t.Fatalf("current nodes after startup recovery = %+v, want interrupted admitted current node %v", currentNodes, currentNode)
	}
	time.Sleep(100 * time.Millisecond)
	stable, err := restarted.WorkflowClient().GetWorkflowTask(context.Background(), serverapi.WorkflowTaskGetRequest{TaskID: string(taskID)})
	if err != nil {
		t.Fatalf("GetWorkflowTask after recovery stability window: %v", err)
	}
	if !stable.Task.Actions.CanResume || stable.Task.Actions.CanInterrupt {
		t.Fatalf("task actions after recovery stability window = %+v, want resumable and not interruptible", stable.Task.Actions)
	}
	if count, err := store.CountTaskSessions(context.Background(), taskID); err != nil || count != 0 {
		t.Fatalf("retained Sessions after restart recovery = %d, %v; want no automatic Agent start", count, err)
	}
}

func createAdmittedCurrentNodeForRecovery(t *testing.T, server *ServeServer) (workflow.TaskID, workflow.CurrentNodeReference) {
	t.Helper()
	ctx := context.Background()
	client := server.WorkflowClient()
	created, err := client.CreateAndLinkWorkflowToProject(ctx, serverapi.WorkflowCreateAndLinkProjectRequest{
		Name:          "Restart recovery",
		ProjectID:     server.ProjectID(),
		DefaultPolicy: serverapi.WorkflowProjectLinkDefaultAlways,
	})
	if err != nil {
		t.Fatalf("CreateAndLinkWorkflowToProject: %v", err)
	}
	definition, err := client.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: created.Workflow.ID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	var startID, terminalID string
	for _, node := range definition.Definition.Nodes {
		switch node.Kind {
		case "start":
			startID = node.ID
		case "terminal":
			terminalID = node.ID
		}
	}
	if startID == "" || terminalID == "" {
		t.Fatalf("default workflow nodes = %+v", definition.Definition.Nodes)
	}
	agentID := runtimeids.NewGraphEntityID()
	startGroupID := runtimeids.NewGraphEntityID()
	doneGroupID := runtimeids.NewGraphEntityID()
	graph := serverapi.WorkflowGraphDraftFromDefinition(definition.Definition)
	graph.Nodes = append(graph.Nodes, serverapi.WorkflowGraphDraftNode{
		ID: agentID, Key: "agent", Kind: "agent", DisplayName: "Agent", SubagentRole: "coder",
	})
	graph.TransitionGroups = append(graph.TransitionGroups,
		serverapi.WorkflowGraphDraftTransitionGroup{ID: startGroupID, SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: doneGroupID, SourceNodeID: agentID, TransitionID: "done", DisplayName: "Done"},
	)
	graph.Edges = append(graph.Edges,
		serverapi.WorkflowGraphDraftEdge{ID: runtimeids.NewGraphEntityID(), TransitionGroupID: startGroupID, Key: "start", TargetNodeID: agentID, AssigneeSelection: "configured", ThinkingSelection: "configured", ContextMode: "new_session", PromptTemplate: "Perform the work."},
		serverapi.WorkflowGraphDraftEdge{ID: runtimeids.NewGraphEntityID(), TransitionGroupID: doneGroupID, Key: "done", TargetNodeID: terminalID, AssigneeSelection: "configured", ThinkingSelection: "configured", ContextMode: "new_session"},
	)
	saved, err := client.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID: created.Workflow.ID, ExpectedVersion: definition.Definition.Workflow.Version, Graph: graph,
	})
	if err != nil || !saved.Saved {
		t.Fatalf("SaveWorkflowGraph recovery fixture = %+v, err = %v", saved, err)
	}
	workflowID := created.Workflow.ID
	task, err := client.CreateWorkflowTask(ctx, serverapi.WorkflowTaskCreateRequest{
		ProjectID:  server.ProjectID(),
		WorkflowID: &workflowID,
		Title:      "Recover admitted current node",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowTask: %v", err)
	}
	store, err := workflowstore.New(server.MetadataStore(), workflowstore.WithRoleResolver(testsetup.QuestionsEnabled("coder")))
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	started, err := store.StartTask(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if len(started.Mutation.Created) != 1 {
		t.Fatalf("StartTask created current nodes = %+v, want one", started.Mutation.Created)
	}
	currentNode := started.Mutation.Created[0].Reference
	if err := store.AdmitCurrentNode(ctx, currentNode); err != nil {
		t.Fatalf("AdmitCurrentNode: %v", err)
	}
	return workflow.TaskID(task.Task.ID), currentNode
}

func TestServeRequiresContext(t *testing.T) {
	server := &ServeServer{}
	if err := server.Serve(nil); err == nil || !errors.Is(err, errContextRequired) {
		t.Fatalf("Serve error = %v, want missing context error", err)
	}
}

func TestServeExposesConfiguredHealthEndpoints(t *testing.T) {
	workspace := newServeWorkspace(t)
	server := startServeTestServer(t, Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, envAuthHandler{}, noopOnboarding)
	startServingTestServer(t, server)

	cfg := server.Config()
	healthResp := requireServeResponse(t, http.DefaultClient, config.ServerHTTPBaseURL(cfg)+protocol.HealthPath, http.StatusOK)
	defer func() { _ = healthResp.Body.Close() }()
	readyResp := requireServeResponse(t, http.DefaultClient, config.ServerHTTPBaseURL(cfg)+protocol.ReadinessPath, http.StatusOK)
	defer func() { _ = readyResp.Body.Close() }()

}

func TestServeExposesDerivedLocalUnixSocketAndCleansStalePath(t *testing.T) {
	workspace := newServeWorkspace(t)
	server := startServeTestServer(t, Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, envAuthHandler{}, noopOnboarding)
	cfg := server.Config()
	socketPath, ok, err := config.ServerLocalRPCSocketPath(cfg)
	if err != nil {
		t.Fatalf("ServerLocalRPCSocketPath: %v", err)
	}
	if !ok {
		t.Skip("local unix sockets unsupported on this platform")
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		t.Fatalf("MkdirAll socket dir: %v", err)
	}
	staleListener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen stale unix socket: %v", err)
	}
	if err := staleListener.Close(); err != nil {
		t.Fatalf("close stale unix socket: %v", err)
	}

	startServingTestServer(t, server)

	deadline := time.Now().Add(5 * time.Second)
	var socketErr error
	if !testsetup.Until(deadline, 10*time.Millisecond, func() bool {
		_, socketErr = os.Stat(socketPath)
		return socketErr == nil
	}) {
		t.Fatalf("unix socket path did not appear: %v", socketErr)
	}
	var dialErr error
	if !testsetup.Until(deadline, 10*time.Millisecond, func() bool {
		var conn net.Conn
		conn, dialErr = net.DialTimeout("unix", socketPath, 100*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return true
		}
		return false
	}) {
		t.Fatalf("unix socket path did not become dialable: %v", dialErr)
	}

	var localRemote *client.Remote
	if !testsetup.Until(deadline, 10*time.Millisecond, func() bool {
		localRemote, err = client.DialConfiguredRemote(context.Background(), cfg)
		return err == nil
	}) {
		t.Fatalf("DialConfiguredRemote: %v", err)
	}
	if localRemote.Identity().ServerID == "" {
		t.Fatal("expected configured remote identity")
	}
	_ = localRemote.Close()

}

func TestServeDegradesToTCPWhenDerivedLocalSocketFails(t *testing.T) {
	workspace := newServeWorkspace(t)

	originalLocalSocketListener := localSocketListener
	localSocketListener = func(config.App) (net.Listener, func(), bool, error) {
		return nil, nil, false, errors.New("uds setup failed")
	}
	t.Cleanup(func() { localSocketListener = originalLocalSocketListener })

	server := startServeTestServer(t, Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, envAuthHandler{}, noopOnboarding)
	startServingTestServer(t, server)

	cfg := server.Config()
	healthURL := config.ServerHTTPBaseURL(cfg) + protocol.HealthPath
	_ = requireServeResponse(t, http.DefaultClient, healthURL, http.StatusOK).Body.Close()

	tcpRemote, err := client.DialRemoteURL(context.Background(), config.ServerRPCURL(cfg))
	if err != nil {
		t.Fatalf("DialRemoteURL TCP: %v", err)
	}
	_ = tcpRemote.Close()
}

func TestServeStartsUnauthenticatedAndReportsBootstrapReadiness(t *testing.T) {
	workspace := newServeWorkspace(t)
	server := startServeTestServer(t,
		Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true, AllowUnauthenticated: true},
		envAuthHandler{lookupEnv: func(string) string { return "" }},
		noopOnboarding,
	)
	startServingTestServer(t, server)

	cfg := server.Config()
	healthURL := config.ServerHTTPBaseURL(cfg) + protocol.HealthPath
	readyURL := config.ServerHTTPBaseURL(cfg) + protocol.ReadinessPath
	healthResp := requireServeResponse(t, http.DefaultClient, healthURL, http.StatusOK)
	defer func() { _ = healthResp.Body.Close() }()
	var healthBody map[string]any
	if err := json.NewDecoder(healthResp.Body).Decode(&healthBody); err != nil {
		t.Fatalf("decode health body: %v", err)
	}
	if healthBody["auth_ready"] != false {
		t.Fatalf("expected auth_ready=false health payload, got %+v", healthBody)
	}

	readyResp := requireServeResponse(t, http.DefaultClient, readyURL, http.StatusServiceUnavailable)
	defer func() { _ = readyResp.Body.Close() }()
	var readyBody map[string]any
	if err := json.NewDecoder(readyResp.Body).Decode(&readyBody); err != nil {
		t.Fatalf("decode ready body: %v", err)
	}
	if readyBody["ready"] != false || readyBody["auth_ready"] != false || readyBody["transport_ready"] != true {
		t.Fatalf("unexpected readiness payload: %+v", readyBody)
	}

}

func TestServeReadinessDoesNotRequireAuthForNonFirstPartyProvider(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	writeServeSettings(t, home, `
model = "gpt-5"
openai_base_url = "http://127.0.0.1:11434/v1"
`)
	configureServeTestServerPort(t)
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load for binding: %v", err)
	}
	if _, err := metadata.RegisterBinding(context.Background(), cfg.PersistenceRoot, workspace); err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}

	server := startServeTestServer(t,
		Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true, AllowUnauthenticated: true},
		envAuthHandler{lookupEnv: func(string) string { return "" }},
		noopOnboarding,
	)
	startServingTestServer(t, server)

	readyURL := config.ServerHTTPBaseURL(server.Config()) + protocol.ReadinessPath
	client := &http.Client{Timeout: time.Second}
	resp := requireServeResponse(t, client, readyURL, http.StatusOK)
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode readiness body: %v", err)
	}
	if body["ready"] != true || body["auth_ready"] != false {
		t.Fatalf("readiness body = %+v, want ready with unavailable auth", body)
	}
}

func TestMissingConfigServeStartsBootstrapSurfaceBeforeAuthReady(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	configureServeTestServerPort(t)

	server := startServeTestServer(t, Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, envAuthHandler{lookupEnv: func(string) string { return "" }}, nil)
	if server.Core != nil || server.deps == nil {
		t.Fatal("expected missing-config serve startup surface without configured core")
	}
	readiness, err := server.deps.ServerStatusClient().GetServerReadiness(context.Background(), serverapi.ServerReadinessRequest{})
	if err != nil {
		t.Fatalf("GetServerReadiness: %v", err)
	}
	if readiness.Ready || len(readiness.Causes) == 0 {
		t.Fatalf("readiness = %+v, want not ready with onboarding cause", readiness)
	}
	cause := readiness.Causes[0]
	if cause.Code != string(serverapi.ServerNotReadyOnboardingRequired) || cause.Severity != "error" || cause.Summary != nil || cause.NextAction != nil {
		t.Fatalf("unexpected onboarding readiness cause: %+v", cause)
	}
	if _, err := server.deps.ServerStatusClient().GetUpdateStatus(context.Background(), serverapi.UpdateStatusRequest{}); !errors.Is(err, serverapi.ErrServerNotReadyOnboardingRequired) {
		t.Fatalf("GetUpdateStatus before activation error = %v, want onboarding not ready", err)
	}
	if _, statErr := os.Stat(filepath.Join(home, config.ConfigDirName, "config.toml")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("settings file should remain absent before finalize, stat err=%v", statErr)
	}
}

func TestStartupGatewayReadsPublishedTupleWhileActivationBuildsCore(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	configureServeTestServerPort(t)
	skillDir := filepath.Join(home, ".claude", "skills", "helper")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("create import skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: helper\ndescription: Test helper\n---\n"), 0o644); err != nil {
		t.Fatalf("write import skill: %v", err)
	}

	server := startServeTestServer(t, Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, envAuthHandler{}, nil)
	originalBuilder := server.deps.buildCore
	buildStarted := make(chan config.App, 1)
	releaseBuild := make(chan struct{})
	var releaseBuildOnce sync.Once
	releaseCoreBuild := func() { releaseBuildOnce.Do(func() { close(releaseBuild) }) }
	server.deps.buildCore = func(
		ctx context.Context,
		cfg config.App,
		authSupport serverbootstrap.AuthSupport,
		runtimeSupport serverbootstrap.RuntimeSupport,
		options corepkg.Options,
	) (*corepkg.Core, error) {
		buildStarted <- cloneStartupConfig(cfg)
		<-releaseBuild
		return originalBuilder(ctx, cfg, authSupport, runtimeSupport, options)
	}
	startServingTestServer(t, server)
	t.Cleanup(releaseCoreBuild)

	activator, err := client.DialConfiguredRemote(context.Background(), server.Config())
	if err != nil {
		t.Fatalf("dial activation remote: %v", err)
	}
	defer func() { _ = activator.Close() }()
	reader, err := client.DialConfiguredRemote(context.Background(), server.Config())
	if err != nil {
		t.Fatalf("dial read remote: %v", err)
	}
	defer func() { _ = reader.Close() }()

	initialReadiness, err := reader.GetServerReadiness(context.Background(), serverapi.ServerReadinessRequest{})
	if err != nil {
		t.Fatalf("initial readiness: %v", err)
	}
	initialBootstrap, err := reader.GetAuthBootstrapStatus(context.Background(), serverapi.AuthGetBootstrapStatusRequest{})
	if err != nil {
		t.Fatalf("initial auth bootstrap: %v", err)
	}
	initialAuth, err := reader.GetAuthStatus(context.Background(), serverapi.AuthStatusRequest{SkipSubscriptionUsage: true})
	if err != nil {
		t.Fatalf("initial auth status: %v", err)
	}
	initialFacts, err := reader.GetCapabilityFacts(context.Background(), serverapi.CapabilityFactsRequest{})
	if err != nil {
		t.Fatalf("initial capability facts: %v", err)
	}
	if initialFacts.Providers.CurrentEffective == nil {
		t.Fatal("initial capability facts omitted the effective provider")
	}
	var helperInitiallyEnabled bool
	for _, projection := range initialFacts.Imports.SkillEnablement {
		for _, candidate := range projection.Candidates {
			if candidate.Ref.TargetName == "helper" && candidate.DefaultEnabled != nil && *candidate.DefaultEnabled {
				helperInitiallyEnabled = true
			}
		}
	}
	if !helperInitiallyEnabled {
		t.Fatalf("initial capability facts did not expose enabled helper import: %+v", initialFacts.Imports.SkillEnablement)
	}

	provider := "anthropic"
	finalizeDone := make(chan error, 1)
	go func() {
		_, finalizeErr := activator.FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{
			MainProvider: &serverapi.OnboardingProviderChoice{ProviderOverride: &provider},
			Model: &serverapi.OnboardingModelChoice{
				Kind:  serverapi.OnboardingModelCustom,
				Alias: "pending-model",
			},
			DisabledSkillNames: []string{"helper"},
		})
		finalizeDone <- finalizeErr
	}()
	pendingConfig := <-buildStarted
	if pendingConfig.Settings.Model != "pending-model" || pendingConfig.Settings.ProviderOverride != provider || pendingConfig.Settings.SkillToggles["helper"] {
		t.Fatalf("pending activation config = %+v", pendingConfig.Settings)
	}
	if initialFacts.Defaults.PrimaryModelID == pendingConfig.Settings.Model ||
		initialFacts.Providers.CurrentEffective.LLMProviderID == pendingConfig.Settings.ProviderOverride {
		t.Fatalf("initial and pending capability settings are not observably different: initial=%+v pending=%+v", initialFacts, pendingConfig.Settings)
	}

	preflightDone := make(chan error, 1)
	go func() {
		preflightDone <- server.deps.RequireCoreActive()
	}()
	select {
	case preflightErr := <-preflightDone:
		if !errors.Is(preflightErr, serverapi.ErrServerNotReadyOnboardingRequired) {
			t.Fatalf("core preflight during activation = %v, want onboarding required", preflightErr)
		}
	case <-time.After(time.Second):
		t.Fatal("core preflight waited for activation to finish")
	}

	readCtx, cancelReads := context.WithTimeout(context.Background(), time.Second)
	defer cancelReads()
	duringReadiness, err := reader.GetServerReadiness(readCtx, serverapi.ServerReadinessRequest{})
	if err != nil {
		t.Fatalf("readiness during activation: %v", err)
	}
	duringBootstrap, err := reader.GetAuthBootstrapStatus(readCtx, serverapi.AuthGetBootstrapStatusRequest{})
	if err != nil {
		t.Fatalf("auth bootstrap during activation: %v", err)
	}
	duringAuth, err := reader.GetAuthStatus(readCtx, serverapi.AuthStatusRequest{SkipSubscriptionUsage: true})
	if err != nil {
		t.Fatalf("auth status during activation: %v", err)
	}
	duringFacts, err := reader.GetCapabilityFacts(readCtx, serverapi.CapabilityFactsRequest{})
	if err != nil {
		t.Fatalf("capability facts during activation: %v", err)
	}
	if _, err := reader.GetUpdateStatus(readCtx, serverapi.UpdateStatusRequest{}); !errors.Is(err, serverapi.ErrServerNotReadyOnboardingRequired) {
		t.Fatalf("update status during activation = %v, want onboarding required", err)
	}
	if !reflect.DeepEqual(duringReadiness, initialReadiness) ||
		!reflect.DeepEqual(duringBootstrap, initialBootstrap) ||
		!reflect.DeepEqual(duringAuth, initialAuth) ||
		!reflect.DeepEqual(duringFacts, initialFacts) {
		t.Fatalf("activation reads crossed published tuples")
	}

	releaseCoreBuild()
	if err := <-finalizeDone; err != nil {
		t.Fatalf("finalize onboarding: %v", err)
	}
	ready, err := reader.GetServerReadiness(context.Background(), serverapi.ServerReadinessRequest{})
	if err != nil || !ready.Ready {
		t.Fatalf("readiness after activation = %+v, %v", ready, err)
	}
	facts, err := reader.GetCapabilityFacts(context.Background(), serverapi.CapabilityFactsRequest{})
	if err != nil {
		t.Fatalf("capability facts after activation: %v", err)
	}
	if facts.Defaults.PrimaryModelID != "pending-model" ||
		facts.Providers.CurrentEffective == nil ||
		facts.Providers.CurrentEffective.LLMProviderID != provider {
		t.Fatalf("capability facts after activation = %+v", facts)
	}
	bootstrap, err := reader.GetAuthBootstrapStatus(context.Background(), serverapi.AuthGetBootstrapStatusRequest{})
	if err != nil {
		t.Fatalf("auth bootstrap after activation: %v", err)
	}
	if bootstrap.AuthRequired {
		t.Fatalf("auth bootstrap after activation = %+v, want non-OpenAI settings", bootstrap)
	}
	authStatus, err := reader.GetAuthStatus(context.Background(), serverapi.AuthStatusRequest{SkipSubscriptionUsage: true})
	if err != nil {
		t.Fatalf("auth status after activation: %v", err)
	}
	if authStatus.Resolution.Facts == nil || authStatus.Resolution.Facts.Provider.Identifier != provider {
		t.Fatalf("auth status after activation = %+v, want provider %q", authStatus, provider)
	}
	if ready.AuthRequired {
		t.Fatalf("readiness after activation = %+v, want non-OpenAI settings", ready)
	}
	var helperDisabled bool
	for _, projection := range facts.Imports.SkillEnablement {
		for _, candidate := range projection.Candidates {
			if candidate.Ref.TargetName == "helper" && candidate.DefaultEnabled != nil && !*candidate.DefaultEnabled {
				helperDisabled = true
			}
		}
	}
	if !helperDisabled {
		t.Fatalf("capability facts after activation did not publish disabled helper import: %+v", facts.Imports.SkillEnablement)
	}
	updateCtx, cancelUpdate := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelUpdate()
	if _, err := reader.GetUpdateStatus(updateCtx, serverapi.UpdateStatusRequest{}); errors.Is(err, serverapi.ErrServerNotReadyOnboardingRequired) {
		t.Fatalf("update status did not reach published Core: %v", err)
	}
}

func TestStartupGatewayReadsPublishedAuthFactsWhileMutationIsBlocked(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	configureServeTestServerPort(t)

	saveStarted := make(chan auth.State, 3)
	releaseSave := make(chan struct{})
	var releaseSaveOnce sync.Once
	releaseAuthSaves := func() { releaseSaveOnce.Do(func() { close(releaseSave) }) }
	handler := blockingAuthHandler{
		envAuthHandler: envAuthHandler{lookupEnv: func(string) string { return "" }},
		saveStarted:    saveStarted,
		releaseSave:    releaseSave,
	}
	server := startServeTestServer(t, Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, handler, nil)
	startServingTestServer(t, server)
	t.Cleanup(releaseAuthSaves)

	dial := func(name string) *client.Remote {
		t.Helper()
		remote, err := client.DialConfiguredRemote(context.Background(), server.Config())
		if err != nil {
			t.Fatalf("dial %s remote: %v", name, err)
		}
		t.Cleanup(func() { _ = remote.Close() })
		return remote
	}
	firstMutation := dial("first mutation")
	secondMutation := dial("second mutation")
	acknowledger := dial("acknowledgement")
	reader := dial("reader")

	initialReadiness, err := reader.GetServerReadiness(context.Background(), serverapi.ServerReadinessRequest{})
	if err != nil {
		t.Fatalf("initial readiness: %v", err)
	}
	initialBootstrap, err := reader.GetAuthBootstrapStatus(context.Background(), serverapi.AuthGetBootstrapStatusRequest{})
	if err != nil {
		t.Fatalf("initial auth bootstrap: %v", err)
	}
	initialAuth, err := reader.GetAuthStatus(context.Background(), serverapi.AuthStatusRequest{SkipSubscriptionUsage: true})
	if err != nil {
		t.Fatalf("initial auth status: %v", err)
	}
	initialFacts, err := reader.GetCapabilityFacts(context.Background(), serverapi.CapabilityFactsRequest{})
	if err != nil {
		t.Fatalf("initial capability facts: %v", err)
	}

	firstDone := make(chan error, 1)
	go func() {
		_, mutationErr := firstMutation.CompleteAuthBootstrap(context.Background(), serverapi.AuthCompleteBootstrapRequest{
			Mode:   serverapi.AuthBootstrapModeAPIKey,
			Force:  true,
			APIKey: "first-blocked-key",
		})
		firstDone <- mutationErr
	}()
	firstSave := <-saveStarted
	if firstSave.Method.APIKey == nil || firstSave.Method.APIKey.Key != "first-blocked-key" {
		t.Fatalf("first blocked auth state = %+v", firstSave)
	}

	secondStarted := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondStarted)
		_, mutationErr := secondMutation.CompleteAuthBootstrap(context.Background(), serverapi.AuthCompleteBootstrapRequest{
			Mode:   serverapi.AuthBootstrapModeAPIKey,
			Force:  true,
			APIKey: "second-serialized-key",
		})
		secondDone <- mutationErr
	}()
	<-secondStarted
	ackStarted := make(chan struct{})
	ackDone := make(chan error, 1)
	go func() {
		close(ackStarted)
		_, ackErr := acknowledger.AcknowledgeNoAuth(context.Background(), serverapi.AuthAcknowledgeNoAuthRequest{})
		ackDone <- ackErr
	}()
	<-ackStarted
	readCtx, cancelReads := context.WithTimeout(context.Background(), time.Second)
	defer cancelReads()
	duringReadiness, err := reader.GetServerReadiness(readCtx, serverapi.ServerReadinessRequest{})
	if err != nil {
		t.Fatalf("readiness during auth mutation: %v", err)
	}
	duringBootstrap, err := reader.GetAuthBootstrapStatus(readCtx, serverapi.AuthGetBootstrapStatusRequest{})
	if err != nil {
		t.Fatalf("auth bootstrap during auth mutation: %v", err)
	}
	duringAuth, err := reader.GetAuthStatus(readCtx, serverapi.AuthStatusRequest{SkipSubscriptionUsage: true})
	if err != nil {
		t.Fatalf("auth status during auth mutation: %v", err)
	}
	duringFacts, err := reader.GetCapabilityFacts(readCtx, serverapi.CapabilityFactsRequest{})
	if err != nil {
		t.Fatalf("capability facts during auth mutation: %v", err)
	}
	if _, err := reader.GetUpdateStatus(readCtx, serverapi.UpdateStatusRequest{}); !errors.Is(err, serverapi.ErrServerNotReadyOnboardingRequired) {
		t.Fatalf("update status during auth mutation = %v, want onboarding required", err)
	}
	if !reflect.DeepEqual(duringReadiness, initialReadiness) ||
		!reflect.DeepEqual(duringBootstrap, initialBootstrap) ||
		!reflect.DeepEqual(duringAuth, initialAuth) ||
		!reflect.DeepEqual(duringFacts, initialFacts) {
		t.Fatal("auth mutation reads did not return the prior published facts")
	}
	select {
	case state := <-saveStarted:
		t.Fatalf("second auth mutation reached Save before the first completed: %+v", state)
	default:
	}
	select {
	case err := <-secondDone:
		t.Fatalf("second auth mutation completed before the first: %v", err)
	default:
	}
	select {
	case err := <-ackDone:
		t.Fatalf("auth acknowledgement completed before the first mutation: %v", err)
	default:
	}
	releaseAuthSaves()
	if err := <-firstDone; err != nil {
		t.Fatalf("first auth mutation: %v", err)
	}
	secondSave := <-saveStarted
	if secondSave.Method.APIKey == nil || secondSave.Method.APIKey.Key != "second-serialized-key" {
		t.Fatalf("second serialized auth state = %+v", secondSave)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second auth mutation: %v", err)
	}
	if err := <-ackDone; err != nil {
		t.Fatalf("auth acknowledgement: %v", err)
	}
}

func TestStartupDependencySnapshotDeepCopiesConfig(t *testing.T) {
	hook := "/tmp/hook"
	preCompaction := 123
	cfg := config.App{
		Settings: config.Settings{
			Model:             "initial-model",
			SystemPromptFiles: []config.SystemPromptFile{{Path: "/tmp/prompt"}},
			EnabledTools:      map[toolspec.ID]bool{toolspec.ToolPatch: true},
			SkillToggles:      map[string]bool{"helper": true},
			Shell:             config.ShellSettings{PostprocessHook: &hook},
			Workflow:          config.WorkflowSettings{PreCompactionTokens: &preCompaction},
			Subagents: map[string]config.SubagentRole{
				"worker": {Sources: map[string]string{"model": "file"}, Settings: config.Settings{SkillToggles: map[string]bool{"nested": true}}},
			},
		},
		Source: config.SourceReport{Sources: map[string]string{"model": "default"}},
	}
	deps := newStartupGatewayDependencies(context.Background(), cfg, serverbootstrap.Request{}, serverbootstrap.AuthSupport{}, nil, nil)
	cfg.Settings.EnabledTools[toolspec.ToolPatch] = false
	cfg.Settings.SkillToggles["helper"] = false
	*cfg.Settings.Shell.PostprocessHook = "mutated"
	cfg.Source.Sources["model"] = "mutated"

	first := cloneStartupConfig(deps.loadSnapshot().cfg)
	first.Settings.SystemPromptFiles[0].Path = "mutated"
	role := first.Settings.Subagents["worker"]
	role.Sources["model"] = "mutated"
	role.Settings.SkillToggles["nested"] = false
	first.Settings.Subagents["worker"] = role
	second := deps.loadSnapshot().cfg
	if !second.Settings.EnabledTools[toolspec.ToolPatch] ||
		!second.Settings.SkillToggles["helper"] ||
		*second.Settings.Shell.PostprocessHook != "/tmp/hook" ||
		second.Source.Sources["model"] != "default" ||
		second.Settings.SystemPromptFiles[0].Path != "/tmp/prompt" ||
		second.Settings.Subagents["worker"].Sources["model"] != "file" ||
		!second.Settings.Subagents["worker"].Settings.SkillToggles["nested"] {
		t.Fatalf("published startup config was aliased: %+v", second)
	}
}

func TestStartupControlSurfaceRejectsConfigThatAppearsBeforeRootLock(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	configureServeTestServerPort(t)
	loadCfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if _, _, err := config.WriteDefaultSettingsFileAt(loadCfg.Source.HomeSettingsPath); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	_, _, err = buildStartupControlSurface(context.Background(), buildRequest(Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, envAuthHandler{}), envAuthHandler{})
	if !errors.Is(err, errStartupControlSurfaceNotRequired) {
		t.Fatalf("buildStartupControlSurface error = %v, want not required", err)
	}
}

func TestServeOnboardingHandlerReceivesCapabilityFactsClient(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	configureServeTestServerPort(t)

	receivedFacts := false
	onboarding := OnboardingHandler(func(ctx context.Context, req OnboardingRequest) (config.App, error) {
		if req.CapabilityFactsClient == nil {
			t.Fatal("capability facts client was not threaded into serve onboarding")
		}
		if _, err := req.CapabilityFactsClient.GetCapabilityFacts(ctx, serverapi.CapabilityFactsRequest{}); err != nil {
			t.Fatalf("GetCapabilityFacts: %v", err)
		}
		receivedFacts = true
		return req.Config, ErrOnboardingRequired
	})

	startServeTestServer(t, Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, envAuthHandler{}, onboarding)
	if !receivedFacts {
		t.Fatal("expected onboarding handler to receive capability facts")
	}
}

func TestConfiguredRemoteGetsServerReadinessWhenAuthMissing(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	writeServeSettings(t, home, `
model = "gpt-5"

[subagents.coder]
model = "coder-model"

[subagents.blocked]
agent_callable = false
model = "blocked-model"
`)

	registerServeWorkspace(t, workspace)

	server := startServeTestServer(t,
		Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true, AllowUnauthenticated: true},
		envAuthHandler{lookupEnv: func(string) string { return "" }},
		noopOnboarding,
	)
	startServingTestServer(t, server)

	cfg := server.Config()
	healthURL := config.ServerHTTPBaseURL(cfg) + protocol.HealthPath
	healthResp := waitForServeResponse(t, http.DefaultClient, healthURL)
	_ = healthResp.Body.Close()

	remote, err := client.DialConfiguredRemote(context.Background(), cfg)
	if err != nil {
		t.Fatalf("DialConfiguredRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	readiness, err := remote.GetServerReadiness(context.Background(), serverapi.ServerReadinessRequest{})
	if err != nil {
		t.Fatalf("GetServerReadiness: %v", err)
	}
	if readiness.Ready {
		t.Fatalf("ready = true, want false: %+v", readiness)
	}
	if readiness.ServerID == "" || readiness.ProtocolVersion != protocol.Version || readiness.ServerVersion == "" {
		t.Fatalf("missing readiness identity fields: %+v", readiness)
	}
	if readiness.AuthReady || !readiness.AuthRequired {
		t.Fatalf("auth flags = ready:%t required:%t, want ready:false required:true", readiness.AuthReady, readiness.AuthRequired)
	}
	if readiness.Endpoint == "" {
		t.Fatalf("expected endpoint in readiness response: %+v", readiness)
	}
	if len(readiness.Causes) != 1 {
		t.Fatalf("cause count = %d, want 1: %+v", len(readiness.Causes), readiness.Causes)
	}
	assertReadinessRoles(t, readiness.SubagentRoles, []string{"default", "fast", "blocked", "coder"})
	cause := readiness.Causes[0]
	if cause.Code != "server_not_ready" || cause.Severity != "error" || cause.Summary != nil || cause.NextAction != nil {
		t.Fatalf("unexpected generic readiness cause: %+v", cause)
	}
}

func TestMissingConfigFinalizeActivationFailureIsTypedAndRetryConflicts(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	configureServeTestServerPort(t)

	server := startServeTestServer(t, Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, envAuthHandler{}, nil)
	if server.Core != nil || server.deps == nil {
		t.Fatal("expected missing-config serve startup surface")
	}
	metadataBlocker := filepath.Join(server.cfg.PersistenceRoot, "db")
	if err := os.WriteFile(metadataBlocker, []byte("block metadata open"), 0o644); err != nil {
		t.Fatalf("write metadata blocker: %v", err)
	}

	_, err := server.deps.OnboardingFinalizeClient().FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{})
	if !errors.Is(err, serverapi.ErrServerNotReadyActivationFailed) {
		t.Fatalf("finalize error = %v, want activation_failed", err)
	}
	var readyErr *serverapi.ServerNotReadyError
	if !errors.As(err, &readyErr) {
		t.Fatalf("finalize error = %T %v, want ServerNotReadyError", err, err)
	}
	details := readyErr.Details.(serverapi.ServerNotReadyDetails)
	if !details.OnboardingCompleted || details.SettingsPath == nil || *details.SettingsPath == "" || details.Diagnostic == nil || *details.Diagnostic == "" {
		t.Fatalf("activation details = %+v", details)
	}
	if _, statErr := os.Stat(filepath.Join(home, config.ConfigDirName, "config.toml")); statErr != nil {
		t.Fatalf("config should remain written after activation failure: %v", statErr)
	}
	competingLease, competingErr := corepkg.AcquireRootLock(server.cfg.PersistenceRoot)
	if competingLease != nil {
		_ = competingLease.Close()
	}
	if !errors.Is(competingErr, corepkg.ErrPersistenceRootBusy) {
		t.Fatalf("root ownership after activation failure = %v, want ErrPersistenceRootBusy", competingErr)
	}
	if state := server.deps.ServerReadinessState(); state.Ready || state.Reason == nil || *state.Reason != serverapi.ServerNotReadyActivationFailed || state.Diagnostic == nil || *state.Diagnostic == "" {
		t.Fatalf("readiness = %+v, want activation_failed diagnostic", state)
	}
	readiness, statusErr := server.deps.ServerStatusClient().GetServerReadiness(context.Background(), serverapi.ServerReadinessRequest{})
	if statusErr != nil {
		t.Fatalf("GetServerReadiness after activation failure: %v", statusErr)
	}
	if readiness.Ready || len(readiness.Causes) == 0 || readiness.Causes[0].DiagnosticID == "" {
		t.Fatalf("readiness response after activation failure = %+v", readiness)
	}
	_, retryErr := server.deps.OnboardingFinalizeClient().FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{})
	if !errors.Is(retryErr, serverapi.ErrOnboardingFinalizeConfigAlreadyExists) {
		t.Fatalf("retry error = %v, want config_already_exists", retryErr)
	}
}

type finalizeServiceFunc func(context.Context, serverapi.OnboardingFinalizeRequest) (serverapi.OnboardingFinalizeResponse, error)

func (f finalizeServiceFunc) FinalizeOnboarding(ctx context.Context, req serverapi.OnboardingFinalizeRequest) (serverapi.OnboardingFinalizeResponse, error) {
	return f(ctx, req)
}

func TestStartupFinalizeActivationUsesServerOwnedContext(t *testing.T) {
	requestCtx, cancel := context.WithCancel(context.Background())
	cancel()
	activationCtxCanceled := true
	service := startupFinalizeService{
		service: finalizeServiceFunc(func(context.Context, serverapi.OnboardingFinalizeRequest) (serverapi.OnboardingFinalizeResponse, error) {
			return serverapi.OnboardingFinalizeResponse{Completed: true, SettingsPath: "/tmp/config.toml"}, nil
		}),
		activationContext: context.Background(),
		activate: func(ctx context.Context, _ serverapi.OnboardingFinalizeResponse) error {
			activationCtxCanceled = ctx.Err() != nil
			return nil
		},
	}
	if _, err := service.FinalizeOnboarding(requestCtx, serverapi.OnboardingFinalizeRequest{}); err != nil {
		t.Fatalf("FinalizeOnboarding: %v", err)
	}
	if activationCtxCanceled {
		t.Fatal("activation used canceled request context")
	}
}

func writeServeSettings(t *testing.T, home string, contents string) {
	t.Helper()
	settingsDir := filepath.Join(home, config.ConfigDirName)
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatalf("create settings dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "config.toml"), []byte(contents), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}
}

func assertReadinessRoles(t *testing.T, roles []serverapi.SubagentRoleSummary, want []string) {
	t.Helper()
	got := make([]string, 0, len(roles))
	for _, role := range roles {
		got = append(got, role.Name)
	}
	if len(got) != len(want) {
		t.Fatalf("subagent roles = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("subagent roles = %+v, want %+v", got, want)
		}
	}
}

func TestServeFailsWhenConfiguredPortIsOccupied(t *testing.T) {
	workspace := newServeWorkspace(t)
	server := startServeTestServer(t, Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, envAuthHandler{}, noopOnboarding)
	cfg := server.Config()
	releaseServeTestPortForConfig(cfg)
	listener, err := net.Listen("tcp", net.JoinHostPort(cfg.Settings.ServerHost, strconv.Itoa(cfg.Settings.ServerPort)))
	if err != nil {
		t.Fatalf("occupy configured port: %v", err)
	}
	defer func() { _ = listener.Close() }()
	if err := server.Serve(context.Background()); err == nil {
		t.Fatal("expected serve to fail when configured port is occupied")
	}
}
