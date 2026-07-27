package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/metadata"
	"core/server/workflow"
	"core/shared/config"
	"core/shared/serverapi"
)

type runLifecycleFixture struct {
	ctx     context.Context
	store   *Store
	binding metadata.Binding
	cfg     config.App
	task    TaskRecord
	started StartTaskResult
}

func newRunLifecycleFixture(t *testing.T) runLifecycleFixture {
	t.Helper()
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	return runLifecycleFixture{
		ctx:     ctx,
		store:   store,
		binding: binding,
		cfg:     cfg,
		task:    task,
		started: startTask(t, ctx, store, task.ID),
	}
}

func (f runLifecycleFixture) claim(t *testing.T) RunnableRunRecord {
	t.Helper()
	return claimRunFixture(t, f.ctx, f.store, f.started.RunID, 0)
}

func (f runLifecycleFixture) attachSession(t *testing.T, claimed RunnableRunRecord) string {
	t.Helper()
	return createAndAttachRunSessionFixture(t, f.ctx, f.store, f.binding, f.cfg, claimed.ID, claimed.Generation)
}

func (f runLifecycleFixture) runs(t *testing.T) []RunRecord {
	t.Helper()
	runs, err := f.store.ListRuns(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	return runs
}

func (f runLifecycleFixture) resume(t *testing.T) []RunRecord {
	t.Helper()
	runs, err := f.store.ResumeTaskRuns(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("ResumeTaskRuns: %v", err)
	}
	return runs.Runs
}

func (f runLifecycleFixture) recordViolation(t *testing.T, req RecordProtocolViolationRequest) RecordProtocolViolationResult {
	t.Helper()
	req.RunID = f.started.RunID
	result, err := f.store.RecordProtocolViolation(f.ctx, req)
	if err != nil {
		t.Fatalf("RecordProtocolViolation: %v", err)
	}
	return result
}

func idleSessionCompletionAdmission(t *testing.T, sessionID string) CompletionAdmission {
	t.Helper()
	admission, err := NewIdleSessionCompletionAdmission(sessionID)
	if err != nil {
		t.Fatalf("NewIdleSessionCompletionAdmission: %v", err)
	}
	return admission
}

func TestRecordProtocolViolationInterruptsAtCap(t *testing.T) {
	f := newRunLifecycleFixture(t)
	first := f.recordViolation(t, RecordProtocolViolationRequest{Kind: ProtocolViolationInvalidCompletion, MaxCount: 2, Detail: `{"detail":"first"}`})
	if first.Count != 1 || first.Interrupted {
		t.Fatalf("first violation = %+v, want count 1 active", first)
	}
	second := f.recordViolation(t, RecordProtocolViolationRequest{Kind: ProtocolViolationInvalidCompletion, MaxCount: 2, Detail: `{"detail":"second"}`})
	if second.Count != 2 || !second.Interrupted {
		t.Fatalf("second violation = %+v, want count 2 interrupted", second)
	}
	runs := f.runs(t)
	if len(runs) != 1 || runs[0].InvalidCompletions != 2 || runs[0].InterruptedAt == nil || runs[0].InterruptionReason == nil || *runs[0].InterruptionReason != "workflow_protocol_violation_limit" {
		t.Fatalf("run after cap = %+v", runs)
	}
}

func TestResetProtocolViolationBudgetResetsCurrentRunGeneration(t *testing.T) {
	f := newRunLifecycleFixture(t)
	claimed := f.claim(t)
	f.recordViolation(t, RecordProtocolViolationRequest{
		Kind:               ProtocolViolationInvalidCompletion,
		MaxCount:           2,
		Detail:             `{"detail":"before compaction"}`,
		ExpectedGeneration: claimed.Generation,
		RequireGeneration:  true,
	})
	if err := f.store.ResetProtocolViolationBudget(f.ctx, ResetProtocolViolationBudgetRequest{
		RunID:              f.started.RunID,
		ExpectedGeneration: claimed.Generation,
		RequireGeneration:  true,
	}); err != nil {
		t.Fatalf("ResetProtocolViolationBudget: %v", err)
	}

	afterReset := f.recordViolation(t, RecordProtocolViolationRequest{
		Kind:               ProtocolViolationInvalidCompletion,
		MaxCount:           2,
		Detail:             `{"detail":"after compaction"}`,
		ExpectedGeneration: claimed.Generation,
		RequireGeneration:  true,
	})
	if afterReset.Count != 1 || afterReset.Interrupted {
		t.Fatalf("violation after reset = %+v, want count 1 active", afterReset)
	}
}

func TestResetProtocolViolationBudgetRevivesProtocolInterruptedRun(t *testing.T) {
	f := newRunLifecycleFixture(t)
	claimed := f.claim(t)
	violation := f.recordViolation(t, RecordProtocolViolationRequest{
		Kind:               ProtocolViolationInvalidCompletion,
		MaxCount:           1,
		Detail:             `{"detail":"invalid completion"}`,
		ExpectedGeneration: claimed.Generation,
		RequireGeneration:  true,
	})
	if !violation.Interrupted {
		t.Fatalf("protocol violation = %+v, want interrupted run", violation)
	}

	if err := f.store.ResetProtocolViolationBudget(f.ctx, ResetProtocolViolationBudgetRequest{
		RunID:              f.started.RunID,
		ExpectedGeneration: claimed.Generation,
		RequireGeneration:  true,
	}); err != nil {
		t.Fatalf("ResetProtocolViolationBudget: %v", err)
	}

	runs := f.runs(t)
	if len(runs) != 1 ||
		runs[0].Generation != claimed.Generation ||
		runs[0].InvalidCompletions != 0 ||
		runs[0].InterruptedAt != nil ||
		runs[0].InterruptionReason != nil {
		t.Fatalf("run after protocol budget reset = %+v, want active current-generation run", runs)
	}

	afterReset := f.recordViolation(t, RecordProtocolViolationRequest{
		Kind:               ProtocolViolationInvalidCompletion,
		MaxCount:           2,
		Detail:             `{"detail":"after reset"}`,
		ExpectedGeneration: claimed.Generation,
		RequireGeneration:  true,
	})
	if afterReset.Count != 1 || afterReset.Interrupted {
		t.Fatalf("violation after revived budget reset = %+v, want first active violation", afterReset)
	}
}

func TestResetProtocolViolationBudgetDoesNotReviveOtherInterruptions(t *testing.T) {
	f := newRunLifecycleFixture(t)
	claimed := f.claim(t)
	if err := f.store.InterruptRun(f.ctx, f.started.RunID, "user_interrupt", "{}"); err != nil {
		t.Fatalf("InterruptRun: %v", err)
	}

	err := f.store.ResetProtocolViolationBudget(f.ctx, ResetProtocolViolationBudgetRequest{
		RunID:              f.started.RunID,
		ExpectedGeneration: claimed.Generation,
		RequireGeneration:  true,
	})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("ResetProtocolViolationBudget error = %v, want sql.ErrNoRows", err)
	}

	runs := f.runs(t)
	if len(runs) != 1 ||
		runs[0].InterruptedAt == nil ||
		runs[0].InterruptionReason == nil ||
		*runs[0].InterruptionReason != "user_interrupt" {
		t.Fatalf("run after protocol budget reset = %+v, want user interruption preserved", runs)
	}
}

func TestResumeTaskRunsResetsProtocolViolationBudget(t *testing.T) {
	for _, tc := range []struct {
		name     string
		maxCount int
	}{
		{name: "before cap", maxCount: 2},
		{name: "at cap", maxCount: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRunLifecycleFixture(t)
			initial := f.recordViolation(t, RecordProtocolViolationRequest{Kind: ProtocolViolationInvalidCompletion, MaxCount: tc.maxCount, Detail: `{"detail":"before resume"}`})
			if tc.maxCount == 1 {
				if initial.Count != 1 || !initial.Interrupted {
					t.Fatalf("capped violation = %+v, want count 1 interrupted", initial)
				}
			} else if err := f.store.InterruptRun(f.ctx, f.started.RunID, "user_interrupt", "{}"); err != nil {
				t.Fatalf("InterruptRun: %v", err)
			}
			f.resume(t)

			resumed := f.recordViolation(t, RecordProtocolViolationRequest{Kind: ProtocolViolationInvalidCompletion, MaxCount: 2, Detail: `{"detail":"after resume"}`})
			if resumed.Count != 1 || resumed.Interrupted {
				t.Fatalf("violation after resume = %+v, want count 1 active", resumed)
			}
		})
	}
}

func TestNodeTransitionStartsWithFreshProtocolViolationBudget(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)

	if _, err := store.RecordProtocolViolation(ctx, RecordProtocolViolationRequest{
		RunID:    started.RunID,
		Kind:     ProtocolViolationInvalidCompletion,
		MaxCount: 2,
		Detail:   `{"detail":"plan attempt"}`,
	}); err != nil {
		t.Fatalf("RecordProtocolViolation plan: %v", err)
	}
	completeRun(t, ctx, store, CompleteRunRequest{
		RunID:        started.RunID,
		TransitionID: "next",
		OutputValues: map[string]string{"prior_summary": "plan complete"},
	})

	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implementation := nodeByKey(t, def, "implement")
	nextRun := runForNode(t, ctx, store, task.ID, workflow.NodeIDOf(implementation))
	if nextRun.InvalidCompletions != 0 {
		t.Fatalf("new node run invalid completions = %d, want 0", nextRun.InvalidCompletions)
	}
	nextViolation, err := store.RecordProtocolViolation(ctx, RecordProtocolViolationRequest{
		RunID:    nextRun.ID,
		Kind:     ProtocolViolationInvalidCompletion,
		MaxCount: 2,
		Detail:   `{"detail":"implementation attempt"}`,
	})
	if err != nil {
		t.Fatalf("RecordProtocolViolation implementation: %v", err)
	}
	if nextViolation.Count != 1 || nextViolation.Interrupted {
		t.Fatalf("violation after node transition = %+v, want count 1 active", nextViolation)
	}
}

func TestSetRunEffectiveCompletionModePersistsAndRefusesDrift(t *testing.T) {
	f := newRunLifecycleFixture(t)
	claimed := f.claim(t)
	if claimed.EffectiveCompletionMode != nil {
		t.Fatalf("claimed effective mode = %v, want absent before resolution", claimed.EffectiveCompletionMode)
	}
	if err := f.store.SetRunEffectiveCompletionMode(f.ctx, f.started.RunID, claimed.Generation, "invalid"); !errors.Is(err, ErrInvalidEffectiveCompletionMode) {
		t.Fatalf("SetRunEffectiveCompletionMode invalid error = %v, want ErrInvalidEffectiveCompletionMode", err)
	}
	if err := f.store.SetRunEffectiveCompletionMode(f.ctx, f.started.RunID, claimed.Generation, "shell_command"); err != nil {
		t.Fatalf("SetRunEffectiveCompletionMode: %v", err)
	}
	runs := f.runs(t)
	if len(runs) != 1 || runs[0].EffectiveCompletionMode == nil || *runs[0].EffectiveCompletionMode != "shell_command" {
		t.Fatalf("run effective mode = %+v, want shell_command", runs)
	}
	if err := f.store.SetRunEffectiveCompletionMode(f.ctx, f.started.RunID, claimed.Generation, "shell_command"); err != nil {
		t.Fatalf("SetRunEffectiveCompletionMode same mode: %v", err)
	}
	if err := f.store.SetRunEffectiveCompletionMode(f.ctx, f.started.RunID, claimed.Generation, "tool"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("SetRunEffectiveCompletionMode drift error = %v, want sql.ErrNoRows", err)
	}
}

func TestWorkflowNodeCompletionModePersistsThroughGraphAndRunSnapshot(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agent := nodeByKey(t, def, "agent")
	if _, err := store.UpdateNode(ctx, NodeRecord{ID: workflow.NodeIDOf(agent), WorkflowID: workflowID, Key: workflow.NodeKey(agent), Kind: agent.Kind(), DisplayName: workflow.NodeDisplayName(agent), SubagentRole: workflow.NodeSubagentRole(agent), PromptTemplate: workflow.NodePromptTemplate(agent), CompletionMode: "tool", OutputFields: workflow.NodeOutputFields(agent)}); err != nil {
		t.Fatalf("UpdateNode completion mode: %v", err)
	}
	updated, updatedRecord, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition updated: %v", err)
	}
	updatedAgent := nodeByKey(t, updated, "agent")
	if workflow.NodeCompletionMode(updatedAgent) != "tool" || updatedRecord.Version != record.Version+1 {
		t.Fatalf("updated agent mode=%q version=%d, want tool version %d", workflow.NodeCompletionMode(updatedAgent), updatedRecord.Version, record.Version+1)
	}
	if _, err := store.UpdateNode(ctx, NodeRecord{ID: "node-terminal-invalid", WorkflowID: workflowID, Key: "invalid", Kind: workflow.NodeKindTerminal, DisplayName: "Invalid", CompletionMode: "tool"}); err == nil {
		t.Fatal("UpdateNode accepted completion mode override on terminal node")
	}
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	claimed := claimRunFixture(t, ctx, store, started.RunID, 0)
	input, err := store.GetRunStartContext(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("GetRunStartContext: %v", err)
	}
	if input.Node.CompletionMode != "tool" {
		t.Fatalf("run start node completion mode = %q, want tool", input.Node.CompletionMode)
	}
}

func TestCompleteRunRejectsStaleGeneration(t *testing.T) {
	f := newRunLifecycleFixture(t)
	claimed := f.claim(t)
	if _, err := f.store.CompleteRun(f.ctx, CompleteRunRequest{RunID: f.started.RunID, TransitionID: "done", ExpectedGeneration: 0, RequireGeneration: true}); !errors.Is(err, ErrStaleRunGeneration) {
		t.Fatalf("expected stale generation error, got %v", err)
	}
	completeRun(t, f.ctx, f.store, CompleteRunRequest{RunID: f.started.RunID, TransitionID: "done", ExpectedGeneration: claimed.Generation, RequireGeneration: true})
}

func TestRunStartContextHandlesMissingInputEdgeAndRejectsNonArrayInputJSON(t *testing.T) {
	f := newRunLifecycleFixture(t)
	// Intentional corruption fixture: delete the transition-edge snapshot to
	// verify run context handles legacy/corrupt missing-edge rows safely.
	if _, err := f.store.db.ExecContext(f.ctx, `DELETE FROM task_transition_edges WHERE target_placement_id = ?`, string(f.started.PlacementID)); err != nil {
		t.Fatalf("delete transition edge snapshot: %v", err)
	}
	input, err := f.store.GetRunStartContext(f.ctx, f.started.RunID)
	if err != nil {
		t.Fatalf("GetRunStartContext without input edge: %v", err)
	}
	if len(input.InputValues) != 0 {
		t.Fatalf("input values without input edge = %+v, want empty", input.InputValues)
	}
	taskWithInvalidInputs := createDefaultTask(t, f.ctx, f.store, f.binding.ProjectID)
	startedInvalidInputs := startTask(t, f.ctx, f.store, taskWithInvalidInputs.ID)
	// Intentional corruption fixture: force non-array input JSON that product
	// APIs cannot create, then assert the store rejects it at read boundary.
	if _, err := f.store.db.ExecContext(f.ctx, `UPDATE task_transition_edges SET input_bindings_json = '{}' WHERE target_placement_id = ?`, string(startedInvalidInputs.PlacementID)); err == nil {
		t.Fatalf("expected non-array transition edge input bindings to be rejected")
	}
	taskWithJoinInputs := createDefaultTask(t, f.ctx, f.store, f.binding.ProjectID)
	startedJoinInputs := startTask(t, f.ctx, f.store, taskWithJoinInputs.ID)
	joinInputsJSON, err := workflow.MarshalString([]workflow.InputBinding{{Name: "joined", Source: workflow.BindingSourceJoin, Field: "aggregate"}})
	if err != nil {
		t.Fatalf("marshal join inputs: %v", err)
	}
	// Intentional snapshot fixture: inject a join binding into the stored edge
	// snapshot to verify join-sourced inputs resolve through run context.
	if _, err := f.store.db.ExecContext(f.ctx, `UPDATE task_transition_edges SET input_bindings_json = ? WHERE target_placement_id = ?`, joinInputsJSON, string(startedJoinInputs.PlacementID)); err != nil {
		t.Fatalf("set join transition edge inputs: %v", err)
	}
	joinInput, err := f.store.GetRunStartContext(f.ctx, startedJoinInputs.RunID)
	if err != nil {
		t.Fatalf("GetRunStartContext join inputs: %v", err)
	}
	if joinInput.InputValues["joined"] != "" {
		t.Fatalf("join input without aggregate = %+v, want empty value", joinInput.InputValues)
	}
}

func TestAttachRunSessionGenerationGuard(t *testing.T) {
	f := newRunLifecycleFixture(t)
	claimed := f.claim(t)
	sessionID := createTestSession(t, f.ctx, f.store, f.binding, f.cfg)
	if err := f.store.AttachRunSession(f.ctx, f.started.RunID, claimed.Generation-1, "session-stale"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale AttachRunSession error = %v, want sql.ErrNoRows", err)
	}
	if err := f.store.AttachRunSession(f.ctx, f.started.RunID, claimed.Generation, sessionID); err != nil {
		t.Fatalf("AttachRunSession current generation: %v", err)
	}
	if err := f.store.AttachRunSession(f.ctx, f.started.RunID, claimed.Generation, "session-second"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second AttachRunSession error = %v, want sql.ErrNoRows", err)
	}
	runs := f.runs(t)
	if runs[0].SessionID != sessionID {
		t.Fatalf("attached session = %q, want %q", runs[0].SessionID, sessionID)
	}
}

func TestSetAndClearRunWaitingAskGenerationGuard(t *testing.T) {
	f := newRunLifecycleFixture(t)
	claimed := f.claim(t)
	sessionID := f.attachSession(t, claimed)

	if err := f.store.SetRunWaitingAsk(f.ctx, f.started.RunID, claimed.Generation-1, "ask-stale"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale SetRunWaitingAsk error = %v, want sql.ErrNoRows", err)
	}
	if err := f.store.SetRunWaitingAsk(f.ctx, f.started.RunID, claimed.Generation, "ask-1"); err != nil {
		t.Fatalf("SetRunWaitingAsk current generation: %v", err)
	}
	waiting, err := f.store.ListWaitingAskRuns(f.ctx)
	if err != nil {
		t.Fatalf("ListWaitingAskRuns: %v", err)
	}
	if len(waiting) != 1 || waiting[0].ID != f.started.RunID || waiting[0].WaitingAskID == nil || *waiting[0].WaitingAskID != "ask-1" || waiting[0].SessionID != sessionID {
		t.Fatalf("waiting runs = %+v", waiting)
	}
	if _, err := f.store.ClaimRun(f.ctx, f.started.RunID, claimed.Generation); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("ClaimRun while waiting error = %v, want sql.ErrNoRows", err)
	}
	if err := f.store.ClearRunWaitingAsk(f.ctx, f.started.RunID, claimed.Generation-1, "ask-1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale ClearRunWaitingAsk error = %v, want sql.ErrNoRows", err)
	}
	if err := f.store.ClearRunWaitingAsk(f.ctx, f.started.RunID, claimed.Generation, "ask-other"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("wrong ask ClearRunWaitingAsk error = %v, want sql.ErrNoRows", err)
	}
	if err := f.store.ClearRunWaitingAsk(f.ctx, f.started.RunID, claimed.Generation, "ask-1"); err != nil {
		t.Fatalf("ClearRunWaitingAsk current ask: %v", err)
	}
	runs := f.runs(t)
	if runs[0].WaitingAskID != nil || runs[0].CompletedAt != nil || runs[0].InterruptedAt != nil {
		t.Fatalf("run after clear = %+v", runs[0])
	}
}

func TestSetAndClearRunWaitingAskPublishTaskEvents(t *testing.T) {
	f := newRunLifecycleFixture(t)
	f.store.now = func() time.Time { return time.UnixMilli(1234).UTC() }
	claimed := f.claim(t)
	f.attachSession(t, claimed)
	sink := &recordingWorkflowEventPublisher{}
	f.store.SetWorkflowEventPublisher(sink)

	if err := f.store.SetRunWaitingAsk(f.ctx, f.started.RunID, claimed.Generation, "ask-1"); err != nil {
		t.Fatalf("SetRunWaitingAsk: %v", err)
	}
	if err := f.store.ClearRunWaitingAsk(f.ctx, f.started.RunID, claimed.Generation, "ask-1"); err != nil {
		t.Fatalf("ClearRunWaitingAsk: %v", err)
	}

	if len(sink.records) != 2 {
		t.Fatalf("published records = %+v, want waiting and cleared events", sink.records)
	}
	for _, record := range sink.records {
		if !workflowEventIDEquals(record.ProjectID, f.binding.ProjectID) || !workflowEventIDEquals(record.WorkflowID, string(f.task.WorkflowID)) || record.Resource != "task" {
			t.Fatalf("published record identity = %+v", record)
		}
		if record.OccurredAtUnixMs != 1234 {
			t.Fatalf("published record time = %d, want 1234", record.OccurredAtUnixMs)
		}
		if record.PrimaryEntityID != string(f.task.ID) || len(record.RelatedIDs) != 2 || record.RelatedIDs[0] != string(f.started.RunID) || record.RelatedIDs[1] != "ask-1" {
			t.Fatalf("published record entity ids = %q %+v", record.PrimaryEntityID, record.RelatedIDs)
		}
	}
	if sink.records[0].Action != "question_waiting" || sink.records[1].Action != "question_cleared" {
		t.Fatalf("published actions = %+v, want question_waiting then question_cleared", []serverapi.WorkflowProjectEventAction{sink.records[0].Action, sink.records[1].Action})
	}
}

func TestInterruptRunGenerationGuard(t *testing.T) {
	f := newRunLifecycleFixture(t)
	claimed := f.claim(t)
	if err := f.store.InterruptRunGeneration(f.ctx, f.started.RunID, claimed.Generation-1, "stale", "{}"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale InterruptRunGeneration error = %v, want sql.ErrNoRows", err)
	}
	runs := f.runs(t)
	if runs[0].InterruptedAt != nil {
		t.Fatalf("stale generation interrupted run: %+v", runs[0])
	}
	if err := f.store.InterruptRunGeneration(f.ctx, f.started.RunID, claimed.Generation, "current", "{}"); err != nil {
		t.Fatalf("InterruptRunGeneration current generation: %v", err)
	}
	if err := f.store.InterruptRunGeneration(f.ctx, f.started.RunID, claimed.Generation, "second", "{}"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second InterruptRunGeneration error = %v, want sql.ErrNoRows", err)
	}
	runs = f.runs(t)
	if runs[0].InterruptedAt == nil || runs[0].InterruptionReason == nil || *runs[0].InterruptionReason != "current" {
		t.Fatalf("run after interrupt = %+v, want current interruption", runs[0])
	}
}

func TestInterruptRunBlankReasonPersistsNull(t *testing.T) {
	for _, tc := range []struct {
		name       string
		generation bool
	}{
		{name: "run"},
		{name: "generation", generation: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRunLifecycleFixture(t)
			claimed := f.claim(t)
			var err error
			if tc.generation {
				err = f.store.InterruptRunGeneration(f.ctx, f.started.RunID, claimed.Generation, " \t ", "{}")
			} else {
				err = f.store.InterruptRun(f.ctx, f.started.RunID, " \t ", "{}")
			}
			if err != nil {
				t.Fatalf("interrupt blank reason: %v", err)
			}
			runs := f.runs(t)
			if len(runs) != 1 || runs[0].InterruptedAt == nil || runs[0].InterruptionReason != nil {
				t.Fatalf("interrupted run = %+v, want interrupted run with nil reason", runs)
			}
		})
	}
}

func TestResumeTaskRunRequeuesInterruptedRunWithSameSession(t *testing.T) {
	f := newRunLifecycleFixture(t)
	claimed := f.claim(t)
	sessionID := f.attachSession(t, claimed)
	if err := f.store.InterruptRunGeneration(f.ctx, f.started.RunID, claimed.Generation, "manual", `{"reason":"test"}`); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}
	interrupted := f.runs(t)[0]

	result, err := f.store.ResumeTaskRuns(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("ResumeTaskRuns: %v", err)
	}
	if len(result.ResolvedInterruptedRunProjections) != 1 {
		t.Fatalf("resolved interruption projections = %+v, want one", result.ResolvedInterruptedRunProjections)
	}
	projection := result.ResolvedInterruptedRunProjections[0]
	if projection.RunID != f.started.RunID || projection.SessionID != sessionID || projection.InterruptionReason != "manual" || projection.InterruptionDetailJSON == nil || *projection.InterruptionDetailJSON != `{"reason":"test"}` || interrupted.InterruptedAt == nil || projection.OccurredAtUnixMs != *interrupted.InterruptedAt {
		t.Fatalf("resolved interruption projection = %+v, interrupted run = %+v", projection, interrupted)
	}
	if len(result.Runs) != 1 {
		t.Fatalf("resumed runs = %+v, want one", result.Runs)
	}
	resumed := result.Runs[0]
	if resumed.ID != f.started.RunID || resumed.SessionID != sessionID || resumed.StartedAt != nil || resumed.InterruptedAt != nil || resumed.Generation <= claimed.Generation {
		t.Fatalf("resumed run = %+v, want same run/session requeued with newer generation", resumed)
	}
	if resumed.AutomationRequestedAt != nil {
		t.Fatalf("resumed run persisted automatic intent: %+v", resumed)
	}
	reclaimed := claimRunFixture(t, f.ctx, f.store, f.started.RunID, resumed.Generation)
	if err := f.store.AttachRunSession(f.ctx, f.started.RunID, reclaimed.Generation, sessionID); err != nil {
		t.Fatalf("AttachRunSession same session after resume: %v", err)
	}
}

func TestCompleteInterruptedRunFromIdleSessionSupersedesInterruption(t *testing.T) {
	f := newRunLifecycleFixture(t)
	claimed := f.claim(t)
	sessionID := f.attachSession(t, claimed)
	if err := f.store.InterruptRunGeneration(
		f.ctx,
		f.started.RunID,
		claimed.Generation,
		"workflow_runtime_failed",
		`{"error":"provider unavailable"}`,
	); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}

	completed, err := f.store.CompleteRun(f.ctx, CompleteRunRequest{
		RunID:              f.started.RunID,
		ExpectedGeneration: claimed.Generation,
		RequireGeneration:  true,
		Admission:          idleSessionCompletionAdmission(t, sessionID),
	})
	if err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}
	if len(completed.Result.ResolvedInterruptedRunProjections) != 1 {
		t.Fatalf("resolved interrupted run projections = %+v, want one", completed.Result.ResolvedInterruptedRunProjections)
	}
	projection := completed.Result.ResolvedInterruptedRunProjections[0]
	if projection.RunID != f.started.RunID || projection.SessionID != sessionID {
		t.Fatalf("resolved interrupted run projection = %+v, want run %s session %s", projection, f.started.RunID, sessionID)
	}
	runs := f.runs(t)
	if len(runs) != 1 || runs[0].CompletedAt == nil || runs[0].InterruptedAt != nil || runs[0].InterruptionReason != nil {
		t.Fatalf("completed run = %+v, want completed non-interrupted source", runs)
	}
	persisted, err := f.store.queries.GetTaskRun(f.ctx, string(f.started.RunID))
	if err != nil {
		t.Fatalf("GetTaskRun: %v", err)
	}
	if persisted.InterruptionDetailJson != "{}" {
		t.Fatalf("completed interruption detail = %q, want legacy empty object", persisted.InterruptionDetailJson)
	}
}

func TestCompleteInterruptedRunFromIdleSessionHasOneCompletionAuthority(t *testing.T) {
	f := newRunLifecycleFixture(t)
	claimed := f.claim(t)
	sessionID := f.attachSession(t, claimed)
	if err := f.store.InterruptRunGeneration(
		f.ctx,
		f.started.RunID,
		claimed.Generation,
		"manual",
		"{}",
	); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}

	request := CompleteRunRequest{
		RunID:              f.started.RunID,
		ExpectedGeneration: claimed.Generation,
		RequireGeneration:  true,
		Admission:          idleSessionCompletionAdmission(t, sessionID),
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			_, err := f.store.CompleteRun(context.Background(), request)
			results <- err
		}()
	}
	ready.Wait()
	close(start)

	successes := 0
	rejections := 0
	for range 2 {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, sql.ErrNoRows), errors.Is(err, ErrRunAlreadyCompleted):
			rejections++
		default:
			t.Fatalf("concurrent idle completion error = %T %v, want stale completion rejection", err, err)
		}
	}
	if successes != 1 || rejections != 1 {
		t.Fatalf("concurrent idle completions successes=%d rejections=%d, want 1/1", successes, rejections)
	}
	transitions, err := f.store.ListTransitions(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	completionTransitions := 0
	for _, transition := range transitions {
		if transition.TransitionID == "done" {
			completionTransitions++
		}
	}
	if completionTransitions != 1 {
		t.Fatalf("transitions after concurrent idle completion = %+v, want one done transition", transitions)
	}
}

func TestCompleteInterruptedRunFromIdleSessionRejectsDifferentSession(t *testing.T) {
	f := newRunLifecycleFixture(t)
	claimed := f.claim(t)
	f.attachSession(t, claimed)
	if err := f.store.InterruptRunGeneration(
		f.ctx,
		f.started.RunID,
		claimed.Generation,
		"manual",
		"{}",
	); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}

	if _, err := f.store.CompleteRun(f.ctx, CompleteRunRequest{
		RunID:              f.started.RunID,
		ExpectedGeneration: claimed.Generation,
		RequireGeneration:  true,
		Admission:          idleSessionCompletionAdmission(t, "session-other"),
	}); err == nil {
		t.Fatal("CompleteRun accepted a different retained session")
	}
	runs := f.runs(t)
	if len(runs) != 1 || runs[0].CompletedAt != nil || runs[0].InterruptedAt == nil {
		t.Fatalf("run after different-session completion = %+v, want interrupted source unchanged", runs)
	}
}

func TestCompleteInterruptedScriptRunRejectsIdleSessionAdmission(t *testing.T) {
	f := newScriptExecutionFixture(t, "scripts/run", []byte("#!/bin/sh\nexit 0\n"))
	started := startTask(t, f.ctx, f.store, f.task.ID)
	claimed := claimRunFixture(t, f.ctx, f.store, started.RunID, 0)
	sessionID := "session-invalid-script-binding"
	if err := f.store.InterruptRunGeneration(
		f.ctx,
		started.RunID,
		claimed.Generation,
		"manual",
		"{}",
	); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}

	if _, err := f.store.CompleteRun(f.ctx, CompleteRunRequest{
		RunID:              started.RunID,
		ExpectedGeneration: claimed.Generation,
		RequireGeneration:  true,
		Admission:          idleSessionCompletionAdmission(t, sessionID),
	}); err == nil {
		t.Fatal("CompleteRun accepted idle session admission for a Script Node")
	}
	runs, err := f.store.ListRuns(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].CompletedAt != nil || runs[0].InterruptedAt == nil {
		t.Fatalf("script run after idle session completion = %+v, want interrupted source unchanged", runs)
	}
}

func TestResumeTaskRunRejectsRoleDrift(t *testing.T) {
	f := newRunLifecycleFixture(t)
	f.claim(t)
	if err := f.store.InterruptRun(f.ctx, f.started.RunID, "manual", "{}"); err != nil {
		t.Fatalf("InterruptRun: %v", err)
	}
	f.store.roleResolver = testsetup.QuestionsEnabled()

	var roleErr WorkflowValidationError
	if _, err := f.store.ResumeTaskRuns(f.ctx, f.task.ID); !errors.As(err, &roleErr) || !roleErr.HasCode(workflow.CodeAgentRoleMissing) {
		t.Fatalf("ResumeTaskRuns role drift error = %v, want %s", err, workflow.CodeAgentRoleMissing)
	}
}

func TestResumeTaskRunAllowsDefaultAgentRoleWithoutResolver(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agent := nodeByKey(t, def, "agent")
	if _, err := store.UpdateNode(ctx, NodeRecord{ID: workflow.NodeIDOf(agent), WorkflowID: workflowID, Key: workflow.NodeKey(agent), Kind: agent.Kind(), DisplayName: workflow.NodeDisplayName(agent), SubagentRole: workflow.DefaultAgentRole, PromptTemplate: workflow.NodePromptTemplate(agent), OutputFields: workflow.NodeOutputFields(agent)}); err != nil {
		t.Fatalf("UpdateNode default role: %v", err)
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	claimRunFixture(t, ctx, store, started.RunID, 0)
	if err := store.InterruptRun(ctx, started.RunID, "manual", "{}"); err != nil {
		t.Fatalf("InterruptRun: %v", err)
	}
	store.roleResolver = testsetup.QuestionsEnabled()

	resumedRuns, err := store.ResumeTaskRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ResumeTaskRuns default role: %v", err)
	}
	if len(resumedRuns.Runs) != 1 {
		t.Fatalf("resumed runs = %+v, want one", resumedRuns)
	}
	resumed := resumedRuns.Runs[0]
	if resumed.ID != started.RunID || resumed.InterruptedAt != nil || resumed.StartedAt != nil {
		t.Fatalf("resumed run = %+v, want default-role run requeued", resumed)
	}
}

func TestResumeTaskRunCanResumeInterruptedWaitingAskRun(t *testing.T) {
	f := newRunLifecycleFixture(t)
	claimed := f.claim(t)
	f.attachSession(t, claimed)
	if err := f.store.SetRunWaitingAsk(f.ctx, f.started.RunID, claimed.Generation, "ask-missing"); err != nil {
		t.Fatalf("SetRunWaitingAsk: %v", err)
	}
	if err := f.store.InterruptRun(f.ctx, f.started.RunID, "workflow_pending_ask_unavailable", "{}"); err != nil {
		t.Fatalf("InterruptRun: %v", err)
	}

	resumedRuns := f.resume(t)
	if len(resumedRuns) != 1 {
		t.Fatalf("resumed runs = %+v, want one", resumedRuns)
	}
	resumed := resumedRuns[0]
	if resumed.ID != f.started.RunID || resumed.WaitingAskID != nil || resumed.InterruptedAt != nil || resumed.StartedAt != nil {
		t.Fatalf("resumed waiting ask run = %+v, want requeued same run without waiting ask", resumed)
	}
}

func TestInterruptTargetsBySessionAndResumeRequeuesAllRuns(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task, branchRuns := startFanoutTask(t, ctx, store, binding.ProjectID, workflowID)
	runIDs := make([]workflow.RunID, 0, len(branchRuns))
	for _, runID := range branchRuns {
		runIDs = append(runIDs, runID)
	}
	if len(runIDs) != 2 {
		t.Fatalf("branch runs = %+v, want two", branchRuns)
	}
	sessions := make(map[workflow.RunID]string, len(runIDs))
	for _, runID := range runIDs {
		claimed := claimRunFixture(t, ctx, store, runID, 0)
		sessions[runID] = createAndAttachRunSessionFixture(t, ctx, store, binding, cfg, runID, claimed.Generation)
	}
	interrupted, err := store.InterruptTaskRuns(ctx, task.ID, sessions[runIDs[0]], "manual")
	if err != nil {
		t.Fatalf("InterruptTaskRuns by session: %v", err)
	}
	if len(interrupted) != 1 || interrupted[0].ID != runIDs[0] || interrupted[0].InterruptedAt == nil {
		t.Fatalf("interrupted = %+v, want only %s", interrupted, runIDs[0])
	}
	interruptedRest, err := store.InterruptTaskRuns(ctx, task.ID, "", "manual")
	if err != nil {
		t.Fatalf("InterruptTaskRuns all: %v", err)
	}
	if len(interruptedRest) != 1 || interruptedRest[0].ID != runIDs[1] {
		t.Fatalf("interrupted rest = %+v, want only %s", interruptedRest, runIDs[1])
	}
	resumed, err := store.ResumeTaskRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ResumeTaskRuns: %v", err)
	}
	if len(resumed.Runs) != 2 {
		t.Fatalf("resumed = %+v, want both runs", resumed)
	}
	for _, run := range resumed.Runs {
		if run.InterruptedAt != nil || run.StartedAt != nil {
			t.Fatalf("resumed run = %+v, want reset", run)
		}
	}
}
