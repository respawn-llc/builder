package runtime

import (
	"context"
	"testing"

	"core/server/llm"
	"core/shared/config"
)

func TestPrepareInspectionRequestWithoutToolsUsesAutomaticChoice(t *testing.T) {
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
}
