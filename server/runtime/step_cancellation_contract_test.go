package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
)

func TestRunStepLoopCancellationPreventsModelDispatch(t *testing.T) {
	t.Parallel()
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("unused")},
	}}}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{Model: "gpt-5"})
	if err := steerTestActiveStep(engine, "seed", steerMessagesWithPersistenceIntent(steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist input: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := engine.runStepLoop(ctx, runtimeTestStepID("step")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled step error = %v", err)
	}
	if calls := len(client.calls); calls != 0 {
		t.Fatalf("canceled step model dispatches = %d, want zero", calls)
	}
}
