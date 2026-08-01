package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
)

func TestManualCompactionRequiresToolCallSinceLatestCompaction(t *testing.T) {
	client := &fakeCompactionClient{
		responses: []llm.Response{{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("summary"),
			},
		}},
	}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{
		Model:          "gpt-5",
		CompactionMode: "local",
	})

	err := engine.CompactContext(context.Background(), "")
	if !errors.Is(err, ErrManualCompactionTooSoon) {
		t.Fatalf("fresh-session compaction error = %v, want too-soon", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.compactionCalls) != 0 || len(client.calls) != 0 {
		t.Fatalf("provider calls after rejected compaction = compaction:%d completion:%d, want zero", len(client.compactionCalls), len(client.calls))
	}
}

func TestManualCompactionAcceptsAfterAgentStepBoundary(t *testing.T) {
	client := &fakeCompactionClient{
		responses: []llm.Response{{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("summary"),
			},
		}},
	}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{
		Model:          "gpt-5",
		CompactionMode: "local",
	})
	if err := engine.stepLifecycle.Run(
		context.Background(),
		exclusiveStepOptions{ActiveKind: ActiveKindUserTurn},
		func(context.Context, string) error { return nil },
	); err != nil {
		t.Fatalf("complete agent step: %v", err)
	}

	if err := engine.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compaction after editing tool call: %v", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.calls) != 1 {
		t.Fatalf("provider completion calls = %d, want one", len(client.calls))
	}

	if err := engine.CompactContext(context.Background(), ""); !errors.Is(err, ErrManualCompactionTooSoon) {
		t.Fatalf("immediate repeat compaction error = %v, want too-soon", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.calls) != 1 {
		t.Fatalf("provider completion calls after rejected repeat = %d, want one", len(client.calls))
	}
}
