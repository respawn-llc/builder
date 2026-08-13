package core

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"core/server/auth"
	serverbootstrap "core/server/bootstrap"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionruntime"
	"core/shared/clientui"
	brand "core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/toolspec"
)

type lazyGoalBlockingClient struct {
	started     chan struct{}
	startedOnce sync.Once
}

func (c *lazyGoalBlockingClient) Generate(ctx context.Context, _ llm.Request) (llm.Response, error) {
	c.startedOnce.Do(func() { close(c.started) })
	<-ctx.Done()
	return llm.Response{}, context.Cause(ctx)
}

func (*lazyGoalBlockingClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.InferProviderCapabilities("openai")
}

type lazyGoalFailingClient struct {
	started      chan struct{}
	returned     chan struct{}
	startedOnce  sync.Once
	returnedOnce sync.Once
	mu           sync.Mutex
	calls        int
	release      chan struct{}
}

type lazyGoalLaunchClient interface {
	PlanSession(context.Context, serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error)
	MaterializeWorkspaceChat(context.Context, serverapi.WorkspaceChatMaterializeRequest) (serverapi.WorkspaceChatMaterializeResponse, error)
}

type lazyGoalFixture struct {
	appCore      *Core
	binding      metadata.Binding
	launch       lazyGoalLaunchClient
	draft        metadata.WorkspaceChatDraftDocument
	materialized serverapi.WorkspaceChatMaterializeResponse
	planned      serverapi.SessionPlanResponse
	activation   serverapi.SessionRuntimeActivateResponse
	ownerID      string
	released     bool
}

func (c *lazyGoalFailingClient) Generate(ctx context.Context, _ llm.Request) (llm.Response, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	c.startedOnce.Do(func() { close(c.started) })
	select {
	case <-c.release:
	case <-ctx.Done():
		return llm.Response{}, context.Cause(ctx)
	}
	c.returnedOnce.Do(func() { close(c.returned) })
	return llm.Response{}, &llm.AuthError{Err: auth.ErrAuthNotConfigured}
}

func (*lazyGoalFailingClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.InferProviderCapabilities("openai")
}

func TestCoreLazyChatMaterializesBeforeGoalSet(t *testing.T) {
	blocking := &lazyGoalBlockingClient{started: make(chan struct{})}
	fixture := newLazyGoalFixture(t, map[toolspec.ID]bool{toolspec.ToolAskQuestion: true}, blocking)
	appCore := fixture.appCore
	binding := fixture.binding
	goalClient := appCore.RuntimeControlClient()

	page, err := appCore.ProjectViewClient().ListSessionPage(t.Context(), serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategoryMain,
		Limit:     intPointer(20),
	})
	if err != nil {
		t.Fatalf("ListSessionPage before materialization: %v", err)
	}
	if len(page.Sessions) != 0 {
		t.Fatalf("untouched lazy Chat exposed Sessions: %+v", page.Sessions)
	}

	fixture.materializeAndActivate(t, "lazy-goal")
	materialized := fixture.materialized
	response, err := goalClient.SetGoal(t.Context(), serverapi.RuntimeGoalSetRequest{
		ClientRequestID: "lazy-goal-set",
		SessionID:       materialized.SessionID.String(),
		Objective:       "ship the lazy Chat Goal",
		Actor:           "user",
	})
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if response.Goal == nil || response.Goal.Objective != "ship the lazy Chat Goal" || response.Goal.Status != clientui.RuntimeGoalStatusActive {
		t.Fatalf("SetGoal response = %+v, want active Goal", response)
	}
	select {
	case <-blocking.started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for first model request")
	}

	record, err := appCore.MetadataStore().ResolvePersistedSession(t.Context(), materialized.SessionID.String())
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	state, err := session.ChatDraftStateFromMeta(*record.Meta)
	if err != nil {
		t.Fatalf("ChatDraftStateFromMeta: %v", err)
	}
	if state.Message != fixture.draft.Message || state.Agent != fixture.draft.Agent ||
		state.Settings == nil ||
		*state.Settings.Supervisor != fixture.draft.Supervisor ||
		*state.Settings.Thinking != fixture.draft.Thinking ||
		*state.Settings.Fast != fixture.draft.Fast ||
		*state.Settings.Questions != fixture.draft.Questions ||
		*state.Settings.AutoCompaction != fixture.draft.AutoCompaction {
		t.Fatalf("materialized Chat state = %+v, want %+v", state, fixture.draft)
	}
	if record.Meta.Goal == nil || record.Meta.Goal.Objective != response.Goal.Objective || record.Meta.Goal.Status != session.GoalStatusActive {
		t.Fatalf("persisted Goal = %+v, want active Goal", record.Meta.Goal)
	}
	fixture.requireGoalNoticeWithoutUserMessage(t)
	page, err = appCore.ProjectViewClient().ListSessionPage(t.Context(), serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategoryMain,
		Limit:     intPointer(20),
	})
	if err != nil {
		t.Fatalf("ListSessionPage after Goal: %v", err)
	}
	if len(page.Sessions) != 1 {
		t.Fatalf("materialized Session list = %+v, want one", page.Sessions)
	}
	fixture.release(t)
}

func TestCoreLazyChatGoalRejectsUnavailableCapabilityAfterMaterialization(t *testing.T) {
	client := &lazyGoalBlockingClient{started: make(chan struct{})}
	fixture := newLazyGoalFixture(t, map[toolspec.ID]bool{toolspec.ToolExecCommand: true}, client)
	fixture.materializeAndActivate(t, "lazy-capability")
	materialized := fixture.materialized
	appCore := fixture.appCore
	if containsTool(fixture.planned.Plan.EnabledToolIDs, string(toolspec.ToolAskQuestion)) {
		t.Fatalf("planned materialized tool contract unexpectedly enables ask_question: %v", fixture.planned.Plan.EnabledToolIDs)
	}

	_, err := appCore.RuntimeControlClient().SetGoal(t.Context(), serverapi.RuntimeGoalSetRequest{
		ClientRequestID: "lazy-capability-set",
		SessionID:       materialized.SessionID.String(),
		Objective:       "this must be rejected",
		Actor:           "user",
	})
	if !errors.Is(err, runtime.ErrGoalRequiresAskQuestion) {
		t.Fatalf("SetGoal error = %v, want ErrGoalRequiresAskQuestion", err)
	}
	record, err := appCore.MetadataStore().ResolvePersistedSession(t.Context(), materialized.SessionID.String())
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	if record.Meta.Goal != nil {
		t.Fatalf("rejected capability Goal persisted: %+v", record.Meta.Goal)
	}
	fixture.requireNoTranscript(t)
	fixture.requireOneSession(t)
	fixture.release(t)
}

func TestCoreLazyChatGoalAdmissionRejectionRetainsMaterializedSession(t *testing.T) {
	fixture := newLazyGoalFixture(t, map[toolspec.ID]bool{toolspec.ToolAskQuestion: true}, &lazyGoalBlockingClient{started: make(chan struct{})})
	fixture.materialize(t)
	appCore := fixture.appCore
	materialized := fixture.materialized
	releaseBlock, err := appCore.safeBundles().Runtime.runtimeAuthority.BlockSessionStarts(
		t.Context(),
		[]runtimeids.SessionID{materialized.SessionID},
		sessionruntime.SessionStartBlockMaintenance,
	)
	if err != nil {
		t.Fatalf("BlockSessionStarts: %v", err)
	}
	t.Cleanup(func() { _ = releaseBlock.Close(context.Background()) })

	_, err = appCore.RuntimeControlClient().SetGoal(t.Context(), serverapi.RuntimeGoalSetRequest{
		ClientRequestID: "lazy-admission-set",
		SessionID:       materialized.SessionID.String(),
		Objective:       "valid nonblank objective",
		Actor:           "user",
	})
	if !errors.Is(err, sessionruntime.ErrSessionStartsBlocked) {
		t.Fatalf("SetGoal error = %v, want ErrSessionStartsBlocked", err)
	}
	record, err := appCore.MetadataStore().ResolvePersistedSession(t.Context(), materialized.SessionID.String())
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	if record.Meta.Goal != nil {
		t.Fatalf("admission-rejected Goal persisted: %+v", record.Meta.Goal)
	}
	fixture.requireNoTranscript(t)
	fixture.requireOneSession(t)
}

func TestCoreLazyChatGoalProviderFailurePreservesAcceptedGoal(t *testing.T) {
	client := &lazyGoalFailingClient{
		started:  make(chan struct{}),
		returned: make(chan struct{}),
		release:  make(chan struct{}),
	}
	fixture := newLazyGoalFixture(t, map[toolspec.ID]bool{toolspec.ToolAskQuestion: true}, client)
	fixture.materializeAndActivate(t, "lazy-failure")
	appCore := fixture.appCore
	materialized := fixture.materialized
	response, err := appCore.RuntimeControlClient().SetGoal(t.Context(), serverapi.RuntimeGoalSetRequest{
		ClientRequestID: "lazy-failure-set",
		SessionID:       materialized.SessionID.String(),
		Objective:       "accepted before provider failure",
		Actor:           "user",
	})
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if response.Goal == nil || response.Goal.Status != clientui.RuntimeGoalStatusActive {
		t.Fatalf("SetGoal response = %+v, want active Goal", response)
	}
	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for first model request")
	}
	close(client.release)
	select {
	case <-client.returned:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for provider failure return")
	}
	settleCtx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for {
		view, viewErr := appCore.SessionViewClient().GetSessionMainView(settleCtx, serverapi.SessionMainViewRequest{
			SessionID: materialized.SessionID.String(),
		})
		if viewErr == nil && view.MainView.Activity.State == clientui.RuntimeActivityRegisteredIdle {
			break
		}
		select {
		case <-settleCtx.Done():
			t.Fatalf("runtime did not settle idle after provider failure: view error=%v", viewErr)
		case <-time.After(10 * time.Millisecond):
		}
	}
	fixture.release(t)
	client.mu.Lock()
	calls := client.calls
	client.mu.Unlock()
	if calls != 1 {
		t.Fatalf("provider Generate calls = %d, want one", calls)
	}
	record, err := appCore.MetadataStore().ResolvePersistedSession(t.Context(), materialized.SessionID.String())
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	if record.Meta.Goal == nil || record.Meta.Goal.ID != response.Goal.ID || record.Meta.Goal.Status != session.GoalStatusActive {
		t.Fatalf("durable Goal = %+v, want accepted active Goal", record.Meta.Goal)
	}
	fixture.requireDraftAndTranscript(t, nil)
	fixture.requireOneSession(t)
}

func newLazyGoalCore(t *testing.T, enabledTools map[toolspec.ID]bool, client llm.Client) (*Core, metadata.Binding, lazyGoalLaunchClient) {
	workspace := t.TempDir()
	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{
		WorkspaceRoot: workspace,
		LoadOptions:   brand.LoadOptions{ConfigRoot: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	resolved.Config.Settings.EnabledTools = enabledTools
	resolved.Config.Settings.Model = "test-model"
	resolved.Config.Settings.ModelContextWindow = 200000
	binding, err := metadata.RegisterBinding(t.Context(), resolved.Config.PersistenceRoot, workspace)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	appCore := newCoreTestApp(t, resolved.Config, auth.EmptyState(), runtimewire.RuntimeClientFactoryFunc(
		func(context.Context, runtimewire.RuntimeClientRequest) (llm.Client, error) {
			return client, nil
		},
	))
	launchClient, err := appCore.SessionLaunchClientForProjectWorkspace(t.Context(), binding.ProjectID, workspace)
	if err != nil {
		t.Fatalf("SessionLaunchClientForProjectWorkspace: %v", err)
	}
	return appCore, binding, launchClient
}

func newLazyGoalFixture(t *testing.T, enabledTools map[toolspec.ID]bool, client llm.Client) *lazyGoalFixture {
	appCore, binding, launch := newLazyGoalCore(t, enabledTools, client)
	fixture := &lazyGoalFixture{
		appCore: appCore,
		binding: binding,
		launch:  launch,
		draft:   writeLazyGoalDraft(t, appCore, binding),
	}
	t.Cleanup(func() {
		if fixture.released || fixture.activation.Attachment.SessionID == "" {
			return
		}
		_, _ = appCore.SessionRuntimeClient().ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
			ClientRequestID: "lazy-goal-release-cleanup",
			Attachment:      fixture.activation.Attachment,
			OwnerID:         fixture.ownerID,
			DropOwner:       true,
		})
	})
	return fixture
}

func (f *lazyGoalFixture) materialize(t *testing.T) {
	materialized, err := f.launch.MaterializeWorkspaceChat(t.Context(), serverapi.WorkspaceChatMaterializeRequest{})
	if err != nil {
		t.Fatalf("MaterializeWorkspaceChat: %v", err)
	}
	f.materialized = materialized
}

func (f *lazyGoalFixture) materializeAndActivate(t *testing.T, prefix string) {
	f.materialize(t)
	planned, err := f.launch.PlanSession(t.Context(), serverapi.SessionPlanRequest{
		ClientRequestID: prefix + "-plan",
		Mode:            serverapi.SessionLaunchModeInteractive,
		Intent:          serverapi.OpenExistingSessionLaunchIntent(f.materialized.SessionID),
	})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	f.planned = planned
	f.ownerID = prefix + "-owner"
	activation, err := f.appCore.SessionRuntimeClient().ActivateSessionRuntime(t.Context(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID:          prefix + "-activate",
		SessionID:                f.materialized.SessionID.String(),
		OwnerID:                  f.ownerID,
		ActiveSettings:           planned.Plan.ActiveSettings,
		EnabledToolIDs:           planned.Plan.EnabledToolIDs,
		QuestionsEnabled:         boolPointer(planned.Plan.QuestionsEnabled),
		AutoCompactionEnabled:    boolPointer(planned.Plan.AutoCompactionEnabled),
		ThinkingOverrideExplicit: planned.Plan.ThinkingOverrideExplicit,
		Source:                   planned.Plan.Source,
	})
	if err != nil {
		t.Fatalf("ActivateSessionRuntime: %v", err)
	}
	f.activation = activation
}

func (f *lazyGoalFixture) release(t *testing.T) {
	response, err := f.appCore.SessionRuntimeClient().ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "lazy-goal-release",
		Attachment:      f.activation.Attachment,
		OwnerID:         f.ownerID,
		DropOwner:       true,
	})
	if err != nil {
		t.Fatalf("ReleaseSessionRuntime: %v", err)
	}
	if !response.Released || response.Active {
		t.Fatalf("ReleaseSessionRuntime response = %+v, want released inactive", response)
	}
	f.released = true
}

func (f *lazyGoalFixture) requireDraftAndTranscript(t *testing.T, wantRecords *int) {
	record, err := f.appCore.MetadataStore().ResolvePersistedSession(t.Context(), f.materialized.SessionID.String())
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	state, err := session.ChatDraftStateFromMeta(*record.Meta)
	if err != nil {
		t.Fatalf("ChatDraftStateFromMeta: %v", err)
	}
	if state.Message != f.draft.Message {
		t.Fatalf("composer draft = %q, want %q", state.Message, f.draft.Message)
	}
	records, messages := f.collectMessageRecords(t, record.SessionDir)
	if wantRecords != nil && len(records) != *wantRecords {
		t.Fatalf("materialized transcript records = %d, want %d", len(records), *wantRecords)
	}
	for _, event := range messages {
		if event.Role == session.MessageRoleUser {
			t.Fatalf("materialized transcript contains user message: %+v", event)
		}
	}
}

func (f *lazyGoalFixture) requireNoTranscript(t *testing.T) {
	wantRecords := 0
	f.requireDraftAndTranscript(t, &wantRecords)
}

func (f *lazyGoalFixture) requireGoalNoticeWithoutUserMessage(t *testing.T) {
	record, err := f.appCore.MetadataStore().ResolvePersistedSession(t.Context(), f.materialized.SessionID.String())
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	_, records := f.collectMessageRecords(t, record.SessionDir)
	goalNotices, userMessages := 0, 0
	for _, event := range records {
		message := event
		if message.MessageType != nil && *message.MessageType == session.MessageTypeGoal {
			goalNotices++
		}
		if message.Role == session.MessageRoleUser {
			userMessages++
		}
	}
	if goalNotices != 1 || userMessages != 0 {
		t.Fatalf("materialized Goal records = notices:%d user_messages:%d, want one notice and no user message", goalNotices, userMessages)
	}
}

func (f *lazyGoalFixture) collectMessageRecords(t *testing.T, sessionDir string) ([]session.EventRecord, []session.MessageRecord) {
	store, err := session.Open(sessionDir, f.appCore.MetadataStore().AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("open materialized Session: %v", err)
	}
	records, err := sessiontest.CollectRecords(store)
	if err != nil {
		t.Fatalf("CollectRecords: %v", err)
	}
	messages := make([]session.MessageRecord, 0, len(records))
	for _, event := range records {
		payload, payloadErr := event.Payload()
		if payloadErr != nil {
			t.Fatalf("event payload: %v", payloadErr)
		}
		message, ok := payload.(session.MessageRecord)
		if ok {
			messages = append(messages, message)
		}
	}
	return records, messages
}

func (f *lazyGoalFixture) requireOneSession(t *testing.T) {
	page, err := f.appCore.ProjectViewClient().ListSessionPage(t.Context(), serverapi.SessionPageRequest{
		ProjectID: f.binding.ProjectID,
		Category:  sessioncontract.SessionCategoryMain,
		Limit:     intPointer(20),
	})
	if err != nil {
		t.Fatalf("ListSessionPage: %v", err)
	}
	if len(page.Sessions) != 1 {
		t.Fatalf("retained Session list = %+v, want one", page.Sessions)
	}
}

func writeLazyGoalDraft(t *testing.T, appCore *Core, binding metadata.Binding) metadata.WorkspaceChatDraftDocument {
	draft := metadata.WorkspaceChatDraftDocument{
		Message:        "unsent composer draft",
		Agent:          brand.DefaultSubagentRole,
		Supervisor:     "all",
		Thinking:       "medium",
		Fast:           true,
		Questions:      true,
		AutoCompaction: false,
	}
	if err := appCore.MetadataStore().ReplaceWorkspaceChatDraft(t.Context(), binding.WorkspaceID, &draft); err != nil {
		t.Fatalf("ReplaceWorkspaceChatDraft: %v", err)
	}
	return draft
}

func containsTool(tools []string, want string) bool {
	for _, tool := range tools {
		if tool == want {
			return true
		}
	}
	return false
}

func boolPointer(value bool) *bool {
	return &value
}

func intPointer(value int) *int {
	return &value
}
