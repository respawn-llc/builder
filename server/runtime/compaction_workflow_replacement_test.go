package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestWorkflowPostCompletionCompactionKeepsCompletedOutputAndDormantMetaContext(t *testing.T) {
	t.Parallel()
	scopeID := runtimeids.NewExecutionScopeID()
	currentNode := mustTestCurrentNodeReference(t, "task", "node", nil)
	client := &fakeCompactionClient{
		compactionResponses: []llm.CompactionResponse{
			remoteCompactionReplacement(1_000, 100, 200_000),
		},
	}
	engine := mustNewWorkflowTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		&workflowruntime.CurrentNodeExecutionConfig{
			ScopeID:        scopeID,
			CompletionMode: workflowruntime.CompletionModeTool,
			Controller:     &externallyCompletedWorkflowController{},
			Instructions:   workflowruntime.TaskInstructions{CurrentNode: currentNode},
		},
		Config{Model: "gpt-5"},
	)
	workflowIdentity := workflowruntime.CurrentNodePromptIdentity(currentNode)
	if err := engine.steer("assignment", steerMessagesWithPersistenceIntent(
		steeringPriorityRuntimeContext,
		steeringMessageEventDefault,
		true,
		[]llm.Message{{
			Role:        llm.RoleDeveloper,
			MessageType: textutil.Value(llm.MessageTypeWorkflowMode),
			SourcePath:  textutil.Value(workflowIdentity),
			Content:     textutil.Value("previous assignment"),
		}},
	)); err != nil {
		t.Fatalf("persist previous workflow assignment: %v", err)
	}
	if err := engine.steer("terminal", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventDefault,
		true,
		[]llm.Message{{
			Role:    llm.RoleAssistant,
			Phase:   textutil.Value(llm.MessagePhaseFinal),
			Content: textutil.Value("completed terminal output"),
		}},
	)); err != nil {
		t.Fatalf("persist terminal output: %v", err)
	}

	_, receipt, err := engine.compactNow(
		context.Background(),
		"workflow-post-completion",
		compactionModeWorkflowPostCompletion,
		compactionInstructionsInput{},
		false,
	)
	if err != nil || !receipt.Committed {
		t.Fatalf("workflow post-completion compaction: receipt=%+v error=%v", receipt, err)
	}
	if len(client.compactionCalls) != 1 {
		t.Fatalf("compaction calls = %d, want one", len(client.compactionCalls))
	}
	foundTerminalOutput := false
	for _, item := range client.compactionCalls[0].InputItems {
		if item.Type != llm.ResponseItemTypeMessage ||
			item.Role == nil ||
			*item.Role != llm.RoleAssistant ||
			item.Phase == nil ||
			*item.Phase != llm.MessagePhaseFinal {
			continue
		}
		foundTerminalOutput = true
	}
	if !foundTerminalOutput {
		t.Fatalf("compaction input omitted durable terminal assistant output: %+v", client.compactionCalls[0].InputItems)
	}

	workflowModes := 0
	compactionReminders := 0
	for _, item := range engine.transcriptRuntimeState().SnapshotItems() {
		if item.Type != llm.ResponseItemTypeMessage || item.MessageType == nil {
			continue
		}
		switch *item.MessageType {
		case llm.MessageTypeWorkflowMode:
			workflowModes++
		case llm.MessageTypeCompactionSoonReminder:
			compactionReminders++
		}
	}
	if workflowModes != 0 || compactionReminders != 0 {
		t.Fatalf("dormant replacement retained workflow assignment meta: workflow_modes=%d reminders=%d", workflowModes, compactionReminders)
	}

	window, err := mustMaterializeTestEventLog(t, engine.store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read replacement record: %v", err)
	}
	foundReplacement := false
	for _, record := range window.Records {
		replacement, ok := mustSessionEventPayload(record).(session.HistoryReplacementRecord)
		if !ok {
			continue
		}
		if replacement.Mode == session.CompactionModeWorkflowPostCompletion {
			foundReplacement = true
			break
		}
	}
	if !foundReplacement {
		t.Fatalf("workflow post-completion replacement mode was not persisted: %+v", window.Records)
	}
}

func TestWorkflowPostCompletionCompactionRestoresBoundaryAndLazyContinuationConsumesIt(t *testing.T) {
	t.Parallel()
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	fixture := newCommittedRemoteCompactionFixture(t, gate, nil)
	fixture.client.compactionResponses = []llm.CompactionResponse{
		remoteCompactionReplacement(1_000, 100, 200_000),
	}

	_, receipt, err := fixture.engine.compactNow(
		context.Background(),
		"workflow-post-completion",
		compactionModeWorkflowPostCompletion,
		compactionInstructionsInput{},
		false,
	)
	if err != nil || !receipt.Committed {
		t.Fatalf("workflow post-completion compaction: receipt=%+v error=%v", receipt, err)
	}
	if !fixture.engine.compactionRuntimeState().WorkflowPostCompletionBoundary() {
		t.Fatal("committed post-completion replacement did not expose a boundary")
	}

	if err := fixture.engine.Close(); err != nil {
		t.Fatalf("close source engine: %v", err)
	}
	reopenedStore := mustOpenTestSession(t, fixture.store.Dir())
	reopened := mustNewTestEngine(t, reopenedStore, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	mode, present := reopened.compactionRuntimeState().HistoryReplacementMode()
	if !present || mode == nil || *mode != string(compactionModeWorkflowPostCompletion) {
		t.Fatalf(
			"restored history replacement mode = %v, want %q",
			mode,
			compactionModeWorkflowPostCompletion,
		)
	}
	if !reopened.compactionRuntimeState().WorkflowPostCompletionBoundary() {
		t.Fatal("unconsumed post-completion boundary was not restored from active segment")
	}
	if !reopened.ConsumeWorkflowPostCompletionBoundary() ||
		reopened.compactionRuntimeState().WorkflowPostCompletionBoundary() {
		t.Fatal("restored boundary did not have one successful consumption")
	}

	receipt, err = newCompactionPersistence(reopened).replaceHistory(
		"ordinary-replacement",
		"local",
		compactionModeManual,
		llm.ItemsFromMessages([]llm.Message{{
			Role:        llm.RoleDeveloper,
			MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
			Content:     textutil.Value("ordinary replacement"),
		}}),
	)
	if err != nil || !receipt.Committed {
		t.Fatalf("ordinary replacement after restored boundary: receipt=%+v error=%v", receipt, err)
	}
	if reopened.compactionRuntimeState().WorkflowPostCompletionBoundary() {
		t.Fatal("ordinary replacement restored a stale post-completion boundary")
	}
	mode, present = reopened.compactionRuntimeState().HistoryReplacementMode()
	if !present || mode == nil || *mode != string(compactionModeManual) {
		t.Fatalf("latest replacement mode = %v, want %q", mode, compactionModeManual)
	}
}

func TestWorkflowPostCompletionCompactionKeepsCommittedReceiptAfterFinalizationDiagnostic(t *testing.T) {
	t.Parallel()
	diagnostic := errors.New("workflow post-completion finalization diagnostic")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	fixture := newCommittedRemoteCompactionFixture(t, gate, nil)
	gate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
		return snapshot.Meta.LastSequence >= 2 && snapshot.Meta.UsageState == nil
	}, diagnostic)

	workflowResult := fixture.engine.CompactContextForWorkflowPostCompletion(context.Background())
	if !workflowResult.CommitReceipt.Committed || !errors.Is(workflowResult.Diagnostic, diagnostic) {
		t.Fatalf(
			"workflow post-completion result = receipt:%+v diagnostic:%v",
			workflowResult.CommitReceipt,
			workflowResult.Diagnostic,
		)
	}
	if !fixture.engine.compactionRuntimeState().WorkflowPostCompletionBoundary() {
		t.Fatal("committed replacement lost its post-completion boundary after diagnostic")
	}
	if len(fixture.client.compactionCalls) != 1 {
		t.Fatalf("compaction calls = %d, want one", len(fixture.client.compactionCalls))
	}
}

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
	if controller.resetSessionID == nil || controller.resetSessionID.String() != engine.SessionID() {
		t.Fatalf("workflow protocol budget reset Session = %v, want %s", controller.resetSessionID, engine.SessionID())
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
	resetSessionID *runtimeids.SessionID
}

func (c *workflowProtocolBudgetController) RecordProtocolViolation(
	context.Context,
	workflowruntime.ViolationRequest,
) (workflowruntime.ViolationResult, error) {
	return workflowruntime.ViolationResult{Count: c.violationCount.Add(1)}, nil
}

func (c *workflowProtocolBudgetController) ResetProtocolViolationBudget(
	_ context.Context,
	request workflowruntime.ViolationResetRequest,
) error {
	c.resetSessionID = request.SessionID
	c.violationCount.Store(0)
	return nil
}
