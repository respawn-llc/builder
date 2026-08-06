package workflowexecution

import (
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"core/server/workflow"
	"core/shared/runtimeids"
)

func TestInterruptedFinalizerSupervisorRecordsDiagnosticsAndTerminatesProcess(t *testing.T) {
	const helperEnvironment = "KENT_TEST_NONRETURNING_FINALIZER"
	if os.Getenv(helperEnvironment) == "1" {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
		reference, err := workflow.NewCurrentNodeReference(
			"task-finalizer-timeout",
			"node-finalizer-timeout",
			nil,
		)
		if err != nil {
			panic(err)
		}
		superviseInterruptedFinalizer(
			func() { select {} },
			interruptedFinalizerDiagnostic{
				TaskID:         reference.TaskID,
				CurrentNode:    reference,
				ScopeID:        runtimeids.NewExecutionScopeID(),
				RunPhase:       currentNodeRunRunning,
				FinalizerPhase: workflowFinalizerPhaseResult,
				Canceled:       true,
			},
			10*time.Millisecond,
		)
		select {}
	}

	command := exec.Command(os.Args[0], "-test.run=^TestInterruptedFinalizerSupervisorRecordsDiagnosticsAndTerminatesProcess$")
	command.Env = append(os.Environ(), helperEnvironment+"=1")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("non-returning finalizer supervisor did not terminate the process")
	}
	var payload map[string]any
	for _, line := range strings.Split(string(output), "\n") {
		var candidate map[string]any
		if json.Unmarshal([]byte(line), &candidate) == nil &&
			candidate["event"] == workflowFinalizerTimeoutEvent {
			payload = candidate
			break
		}
	}
	if payload == nil {
		t.Fatalf("fatal finalizer output has no structured diagnostic payload: %s", output)
	}
	for _, field := range []string{
		"task_id",
		"current_node",
		"scope_id",
		"run_phase",
		"finalizer_phase",
		"elapsed",
		"canceled",
		"goroutine_stacks",
	} {
		if _, exists := payload[field]; !exists {
			t.Fatalf("fatal finalizer diagnostic has no %q field: %+v", field, payload)
		}
	}
}
