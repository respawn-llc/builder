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
	"core/shared/transcript"
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
	if err := steerTestActiveStep(engine, "assignment", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{
		Role:        llm.RoleDeveloper,
		MessageType: textutil.Value(llm.MessageTypeWorkflowMode),
		SourcePath:  textutil.Value(workflowIdentity),
		Content:     textutil.Value("previous assignment"),
	}})); err != nil {
		t.Fatalf("persist previous workflow assignment: %v", err)
	}
	if err := steerTestActiveStep(engine, "user", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("workflow carryover")}})); err != nil {
		t.Fatalf("persist workflow carryover prompt: %v", err)
	}
	if err := steerTestActiveStep(engine, "terminal", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{
		Role:    llm.RoleAssistant,
		Phase:   textutil.Value(llm.MessagePhaseFinal),
		Content: textutil.Value("completed terminal output"),
	}})); err != nil {
		t.Fatalf("persist terminal output: %v", err)
	}

	stepID := runtimeTestStepID("workflow-post-completion")
	restoreStep := setTestActiveStep(engine, stepID)
	_, receipt, err := engine.compactNow(
		context.Background(),
		stepID,
		compactionModeWorkflowPostCompletion,
		compactionInstructionsInput{},
		false,
	)
	restoreStep()
	if err != nil || !receipt.Committed {
		t.Fatalf("workflow post-completion compaction: receipt=%+v error=%v", receipt, err)
	}
	if len(client.compactionCalls) != 1 {
		t.Fatalf("compaction calls = %d, want one", len(client.compactionCalls))
	}
	foundTerminalOutput := false
	for _, item := range client.compactionCalls[0].Items {
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
		t.Fatalf("compaction input omitted durable terminal assistant output: %+v", client.compactionCalls[0].Items)
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
	assertCompactionReplacementOrder(t, engine.transcriptRuntimeState().SnapshotItems(), false)

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

	var receipt session.CommitReceipt
	stepID := runtimeTestStepID("workflow-post-completion")
	err := runTestActiveStep(fixture.engine, stepID, func() error {
		var compactErr error
		_, receipt, compactErr = fixture.engine.compactNow(
			context.Background(),
			stepID,
			compactionModeWorkflowPostCompletion,
			compactionInstructionsInput{},
			false,
		)
		return compactErr
	})
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
	if !present || mode == nil || *mode != session.CompactionModeWorkflowPostCompletion {
		t.Fatalf(
			"restored history replacement mode = %v, want %q",
			mode,
			compactionModeWorkflowPostCompletion,
		)
	}
	if !reopened.compactionRuntimeState().WorkflowPostCompletionBoundary() {
		t.Fatal("unconsumed post-completion boundary was not restored from active segment")
	}
	if !reopened.compactionRuntimeState().ApplyWorkflowPostCompletionActivity(workflowPostCompletionDurableActivity) ||
		reopened.compactionRuntimeState().WorkflowPostCompletionBoundary() {
		t.Fatal("restored boundary did not have one successful consumption")
	}

	stepID = runtimeTestStepID("ordinary-replacement")
	restoreStep := setTestActiveStep(reopened, stepID)
	receipt, err = newCompactionPersistence(reopened).replaceHistory(
		stepID,
		"local",
		compactionModeManual,
		llm.ItemsFromMessages([]llm.Message{{
			Role:        llm.RoleDeveloper,
			MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
			Content:     textutil.Value("ordinary replacement"),
		}}),
	)
	restoreStep()
	if err != nil || !receipt.Committed {
		t.Fatalf("ordinary replacement after restored boundary: receipt=%+v error=%v", receipt, err)
	}
	if reopened.compactionRuntimeState().WorkflowPostCompletionBoundary() {
		t.Fatal("ordinary replacement restored a stale post-completion boundary")
	}
	mode, present = reopened.compactionRuntimeState().HistoryReplacementMode()
	if !present || mode == nil || *mode != session.CompactionModeManual {
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

	receipt, compactErr := fixture.engine.CompactContextForWorkflowPostCompletion(context.Background())
	if !receipt.Committed || !errors.Is(compactErr, diagnostic) {
		t.Fatalf(
			"workflow post-completion result = receipt:%+v diagnostic:%v",
			receipt,
			compactErr,
		)
	}
	if !fixture.engine.compactionRuntimeState().WorkflowPostCompletionBoundary() {
		t.Fatal("committed replacement lost its post-completion boundary after diagnostic")
	}
	if len(fixture.client.compactionCalls) != 1 {
		t.Fatalf("compaction calls = %d, want one", len(fixture.client.compactionCalls))
	}
}

func TestWorkflowPostCompletionCompactionPreCommitFailureDoesNotCreateBoundary(t *testing.T) {
	t.Parallel()
	fixture := newCommittedRemoteCompactionFixture(t, runtimeTestSessionPersistence, nil)
	blocker := mustBlockTestEventLogAppends(t, fixture.store)
	t.Cleanup(func() {
		if err := blocker.Restore(); err != nil {
			t.Errorf("restore event-log appends: %v", err)
		}
	})

	receipt, compactErr := fixture.engine.CompactContextForWorkflowPostCompletion(context.Background())
	if receipt.Committed || compactErr == nil {
		t.Fatalf("pre-commit workflow post-completion result = receipt:%+v diagnostic:%v", receipt, compactErr)
	}
	if fixture.engine.compactionRuntimeState().WorkflowPostCompletionBoundary() {
		t.Fatal("failed workflow replacement created a post-completion boundary")
	}
}

func TestWorkflowAssignmentApplicationPreservesPostCompletionBoundary(t *testing.T) {
	t.Parallel()
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	mode := session.CompactionModeWorkflowPostCompletion
	if err := engine.compactionRuntimeState().SetHistoryReplacementMode(&mode); err != nil {
		t.Fatalf("set post-completion replacement mode: %v", err)
	}
	message, err := buildWorkflowAssignmentMessage(workflowAssignmentForCompactionTest())
	if err != nil {
		t.Fatalf("build workflow assignment: %v", err)
	}
	receipt, err := engine.steerRuntimeWithCommitReceipt(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{message}))
	if err != nil || !receipt.Committed {
		t.Fatalf("workflow assignment application: %+v error=%v", receipt, err)
	}
	if !engine.compactionRuntimeState().WorkflowPostCompletionBoundary() {
		t.Fatal("workflow assignment steering consumed the post-completion boundary")
	}
}

func workflowAssignmentForCompactionTest() WorkflowAssignment {
	reference := workflow.CurrentNodeReference{
		TaskID: "task-assignment-receipt",
		NodeID: "node-assignment-receipt",
	}
	return WorkflowAssignment{
		ContextMode:    workflow.ContextModeNewSession,
		CompletionMode: workflowruntime.CompletionModeTool,
		Prompt: workflowruntime.PromptContract{
			Identity:       workflowruntime.CurrentNodePromptIdentity(reference),
			CompletionMode: workflowruntime.CompletionModeTool,
			Instructions: workflowruntime.TaskInstructions{
				CurrentNode:      reference,
				WorkflowID:       runtimeids.NewWorkflowID(),
				TransitionPrompt: "Perform the assigned workflow step.",
			},
		},
	}
}

func TestWorkflowPostCompletionBoundarySurvivesFailedWorkflowRequest(t *testing.T) {
	t.Parallel()
	requestErr := &llm.ProviderAPIError{Code: llm.UnifiedErrorCodeProviderContract}
	engine := mustNewWorkflowTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{errors: []error{requestErr}},
		&workflowruntime.CurrentNodeExecutionConfig{
			ScopeID:        runtimeids.NewExecutionScopeID(),
			CompletionMode: workflowruntime.CompletionModeTool,
			Controller:     &externallyCompletedWorkflowController{},
			Instructions: workflowruntime.TaskInstructions{
				CurrentNode: mustTestCurrentNodeReference(t, "task", "node", nil),
			},
		},
		Config{Model: "gpt-5"},
	)
	mode := session.CompactionModeWorkflowPostCompletion
	if err := engine.compactionRuntimeState().SetHistoryReplacementMode(&mode); err != nil {
		t.Fatalf("set post-completion replacement mode: %v", err)
	}

	if _, err := engine.SubmitWorkflowTurn(context.Background()); !errors.Is(err, requestErr) {
		t.Fatalf("failed workflow request error = %v, want %v", err, requestErr)
	}
	if !engine.compactionRuntimeState().WorkflowPostCompletionBoundary() {
		t.Fatal("failed workflow request consumed the post-completion boundary")
	}
}

func TestWorkflowContinuationPreservesBoundaryAcrossFailedCACAttempt(t *testing.T) {
	t.Parallel()
	requestErr := &llm.ProviderAPIError{
		Code: llm.UnifiedErrorCodeProviderContract,
	}
	client := &fakeClient{
		errors: []error{requestErr},
		responses: []llm.Response{
			finalTextResponse(`{"commentary":"retry succeeded"}`),
		},
	}
	engine := mustNewWorkflowTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		&workflowruntime.CurrentNodeExecutionConfig{
			ScopeID: runtimeids.NewExecutionScopeID(),
			Contract: workflowruntime.CompletionContract{
				Transitions: []workflowruntime.CompletionTransition{{ID: "done"}},
			},
			CompletionMode: workflowruntime.CompletionModeUnstructuredOutput,
			Controller:     &externallyCompletedWorkflowController{},
			Instructions: workflowruntime.TaskInstructions{
				CurrentNode: mustTestCurrentNodeReference(t, "task", "node", nil),
			},
		},
		Config{Model: "gpt-5"},
	)
	mode := session.CompactionModeWorkflowPostCompletion
	if err := engine.compactionRuntimeState().SetHistoryReplacementMode(&mode); err != nil {
		t.Fatalf("set post-completion replacement mode: %v", err)
	}
	headlessType := llm.MessageTypeHeadlessMode
	if err := steerTestActiveStep(engine, "meta", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: &headlessType}})); err != nil {
		t.Fatalf("steer canonical meta context: %v", err)
	}

	if _, err := engine.SubmitWorkflowContinuationTurn(context.Background()); !errors.Is(err, requestErr) {
		t.Fatalf("first CAC attempt error = %v, want %v", err, requestErr)
	}
	if !engine.compactionRuntimeState().WorkflowPostCompletionBoundary() {
		t.Fatal("failed CAC attempt consumed the committed boundary")
	}
	if _, err := engine.SubmitWorkflowContinuationTurn(context.Background()); err != nil {
		t.Fatalf("retry CAC attempt: %v", err)
	}
	if engine.compactionRuntimeState().WorkflowPostCompletionBoundary() {
		t.Fatal("successful CAC retry did not consume the committed boundary")
	}
	if len(client.calls) != 2 {
		t.Fatalf("CAC model calls = %d, want failed attempt plus retry", len(client.calls))
	}
}

func TestWorkflowPostCompletionRestoreIgnoresCacheRequestObservation(t *testing.T) {
	t.Parallel()
	state := newCompactionRuntimeState()
	mode := session.CompactionModeWorkflowPostCompletion
	if err := state.SetHistoryReplacementMode(&mode); err != nil {
		t.Fatalf("set post-completion replacement mode: %v", err)
	}

	activity := workflowPostCompletionActivityForSessionRecord(session.CacheRequestObservationRecord{})
	if activity != workflowPostCompletionNoActivity {
		t.Fatalf("cache request observation activity = %d, want no activity", activity)
	}
	if state.ApplyWorkflowPostCompletionActivity(activity) {
		t.Fatal("cache request observation consumed the post-completion boundary")
	}
	if !state.WorkflowPostCompletionBoundary() {
		t.Fatal("cache request observation removed the post-completion boundary")
	}
}

func TestWorkflowPostCompletionBoundaryPreservesLocalDiagnosticSteering(t *testing.T) {
	t.Parallel()
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	mode := session.CompactionModeWorkflowPostCompletion
	if err := engine.compactionRuntimeState().SetHistoryReplacementMode(&mode); err != nil {
		t.Fatalf("set post-completion replacement mode: %v", err)
	}

	if err := steerTestActiveStep(engine, "diagnostic", steerLocalEntryIntent(storedLocalEntry{
		Role: string(transcript.EntryRoleDeveloperErrorFeedback),
		Text: "internal compaction repair",
	})); err != nil {
		t.Fatalf("persist local diagnostic: %v", err)
	}
	if !engine.compactionRuntimeState().WorkflowPostCompletionBoundary() {
		t.Fatal("local diagnostic steering consumed the post-completion boundary")
	}
}

func TestHistoryReplacementModeRejectsEmptyPresentValue(t *testing.T) {
	t.Parallel()
	state := newCompactionRuntimeState()
	empty := session.CompactionMode("")
	if err := state.SetHistoryReplacementMode(&empty); err == nil {
		t.Fatal("empty present history replacement mode was accepted")
	}
	if err := state.SetHistoryReplacementMode(nil); err != nil {
		t.Fatalf("clear absent history replacement mode: %v", err)
	}
}

func TestWorkflowPostCompletionActivityPolicyPreservesMetaAndConsumesActivity(t *testing.T) {
	t.Parallel()
	preservedMessageTypes := []llm.MessageType{
		llm.MessageTypeAgentsMD,
		llm.MessageTypeSkills,
		llm.MessageTypeSubagents,
		llm.MessageTypeEnvironment,
		llm.MessageTypeHeadlessMode,
		llm.MessageTypeHeadlessModeExit,
		llm.MessageTypeActiveGoalContinuation,
		llm.MessageTypeWorkflowMode,
		llm.MessageTypeWorktreeMode,
		llm.MessageTypeWorktreeModeExit,
		llm.MessageTypeCompactionSoonReminder,
	}
	for _, messageType := range preservedMessageTypes {
		t.Run(string(messageType), func(t *testing.T) {
			state := newCompactionRuntimeState()
			mode := session.CompactionModeWorkflowPostCompletion
			if err := state.SetHistoryReplacementMode(&mode); err != nil {
				t.Fatalf("set replacement mode: %v", err)
			}
			message := llm.Message{
				Role:        llm.RoleDeveloper,
				MessageType: &messageType,
				SourcePath:  textutil.Value("workflow-test"),
			}
			if activity := workflowPostCompletionMessageActivity(message); activity != workflowPostCompletionNoActivity {
				t.Fatalf("meta message activity = %d, want no activity", activity)
			}
			state.ApplyWorkflowPostCompletionActivity(workflowPostCompletionNoActivity)
			if !state.WorkflowPostCompletionBoundary() {
				t.Fatal("meta message consumed the boundary")
			}
		})
	}

	state := newCompactionRuntimeState()
	mode := session.CompactionModeWorkflowPostCompletion
	if err := state.SetHistoryReplacementMode(&mode); err != nil {
		t.Fatalf("set replacement mode: %v", err)
	}
	if activity := workflowPostCompletionMessageActivity(llm.Message{Role: llm.RoleUser}); activity != workflowPostCompletionDurableActivity {
		t.Fatalf("ordinary message activity = %d, want durable activity", activity)
	}
	if !state.ApplyWorkflowPostCompletionActivity(workflowPostCompletionDurableActivity) ||
		state.WorkflowPostCompletionBoundary() {
		t.Fatal("ordinary activity did not consume the boundary exactly once")
	}
	if activity := workflowPostCompletionActivityForSteeringItem(steeringItem{
		queuedRestore: &steeringQueuedUserMessageRestore{},
	}); activity != workflowPostCompletionNoActivity {
		t.Fatalf("queued restore activity = %d, want no activity", activity)
	}
	if activity := workflowPostCompletionActivityForSteeringItem(steeringItem{
		goalNoticeAndStatus: &steeringGoalNoticeAndStatus{},
	}); activity != workflowPostCompletionDurableActivity {
		t.Fatalf("goal notice activity = %d, want durable activity", activity)
	}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	if err := engine.compactionRuntimeState().SetHistoryReplacementMode(&mode); err != nil {
		t.Fatalf("set engine replacement mode: %v", err)
	}
	if err := steerTestActiveStep(engine, "restore", steerQueuedUserMessageRestoreIntent(nil)); err != nil {
		t.Fatalf("steer queued restore: %v", err)
	}
	if !engine.compactionRuntimeState().WorkflowPostCompletionBoundary() {
		t.Fatal("queued restore consumed the boundary")
	}
}

func TestWorkflowPostCompletionCompactionUsesLocalGenerateClient(t *testing.T) {
	t.Parallel()
	client := &fakeClient{
		caps: llm.ProviderCapabilities{
			ProviderID:               "local",
			SupportsResponsesAPI:     true,
			SupportsResponsesCompact: false,
		},
		responses: []llm.Response{{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("local workflow summary"),
			},
		}},
	}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		tools.NewRegistry(),
		Config{Model: "gpt-5", CompactionMode: "local"},
	)
	if err := steerTestActiveStep(engine, "user", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("local workflow carryover")}})); err != nil {
		t.Fatalf("persist local workflow carryover prompt: %v", err)
	}
	if err := steerTestActiveStep(engine, "terminal", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{
		Role:    llm.RoleAssistant,
		Phase:   textutil.Value(llm.MessagePhaseFinal),
		Content: textutil.Value("completed terminal output"),
	}})); err != nil {
		t.Fatalf("persist terminal output: %v", err)
	}

	receipt, compactErr := engine.CompactContextForWorkflowPostCompletion(context.Background())
	if !receipt.Committed || compactErr != nil {
		t.Fatalf("local workflow post-completion result = receipt:%+v diagnostic:%v", receipt, compactErr)
	}
	if len(client.calls) != 1 {
		t.Fatalf("local Generate calls = %d, want one", len(client.calls))
	}
	if !engine.compactionRuntimeState().WorkflowPostCompletionBoundary() {
		t.Fatal("local workflow compaction did not commit its post-completion boundary")
	}
	assertCompactionReplacementOrder(t, engine.transcriptRuntimeState().SnapshotItems(), false)
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
	if err := steerTestActiveStep(engine, "stale", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{
		Role:        llm.RoleDeveloper,
		MessageType: textutil.Value(llm.MessageTypeWorkflowMode),
		SourcePath:  textutil.Value("stale-workflow"),
		Content:     textutil.Value("stale"),
	}})); err != nil {
		t.Fatalf("persist stale workflow context: %v", err)
	}
	if err := steerTestActiveStep(engine, "input", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}})); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}

	compactionStepID := runtimeTestStepID("compact")
	restoreStep := setTestActiveStep(engine, compactionStepID)
	if _, _, err := engine.compactNow(context.Background(), compactionStepID, compactionModeManual, compactionInstructionsInput{}, false); err != nil {
		t.Fatalf("compact workflow context: %v", err)
	}
	restoreStep()
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
			if err := steerTestActiveStep(engine, "input", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{
				{
					Role:        llm.RoleDeveloper,
					MessageType: textutil.Value(llm.MessageTypeWorkflowMode),
					SourcePath:  textutil.Value(existingIdentity),
					Content:     textutil.Value("existing workflow instructions"),
				},
				{Role: llm.RoleUser, Content: textutil.Value("input")},
			})); err != nil {
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
	if err := steerTestActiveStep(engine, "input", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}})); err != nil {
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

	stepID := runtimeTestStepID("compact")
	restoreStep := setTestActiveStep(engine, stepID)
	_, receipt, err := engine.compactNow(
		context.Background(),
		stepID,
		compactionModeManual,
		compactionInstructionsInput{},
		false,
	)
	restoreStep()
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
