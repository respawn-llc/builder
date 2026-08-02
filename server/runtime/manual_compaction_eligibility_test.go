package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
)

func completeManualEligibilityAgentStep(t *testing.T, engine *Engine) {
	t.Helper()
	engine.compactionRuntimeState().SetManualCompactionEligible(true)
}

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
	engine.compactionRuntimeState().SetManualCompactionEligible(false)

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
	completeManualEligibilityAgentStep(t, engine)

	if err := engine.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compaction after editing tool call: %v", err)
	}
	client.mu.Lock()
	if len(client.calls) != 1 {
		client.mu.Unlock()
		t.Fatalf("provider completion calls = %d, want one", len(client.calls))
	}
	client.mu.Unlock()

	if engine.compactionRuntimeState().ManualCompactionEligible() {
		t.Fatal("successful compaction retained manual eligibility")
	}
}
