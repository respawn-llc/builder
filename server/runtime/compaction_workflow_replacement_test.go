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

func TestRemoteCompactionRefreshesWorkflowTaskAwareness(t *testing.T) {
	t.Parallel()
	scopeID := runtimeids.NewExecutionScopeID()
	branchKey := workflow.TransitionBranchKey("implementation")
	source := &workflowTaskAwarenessProbe{awareness: workflowruntime.TaskAwareness{
		CommentCount:               3,
		UnsatisfiedDependencyCount: 2,
	}}
	client := &fakeCompactionClient{compactionResponses: []llm.CompactionResponse{
		remoteCompactionReplacement(1_000, 100, 200_000),
	}}
	engine := mustNewWorkflowTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		&workflowruntime.CurrentNodeExecutionConfig{
			ScopeID:             scopeID,
			CompletionMode:      workflowruntime.CompletionModeTool,
			Controller:          &externallyCompletedWorkflowController{},
			TaskAwarenessSource: source,
			Instructions:        workflowruntime.TaskInstructions{CurrentNode: mustTestCurrentNodeReference(t, "task", "node", &branchKey)},
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
	expectedIdentity := workflowruntime.CurrentNodePromptIdentity(
		mustTestCurrentNodeReference(t, "task", "node", &branchKey),
	)
	workflowModes := 0
	for _, item := range engine.transcriptRuntimeState().SnapshotItems() {
		if item.Type == llm.ResponseItemTypeMessage &&
			item.MessageType != nil &&
			*item.MessageType == llm.MessageTypeWorkflowMode {
			if item.SourcePath == nil || *item.SourcePath != expectedIdentity {
				t.Fatalf("workflow replacement source identity = %+v, want Current Node identity %q", item, expectedIdentity)
			}
			workflowModes++
		}
	}
	if source.calls.Load() != 1 || workflowModes != 1 || len(client.compactionCalls) != 1 {
		t.Fatalf(
			"workflow replacement counter-calls/mode-items/remote-calls = %d/%d/%d, want one/one/one",
			source.calls.Load(),
			workflowModes,
			len(client.compactionCalls),
		)
	}
}

func TestWorkflowRequestAfterCompactionUsesOneCurrentAssignmentPrompt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                string
		existingCurrentNode bool
		existingBranchKey   *workflow.TransitionBranchKey
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
		{
			name: "parallel same-node branch reassignment after compaction",
			existingBranchKey: func() *workflow.TransitionBranchKey {
				key := workflow.TransitionBranchKey("implementation")
				return &key
			}(),
			compact: func(ctx context.Context, engine *Engine) error {
				return engine.CompactContextForWorkflowContinuation(ctx)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scopeID := runtimeids.NewExecutionScopeID()
			currentBranchKey := workflow.TransitionBranchKey("review")
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
				&workflowruntime.CurrentNodeExecutionConfig{
					ScopeID: scopeID,
					Contract: workflowruntime.CompletionContract{
						Transitions: []workflowruntime.CompletionTransition{{
							ID:         "done",
							Parameters: []workflow.Parameter{{Key: "summary"}},
						}},
					},
					CompletionMode: workflowruntime.CompletionModeTool,
					Controller:     &externallyCompletedWorkflowController{},
					Instructions:   workflowruntime.TaskInstructions{CurrentNode: mustTestCurrentNodeReference(t, "task", "node", &currentBranchKey)},
				},
				Config{Model: "gpt-5"},
			)
			currentNodeIdentity := workflowruntime.CurrentNodePromptIdentity(
				mustTestCurrentNodeReference(t, "task", "node", &currentBranchKey),
			)
			existingIdentity := "previous-task/previous-node"
			if test.existingCurrentNode {
				existingIdentity = currentNodeIdentity
			}
			if test.existingBranchKey != nil {
				existingIdentity = workflowruntime.CurrentNodePromptIdentity(
					mustTestCurrentNodeReference(t, "task", "node", test.existingBranchKey),
				)
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
	t.Parallel()
	controller := &workflowProtocolBudgetController{}
	client := &fakeCompactionClient{compactionResponses: []llm.CompactionResponse{
		remoteCompactionReplacement(1_000, 100, 200_000),
	}}
	engine := mustNewWorkflowTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		&workflowruntime.CurrentNodeExecutionConfig{
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

type workflowTaskAwarenessProbe struct {
	awareness workflowruntime.TaskAwareness
	calls     atomic.Int32
}

func (p *workflowTaskAwarenessProbe) TaskAwareness(context.Context, workflow.TaskID) (workflowruntime.TaskAwareness, error) {
	p.calls.Add(1)
	return p.awareness, nil
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
