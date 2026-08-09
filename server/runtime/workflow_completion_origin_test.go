package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/runtimecommand"
	"core/server/workflowruntime"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

func TestWorkflowCompletionAuthorityOrdersHumanInputAndCompletion(t *testing.T) {
	tests := []struct {
		name             string
		inputFirst       bool
		wantCompletions  int64
		wantInputApplied bool
	}{
		{
			name:             "input admitted first supersedes completion",
			inputFirst:       true,
			wantCompletions:  0,
			wantInputApplied: true,
		},
		{
			name:             "completion admitted first finalizes before later input",
			wantCompletions:  1,
			wantInputApplied: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine, controller, origin := newWorkflowCompletionOriginEngine(t)
			input := func() error {
				_, err := engine.acceptHumanAgendaItem(
					queuedUserMessageWithID(runtimeids.NewQueueItemID().String(), "new direction"),
					boundaryEligibilityStep,
					true,
				)
				return err
			}
			complete := func() error {
				_, err := engine.completeWorkflowCurrentNode(
					context.Background(),
					origin,
					workflowruntime.ParsedCompletion{TransitionID: "done"},
				)
				return err
			}

			var inputErr error
			if test.inputFirst {
				inputErr = input()
				if err := complete(); err == nil {
					t.Fatal("completion succeeded after Pending Work was admitted")
				}
			} else {
				if err := complete(); err != nil {
					t.Fatalf("completion admitted first: %v", err)
				}
				inputErr = input()
			}
			if (inputErr == nil) != test.wantInputApplied {
				t.Fatalf("input error = %v, want applied %t", inputErr, test.wantInputApplied)
			}
			if got := controller.completed.Load(); got != test.wantCompletions {
				t.Fatalf("Workflow mutations = %d, want %d", got, test.wantCompletions)
			}
		})
	}
}

func TestWorkflowCompletionRejectsMissingMismatchedAndBoundaryClosedOriginBeforeMutation(t *testing.T) {
	engine, controller, origin := newWorkflowCompletionOriginEngine(t)
	tests := []struct {
		name   string
		origin serverapi.RuntimeStepOrigin
	}{
		{name: "missing"},
		{
			name: "mismatched run",
			origin: serverapi.RuntimeStepOrigin{
				RunID:  uuid.NewString(),
				StepID: origin.StepID,
			},
		},
		{
			name: "mismatched step",
			origin: serverapi.RuntimeStepOrigin{
				RunID:  origin.RunID,
				StepID: uuid.NewString(),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := engine.completeWorkflowCurrentNode(
				context.Background(),
				test.origin,
				workflowruntime.ParsedCompletion{TransitionID: "done"},
			); err == nil {
				t.Fatal("completion succeeded with an ineligible origin")
			}
		})
	}

	wait := &blockingAgentStepWorktreeWait{
		started: make(chan struct{}),
		release: make(chan AgentStepReducerGrant, 1),
	}
	probe := &agentStepOriginProbe{firstBoundaryWait: wait}
	engine.cfg.StepLifecycle = probe
	deferred, err := runtimecommand.Submit(
		engine.lifecycleCtx,
		engine.runtimeEvents,
		agentStepBoundaryRequest{continueTurn: true},
		func(
			command runtimecommand.Admission,
			request agentStepBoundaryRequest,
			complete func(agentStepBoundaryDecision, error),
		) error {
			decision, handleErr := engine.applyAgentStepBoundary(
				runtimeEventAdmission{engine: engine, command: command},
				request,
				complete,
			)
			if handleErr != nil || decision != nil {
				complete(decision, handleErr)
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("submit Boundary: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := deferred.Await(context.Background())
		done <- err
	}()
	<-wait.started
	if _, err := engine.completeWorkflowCurrentNode(
		context.Background(),
		origin,
		workflowruntime.ParsedCompletion{TransitionID: "done"},
	); err == nil {
		t.Fatal("completion succeeded while no Agent Step origin existed between Steps")
	}
	if got := controller.completed.Load(); got != 0 {
		t.Fatalf("Workflow mutations = %d, want zero for stale origins", got)
	}
	wait.release <- agentStepOriginProbeGrant{probe: probe}
	if err := <-done; err != nil {
		t.Fatalf("release Boundary: %v", err)
	}
}

func TestWorkflowCompletionProducerAdaptersSupplyExactOrigin(t *testing.T) {
	tests := []struct {
		name   string
		invoke func(context.Context, *Engine, serverapi.RuntimeStepOrigin) error
	}{
		{
			name: "complete_node",
			invoke: func(ctx context.Context, engine *Engine, origin serverapi.RuntimeStepOrigin) error {
				result := (&defaultToolExecutor{engine: engine}).executeCompleteNodeTool(
					ctx,
					origin.StepID,
					completeNodeCall("complete", []byte(`{"commentary":"done","summary":"done"}`)),
				)
				if result.IsError {
					return errors.New("complete_node returned an error result")
				}
				return nil
			},
		},
		{
			name: "structured output",
			invoke: func(ctx context.Context, engine *Engine, origin serverapi.RuntimeStepOrigin) error {
				return (&defaultStepExecutor{engine: engine}).completeCurrentNodeExecutionFromParsed(
					ctx,
					origin.StepID,
					workflowruntime.ParsedCompletion{TransitionID: "done"},
				)
			},
		},
		{
			name: "unstructured output",
			invoke: func(ctx context.Context, engine *Engine, origin serverapi.RuntimeStepOrigin) error {
				return (&defaultStepExecutor{engine: engine}).completeCurrentNodeExecutionFromParsed(
					ctx,
					origin.StepID,
					workflowruntime.ParsedCompletion{TransitionID: "done"},
				)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine, controller, origin := newWorkflowCompletionOriginEngine(t)
			if err := test.invoke(context.Background(), engine, origin); err != nil {
				t.Fatalf("invoke producer: %v", err)
			}
			requests := controller.completionRequests()
			if len(requests) != 1 || requests[0].Origin != origin {
				t.Fatalf("completion requests = %+v, want exact origin %+v", requests, origin)
			}
		})
	}
}

func newWorkflowCompletionOriginEngine(
	t *testing.T,
) (*Engine, *fakeWorkflowController, serverapi.RuntimeStepOrigin) {
	t.Helper()
	controller := &fakeWorkflowController{}
	workflowCfg := testWorkflowConfig(controller, config.WorkflowCompletionModeTool)
	engine := mustNewWorkflowTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		workflowCfg,
		Config{},
	)
	origin := serverapi.RuntimeStepOrigin{
		RunID:  uuid.NewString(),
		StepID: uuid.NewString(),
	}
	engine.liveRun.beginStep(&RunSnapshot{
		RunID:      origin.RunID,
		StepID:     origin.StepID,
		Status:     RunStatusRunning,
		ActiveKind: ActiveKindWorkflowTurn,
	})
	scopeID := workflowCfg.ScopeID
	engine.agentSteps.scopeID = scopeID
	engine.agentSteps.current = &activeAgentStep{
		scopeID: scopeID,
		origin:  origin,
		phase:   agentStepProviderRunning,
	}
	return engine, controller, origin
}
