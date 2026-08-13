package runtime

import (
	"context"
	"testing"

	"core/server/llm"
	"core/server/workflowruntime"
	"core/shared/config"
)

func TestPrepareInspectionRequestWithoutToolsUsesAutomaticChoice(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewWorkflowTestEngine(
		t,
		store,
		&fakeClient{},
		testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeTool),
		Config{GlobalConfigDir: t.TempDir()},
	)

	request, err := PrepareInspectionRequest(context.Background(), engine, false)
	if err != nil {
		t.Fatalf("PrepareInspectionRequest: %v", err)
	}
	if len(request.Tools) != 0 || request.EnableNativeWebSearch {
		t.Fatalf("inspection advertised tools: local=%+v native_web_search=%t", request.Tools, request.EnableNativeWebSearch)
	}
	if request.ToolChoiceMode != llm.ToolChoiceModeAutomatic {
		t.Fatalf("tool choice mode = %q, want automatic", request.ToolChoiceMode)
	}
	if request.SessionID != "" {
		t.Fatalf("inspection SessionID = %q, want absent", request.SessionID)
	}
	if request.CodexDispatch != nil {
		t.Fatal("inspection request unexpectedly carries Codex dispatch context")
	}
}

func TestPersistedWorkflowPromptCarriesTaskAwarenessWithoutLiveExecution(t *testing.T) {
	t.Parallel()
	want := workflowruntime.TaskAwareness{CommentCount: 4, UnsatisfiedDependencyCount: 2}
	engine := mustNewExecTestEngine(t, mustCreateTestSession(t), &fakeClient{}, Config{
		WorkflowPrompt: &workflowruntime.PromptContract{TaskAwareness: want},
	})
	got, err := engine.currentWorkflowTaskAwareness(context.Background())
	if err != nil {
		t.Fatalf("currentWorkflowTaskAwareness: %v", err)
	}
	if got != want {
		t.Fatalf("persisted Task awareness = %+v, want %+v", got, want)
	}
}
