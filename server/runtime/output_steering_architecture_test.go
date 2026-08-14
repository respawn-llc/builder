package runtime

import (
	"testing"

	"core/server/session"
)

func TestResultGroupFlushConsumesWorkflowPostCompletionBoundaryOnlyAfterCommit(t *testing.T) {
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, newTestToolRegistry(t), Config{Model: "gpt-5"})
	restoreStep := setTestActiveStep(engine, "step")
	defer restoreStep()
	prepareSimpleResultGroupCall(t, engine, "step", "first")
	prepareSimpleResultGroupCall(t, engine, "step", "second")
	mode := session.CompactionModeWorkflowPostCompletion
	if err := engine.compactionRuntimeState().SetHistoryReplacementMode(&mode); err != nil {
		t.Fatalf("set workflow post-completion boundary: %v", err)
	}
	collector := testResultGroupCollector(t, "first", "second")
	var secondOutcome *resultGroupReportOutcome
	if err := engine.steer(
		"step",
		steerResultGroupReportIntent(collector, "second", testResultGroupUnit("second"), &secondOutcome),
		steerResultGroupFlushIntent(collector, ResultGroupFlushQuestion),
	); err != nil {
		t.Fatalf("flush blocked later result: %v", err)
	}
	if !engine.compactionRuntimeState().WorkflowPostCompletionBoundary() {
		t.Fatal("non-committing Result Group flush consumed the boundary")
	}
	var firstOutcome *resultGroupReportOutcome
	if err := engine.steer(
		"step",
		steerResultGroupReportIntent(collector, "first", testResultGroupUnit("first"), &firstOutcome),
		steerResultGroupFlushIntent(collector, ResultGroupFlushQuestion),
	); err != nil {
		t.Fatalf("flush committed Result Group: %v", err)
	}
	if engine.compactionRuntimeState().WorkflowPostCompletionBoundary() {
		t.Fatal("committed Result Group flush preserved the boundary")
	}
}

func TestMissingToolOutputRepairConsumesWorkflowPostCompletionBoundaryOnlyAfterRepair(t *testing.T) {
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, newTestToolRegistry(t), Config{Model: "gpt-5"})
	restoreStep := setTestActiveStep(engine, "step")
	defer restoreStep()
	mode := session.CompactionModeWorkflowPostCompletion
	if err := engine.compactionRuntimeState().SetHistoryReplacementMode(&mode); err != nil {
		t.Fatalf("set workflow post-completion boundary: %v", err)
	}
	stepID := "step"
	if repaired, err := engine.repairMissingToolOutputsByAppending(&stepID, missingToolOutputRepairFreshResource); err != nil || repaired != 0 {
		t.Fatalf("no-op missing-output repair = count:%d error:%v", repaired, err)
	}
	if !engine.compactionRuntimeState().WorkflowPostCompletionBoundary() {
		t.Fatal("no-op missing-output repair consumed the boundary")
	}
	prepareSimpleResultGroupCall(t, engine, stepID, "missing")
	if err := engine.compactionRuntimeState().SetHistoryReplacementMode(&mode); err != nil {
		t.Fatalf("reset workflow post-completion boundary: %v", err)
	}
	if repaired, err := engine.repairMissingToolOutputsByAppending(&stepID, missingToolOutputRepairFreshResource); err != nil || repaired != 1 {
		t.Fatalf("committed missing-output repair = count:%d error:%v", repaired, err)
	}
	if engine.compactionRuntimeState().WorkflowPostCompletionBoundary() {
		t.Fatal("committed missing-output repair preserved the boundary")
	}
}
