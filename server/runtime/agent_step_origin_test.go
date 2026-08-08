package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/tools"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestAgentTurnRotatesCompletionOriginAtEveryProviderStep(t *testing.T) {
	store := mustCreateTestSession(t)
	probe := &agentStepOriginProbe{}
	var eventMu sync.Mutex
	var providerEventStepIDs []string
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("checking"),
				Phase:   textutil.Value(llm.MessagePhaseCommentary),
			},
			ToolCalls: []llm.ToolCall{{
				ID:    "call-origin-step",
				Name:  string(toolspec.ToolExecCommand),
				Input: mustJSON(map[string]any{"cmd": "true"}),
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("done"),
				Phase:   textutil.Value(llm.MessagePhaseFinal),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
	}}
	engine := mustNewTestEngine(
		t,
		store,
		client,
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: fakeTool{name: toolspec.ToolExecCommand},
		}),
		Config{
			Model:         "gpt-5",
			StepLifecycle: probe,
			OnEvent: func(event Event) {
				if event.Kind != EventModelResponse {
					return
				}
				eventMu.Lock()
				providerEventStepIDs = append(providerEventStepIDs, event.StepID)
				eventMu.Unlock()
			},
		},
	)

	if _, err := engine.SubmitUserMessage(context.Background(), "inspect"); err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}

	began, boundaries := probe.snapshot()
	if len(began) != 2 || len(boundaries) != 2 {
		t.Fatalf("Agent Step lifecycle = began:%+v boundaries:%+v, want two complete Steps", began, boundaries)
	}
	if began[0] != boundaries[0] || began[1] != boundaries[1] {
		t.Fatalf("Agent Step boundaries do not close their matching origins: began=%+v boundaries=%+v", began, boundaries)
	}
	if began[0].RunID != began[1].RunID {
		t.Fatalf("Agent Turn Run IDs = %q and %q, want one stable Run", began[0].RunID, began[1].RunID)
	}
	if began[0].StepID == began[1].StepID {
		t.Fatalf("provider Steps reused Step ID %q", began[0].StepID)
	}
	eventMu.Lock()
	recordedStepIDs := append([]string(nil), providerEventStepIDs...)
	eventMu.Unlock()
	if len(recordedStepIDs) != len(began) {
		t.Fatalf("provider response event Step IDs = %+v, want one per Agent Step %+v", recordedStepIDs, began)
	}
	for index := range began {
		if recordedStepIDs[index] != began[index].StepID {
			t.Fatalf(
				"provider response event %d Step ID = %q, want registered provider Step ID %q",
				index,
				recordedStepIDs[index],
				began[index].StepID,
			)
		}
	}
}

func TestAgentTurnExposesNoCompletionOriginWhileWorktreeOwnsBoundary(t *testing.T) {
	store := mustCreateTestSession(t)
	wait := &blockingAgentStepWorktreeWait{
		started: make(chan struct{}),
		release: make(chan AgentStepReducerGrant, 1),
	}
	probe := &agentStepOriginProbe{firstBoundaryWait: wait}
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("checking"),
				Phase:   textutil.Value(llm.MessagePhaseCommentary),
			},
			ToolCalls: []llm.ToolCall{{
				ID:    "call-boundary-wait",
				Name:  string(toolspec.ToolExecCommand),
				Input: mustJSON(map[string]any{"cmd": "true"}),
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("done"),
				Phase:   textutil.Value(llm.MessagePhaseFinal),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
	}}
	engine := mustNewTestEngine(
		t,
		store,
		client,
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: fakeTool{name: toolspec.ToolExecCommand},
		}),
		Config{Model: "gpt-5", StepLifecycle: probe},
	)

	done := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(context.Background(), "inspect")
		done <- err
	}()
	select {
	case <-wait.started:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not transfer the Step Boundary to Worktree")
	}
	began, boundaries := probe.snapshot()
	if len(began) != 1 || len(boundaries) != 1 {
		t.Fatalf(
			"lifecycle while Worktree owns Boundary = began:%+v boundaries:%+v, want one closed origin and no next origin",
			began,
			boundaries,
		)
	}
	wait.release <- agentStepOriginProbeGrant{probe: probe}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not resume after Worktree released the Boundary")
	}
	began, boundaries = probe.snapshot()
	if len(began) != 2 || len(boundaries) != 2 {
		t.Fatalf("completed lifecycle = began:%+v boundaries:%+v", began, boundaries)
	}
	if began[1].StepID == began[0].StepID {
		t.Fatalf("continue decision reused Step ID %q", began[1].StepID)
	}
}

type agentStepOriginProbe struct {
	mu                sync.Mutex
	began             []serverapi.RuntimeStepOrigin
	boundaries        []serverapi.RuntimeStepOrigin
	firstBoundaryWait AgentStepWorktreeWait
}

func (*agentStepOriginProbe) StepBegan(context.Context, StepLifecycleSnapshot) error {
	return nil
}

func (*agentStepOriginProbe) StepEnded(context.Context, StepLifecycleSnapshot) error {
	return nil
}

func (p *agentStepOriginProbe) AgentStepBegan(
	_ context.Context,
	origin serverapi.RuntimeStepOrigin,
) (runtimeids.ExecutionScopeID, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.began = append(p.began, origin)
	return runtimeids.NewExecutionScopeID(), nil
}

func (p *agentStepOriginProbe) AgentStepBoundary(
	_ context.Context,
	origin serverapi.RuntimeStepOrigin,
) (AgentStepBoundaryTransfer, error) {
	p.mu.Lock()
	p.boundaries = append(p.boundaries, origin)
	wait := p.firstBoundaryWait
	p.firstBoundaryWait = nil
	p.mu.Unlock()
	if wait != nil {
		return AgentStepWorktreeBoundary{Wait: wait}, nil
	}
	return AgentStepReducerBoundary{Grant: agentStepOriginProbeGrant{probe: p}}, nil
}

func (p *agentStepOriginProbe) snapshot() (
	[]serverapi.RuntimeStepOrigin,
	[]serverapi.RuntimeStepOrigin,
) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]serverapi.RuntimeStepOrigin(nil), p.began...),
		append([]serverapi.RuntimeStepOrigin(nil), p.boundaries...)
}

type agentStepOriginProbeGrant struct {
	probe *agentStepOriginProbe
}

func (g agentStepOriginProbeGrant) RegisterNext(
	ctx context.Context,
	origin serverapi.RuntimeStepOrigin,
) (runtimeids.ExecutionScopeID, error) {
	return g.probe.AgentStepBegan(ctx, origin)
}

func (agentStepOriginProbeGrant) Release() error {
	return nil
}

type blockingAgentStepWorktreeWait struct {
	started chan struct{}
	release chan AgentStepReducerGrant
}

func (w *blockingAgentStepWorktreeWait) Await(
	ctx context.Context,
) (AgentStepReducerGrant, error) {
	close(w.started)
	select {
	case grant := <-w.release:
		return grant, nil
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}
