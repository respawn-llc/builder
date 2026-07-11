package runtime

import (
	"context"
	"testing"

	"core/server/llm"
	"core/shared/config"
)

func TestWorkflowCompletionAdapterDecidesFromOutput(t *testing.T) {
	ctx := context.Background()
	newAdapter := func(mode config.WorkflowCompletionMode) workflowCompletionAdapter {
		store := mustCreateTestSession(t)
		eng := mustNewWorkflowTestEngine(t, store, &fakeClient{}, testWorkflowConfig(&fakeWorkflowController{}, mode), Config{})
		return (&defaultStepExecutor{engine: eng}).workflowCompletionAdapter()
	}

	valid, err := newAdapter(config.WorkflowCompletionModeUnstructured).Evaluate(ctx, llm.Message{Phase: llm.MessagePhaseFinal, Content: `{"commentary":"complete","summary":"done"}`})
	if err != nil || !valid.Applicable || !valid.Done || valid.Complete == nil {
		t.Fatalf("valid final: outcome=%+v err=%v, want Applicable+Done with a completion side effect", valid, err)
	}

	invalid, err := newAdapter(config.WorkflowCompletionModeUnstructured).Evaluate(ctx, llm.Message{Phase: llm.MessagePhaseFinal, Content: `{"summary":""}`})
	if err != nil || !invalid.Applicable || invalid.Done || invalid.Continue == nil {
		t.Fatalf("invalid final: outcome=%+v err=%v, want Applicable, not Done, Continue carries the decode error", invalid, err)
	}

	nonFinal, err := newAdapter(config.WorkflowCompletionModeUnstructured).Evaluate(ctx, llm.Message{Content: "thinking"})
	if err != nil || nonFinal.Applicable {
		t.Fatalf("non-final: outcome=%+v err=%v, want not Applicable", nonFinal, err)
	}

	toolMode, err := newAdapter(config.WorkflowCompletionModeTool).Evaluate(ctx, llm.Message{Phase: llm.MessagePhaseFinal, Content: `{"commentary":"complete","summary":"done"}`})
	if err != nil || toolMode.Applicable {
		t.Fatalf("tool mode final: outcome=%+v err=%v, want not Applicable", toolMode, err)
	}
}
