package workflowview

import (
	"testing"

	"core/server/workflow"
	"core/shared/invariant"
	"core/shared/serverapi"
)

func TestTaskLifecycleIntegrityPanicsWithRootCauseDiagnosticInDebugMode(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", "panic")
	runID := "run-debug-integrity"
	sessionID := "session-debug-integrity"
	generation := int64(7)
	fact := taskExecutablePlacementFact{
		taskID:      "task-debug-integrity",
		placementID: "placement-debug-integrity",
		nodeID:      "node-debug-integrity",
		nodeKind:    workflow.NodeKindAgent,
		runID:       &runID,
		sessionID:   &sessionID,
		generation:  &generation,
		started:     true,
	}

	defer func() {
		recovered := recover()
		diagnostic, ok := recovered.(invariant.Diagnostic)
		if !ok {
			t.Fatalf("debug invariant panic = %T %v, want invariant.Diagnostic", recovered, recovered)
		}
		if diagnostic.Scope != invariant.ScopeWorkflowProjection ||
			diagnostic.Fields[invariant.FieldTaskID] != fact.taskID ||
			diagnostic.Fields[invariant.FieldNodeID] != fact.nodeID ||
			diagnostic.Fields[invariant.FieldNodeKind] != string(fact.nodeKind) ||
			diagnostic.Fields[invariant.FieldRunID] != runID ||
			diagnostic.Fields[invariant.FieldSessionID] != sessionID ||
			diagnostic.Fields[invariant.FieldGeneration] != "7" ||
			diagnostic.Fields[invariant.FieldDurableLifecycle] == "" ||
			diagnostic.Fields[invariant.FieldExactExecution] == "" ||
			diagnostic.Fields[invariant.FieldActionProjection] == "" ||
			diagnostic.Stack == "" {
			t.Fatalf("debug invariant diagnostic = %+v", diagnostic)
		}
	}()

	_ = (&TaskLifecycleProjection{}).fail(
		serverapi.WorkflowTaskStatusKindRunning,
		serverapi.WorkflowTaskIntegrityReasonExactExecutionMissing,
		fact,
		taskRunActionFacts{},
	)
}
