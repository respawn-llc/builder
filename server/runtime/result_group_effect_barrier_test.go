package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
)

type resultGroupFlushRecorder struct {
	mu           sync.Mutex
	observations []ResultGroupFlushObservation
}

func (r *resultGroupFlushRecorder) ObserveResultGroupFlush(observation ResultGroupFlushObservation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observations = append(r.observations, observation)
}

func (r *resultGroupFlushRecorder) snapshot() []ResultGroupFlushObservation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ResultGroupFlushObservation(nil), r.observations...)
}

func questionBarrierAcceptedCalls() acceptedResponseCalls {
	return acceptedResponseCalls{
		hosted: []hostedToolExecution{{
			Call: llm.ToolCall{
				ID:    "hosted",
				Name:  string(toolspec.ToolWebSearch),
				Input: json.RawMessage(`{"query":"kent"}`),
			},
			Result: tools.Result{
				CallID: "hosted",
				Name:   toolspec.ToolWebSearch,
				Output: json.RawMessage(`{"ok":true}`),
			},
		}},
		local: []llm.ToolCall{{
			ID:    "question",
			Name:  string(toolspec.ToolAskQuestion),
			Input: json.RawMessage(`{"question":"Continue?"}`),
		}},
		order: []acceptedResponseCallRef{
			{source: acceptedResponseCallHosted, index: 0},
			{source: acceptedResponseCallLocal, index: 0},
		},
	}
}

func twoQuestionBarrierAcceptedCalls() acceptedResponseCalls {
	calls := questionBarrierAcceptedCalls()
	calls.local = append(calls.local, llm.ToolCall{
		ID:    "question-2",
		Name:  string(toolspec.ToolAskQuestion),
		Input: json.RawMessage(`{"question":"Continue again?"}`),
	})
	calls.order = append(calls.order, acceptedResponseCallRef{
		source: acceptedResponseCallLocal,
		index:  1,
	})
	return calls
}

func persistAcceptedToolCallIntents(
	t *testing.T,
	engine *Engine,
	stepID string,
	calls acceptedResponseCalls,
) {
	t.Helper()
	ordered := make([]llm.ToolCall, 0, len(calls.order))
	for _, ref := range calls.order {
		switch ref.source {
		case acceptedResponseCallHosted:
			ordered = append(ordered, calls.hosted[ref.index].Call)
		case acceptedResponseCallLocal:
			ordered = append(ordered, calls.local[ref.index])
		default:
			t.Fatalf("unsupported accepted response call source %d", ref.source)
		}
	}
	if err := engine.steer(runtimeTestStepID(stepID), steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, ToolCalls: ordered}})); err != nil {
		t.Fatalf("persist accepted tool-call intents: %v", err)
	}
}

func TestQuestionBarrierCommitsReadyHostedSiblingBeforeInteraction(t *testing.T) {
	store := mustCreateTestSession(t)
	broker := tools.NewAskQuestionBroker()
	flushes := &resultGroupFlushRecorder{}
	var engine *Engine
	var interactionErr error
	broker.SetAskHandler(func(_ context.Context, _ tools.AskQuestionRequest) (tools.AskQuestionResolution, error) {
		if _, found := engine.transcriptRuntimeState().ToolCompletionSnapshot("hosted"); !found {
			interactionErr = errors.New("Question became visible before the ready hosted sibling committed")
			return nil, interactionErr
		}
		return tools.AskQuestionAnswer{Freeform: textutil.Value("continue")}, nil
	})
	engine = mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		newTestToolRegistry(t, tools.HandlerRegistration{
			ID:      toolspec.ToolAskQuestion,
			Handler: tools.NewAskQuestionTool(broker, func() bool { return true }),
		}),
		Config{Model: "gpt-5", DurabilityObserver: flushes},
	)
	stepID := runtimeTestStepID("step")
	restoreStep := setTestActiveStep(engine, stepID)
	defer restoreStep()
	results, err := engine.executeAcceptedToolCalls(
		context.Background(),
		stepID,
		questionBarrierAcceptedCalls(),
	)
	if err != nil {
		t.Fatalf("execute accepted calls: %v", err)
	}
	if interactionErr != nil {
		t.Fatal(interactionErr)
	}
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("Question results = %+v, want one successful result", results)
	}
	observations := flushes.snapshot()
	if len(observations) != 2 ||
		observations[0].Reason != ResultGroupFlushQuestion ||
		observations[0].ResultCount != 1 ||
		observations[1].Reason != ResultGroupFlushStepBoundary {
		t.Fatalf("result group flushes = %+v, want Question sibling flush then Step Boundary close", observations)
	}
}

type approvalBarrierProbe struct {
	broker *tools.AskQuestionBroker
}

func (p approvalBarrierProbe) Call(
	ctx context.Context,
	call tools.Call,
) (tools.Result, error) {
	_, err := p.broker.Ask(ctx, tools.AskQuestionRequest{
		ToolCallID: call.ID,
		Question:   "Approve?",
		Approval:   true,
		ApprovalOptions: []tools.AskQuestionApprovalOption{{
			Decision: tools.AskQuestionApprovalDecisionAllowOnce,
			Label:    "Allow once",
		}},
	})
	if err != nil {
		return tools.ErrorResult(call, err.Error()), nil
	}
	return tools.Result{
		CallID: call.ID,
		Name:   call.Name,
		Output: json.RawMessage(`{"ok":true}`),
	}, nil
}

func TestApprovalBarrierUsesRuntimeFlushBeforeNestedApprovalVisibility(t *testing.T) {
	store := mustCreateTestSession(t)
	broker := tools.NewAskQuestionBroker()
	flushes := &resultGroupFlushRecorder{}
	var engine *Engine
	var interactionErr error
	broker.SetAskHandler(func(_ context.Context, _ tools.AskQuestionRequest) (tools.AskQuestionResolution, error) {
		if _, found := engine.transcriptRuntimeState().ToolCompletionSnapshot("hosted"); !found {
			interactionErr = errors.New("Approval became visible before the ready hosted sibling committed")
			return nil, interactionErr
		}
		return tools.AskQuestionApproval{
			Decision: tools.AskQuestionApprovalDecisionAllowOnce,
		}, nil
	})
	engine = mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		newTestToolRegistry(t, tools.HandlerRegistration{
			ID:      toolspec.ToolPatch,
			Handler: approvalBarrierProbe{broker: broker},
		}),
		Config{Model: "gpt-5", DurabilityObserver: flushes},
	)
	stepID := runtimeTestStepID("step")
	restoreStep := setTestActiveStep(engine, stepID)
	defer restoreStep()
	calls := questionBarrierAcceptedCalls()
	calls.local[0] = llm.ToolCall{
		ID:    "patch",
		Name:  string(toolspec.ToolPatch),
		Input: json.RawMessage(`{"patch":"*** Begin Patch\n*** Add File: approval-barrier.txt\n+approved\n*** End Patch\n"}`),
	}

	results, err := engine.executeAcceptedToolCalls(context.Background(), stepID, calls)
	if err != nil {
		t.Fatalf("execute accepted calls: %v", err)
	}
	if interactionErr != nil {
		t.Fatal(interactionErr)
	}
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("Approval results = %+v, want one successful result", results)
	}
	observations := flushes.snapshot()
	if len(observations) != 2 ||
		observations[0].Reason != ResultGroupFlushApproval ||
		observations[0].ResultCount != 1 ||
		observations[1].Reason != ResultGroupFlushStepBoundary {
		t.Fatalf("result group flushes = %+v, want Approval sibling flush then Step Boundary close", observations)
	}
}
