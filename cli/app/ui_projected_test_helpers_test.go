package app

import (
	"context"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/metadata"
	"core/server/registry"
	"core/server/runtime"
	"core/server/runtimecontrol"
	"core/server/runtimeview"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionruntime"
	"core/server/sessionview"
	"core/server/tools"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

type testSessionViewSessionResolver struct {
	store *session.Store
}

func (r testSessionViewSessionResolver) ResolveSessionStore(context.Context, string) (*session.Store, error) {
	return r.store, nil
}

func waitForTestCondition(t *testing.T, timeout time.Duration, label string, condition func() bool) {
	t.Helper()
	testsetup.RequireUntil(t, time.Now().Add(timeout), 10*time.Millisecond, condition, "timed out waiting for %s", label)
}

func newProjectedTestUIModel(runtimeClient clientui.RuntimeClient, opts ...UIOption) *uiModel {
	return NewProjectedUIModel(runtimeClient, opts...).(*uiModel)
}

func newProjectedClosedUIModel(runtimeClient clientui.RuntimeClient, opts ...UIOption) *uiModel {
	return newProjectedTestUIModel(runtimeClient, opts...)
}

func newSizedProjectedClosedUIModel(runtimeClient clientui.RuntimeClient, width, height int, opts ...UIOption) *uiModel {
	return sizedTestUIModel(newProjectedClosedUIModel(runtimeClient, opts...), width, height)
}

func setTestUITerminalSize(m *uiModel, width, height int) *uiModel {
	m.terminalGeometry = terminalGeometryKnown(width, height)
	return m
}

func sizedTestUIModel(m *uiModel, width, height int) *uiModel {
	m = setTestUITerminalSize(m, width, height)
	return m
}

func newProjectedStaticUIModel(opts ...UIOption) *uiModel {
	return newProjectedTestUIModel(nil, opts...)
}

type recordingPromptControl struct {
	singlePromptOnlyControl
	askRequests      chan serverapi.AskAnswerRequest
	approvalRequests chan serverapi.ApprovalAnswerRequest
}

type singlePromptOnlyControl struct{}

func (singlePromptOnlyControl) AnswerPromptBatch(
	context.Context,
	serverapi.PromptAnswerBatchRequest,
) (serverapi.PromptAnswerBatchResponse, error) {
	panic("unexpected prompt batch answer")
}

func newRecordingPromptControl() *recordingPromptControl {
	return &recordingPromptControl{
		askRequests:      make(chan serverapi.AskAnswerRequest, 8),
		approvalRequests: make(chan serverapi.ApprovalAnswerRequest, 8),
	}
}

func (c *recordingPromptControl) AnswerAsk(_ context.Context, request serverapi.AskAnswerRequest) error {
	c.askRequests <- request
	return nil
}

func (c *recordingPromptControl) AnswerApproval(_ context.Context, request serverapi.ApprovalAnswerRequest) error {
	c.approvalRequests <- request
	return nil
}

func newProjectedPromptTestUIModel(t *testing.T, opts ...UIOption) (*uiModel, *recordingPromptControl) {
	t.Helper()
	control := newRecordingPromptControl()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	model := newProjectedStaticUIModel(opts...)
	model.promptAnswers = newTranscriptPromptAnswerer(ctx, control)
	return model, control
}

func runPromptDeliveryCommand(t *testing.T, model *uiModel, command tea.Cmd) *uiModel {
	t.Helper()
	if command == nil {
		t.Fatal("expected prompt delivery command")
	}
	return updateUIModel(t, model, command())
}

func submitAskPromptKey(t *testing.T, model *uiModel, control *recordingPromptControl, key tea.KeyMsg) (*uiModel, serverapi.AskAnswerRequest) {
	t.Helper()
	next, command := model.Update(key)
	updated := runPromptDeliveryCommand(t, next.(*uiModel), command)
	return updated, requireAskRequest(t, control)
}

func requireAskRequest(t *testing.T, control *recordingPromptControl) serverapi.AskAnswerRequest {
	t.Helper()
	select {
	case request := <-control.askRequests:
		return request
	default:
		t.Fatal("completed prompt delivery recorded no ask request")
		return serverapi.AskAnswerRequest{}
	}
}

func requireApprovalRequest(t *testing.T, control *recordingPromptControl) serverapi.ApprovalAnswerRequest {
	t.Helper()
	select {
	case request := <-control.approvalRequests:
		return request
	default:
		t.Fatal("completed prompt delivery recorded no approval request")
		return serverapi.ApprovalAnswerRequest{}
	}
}

type projectedAuthorityRuntime struct {
	client    clientui.RuntimeClient
	reads     *sessionview.Service
	sessionID string
}

func newProjectedAuthorityUIModel(t *testing.T, client llm.Client, cfg runtime.Config, opts ...UIOption) *uiModel {
	t.Helper()
	store, persistence := createAuthoritativeTestSession(t, t.TempDir(), "ws", t.TempDir())
	fixture := newProjectedAuthorityRuntime(t, store, persistence, client, cfg)
	return newProjectedTestUIModel(fixture.client, opts...)
}

func newProjectedAuthorityRuntime(
	t *testing.T,
	store *session.Store,
	persistence *sessiontest.Persistence,
	client llm.Client,
	cfg runtime.Config,
) projectedAuthorityRuntime {
	t.Helper()
	if store == nil || persistence == nil {
		t.Fatal("projected Authority runtime requires a durable session fixture")
	}
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	settings := config.DefaultOnboardingSettings()
	settings.ProviderOverride = "openai"
	settings.Reviewer.Frequency = "off"
	if cfg.Model == "" {
		settings.Model = "gpt-5"
	} else {
		settings.Model = cfg.Model
	}
	if cfg.ContextWindowTokens > 0 {
		settings.ModelContextWindow = cfg.ContextWindowTokens
	}
	plan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings: settings,
		FilesystemContext: func() tools.FilesystemContext {
			context, err := runtimewire.NewFilesystemContext(store.Meta().WorkspaceRoot, store.Meta().WorkspaceRoot, metadata.ProjectWorkspaceBoundary{ProjectID: "test"})
			if err != nil {
				t.Fatalf("NewFilesystemContext: %v", err)
			}
			return context
		}(),
		Client: client,
	})
	if err != nil {
		t.Fatalf("new projected runtime plan: %v", err)
	}
	activity := registry.NewRuntimeRegistry()
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot:   t.TempDir(),
		StoreOptions:      persistence.Options(),
		ResourceLifecycle: activity,
		EventFeed: func(resource runtimeids.SessionResourceRef, event runtime.Event) {
			activity.PublishAuthorityRuntimeEvent(resource, event)
		},
	})
	if _, err := authority.OpenRuntime(t.Context(), sessionruntime.RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "projected-ui-test",
		Runtime:   &plan,
	}); err != nil {
		t.Fatalf("open projected runtime: %v", err)
	}
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close projected runtime: %v", err)
		}
	})
	reads := sessionview.NewService(testSessionViewSessionResolver{store: store}, activity, authority, nil)
	controls := runtimecontrol.NewService(authority).WithRuntimeActivityResolver(activity)
	runtimeClient := newUIRuntimeClientWithReads(sessionID.String(), reads, controls).(*sessionRuntimeClient)
	snapshot, err := activity.RuntimeReadModelSnapshot(context.Background(), sessionID.String(), nil)
	if err != nil {
		t.Fatalf("projected runtime snapshot: %v", err)
	}
	err = authority.WithCurrentRuntime(t.Context(), sessionID, func(_ context.Context, engine *runtime.Engine) error {
		view, err := runtimeview.MainViewFromRuntimeActivity(
			engine,
			snapshot.Version,
			snapshot.Activity,
		)
		if err != nil {
			return err
		}
		runtimeClient.storeMainView(view)
		return nil
	})
	if err != nil {
		t.Fatalf("read projected runtime: %v", err)
	}
	return projectedAuthorityRuntime{
		client:    runtimeClient,
		reads:     reads,
		sessionID: sessionID.String(),
	}
}

func newUnavailableRuntimeControlService() *runtimecontrol.Service {
	activity := registry.NewRuntimeRegistry()
	return runtimecontrol.NewService(sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})).
		WithRuntimeActivityResolver(activity)
}

func newTestSessionRuntimeClient(reads apicontract.SessionViewService, controls apicontract.RuntimeControlService) *sessionRuntimeClient {
	return newUIRuntimeClientWithReads("session-1", reads, controls).(*sessionRuntimeClient)
}

func newTestSessionRuntimeClientWithControls(controls apicontract.RuntimeControlService) *sessionRuntimeClient {
	return newTestSessionRuntimeClient(&countingSessionViewClient{}, controls)
}

func testQuestionPrompt(id, question string, suggestions ...string) clientui.TranscriptPrompt {
	return clientui.TranscriptPrompt{
		Kind:        clientui.TranscriptPromptKindQuestion,
		Status:      clientui.TranscriptPromptStatusPending,
		PromptID:    clientui.PromptID(id),
		SessionID:   ongoingTestSessionID(),
		StepID:      ongoingTestStepID(),
		Question:    question,
		CreatedAt:   time.Unix(1, 0).UTC(),
		Suggestions: append([]string(nil), suggestions...),
	}
}

func testApprovalPrompt(id, question string, decisions ...clientui.ApprovalDecision) clientui.TranscriptPrompt {
	return clientui.TranscriptPrompt{
		Kind:            clientui.TranscriptPromptKindApproval,
		Status:          clientui.TranscriptPromptStatusPending,
		PromptID:        clientui.PromptID(id),
		SessionID:       ongoingTestSessionID(),
		StepID:          ongoingTestStepID(),
		Question:        question,
		CreatedAt:       time.Unix(1, 0).UTC(),
		ApprovalOptions: append([]clientui.ApprovalDecision(nil), decisions...),
	}
}

func testQuestionAskEvent(id, question string, suggestions ...string) askEvent {
	return askEvent{prompt: testQuestionPrompt(id, question, suggestions...)}
}

func testQuestionAskEventPtr(id, question string, suggestions ...string) *askEvent {
	event := testQuestionAskEvent(id, question, suggestions...)
	return &event
}

func testApprovalAskEvent(id, question string, decisions ...clientui.ApprovalDecision) askEvent {
	return askEvent{prompt: testApprovalPrompt(id, question, decisions...)}
}
