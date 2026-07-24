package runtime

import (
	"context"
	"sync/atomic"
	"testing"

	"core/server/llm"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestRemoteCompactionRefreshesWorkflowTaskCommentCount(t *testing.T) {
	scopeID := runtimeids.NewExecutionScopeID()
	counter := &workflowTaskCommentCounterProbe{count: 3}
	client := &fakeCompactionClient{compactionResponses: []llm.CompactionResponse{
		remoteCompactionReplacement(1_000, 100, 200_000),
	}}
	engine := mustNewWorkflowTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		&workflowruntime.Config{
			ScopeID:            scopeID,
			CompletionMode:     workflowruntime.CompletionModeTool,
			Controller:         &externallyCompletedWorkflowController{},
			TaskCommentCounter: counter,
			Instructions:       workflowruntime.TaskInstructions{TaskID: "task", NodeID: "node"},
		},
		Config{Model: "gpt-5"},
	)
	if err := engine.steer("stale", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{
			Role:        llm.RoleDeveloper,
			MessageType: textutil.Value(llm.MessageTypeWorkflowMode),
			SourcePath:  textutil.Value("stale-workflow"),
			Content:     textutil.Value("stale"),
		}},
	)); err != nil {
		t.Fatalf("persist stale workflow context: %v", err)
	}
	if err := engine.steer("input", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}

	if _, _, err := engine.compactNow(context.Background(), "compact", compactionModeManual, compactionInstructionsInput{}, false); err != nil {
		t.Fatalf("compact workflow context: %v", err)
	}
	workflowModes := 0
	for _, item := range engine.transcriptRuntimeState().SnapshotItems() {
		if item.Type == llm.ResponseItemTypeMessage &&
			item.MessageType != nil &&
			*item.MessageType == llm.MessageTypeWorkflowMode {
			if item.SourcePath == nil || *item.SourcePath != "task/node" {
				t.Fatalf("workflow replacement source identity = %+v, want task/node", item)
			}
			workflowModes++
		}
	}
	if counter.calls.Load() != 1 || workflowModes != 1 || len(client.compactionCalls) != 1 {
		t.Fatalf(
			"workflow replacement counter-calls/mode-items/remote-calls = %d/%d/%d, want one/one/one",
			counter.calls.Load(),
			workflowModes,
			len(client.compactionCalls),
		)
	}
}

func TestWorkflowRequestAfterCompactionUsesOneCurrentAssignmentPrompt(t *testing.T) {
	tests := []struct {
		name                string
		existingCurrentNode bool
		compact             func(context.Context, *Engine) error
	}{
		{
			name:                "same assignment reminder",
			existingCurrentNode: true,
			compact: func(ctx context.Context, engine *Engine) error {
				return engine.CompactContext(ctx, "")
			},
		},
		{
			name: "compact and continue reassignment",
			compact: func(ctx context.Context, engine *Engine) error {
				return engine.CompactContextForWorkflowContinuation(ctx)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scopeID := runtimeids.NewExecutionScopeID()
			client := &fakeCompactionClient{
				compactionResponses: []llm.CompactionResponse{
					remoteCompactionReplacement(1_000, 100, 200_000),
				},
				responses: []llm.Response{
					commentaryResponse("", llm.ToolCall{
						ID:   "complete",
						Name: string(toolspec.ToolCompleteNode),
						Input: mustJSON(map[string]any{
							"transition": "done",
							"summary":    "done",
						}),
					}),
				},
			}
			engine := mustNewWorkflowTestEngine(
				t,
				mustCreateTestSession(t),
				client,
				&workflowruntime.Config{
					ScopeID: scopeID,
					Contract: workflowruntime.CompletionContract{
						Transitions: []workflowruntime.CompletionTransition{{
							ID:         "done",
							Parameters: []workflow.Parameter{{Key: "summary"}},
						}},
					},
					CompletionMode: workflowruntime.CompletionModeTool,
					Controller:     &externallyCompletedWorkflowController{},
					Instructions:   workflowruntime.TaskInstructions{TaskID: "task", NodeID: "node"},
				},
				Config{Model: "gpt-5"},
			)
			currentNodeIdentity := workflowPromptIdentity(engine.cfg.WorkflowRun.Instructions)
			existingIdentity := "previous-task/previous-node"
			if test.existingCurrentNode {
				existingIdentity = currentNodeIdentity
			}
			if err := engine.steer("input", steerMessagesWithPersistenceIntent(
				steeringPriorityNormal,
				steeringMessageEventNone,
				true,
				[]llm.Message{
					{
						Role:        llm.RoleDeveloper,
						MessageType: textutil.Value(llm.MessageTypeWorkflowMode),
						SourcePath:  textutil.Value(existingIdentity),
						Content:     textutil.Value("existing workflow instructions"),
					},
					{Role: llm.RoleUser, Content: textutil.Value("input")},
				},
			)); err != nil {
				t.Fatalf("persist compaction input: %v", err)
			}

			if err := test.compact(context.Background(), engine); err != nil {
				t.Fatalf("compact workflow context: %v", err)
			}
			if _, err := engine.SubmitWorkflowTurn(context.Background()); err != nil {
				t.Fatalf("submit workflow turn: %v", err)
			}
			if len(client.calls) != 1 {
				t.Fatalf("post-compaction model calls = %d, want one", len(client.calls))
			}

			workflowModes := 0
			for _, item := range client.calls[0].Items {
				if item.Type != llm.ResponseItemTypeMessage ||
					item.MessageType == nil ||
					*item.MessageType != llm.MessageTypeWorkflowMode {
					continue
				}
				if item.SourcePath == nil || *item.SourcePath != currentNodeIdentity {
					t.Fatalf("workflow request mode item = %+v, want source identity %q", item, currentNodeIdentity)
				}
				workflowModes++
			}
			if workflowModes != 1 {
				t.Fatalf("post-compaction workflow-mode-items = %d, want one", workflowModes)
			}
		})
	}
}

func TestWorkflowCompactionResetsProtocolViolationBudget(t *testing.T) {
	controller := &workflowProtocolBudgetController{}
	client := &fakeCompactionClient{compactionResponses: []llm.CompactionResponse{
		remoteCompactionReplacement(1_000, 100, 200_000),
	}}
	engine := mustNewWorkflowTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		&workflowruntime.Config{
			ScopeID:                      runtimeids.NewExecutionScopeID(),
			CompletionMode:               workflowruntime.CompletionModeTool,
			MaxInvalidCompletionAttempts: 3,
			Controller:                   controller,
		},
		Config{Model: "gpt-5"},
	)
	if err := engine.steer("input", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}
	for want := int64(1); want <= 2; want++ {
		violation, err := engine.recordWorkflowProtocolViolation(
			context.Background(),
			workflowruntime.ViolationKindInvalidCompletion,
			"invalid completion",
		)
		if err != nil || violation.Count != want {
			t.Fatalf("pre-compaction violation = %+v error=%v, want count %d", violation, err, want)
		}
	}

	_, receipt, err := engine.compactNow(
		context.Background(),
		"compact",
		compactionModeManual,
		compactionInstructionsInput{},
		false,
	)
	if err != nil || !receipt.Committed {
		t.Fatalf("compact workflow context: receipt=%+v error=%v", receipt, err)
	}

	violation, err := engine.recordWorkflowProtocolViolation(
		context.Background(),
		workflowruntime.ViolationKindInvalidCompletion,
		"invalid completion",
	)
	if err != nil || violation.Count != 1 {
		t.Fatalf("post-compaction violation = %+v error=%v, want count one", violation, err)
	}
}

type workflowTaskCommentCounterProbe struct {
	count int64
	calls atomic.Int32
}

func (p *workflowTaskCommentCounterProbe) CountTaskComments(context.Context, workflow.TaskID) (int64, error) {
	p.calls.Add(1)
	return p.count, nil
}

type workflowProtocolBudgetController struct {
	externallyCompletedWorkflowController
	violationCount atomic.Int64
}

func (c *workflowProtocolBudgetController) RecordProtocolViolation(
	context.Context,
	workflowruntime.ViolationRequest,
) (workflowruntime.ViolationResult, error) {
	return workflowruntime.ViolationResult{Count: c.violationCount.Add(1)}, nil
}

func (c *workflowProtocolBudgetController) ResetProtocolViolationBudget(
	context.Context,
	workflowruntime.ViolationResetRequest,
) error {
	c.violationCount.Store(0)
	return nil
}
