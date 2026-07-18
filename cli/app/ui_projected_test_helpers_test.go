package app

import (
	"context"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/registry"
	"core/server/runtime"
	"core/server/runtimecontrol"
	"core/server/runtimeview"
	"core/server/session"
	"core/server/sessionview"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

type testSessionViewSessionResolver struct {
	store *session.Store
}

func (r testSessionViewSessionResolver) ResolveSessionStore(context.Context, string) (*session.Store, error) {
	return r.store, nil
}

type testSessionViewRuntimeResolver struct {
	engine *runtime.Engine
}

func (r testSessionViewRuntimeResolver) ResolveRuntime(context.Context, string) (*runtime.Engine, error) {
	return r.engine, nil
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
	askRequests      chan serverapi.AskAnswerRequest
	approvalRequests chan serverapi.ApprovalAnswerRequest
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

func newProjectedEngineUIModel(engine *runtime.Engine, opts ...UIOption) *uiModel {
	return newProjectedTestUIModel(newUIRuntimeClient(engine), opts...)
}

func newUIRuntimeClientFromEngine(engine *runtime.Engine) clientui.RuntimeClient {
	if engine == nil {
		return nil
	}
	resolver := testSessionViewRuntimeResolver{engine: engine}
	reads := sessionview.NewService(nil, resolver, nil)
	controlRegistry := registry.NewRuntimeRegistry()
	registerUIRuntime(controlRegistry, engine.SessionID(), engine)
	controls := runtimecontrol.NewService(controlRegistry)
	runtimeClient := newUIRuntimeClientWithReads(engine.SessionID(), reads, controls).(*sessionRuntimeClient)
	snapshot, err := controlRegistry.RuntimeReadModelSnapshot(context.Background(), engine.SessionID(), nil)
	if err != nil {
		panic(err)
	}
	runtimeClient.storeMainView(runtimeview.MainViewFromRuntimeActivity(engine, snapshot.Version, snapshot.Activity))
	return runtimeClient
}

func registerUIRuntime(r *registry.RuntimeRegistry, sessionID string, engine *runtime.Engine) {
	claim, _, _ := r.AcquireRuntimeClaim(sessionID, "test-owner")
	if claim == nil {
		return
	}
	claim.Resolve(engine, nil, nil)
}

func newUIRuntimeClient(engine *runtime.Engine) clientui.RuntimeClient {
	return newUIRuntimeClientFromEngine(engine)
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
		State:       clientui.TranscriptPromptStatePending,
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
		State:           clientui.TranscriptPromptStatePending,
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
