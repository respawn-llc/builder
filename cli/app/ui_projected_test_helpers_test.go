package app

import (
	"context"
	"testing"
	"time"

	"core/server/registry"
	"core/server/runtime"
	"core/server/runtimecontrol"
	"core/server/runtimeview"
	"core/server/sessionview"
	"core/shared/apicontract"
	"core/shared/clientui"
)

func waitForTestCondition(t *testing.T, timeout time.Duration, label string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", label)
		}
		time.Sleep(10 * time.Millisecond)
	}
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

func newProjectedEngineUIModel(engine *runtime.Engine, opts ...UIOption) *uiModel {
	return newProjectedTestUIModel(newUIRuntimeClient(engine), opts...)
}

func newUIRuntimeClientFromEngine(engine *runtime.Engine) clientui.RuntimeClient {
	if engine == nil {
		return nil
	}
	resolver := sessionview.NewStaticRuntimeResolver(engine)
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

func testQuestionAskEvent(id, question string, reply chan askReply, suggestions ...string) askEvent {
	return askEvent{prompt: testQuestionPrompt(id, question, suggestions...), reply: reply}
}

func testQuestionAskEventPtr(id, question string, suggestions ...string) *askEvent {
	event := testQuestionAskEvent(id, question, nil, suggestions...)
	return &event
}

func testApprovalAskEvent(id, question string, reply chan askReply, decisions ...clientui.ApprovalDecision) askEvent {
	return askEvent{prompt: testApprovalPrompt(id, question, decisions...), reply: reply}
}
