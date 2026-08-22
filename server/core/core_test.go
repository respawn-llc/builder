package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"core/server/auth"
	serverbootstrap "core/server/bootstrap"
	"core/server/metadata"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionlaunch"
	"core/shared/clientui"
	brand "core/shared/config"
	"core/shared/protoapi"
	capabilitypb "core/shared/protoapi/gen/kent/api/capability"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	runtimepb "core/shared/protoapi/gen/kent/api/runtime"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"

	"google.golang.org/protobuf/types/known/emptypb"
)

func TestNewBuildsReusableServerCore(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if _, err := metadata.RegisterBinding(context.Background(), resolved.Config.PersistenceRoot, resolved.Config.WorkspaceRoot); err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	appCore := newCoreTestApp(t, resolved.Config, auth.EmptyState())

	if appCore.Config().WorkspaceRoot == "" {
		t.Fatal("expected workspace root")
	}
	if appCore.ProjectID() == "" {
		t.Fatal("expected project id")
	}
	if appCore.AuthManager() == nil {
		t.Fatal("expected auth manager")
	}
	if appCore.Background() == nil {
		t.Fatal("expected background manager")
	}
	if appCore.ProjectViewClient() == nil || appCore.ProcessViewClient() == nil || appCore.SessionLaunchClient() == nil || appCore.SessionViewClient() == nil || appCore.SessionLifecycleClient() == nil || appCore.SessionTranscriptClient() == nil || appCore.RunPromptClient() == nil {
		t.Fatal("expected core clients to be wired")
	}
	if appCore.CapabilityFactsClient() == nil {
		t.Fatal("expected capability facts client to be wired")
	}
	if _, err := appCore.ProjectViewClient().ListProjects(context.Background(), &emptypb.Empty{}); err != nil {
		t.Fatalf("ListProjects via core client: %v", err)
	}
	facts, err := appCore.CapabilityFactsClient().GetFacts(context.Background(), &capabilitypb.GetFactsRequest{})
	if err != nil {
		t.Fatalf("GetFacts via core client: %v", err)
	}
	if facts.Defaults.PrimaryModelId == "" {
		t.Fatalf("capability facts missing defaults: %+v", facts)
	}
}

func TestPromptCommandCatalogUsesRequestedWorkspaceRoot(t *testing.T) {
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	t.Setenv("HOME", home)

	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspaceA})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	bindingA, err := metadata.RegisterBinding(context.Background(), resolved.Config.PersistenceRoot, workspaceA)
	if err != nil {
		t.Fatalf("RegisterBinding A: %v", err)
	}
	bindingB, err := metadata.RegisterBinding(context.Background(), resolved.Config.PersistenceRoot, workspaceB)
	if err != nil {
		t.Fatalf("RegisterBinding B: %v", err)
	}
	writeCorePromptFixture(t, workspaceA, "only-a", "A")
	writeCorePromptFixture(t, workspaceB, "only_b", "B")
	appCore := newCoreTestApp(t, resolved.Config, auth.EmptyState())

	client, err := appCore.PromptCommandCatalogClientForProjectWorkspace(context.Background(), bindingB.ProjectID, workspaceB)
	if err != nil {
		t.Fatalf("PromptCommandCatalogClientForProjectWorkspace: %v", err)
	}
	response, err := client.GetPromptCommandCatalog(context.Background(), serverapi.PromptCommandCatalogRequest{})
	if err != nil {
		t.Fatalf("GetPromptCommandCatalog: %v", err)
	}
	foundWorkspaceB := false
	for _, command := range response.Commands {
		if command.Name == "prompt:only_b" && command.Preview == "B" {
			foundWorkspaceB = true
			break
		}
	}
	if !foundWorkspaceB {
		t.Fatalf("workspace B catalog = %+v, want prompt:only_b from B (A binding %q, B binding %q)", response.Commands, bindingA.ProjectID, bindingB.ProjectID)
	}
}

func TestPromptCommandCatalogUsesRegisteredWorktreeRoot(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	worktree := filepath.Join(t.TempDir(), "feature")
	t.Setenv("HOME", home)

	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	binding, err := metadata.RegisterBinding(context.Background(), resolved.Config.PersistenceRoot, workspace)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	appCore := newCoreTestApp(t, resolved.Config, auth.EmptyState())
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("MkdirAll worktree: %v", err)
	}
	if err := appCore.MetadataStore().UpsertWorktreeRecord(context.Background(), metadata.WorktreeRecord{
		ID:            "worktree-catalog-test",
		WorkspaceID:   binding.WorkspaceID,
		CanonicalRoot: worktree,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	writeCorePromptFixture(t, worktree, "only_worktree", "worktree")

	client, err := appCore.PromptCommandCatalogClientForProjectWorkspace(context.Background(), binding.ProjectID, worktree)
	if err != nil {
		t.Fatalf("PromptCommandCatalogClientForProjectWorkspace: %v", err)
	}
	response, err := client.GetPromptCommandCatalog(context.Background(), serverapi.PromptCommandCatalogRequest{})
	if err != nil {
		t.Fatalf("GetPromptCommandCatalog: %v", err)
	}
	for _, command := range response.Commands {
		if command.Name == "prompt:only_worktree" && command.Preview == "worktree" {
			return
		}
	}
	t.Fatalf("worktree catalog = %+v, want prompt:only_worktree", response.Commands)
}

func TestPromptCommandEffectiveWorkspaceResolverUsesSuppliedWorkspace(t *testing.T) {
	persistenceRoot := t.TempDir()
	workspace := t.TempDir()
	writeCorePromptFixture(t, workspace, "pre_session", "effective workspace body")

	resolver := promptCommandEffectiveWorkspaceResolver{persistenceRoot: persistenceRoot}
	got, err := resolver.ResolvePromptCommandForWorkspace(context.Background(), workspace, "prompt:pre_session", "")
	if err != nil {
		t.Fatalf("ResolvePromptCommandForWorkspace: %v", err)
	}
	if got != "effective workspace body" {
		t.Fatalf("resolved body = %q, want effective workspace body", got)
	}
}

func TestPromptCommandWorkspaceRootUsesCurrentWorktree(t *testing.T) {
	target := clientui.SessionExecutionTarget{
		Worktree: &clientui.SessionExecutionWorktreeTarget{Root: "/worktrees/feature"},
	}
	got, err := clientui.SessionExecutionWorkspaceRoot(target, "/workspace/main")
	if err != nil {
		t.Fatalf("SessionExecutionWorkspaceRoot: %v", err)
	}
	if got != "/worktrees/feature" {
		t.Fatalf("workspace root = %q, want current worktree root", got)
	}
}

func TestPromptCommandCatalogRedactsFilesystemCauseAtClientBoundary(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	binding, err := metadata.RegisterBinding(context.Background(), resolved.Config.PersistenceRoot, workspace)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	promptRoot := filepath.Join(workspace, ".kent", "prompts")
	if err := os.MkdirAll(promptRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	missingTarget := filepath.Join(promptRoot, "missing-target.md")
	if err := os.Symlink(missingTarget, filepath.Join(promptRoot, "broken.md")); err != nil {
		t.Fatal(err)
	}
	appCore := newCoreTestApp(t, resolved.Config, auth.EmptyState())
	client, err := appCore.PromptCommandCatalogClientForProjectWorkspace(context.Background(), binding.ProjectID, workspace)
	if err != nil {
		t.Fatalf("PromptCommandCatalogClientForProjectWorkspace: %v", err)
	}
	response, err := client.GetPromptCommandCatalog(context.Background(), serverapi.PromptCommandCatalogRequest{})
	if err != nil {
		t.Fatalf("catalog broken symlink: %v", err)
	}
	for _, entry := range response.Commands {
		if entry.Name == "prompt:broken" {
			t.Fatalf("catalog included broken symlink: %+v", response.Commands)
		}
	}
}

func writeCorePromptFixture(t *testing.T, workspace, name, content string) {
	t.Helper()
	root := filepath.Join(workspace, ".kent", "prompts")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name+".md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestNewProvidesRegistrationSafeClientsForUnregisteredWorkspace(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	appCore := newCoreTestApp(t, resolved.Config, auth.EmptyState())

	if got := appCore.ProjectID(); got != "" {
		t.Fatalf("project id = %q, want empty for unregistered workspace", got)
	}
	if appCore.SessionLaunchClient() == nil {
		t.Fatal("expected session launch client stub")
	}
	if appCore.RunPromptClient() == nil {
		t.Fatal("expected run prompt client stub")
	}
	_, err = appCore.SessionLaunchClient().PlanSession(context.Background(), &sessionlaunchpb.SessionPlanRequest{})
	if !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		t.Fatalf("PlanSession error = %v, want ErrWorkspaceNotRegistered", err)
	}
	_, err = appCore.RunPromptClient().RunPrompt(context.Background(), serverapi.RunPromptRequest{}, nil)
	if !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		t.Fatalf("RunPrompt error = %v, want ErrWorkspaceNotRegistered", err)
	}
	if _, err := appCore.ProjectViewClient().ListProjects(context.Background(), &emptypb.Empty{}); err != nil {
		t.Fatalf("ListProjects via core client: %v", err)
	}
}

func TestNewRejectsSecondCoreForSamePersistenceRoot(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	newCoreTestApp(t, resolved.Config, auth.EmptyState())
	generatedSkillsRoot := filepath.Join(home, brand.ConfigDirName, ".generated", "skills")
	if entries, err := os.ReadDir(generatedSkillsRoot); err != nil {
		t.Fatalf("expected first core to seed generated skills: %v", err)
	} else if len(entries) == 0 {
		t.Fatal("expected first core to seed at least one generated skill")
	}

	authSupportB, err := serverbootstrap.BuildAuthSupport(auth.NewMemoryStore(auth.EmptyState()), nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthSupport B: %v", err)
	}
	runtimeSupportB, err := serverbootstrap.BuildRuntimeSupport(resolved.Config)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport B: %v", err)
	}
	t.Cleanup(func() { _ = runtimeSupportB.Background.Close() })

	_, err = New(resolved.Config, authSupportB, runtimeSupportB)
	if !errors.Is(err, ErrPersistenceRootBusy) {
		t.Fatalf("New second error = %v, want ErrPersistenceRootBusy", err)
	}
}

func TestSessionLaunchClientForProjectWorkspaceRejectsMissingProject(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	appCore := newCoreTestApp(t, resolved.Config, auth.EmptyState())

	_, err = appCore.SessionLaunchClientForProjectWorkspace(context.Background(), "project-missing", workspace)
	if !errors.Is(err, serverapi.ErrProjectNotFound) {
		t.Fatalf("SessionLaunchClientForProjectWorkspace error = %v, want ErrProjectNotFound", err)
	}
}

func TestSessionLaunchClientForProjectWorkspaceRejectsUnavailableProjectRoot(t *testing.T) {
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	t.Setenv("HOME", home)

	resolvedA, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspaceA})
	if err != nil {
		t.Fatalf("ResolveConfig A: %v", err)
	}
	binding, err := metadata.RegisterBinding(context.Background(), resolvedA.Config.PersistenceRoot, resolvedA.Config.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	missingRoot := filepath.Join(t.TempDir(), "workspace-moved")
	if err := os.Rename(workspaceA, missingRoot); err != nil {
		t.Fatalf("Rename workspaceA: %v", err)
	}

	resolvedB, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspaceB})
	if err != nil {
		t.Fatalf("ResolveConfig B: %v", err)
	}
	appCore := newCoreTestApp(t, resolvedB.Config, auth.EmptyState())

	_, err = appCore.SessionLaunchClientForProjectWorkspace(context.Background(), binding.ProjectID, workspaceB)
	if !errors.Is(err, serverapi.ErrProjectUnavailable) {
		t.Fatalf("SessionLaunchClientForProjectWorkspace error = %v, want ErrProjectUnavailable", err)
	}
	unavailable, ok := serverapi.AsProjectUnavailable(err)
	if !ok {
		t.Fatalf("expected ProjectUnavailableError, got %v", err)
	}
	if unavailable.ProjectID != binding.ProjectID || unavailable.Availability != clientui.ProjectAvailabilityMissing {
		t.Fatalf("unexpected unavailable project: %+v", unavailable)
	}
}

func TestSessionLaunchClientForProjectWorkspaceUsesWorkspaceLocalConfig(t *testing.T) {
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(workspaceB, brand.ConfigDirName), 0o755); err != nil {
		t.Fatalf("create workspace config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceB, brand.ConfigDirName, "config.toml"), []byte("model = \"workspace-b-model\"\nthinking_level = \"high\"\n"), 0o644); err != nil {
		t.Fatalf("write workspace config: %v", err)
	}

	resolvedA, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspaceA})
	if err != nil {
		t.Fatalf("ResolveConfig A: %v", err)
	}
	resolvedB, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspaceB})
	if err != nil {
		t.Fatalf("ResolveConfig B: %v", err)
	}
	bindingB, err := metadata.RegisterBinding(context.Background(), resolvedB.Config.PersistenceRoot, resolvedB.Config.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding B: %v", err)
	}
	appCore := newCoreTestApp(t, resolvedA.Config, auth.EmptyState())

	client, err := appCore.SessionLaunchClientForProjectWorkspace(context.Background(), bindingB.ProjectID, workspaceB)
	if err != nil {
		t.Fatalf("SessionLaunchClientForProjectWorkspace: %v", err)
	}
	intent, err := protoapi.SessionLaunchIntentToProto(
		serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
	)
	if err != nil {
		t.Fatalf("convert Session launch intent: %v", err)
	}
	plan, err := client.PlanSession(context.Background(), &sessionlaunchpb.SessionPlanRequest{
		Mode:   sessionlaunchpb.SessionLaunchMode_SESSION_LAUNCH_MODE_INTERACTIVE,
		Intent: intent,
	})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if plan.Plan.ActiveSettings.Model != "workspace-b-model" || plan.Plan.ActiveSettings.ThinkingLevel != "high" {
		t.Fatalf("unexpected active settings: %+v", plan.Plan.ActiveSettings)
	}
}
func TestCoreComposedWorkspaceDraftServicesShareLane(t *testing.T) {
	workspace := t.TempDir()
	configRoot := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(configRoot, "config.toml"),
		[]byte("tools.ask_question = false\n\n[subagents.worker]\ntools.ask_question = true\n"),
		0o600,
	); err != nil {
		t.Fatalf("write draft service config: %v", err)
	}
	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace, LoadOptions: brand.LoadOptions{ConfigRoot: configRoot}})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := metadata.RegisterBinding(t.Context(), resolved.Config.PersistenceRoot, workspace)
	if err != nil {
		t.Fatal(err)
	}
	appCore := newCoreTestApp(t, resolved.Config, auth.EmptyState())
	ctx := projectContext{config: resolved.Config, projectID: "project-a", workspaceID: binding.WorkspaceID, projectRoot: workspace, projectSession: t.TempDir()}
	first := appCore.sessionLaunchServiceForProjectContext(ctx)
	ctx.projectID = "project-b"
	second := appCore.sessionLaunchServiceForProjectContext(ctx)
	ctx.workspaceID = "workspace-b"
	if third := appCore.sessionLaunchServiceForProjectContext(ctx); third == first {
		t.Fatal("workspace cache key ignored workspace identity")
	}
	entered, release := make(chan struct{}), make(chan struct{})
	message := "new"
	go func() {
		_, err := first.TransformWorkspaceChatDraftAggregate(t.Context(), func(r sessionlaunch.WorkspaceChatDraftResolution) (sessionlaunch.WorkspaceChatDraft, error) {
			close(entered)
			<-release
			next := r.Baselines["worker"]
			next.Message = "new"
			return next, nil
		})
		if err != nil {
			t.Errorf("first: %v", err)
		}
	}()
	<-entered
	done := make(chan error, 1)
	go func() {
		_, err := second.TransformWorkspaceChatDraftAggregate(t.Context(), func(r sessionlaunch.WorkspaceChatDraftResolution) (sessionlaunch.WorkspaceChatDraft, error) {
			r.Draft.Fast = true
			return r.Draft, nil
		})
		done <- err
	}()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	operations := []struct {
		name         string
		request      *sessionlaunchpb.WorkspaceChatDraftRequest
		availability runtimepb.GoalAvailability
	}{
		{
			name: "update message",
			request: &sessionlaunchpb.WorkspaceChatDraftRequest{
				Operation: &sessionlaunchpb.WorkspaceChatDraftRequest_UpdateMessage{UpdateMessage: message},
			},
			availability: runtimepb.GoalAvailability_GOAL_AVAILABILITY_AVAILABLE,
		},
		{
			name: "clear",
			request: &sessionlaunchpb.WorkspaceChatDraftRequest{
				Operation: &sessionlaunchpb.WorkspaceChatDraftRequest_Clear{Clear: &emptypb.Empty{}},
			},
			availability: runtimepb.GoalAvailability_GOAL_AVAILABILITY_AGENT_CAPABILITY_MISSING,
		},
	}
	for _, operation := range operations {
		if response, err := second.WorkspaceChatDraft(t.Context(), operation.request); err != nil || response.GoalAvailability != operation.availability {
			t.Fatalf("%s response=%+v err=%v", operation.name, response, err)
		}
	}
	got, err := first.ResolveWorkspaceChatDraftAggregate(t.Context())
	if err != nil || got.Draft.Message != "" || got.Draft.Fast {
		t.Fatalf("aggregate=%+v err=%v", got.Draft, err)
	}
}

func TestCoreMaterializesWorkspaceChatAtServerBoundary(t *testing.T) {
	workspace := t.TempDir()
	configRoot := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(configRoot, "config.toml"),
		[]byte("priority_request_mode = true\ntools.ask_question = true\n\n[provider_capabilities]\nprovider_id = \"openai\"\nsupports_responses_api = true\nis_openai_first_party = true\n\n[subagents.worker]\nthinking_level = \"high\"\n"),
		0o600,
	); err != nil {
		t.Fatalf("write materialization config: %v", err)
	}
	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{
		WorkspaceRoot: workspace,
		LoadOptions:   brand.LoadOptions{ConfigRoot: configRoot},
	})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	binding, err := metadata.RegisterBinding(t.Context(), resolved.Config.PersistenceRoot, workspace)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	appCore := newCoreTestApp(t, resolved.Config, auth.EmptyState())
	client, err := appCore.SessionLaunchClientForProjectWorkspace(t.Context(), binding.ProjectID, workspace)
	if err != nil {
		t.Fatalf("SessionLaunchClientForProjectWorkspace: %v", err)
	}
	list := func() *projectpb.SessionPageSuccess {
		limit := int32(20)
		page, listErr := appCore.ProjectViewClient().ListSessionPage(t.Context(), &projectpb.SessionPageRequest{
			ProjectId: binding.ProjectID,
			Category:  projectpb.SessionCategory_SESSION_CATEGORY_MAIN,
			Limit:     &limit,
		})
		if listErr != nil {
			t.Fatalf("ListSessionPage: %v", listErr)
		}
		return page
	}

	if page := list(); len(page.Sessions) != 0 {
		t.Fatalf("untouched lazy Chat exposed Sessions: %+v", page.Sessions)
	}
	if _, err := client.WorkspaceChatDraft(t.Context(), &sessionlaunchpb.WorkspaceChatDraftRequest{
		Operation: &sessionlaunchpb.WorkspaceChatDraftRequest_Clear{Clear: &emptypb.Empty{}},
	}); err != nil {
		t.Fatalf("clear workspace Chat draft: %v", err)
	}
	if page := list(); len(page.Sessions) != 0 {
		t.Fatalf("cleared lazy Chat exposed Sessions: %+v", page.Sessions)
	}

	draft := metadata.WorkspaceChatDraftDocument{
		Message:        "unsent complete draft",
		Agent:          "default",
		Supervisor:     "all",
		Thinking:       "medium",
		Fast:           true,
		Questions:      false,
		AutoCompaction: false,
	}
	if err := appCore.MetadataStore().ReplaceWorkspaceChatDraft(t.Context(), binding.WorkspaceID, &draft); err != nil {
		t.Fatalf("ReplaceWorkspaceChatDraft: %v", err)
	}
	materialized, err := client.MaterializeWorkspaceChat(t.Context(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("MaterializeWorkspaceChat: %v", err)
	}
	record, err := appCore.MetadataStore().ResolvePersistedSession(t.Context(), materialized.SessionId)
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	state, err := session.ChatDraftStateFromMeta(*record.Meta)
	if err != nil {
		t.Fatalf("ChatDraftStateFromMeta: %v", err)
	}
	if state.Message != draft.Message || state.Agent != draft.Agent ||
		state.Settings == nil ||
		*state.Settings.Supervisor != draft.Supervisor ||
		*state.Settings.Thinking != draft.Thinking ||
		*state.Settings.Fast != draft.Fast ||
		*state.Settings.Questions != draft.Questions ||
		*state.Settings.AutoCompaction != draft.AutoCompaction {
		t.Fatalf("materialized Chat state = %+v, want %+v", state, draft)
	}
	if record.Meta.Name != "" || record.Meta.FirstPromptPreview != "" ||
		record.Meta.ModelRequestCount != 0 || record.Meta.Locked != nil {
		t.Fatalf("materialized Session has accepted-turn facts: %+v", record.Meta)
	}
	store, err := session.Open(record.SessionDir, appCore.MetadataStore().AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("open materialized Session: %v", err)
	}
	records, err := sessiontest.CollectRecords(store)
	if err != nil {
		t.Fatalf("CollectRecords: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("materialization created transcript records: %+v", records)
	}
	if page := list(); len(page.Sessions) != 1 {
		t.Fatalf("materialized Session list = %+v, want one", page.Sessions)
	}
	if stored, err := appCore.MetadataStore().ReadWorkspaceChatDraft(t.Context(), binding.WorkspaceID); err != nil || stored != nil {
		t.Fatalf("workspace draft after materialization = %+v, err=%v", stored, err)
	}
	if err := store.SetInputDraft("ordinary metadata reimport"); err != nil {
		t.Fatalf("SetInputDraft: %v", err)
	}
	if page := list(); len(page.Sessions) != 1 {
		t.Fatalf("ordinary reimport lost materialized Session: %+v", page.Sessions)
	}

	worker := "worker"
	materializedSessionID, err := runtimeids.ParseSessionID(materialized.SessionId)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	intent, err := protoapi.SessionLaunchIntentToProto(
		serverapi.OpenExistingSessionLaunchIntent(materializedSessionID),
	)
	if err != nil {
		t.Fatalf("convert Session launch intent: %v", err)
	}
	overrides, err := protoapi.RunPromptOverridesToProto(serverapi.RunPromptOverrides{AgentRole: &worker})
	if err != nil {
		t.Fatalf("convert Run Prompt overrides: %v", err)
	}
	if _, err := client.PlanSession(t.Context(), &sessionlaunchpb.SessionPlanRequest{
		Mode:      sessionlaunchpb.SessionLaunchMode_SESSION_LAUNCH_MODE_INTERACTIVE,
		Intent:    intent,
		Overrides: overrides,
	}); err != nil {
		t.Fatalf("materialized Agent was not editable: %v", err)
	}
	if _, err := appCore.RuntimeControlClient().SubmitUserTurn(t.Context(), serverapi.RuntimeSubmitUserTurnRequest{
		SessionID: materialized.SessionId,
		Input:     runtimeinput.Text("ordinary operation fails without an active runtime"),
	}); err == nil {
		t.Fatal("ordinary text operation unexpectedly succeeded without a runtime")
	}
	if page := list(); len(page.Sessions) != 1 {
		t.Fatalf("failed ordinary operation rolled back Session: %+v", page.Sessions)
	}

	if _, err := client.MaterializeWorkspaceChat(t.Context(), &emptypb.Empty{}); err != nil {
		t.Fatalf("ignored blank materialization response: %v", err)
	}
	message, err := client.WorkspaceChatDraft(t.Context(), &sessionlaunchpb.WorkspaceChatDraftRequest{
		Operation: &sessionlaunchpb.WorkspaceChatDraftRequest_ReadMessage{ReadMessage: &emptypb.Empty{}},
	})
	if err != nil {
		t.Fatalf("read fresh workspace Chat: %v", err)
	}
	if message.Message != "" {
		t.Fatalf("fresh workspace Chat message = %q, want empty", message.Message)
	}
	if page := list(); len(page.Sessions) != 2 {
		t.Fatalf("ignored committed response list = %+v, want two Sessions", page.Sessions)
	}
}

func TestSessionChatSettingsPreparationUsesAuthoritativePersistenceRoot(t *testing.T) {
	workspace := t.TempDir()
	persistenceRoot := t.TempDir()
	t.Setenv(brand.PersistenceRootEnvName, t.TempDir())
	if err := os.WriteFile(
		filepath.Join(persistenceRoot, "config.toml"),
		[]byte("[subagents.worker]\nthinking_level = \"high\"\n"),
		0o600,
	); err != nil {
		t.Fatalf("write custom-root config: %v", err)
	}
	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{
		WorkspaceRoot: workspace,
		LoadOptions:   brand.LoadOptions{ConfigRoot: persistenceRoot},
	})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	binding, err := metadata.RegisterBinding(t.Context(), persistenceRoot, workspace)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	appCore := newCoreTestApp(t, resolved.Config, auth.EmptyState())
	client, err := appCore.SessionLaunchClientForProjectWorkspace(t.Context(), binding.ProjectID, workspace)
	if err != nil {
		t.Fatalf("SessionLaunchClientForProjectWorkspace: %v", err)
	}
	materialized, err := client.MaterializeWorkspaceChat(t.Context(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("MaterializeWorkspaceChat: %v", err)
	}
	store, err := session.OpenByID(
		persistenceRoot,
		materialized.SessionId,
		appCore.MetadataStore().AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("OpenByID: %v", err)
	}

	prepared, err := (sessionChatSettingsPreparationResolver{
		metadataStore:   appCore.MetadataStore(),
		authManager:     appCore.AuthManager(),
		persistenceRoot: persistenceRoot,
	}).PrepareSessionChatSettings(t.Context(), store, "worker")
	if err != nil {
		t.Fatalf("PrepareSessionChatSettings: %v", err)
	}
	if prepared.Baseline.Thinking != "high" {
		t.Fatalf("worker Thinking = %q, want custom-root value high", prepared.Baseline.Thinking)
	}
}

func TestChatSettingsMaterializedReadUsesDetachedSessionSnapshotWithoutRebinding(t *testing.T) {
	workspace := t.TempDir()
	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{
		WorkspaceRoot: workspace,
		LoadOptions:   brand.LoadOptions{ConfigRoot: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	binding, err := metadata.RegisterBinding(t.Context(), resolved.Config.PersistenceRoot, workspace)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	appCore := newCoreTestApp(t, resolved.Config, auth.EmptyState())
	store, err := session.Create(
		filepath.Join(resolved.Config.PersistenceRoot, "projects", binding.ProjectID, "sessions"),
		"detached",
		resolved.Config.WorkspaceRoot,
		sessioncontract.SessionCategoryMain,
		appCore.MetadataStore().AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	if err := appCore.MetadataStore().UpdateSessionExecutionTarget(
		t.Context(),
		metadata.SessionExecutionTargetUpdate{SessionID: store.Meta().SessionID},
	); err != nil {
		t.Fatalf("detach Session execution target: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}

	response, err := appCore.ChatSettingsClient().ReadChatSettings(
		t.Context(),
		serverapi.ChatSettingsReadRequest{
			Target: serverapi.SessionChatSettingsTarget(sessionID),
		},
	)
	if err != nil {
		t.Fatalf("ReadChatSettings detached Session: %v", err)
	}
	if response.Session == nil || response.Session.SessionID != sessionID {
		t.Fatalf("detached response = %+v", response)
	}
	executionTarget, err := appCore.MetadataStore().ResolveSessionExecutionTarget(
		t.Context(),
		store.Meta().SessionID,
	)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if executionTarget.WorkspaceID != "" {
		t.Fatalf("detached read rebound workspace %q", executionTarget.WorkspaceID)
	}
}

func TestSessionChatSettingsPreparationUsesPersistedPromptFacingEndpoint(t *testing.T) {
	workspace := t.TempDir()
	persistenceRoot := t.TempDir()
	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{
		WorkspaceRoot: workspace,
		LoadOptions:   brand.LoadOptions{ConfigRoot: persistenceRoot},
	})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	resolved.Config.Settings.Model = "gpt-5.6-sol"
	resolved.Config.Settings.OpenAIBaseURL = "https://api.openai.com/v1"
	resolved.Config.Settings.PriorityRequestMode = true
	binding, err := metadata.RegisterBinding(t.Context(), persistenceRoot, workspace)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	appCore := newCoreTestApp(t, resolved.Config, auth.EmptyState())
	client, err := appCore.SessionLaunchClientForProjectWorkspace(t.Context(), binding.ProjectID, workspace)
	if err != nil {
		t.Fatalf("SessionLaunchClientForProjectWorkspace: %v", err)
	}
	materialized, err := client.MaterializeWorkspaceChat(t.Context(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("MaterializeWorkspaceChat: %v", err)
	}
	store, err := session.OpenByID(
		persistenceRoot,
		materialized.SessionId,
		appCore.MetadataStore().AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("OpenByID: %v", err)
	}
	if err := store.SetContinuationContext(session.ContinuationContext{
		OpenAIBaseURL: textutil.Value("https://compatible.example/v1"),
	}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}

	prepared, err := (sessionChatSettingsPreparationResolver{
		metadataStore:   appCore.MetadataStore(),
		authManager:     appCore.AuthManager(),
		persistenceRoot: persistenceRoot,
	}).PrepareSessionChatSettings(t.Context(), store, brand.DefaultSubagentRole)
	if err != nil {
		t.Fatalf("PrepareSessionChatSettings: %v", err)
	}
	if prepared.FastAvailable || prepared.Baseline.Fast {
		t.Fatalf("prepared Fast = available:%t enabled:%t, want unavailable and disabled for compatible endpoint", prepared.FastAvailable, prepared.Baseline.Fast)
	}
}

func TestSessionChatSettingsPreparationUsesLockedPromptFacingModelCapabilities(t *testing.T) {
	workspace := t.TempDir()
	persistenceRoot := t.TempDir()
	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{
		WorkspaceRoot: workspace,
		LoadOptions:   brand.LoadOptions{ConfigRoot: persistenceRoot},
	})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	resolved.Config.Settings.Model = "gpt-5.6-sol"
	binding, err := metadata.RegisterBinding(t.Context(), persistenceRoot, workspace)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	appCore := newCoreTestApp(t, resolved.Config, auth.EmptyState())
	client, err := appCore.SessionLaunchClientForProjectWorkspace(t.Context(), binding.ProjectID, workspace)
	if err != nil {
		t.Fatalf("SessionLaunchClientForProjectWorkspace: %v", err)
	}
	materialized, err := client.MaterializeWorkspaceChat(t.Context(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("MaterializeWorkspaceChat: %v", err)
	}
	store, err := session.OpenByID(
		persistenceRoot,
		materialized.SessionId,
		appCore.MetadataStore().AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("OpenByID: %v", err)
	}
	if err := store.MarkModelDispatchLocked(session.LockedContract{Model: "gpt-5"}); err != nil {
		t.Fatalf("MarkModelDispatchLocked: %v", err)
	}

	prepared, err := (sessionChatSettingsPreparationResolver{
		metadataStore:   appCore.MetadataStore(),
		authManager:     appCore.AuthManager(),
		persistenceRoot: persistenceRoot,
	}).PrepareSessionChatSettings(t.Context(), store, brand.DefaultSubagentRole)
	if err != nil {
		t.Fatalf("PrepareSessionChatSettings: %v", err)
	}
	if slices.Contains(prepared.SupportedThinkingValues, "ultra") {
		t.Fatalf("locked gpt-5 Thinking values = %v, want no ultra", prepared.SupportedThinkingValues)
	}
}

func TestSessionLaunchClientForProjectWorkspaceRejectsInaccessibleProjectRoot(t *testing.T) {
	home := t.TempDir()
	parent := filepath.Join(t.TempDir(), "blocked-parent")
	workspaceA := filepath.Join(parent, "workspace-a")
	workspaceB := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(workspaceA, 0o755); err != nil {
		t.Fatalf("create workspace A: %v", err)
	}

	resolvedA, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspaceA})
	if err != nil {
		t.Fatalf("ResolveConfig A: %v", err)
	}
	binding, err := metadata.RegisterBinding(context.Background(), resolvedA.Config.PersistenceRoot, resolvedA.Config.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	if err := os.Chmod(parent, 0); err != nil {
		t.Fatalf("make workspace parent inaccessible: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(parent, 0o755); err != nil {
			t.Fatalf("restore workspace parent permissions: %v", err)
		}
	})
	if _, err := os.Stat(workspaceA); err == nil {
		t.Skip("filesystem permissions do not prevent stat for current user")
	}
	metadataStore, err := metadata.Open(resolvedA.Config.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	overview, err := metadataStore.GetProjectOverview(context.Background(), binding.ProjectID)
	if err != nil {
		t.Fatalf("GetProjectOverview: %v", err)
	}
	if overview.Project.RootPath != binding.CanonicalRoot || overview.Project.Availability != clientui.ProjectAvailabilityInaccessible {
		t.Fatalf("overview = %+v, want inaccessible root %q", overview.Project, binding.CanonicalRoot)
	}

	resolvedB, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspaceB})
	if err != nil {
		t.Fatalf("ResolveConfig B: %v", err)
	}
	appCore := newCoreTestApp(t, resolvedB.Config, auth.EmptyState())

	_, err = appCore.SessionLaunchClientForProjectWorkspace(context.Background(), binding.ProjectID, workspaceB)
	if !errors.Is(err, serverapi.ErrProjectUnavailable) {
		t.Fatalf("SessionLaunchClientForProjectWorkspace error = %v, want ErrProjectUnavailable", err)
	}
	unavailable, ok := serverapi.AsProjectUnavailable(err)
	if !ok {
		t.Fatalf("expected ProjectUnavailableError, got %v", err)
	}
	if unavailable.ProjectID != binding.ProjectID || unavailable.Availability != clientui.ProjectAvailabilityInaccessible {
		t.Fatalf("unexpected unavailable project: %+v", unavailable)
	}
}

func newCoreTestApp(t *testing.T, cfg brand.App, state auth.State) *Core {
	return newCoreTestAppWithLoadOptions(t, cfg, state, brand.LoadOptions{})
}

func newCoreTestAppWithLoadOptions(t *testing.T, cfg brand.App, state auth.State, loadOptions brand.LoadOptions) *Core {
	t.Helper()
	authSupport, err := serverbootstrap.BuildAuthSupport(auth.NewMemoryStore(state), nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthSupport: %v", err)
	}
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(cfg)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}
	t.Cleanup(func() { _ = runtimeSupport.Background.Close() })
	appCore, err := NewWithContextOptions(t.Context(), cfg, authSupport, runtimeSupport, Options{
		WorkspaceConfigLoadOptions: loadOptions,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = appCore.Close() })
	return appCore
}
