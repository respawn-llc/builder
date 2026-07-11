package workflowstore

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"core/server/workflow"
)

func TestRecordProtocolViolationInterruptsAtCap(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	first, err := store.RecordProtocolViolation(ctx, RecordProtocolViolationRequest{RunID: started.RunID, Kind: ProtocolViolationInvalidCompletion, MaxCount: 2, Detail: `{"detail":"first"}`})
	if err != nil {
		t.Fatalf("RecordProtocolViolation first: %v", err)
	}
	if first.Count != 1 || first.Interrupted {
		t.Fatalf("first violation = %+v, want count 1 active", first)
	}
	second, err := store.RecordProtocolViolation(ctx, RecordProtocolViolationRequest{RunID: started.RunID, Kind: ProtocolViolationInvalidCompletion, MaxCount: 2, Detail: `{"detail":"second"}`})
	if err != nil {
		t.Fatalf("RecordProtocolViolation second: %v", err)
	}
	if second.Count != 2 || !second.Interrupted {
		t.Fatalf("second violation = %+v, want count 2 interrupted", second)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].InvalidCompletions != 2 || runs[0].InterruptedAt == 0 || runs[0].InterruptionReason != "workflow_protocol_violation_limit" {
		t.Fatalf("run after cap = %+v", runs)
	}
}

func TestSetRunEffectiveCompletionModePersistsAndRefusesDrift(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	claimed, err := store.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if claimed.EffectiveCompletionMode != "" {
		t.Fatalf("claimed effective mode = %q, want empty before resolution", claimed.EffectiveCompletionMode)
	}
	if err := store.SetRunEffectiveCompletionMode(ctx, started.RunID, claimed.Generation, "invalid"); !errors.Is(err, ErrInvalidEffectiveCompletionMode) {
		t.Fatalf("SetRunEffectiveCompletionMode invalid error = %v, want ErrInvalidEffectiveCompletionMode", err)
	}
	if err := store.SetRunEffectiveCompletionMode(ctx, started.RunID, claimed.Generation, "shell_command"); err != nil {
		t.Fatalf("SetRunEffectiveCompletionMode: %v", err)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].EffectiveCompletionMode != "shell_command" {
		t.Fatalf("run effective mode = %+v, want shell_command", runs)
	}
	if err := store.SetRunEffectiveCompletionMode(ctx, started.RunID, claimed.Generation, "shell_command"); err != nil {
		t.Fatalf("SetRunEffectiveCompletionMode same mode: %v", err)
	}
	if err := store.SetRunEffectiveCompletionMode(ctx, started.RunID, claimed.Generation, "tool"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("SetRunEffectiveCompletionMode drift error = %v, want sql.ErrNoRows", err)
	}
}

func TestClaimRunRejectsDeletingProject(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	if _, err := store.db.ExecContext(ctx, "UPDATE projects SET lifecycle_state = 'deleting' WHERE id = ?", binding.ProjectID); err != nil {
		t.Fatalf("mark project deleting: %v", err)
	}

	if _, err := store.ClaimRun(ctx, started.RunID, 0); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("ClaimRun deleting project error = %v, want %v", err, sql.ErrNoRows)
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
	claimed, err := store.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	input, err := store.GetRunStartContext(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("GetRunStartContext: %v", err)
	}
	if input.Node.CompletionMode != "tool" {
		t.Fatalf("run start node completion mode = %q, want tool", input.Node.CompletionMode)
	}
}

func TestCompleteRunRejectsStaleGeneration(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	claimed, err := store.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if _, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "done", ExpectedGeneration: 0, RequireGeneration: true}); !errors.Is(err, ErrStaleRunGeneration) {
		t.Fatalf("expected stale generation error, got %v", err)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done", ExpectedGeneration: claimed.Generation, RequireGeneration: true})
}

func TestRunStartContextHandlesMissingInputEdgeAndRejectsNonArrayInputJSON(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	// Intentional corruption fixture: delete the transition-edge snapshot to
	// verify run context handles legacy/corrupt missing-edge rows safely.
	if _, err := store.db.ExecContext(ctx, `DELETE FROM task_transition_edges WHERE target_placement_id = ?`, string(started.PlacementID)); err != nil {
		t.Fatalf("delete transition edge snapshot: %v", err)
	}
	input, err := store.GetRunStartContext(ctx, started.RunID)
	if err != nil {
		t.Fatalf("GetRunStartContext without input edge: %v", err)
	}
	if len(input.InputValues) != 0 {
		t.Fatalf("input values without input edge = %+v, want empty", input.InputValues)
	}
	taskWithInvalidInputs, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Task 2", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask malformed inputs: %v", err)
	}
	startedInvalidInputs, err := store.StartTask(ctx, taskWithInvalidInputs.ID)
	if err != nil {
		t.Fatalf("StartTask malformed inputs: %v", err)
	}
	// Intentional corruption fixture: force non-array input JSON that product
	// APIs cannot create, then assert the store rejects it at read boundary.
	if _, err := store.db.ExecContext(ctx, `UPDATE task_transition_edges SET input_bindings_json = '{}' WHERE target_placement_id = ?`, string(startedInvalidInputs.PlacementID)); err == nil {
		t.Fatalf("expected non-array transition edge input bindings to be rejected")
	}
	taskWithJoinInputs, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Task 3", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask join inputs: %v", err)
	}
	startedJoinInputs, err := store.StartTask(ctx, taskWithJoinInputs.ID)
	if err != nil {
		t.Fatalf("StartTask join inputs: %v", err)
	}
	joinInputsJSON, err := workflow.MarshalString([]workflow.InputBinding{{Name: "joined", Source: workflow.BindingSourceJoin, Field: "aggregate"}})
	if err != nil {
		t.Fatalf("marshal join inputs: %v", err)
	}
	// Intentional snapshot fixture: inject a join binding into the stored edge
	// snapshot to verify join-sourced inputs resolve through run context.
	if _, err := store.db.ExecContext(ctx, `UPDATE task_transition_edges SET input_bindings_json = ? WHERE target_placement_id = ?`, joinInputsJSON, string(startedJoinInputs.PlacementID)); err != nil {
		t.Fatalf("set join transition edge inputs: %v", err)
	}
	joinInput, err := store.GetRunStartContext(ctx, startedJoinInputs.RunID)
	if err != nil {
		t.Fatalf("GetRunStartContext join inputs: %v", err)
	}
	if joinInput.InputValues["joined"] != "" {
		t.Fatalf("join input without aggregate = %+v, want empty value", joinInput.InputValues)
	}
}

func TestAttachRunSessionGenerationGuard(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	sessionID := createTestSession(t, ctx, store, binding, cfg)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	claimed, err := store.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := store.AttachRunSession(ctx, started.RunID, claimed.Generation-1, "session-stale"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale AttachRunSession error = %v, want sql.ErrNoRows", err)
	}
	if err := store.AttachRunSession(ctx, started.RunID, claimed.Generation, sessionID); err != nil {
		t.Fatalf("AttachRunSession current generation: %v", err)
	}
	if err := store.AttachRunSession(ctx, started.RunID, claimed.Generation, "session-second"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second AttachRunSession error = %v, want sql.ErrNoRows", err)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if runs[0].SessionID != sessionID {
		t.Fatalf("attached session = %q, want %q", runs[0].SessionID, sessionID)
	}
}

func TestSetAndClearRunWaitingAskGenerationGuard(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	sessionID := createTestSession(t, ctx, store, binding, cfg)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	claimed, err := store.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := store.AttachRunSession(ctx, started.RunID, claimed.Generation, sessionID); err != nil {
		t.Fatalf("AttachRunSession: %v", err)
	}

	if err := store.SetRunWaitingAsk(ctx, started.RunID, claimed.Generation-1, "ask-stale"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale SetRunWaitingAsk error = %v, want sql.ErrNoRows", err)
	}
	if err := store.SetRunWaitingAsk(ctx, started.RunID, claimed.Generation, "ask-1"); err != nil {
		t.Fatalf("SetRunWaitingAsk current generation: %v", err)
	}
	waiting, err := store.ListWaitingAskRuns(ctx)
	if err != nil {
		t.Fatalf("ListWaitingAskRuns: %v", err)
	}
	if len(waiting) != 1 || waiting[0].ID != started.RunID || waiting[0].WaitingAskID != "ask-1" || waiting[0].SessionID != sessionID {
		t.Fatalf("waiting runs = %+v", waiting)
	}
	if _, err := store.ClaimRun(ctx, started.RunID, claimed.Generation); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("ClaimRun while waiting error = %v, want sql.ErrNoRows", err)
	}
	if err := store.ClearRunWaitingAsk(ctx, started.RunID, claimed.Generation-1, "ask-1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale ClearRunWaitingAsk error = %v, want sql.ErrNoRows", err)
	}
	if err := store.ClearRunWaitingAsk(ctx, started.RunID, claimed.Generation, "ask-other"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("wrong ask ClearRunWaitingAsk error = %v, want sql.ErrNoRows", err)
	}
	if err := store.ClearRunWaitingAsk(ctx, started.RunID, claimed.Generation, "ask-1"); err != nil {
		t.Fatalf("ClearRunWaitingAsk current ask: %v", err)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if runs[0].WaitingAskID != "" || runs[0].CompletedAt != 0 || runs[0].InterruptedAt != 0 {
		t.Fatalf("run after clear = %+v", runs[0])
	}
}

func TestSetAndClearRunWaitingAskPublishTaskEvents(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	store.now = func() time.Time { return time.UnixMilli(1234).UTC() }
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	sessionID := createTestSession(t, ctx, store, binding, cfg)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	claimed, err := store.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := store.AttachRunSession(ctx, started.RunID, claimed.Generation, sessionID); err != nil {
		t.Fatalf("AttachRunSession: %v", err)
	}
	sink := &recordingWorkflowEventPublisher{}
	store.SetWorkflowEventPublisher(sink)

	if err := store.SetRunWaitingAsk(ctx, started.RunID, claimed.Generation, "ask-1"); err != nil {
		t.Fatalf("SetRunWaitingAsk: %v", err)
	}
	if err := store.ClearRunWaitingAsk(ctx, started.RunID, claimed.Generation, "ask-1"); err != nil {
		t.Fatalf("ClearRunWaitingAsk: %v", err)
	}

	if len(sink.records) != 2 {
		t.Fatalf("published records = %+v, want waiting and cleared events", sink.records)
	}
	for _, record := range sink.records {
		if record.ProjectID != binding.ProjectID || record.WorkflowID != string(task.WorkflowID) || record.Resource != "task" {
			t.Fatalf("published record identity = %+v", record)
		}
		if record.OccurredAtUnixMs != 1234 {
			t.Fatalf("published record time = %d, want 1234", record.OccurredAtUnixMs)
		}
		if len(record.ChangedIDs) != 3 || record.ChangedIDs[0] != string(task.ID) || record.ChangedIDs[1] != string(started.RunID) || record.ChangedIDs[2] != "ask-1" {
			t.Fatalf("published record changed ids = %+v", record.ChangedIDs)
		}
	}
	if sink.records[0].Action != "question_waiting" || sink.records[1].Action != "question_cleared" {
		t.Fatalf("published actions = %+v, want question_waiting then question_cleared", []string{sink.records[0].Action, sink.records[1].Action})
	}
}

func TestInterruptRunGenerationGuard(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	claimed, err := store.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := store.InterruptRunGeneration(ctx, started.RunID, claimed.Generation-1, "stale", "{}"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale InterruptRunGeneration error = %v, want sql.ErrNoRows", err)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns stale: %v", err)
	}
	if runs[0].InterruptedAt != 0 {
		t.Fatalf("stale generation interrupted run: %+v", runs[0])
	}
	if err := store.InterruptRunGeneration(ctx, started.RunID, claimed.Generation, "current", "{}"); err != nil {
		t.Fatalf("InterruptRunGeneration current generation: %v", err)
	}
	if err := store.InterruptRunGeneration(ctx, started.RunID, claimed.Generation, "second", "{}"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second InterruptRunGeneration error = %v, want sql.ErrNoRows", err)
	}
	runs, err = store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns current: %v", err)
	}
	if runs[0].InterruptedAt == 0 || runs[0].InterruptionReason != "current" {
		t.Fatalf("run after interrupt = %+v, want current interruption", runs[0])
	}
}

func TestResumeTaskRunRequeuesInterruptedRunWithSameSession(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	claimed, err := store.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	sessionID := createTestSession(t, ctx, store, binding, cfg)
	if err := store.AttachRunSession(ctx, started.RunID, claimed.Generation, sessionID); err != nil {
		t.Fatalf("AttachRunSession: %v", err)
	}
	if err := store.InterruptRunGeneration(ctx, started.RunID, claimed.Generation, "manual", `{"reason":"test"}`); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}

	resumedRuns, err := store.ResumeTaskRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ResumeTaskRuns: %v", err)
	}
	if len(resumedRuns) != 1 {
		t.Fatalf("resumed runs = %+v, want one", resumedRuns)
	}
	resumed := resumedRuns[0]
	if resumed.ID != started.RunID || resumed.SessionID != sessionID || resumed.StartedAt != 0 || resumed.InterruptedAt != 0 || resumed.Generation <= claimed.Generation {
		t.Fatalf("resumed run = %+v, want same run/session requeued with newer generation", resumed)
	}
	runnable, err := store.ListRunnableRuns(ctx, 10)
	if err != nil {
		t.Fatalf("ListRunnableRuns: %v", err)
	}
	if len(runnable) != 1 || runnable[0].ID != started.RunID || runnable[0].SessionID != sessionID {
		t.Fatalf("runnable after resume = %+v, want same run/session", runnable)
	}
	reclaimed, err := store.ClaimRun(ctx, started.RunID, resumed.Generation)
	if err != nil {
		t.Fatalf("ClaimRun after resume: %v", err)
	}
	if err := store.AttachRunSession(ctx, started.RunID, reclaimed.Generation, sessionID); err != nil {
		t.Fatalf("AttachRunSession same session after resume: %v", err)
	}
}

func TestResumeTaskRunRejectsRoleDrift(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	if _, err := store.ClaimRun(ctx, started.RunID, 0); err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := store.InterruptRun(ctx, started.RunID, "manual", "{}"); err != nil {
		t.Fatalf("InterruptRun: %v", err)
	}
	store.roleResolver = workflow.StaticRoleResolver{}

	var roleErr WorkflowValidationError
	if _, err := store.ResumeTaskRuns(ctx, task.ID); !errors.As(err, &roleErr) || !roleErr.HasCode(workflow.CodeAgentRoleMissing) {
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
	if _, err := store.ClaimRun(ctx, started.RunID, 0); err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := store.InterruptRun(ctx, started.RunID, "manual", "{}"); err != nil {
		t.Fatalf("InterruptRun: %v", err)
	}
	store.roleResolver = workflow.StaticRoleResolver{}

	resumedRuns, err := store.ResumeTaskRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ResumeTaskRuns default role: %v", err)
	}
	if len(resumedRuns) != 1 {
		t.Fatalf("resumed runs = %+v, want one", resumedRuns)
	}
	resumed := resumedRuns[0]
	if resumed.ID != started.RunID || resumed.InterruptedAt != 0 || resumed.StartedAt != 0 {
		t.Fatalf("resumed run = %+v, want default-role run requeued", resumed)
	}
}

func TestResumeTaskRunCanResumeInterruptedWaitingAskRun(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	claimed, err := store.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	sessionID := createTestSession(t, ctx, store, binding, cfg)
	if err := store.AttachRunSession(ctx, started.RunID, claimed.Generation, sessionID); err != nil {
		t.Fatalf("AttachRunSession: %v", err)
	}
	if err := store.SetRunWaitingAsk(ctx, started.RunID, claimed.Generation, "ask-missing"); err != nil {
		t.Fatalf("SetRunWaitingAsk: %v", err)
	}
	if err := store.InterruptRun(ctx, started.RunID, "workflow_pending_ask_unavailable", "{}"); err != nil {
		t.Fatalf("InterruptRun: %v", err)
	}

	resumedRuns, err := store.ResumeTaskRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ResumeTaskRuns: %v", err)
	}
	if len(resumedRuns) != 1 {
		t.Fatalf("resumed runs = %+v, want one", resumedRuns)
	}
	resumed := resumedRuns[0]
	if resumed.ID != started.RunID || resumed.WaitingAskID != "" || resumed.InterruptedAt != 0 || resumed.StartedAt != 0 {
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
		claimed, err := store.ClaimRun(ctx, runID, 0)
		if err != nil {
			t.Fatalf("ClaimRun %s: %v", runID, err)
		}
		sessionID := createTestSession(t, ctx, store, binding, cfg)
		if err := store.AttachRunSession(ctx, runID, claimed.Generation, sessionID); err != nil {
			t.Fatalf("AttachRunSession %s: %v", runID, err)
		}
		sessions[runID] = sessionID
	}
	interrupted, err := store.InterruptTaskRuns(ctx, task.ID, sessions[runIDs[0]], "manual")
	if err != nil {
		t.Fatalf("InterruptTaskRuns by session: %v", err)
	}
	if len(interrupted) != 1 || interrupted[0].ID != runIDs[0] || interrupted[0].InterruptedAt == 0 {
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
	if len(resumed) != 2 {
		t.Fatalf("resumed = %+v, want both runs", resumed)
	}
	for _, run := range resumed {
		if run.InterruptedAt != 0 || run.StartedAt != 0 {
			t.Fatalf("resumed run = %+v, want reset", run)
		}
	}
}
