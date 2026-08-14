package runtime

import (
	"context"
	"testing"

	"core/prompts"
	"core/server/llm"
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

func assertFreshRequestMatchesCompactionProjection(t *testing.T, engine *Engine, want []llm.MessageType) {
	t.Helper()
	if err := engine.ensureMetaContextForRequest(context.Background(), "fresh"); err != nil {
		t.Fatalf("ensure fresh meta context: %v", err)
	}
	freshRequest, err := engine.buildRequest(context.Background(), "fresh", true)
	if err != nil {
		t.Fatalf("build fresh request: %v", err)
	}
	compactionMessages, err := engine.compactionReinjectedMetaMessages(context.Background())
	if err != nil {
		t.Fatalf("build compaction meta context: %v", err)
	}

	assertMetaContextTypes(t, requestMessages(freshRequest), want)
	assertMetaContextTypes(t, compactionMessages, want)
}
