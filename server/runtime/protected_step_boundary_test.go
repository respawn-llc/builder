package runtime

import (
	"context"
	"testing"
	"time"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestSteeringWaitsForCausedToolResultBeforeNextProviderStep(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	client := &fakeClient{responses: []llm.Response{
		{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("checking"), Phase: textutil.Value(llm.MessagePhaseCommentary)}, ToolCalls: []llm.ToolCall{{ID: "protected-tool", Name: string(toolspec.ToolExecCommand), Input: []byte(`{"cmd":"true"}`)}}, Usage: llm.Usage{WindowTokens: 200_000}},
		{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)}, Usage: llm.Usage{WindowTokens: 200_000}},
	}}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t, tools.HandlerRegistration{
		ID: toolspec.ToolExecCommand, Handler: blockingTool{name: toolspec.ToolExecCommand, started: started, release: release},
	}), Config{Model: "gpt-5"})
	done := make(chan error, 1)
	go func() { _, err := engine.SubmitUserMessage(context.Background(), "inspect"); done <- err }()
	select {
	case <-started:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("caused tool did not start")
	}
	if _, err := engine.AcceptHumanSteering("after tool", nil); err != nil || fakeClientCallCount(client) != 1 {
		t.Fatalf("Steering admission/provider boundary = calls:%d err:%v", fakeClientCallCount(client), err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	requests := append([]llm.Request(nil), client.calls...)
	client.mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(requests))
	}
	for _, message := range requestMessages(requests[1]) {
		if message.Role == llm.RoleUser && messageContent(message) == "after tool" {
			return
		}
	}
	t.Fatal("next provider Step omitted Steering accepted during its caused tool")
}
