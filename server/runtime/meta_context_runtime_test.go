package runtime

import (
	"context"
	"errors"
	"testing"

	"core/prompts"
	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/workflowruntime"
	"core/shared/runtimeids"
)

func TestFreshHeadlessRequestMatchesPostCompactionMetaOrder(t *testing.T) {
	previousHeadlessPrompt := prompts.HeadlessModePrompt
	prompts.HeadlessModePrompt = "headless mode instructions"
	t.Cleanup(func() {
		prompts.HeadlessModePrompt = previousHeadlessPrompt
	})

	engine := mustNewExecTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		Config{Model: "gpt-5", HeadlessMode: true},
	)
	assertFreshRequestMatchesCompactionProjection(t, engine, []llm.MessageType{
		llm.MessageTypeHeadlessMode,
		llm.MessageTypeSkills,
		llm.MessageTypeAgentsMD,
		llm.MessageTypeEnvironment,
	})
}

func TestFreshWorkflowRequestMatchesPostCompactionMetaOrder(t *testing.T) {
	currentNode := mustTestCurrentNodeReference(t, "task", "node", nil)
	engine := mustNewWorkflowTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		&workflowruntime.CurrentNodeExecutionConfig{
			ScopeID:        runtimeids.NewExecutionScopeID(),
			CompletionMode: workflowruntime.CompletionModeTool,
			Controller:     &externallyCompletedWorkflowController{},
			Instructions:   workflowruntime.TaskInstructions{CurrentNode: currentNode},
		},
		Config{Model: "gpt-5"},
	)
	assertFreshRequestMatchesCompactionProjection(t, engine, []llm.MessageType{
		llm.MessageTypeSkills,
		llm.MessageTypeAgentsMD,
		llm.MessageTypeWorkflowMode,
		llm.MessageTypeEnvironment,
	})
}

func TestFreshHeadlessWorkflowRequestMatchesPostCompactionMetaOrder(t *testing.T) {
	currentNode := mustTestCurrentNodeReference(t, "task", "node", nil)
	store := mustCreateTestSession(t)
	engine := mustNewWorkflowTestEngine(
		t,
		store,
		&fakeClient{},
		&workflowruntime.CurrentNodeExecutionConfig{
			ScopeID:        runtimeids.NewExecutionScopeID(),
			CompletionMode: workflowruntime.CompletionModeTool,
			Controller:     &externallyCompletedWorkflowController{},
			Instructions:   workflowruntime.TaskInstructions{CurrentNode: currentNode},
		},
		Config{Model: "gpt-5", HeadlessMode: true},
	)
	assertFreshRequestMatchesCompactionProjection(t, engine, []llm.MessageType{
		llm.MessageTypeSkills,
		llm.MessageTypeAgentsMD,
		llm.MessageTypeWorkflowMode,
		llm.MessageTypeEnvironment,
	})
	if store.Meta().HeadlessActive {
		t.Fatal("Workflow request persisted unrelated Headless mode")
	}
}

func TestFreshWorkflowMetaContextRetriesCommittedObserverFailureWithoutDuplicateContext(t *testing.T) {
	t.Parallel()
	observerErr := errors.New("fresh meta-context observer failed")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	currentNode := mustTestCurrentNodeReference(t, "task", "node", nil)
	engine := mustNewWorkflowTestEngine(
		t,
		store,
		&fakeClient{},
		&workflowruntime.CurrentNodeExecutionConfig{
			ScopeID:        runtimeids.NewExecutionScopeID(),
			CompletionMode: workflowruntime.CompletionModeTool,
			Controller:     &externallyCompletedWorkflowController{},
			Instructions:   workflowruntime.TaskInstructions{CurrentNode: currentNode},
		},
		Config{Model: "gpt-5"},
	)
	gate.FailNext(observerErr)

	if err := engine.ensureMetaContextForRequest(context.Background(), "fresh"); !errors.Is(err, observerErr) {
		t.Fatalf("first fresh meta-context error = %v, want %v", err, observerErr)
	}
	if engine.baseMetaInjected {
		t.Fatal("fresh meta-context marked injected before the complete projection committed")
	}

	if err := engine.ensureMetaContextForRequest(context.Background(), "retry"); err != nil {
		t.Fatalf("retry fresh meta-context: %v", err)
	}
	if !engine.baseMetaInjected {
		t.Fatal("fresh meta-context remained uninjected after committed retry")
	}
	if trigger := engine.currentNodeExecutionSnapshot().delivery.trigger(workflowTaskPromptTriggerTaskDelivery); trigger != workflowTaskPromptTriggerTaskDelivery {
		t.Fatalf("workflow delivery trigger after retry = %v, want ordinary task delivery", trigger)
	}

	counts := make(map[llm.MessageType]int)
	for _, message := range engine.transcriptRuntimeState().SnapshotMessages() {
		if message.MessageType != nil {
			counts[*message.MessageType]++
		}
	}
	if counts[llm.MessageTypeWorkflowMode] != 1 || counts[llm.MessageTypeEnvironment] != 1 {
		t.Fatalf("fresh workflow meta-context counts = %+v, want one Workflow and one Environment", counts)
	}
}

func assertFreshRequestMatchesCompactionProjection(t *testing.T, engine *Engine, want []llm.MessageType) {
	t.Helper()
	if err := engine.ensureMetaContextForRequest(context.Background(), "fresh"); err != nil {
		t.Fatalf("ensure fresh meta context: %v", err)
	}
	freshRequest, err := engine.buildRequest(context.Background(), "fresh", true)
	if err != nil {
		t.Fatalf("build fresh request: %v", err)
	}
	projection, err := engine.compactionReinjectedMetaContextProjection(context.Background(), compactionModeManual)
	if err != nil {
		t.Fatalf("build compaction meta context: %v", err)
	}

	assertMetaContextTypes(t, requestMessages(freshRequest), want)
	assertMetaContextTypes(t, projection.Messages(), want)
}
