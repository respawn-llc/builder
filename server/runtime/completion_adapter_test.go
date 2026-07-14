package runtime

import (
	"context"
	"testing"

	"core/shared/config"
)

func TestWorkflowCompletionAdapterDecidesFromOutput(t *testing.T) {
	ctx := context.Background()
	newAdapter := func(mode config.WorkflowCompletionMode) workflowCompletionAdapter {
		store := mustCreateTestSession(t)
		eng := mustNewWorkflowTestEngine(t, store, &fakeClient{}, testWorkflowConfig(&fakeWorkflowController{}, mode), Config{})
		return (&defaultStepExecutor{engine: eng}).workflowCompletionAdapter()
	}

	valid, err := newAdapter(config.WorkflowCompletionModeUnstructured).Evaluate(ctx, `{"commentary":"complete","summary":"done"}`)
	if err != nil || !valid.Applicable || !valid.Done || valid.Complete == nil {
		t.Fatalf("valid authorized submission: outcome=%+v err=%v, want Applicable+Done with a completion side effect", valid, err)
	}

	invalid, err := newAdapter(config.WorkflowCompletionModeUnstructured).Evaluate(ctx, `{"summary":""}`)
	if err != nil || !invalid.Applicable || invalid.Done || invalid.Continue == nil {
		t.Fatalf("invalid authorized submission: outcome=%+v err=%v, want Applicable, not Done, Continue carries the decode error", invalid, err)
	}

	toolMode, err := newAdapter(config.WorkflowCompletionModeTool).Evaluate(ctx, `{"commentary":"complete","summary":"done"}`)
	if err != nil || toolMode.Applicable {
		t.Fatalf("tool mode authorized submission: outcome=%+v err=%v, want not Applicable", toolMode, err)
	}
}
