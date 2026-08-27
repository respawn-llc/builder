package runtime

import (
	"context"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/server/workflowruntime"
	"core/shared/runtimeids"
	"core/shared/textutil"
)

func TestCompactionOmitsActiveGoalContinuationWhenGoalIsNotActive(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{compactionResponses: []llm.CompactionResponse{
		remoteCompactionReplacement(1_000, 100, 200_000),
		remoteCompactionReplacement(1_000, 100, 200_000),
		remoteCompactionReplacement(1_000, 100, 200_000),
		remoteCompactionReplacement(1_000, 100, 200_000),
	}}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	inputStepID := runtimeTestStepID("inactive-goal-compaction-input")
	engine.stepLifecycle = &stubExclusiveStepLifecycle{
		activeStepID: inputStepID,
		snapshot:     &RunSnapshot{RunID: "11111111-1111-4111-8111-111111111111", StepID: inputStepID},
	}

	t.Run("inactive goal transition sequence", func(t *testing.T) {
		t.Run("absent", func(t *testing.T) {
			assertInactiveGoalCompaction(t, engine, "absent")
		})
		if _, err := engine.SetGoal(t.Context(), "goal", session.GoalActorUser); err != nil {
			t.Fatalf("set goal: %v", err)
		}
		t.Run("paused", func(t *testing.T) {
			if _, err := engine.SetGoalStatus(t.Context(), session.GoalStatusPaused, session.GoalActorUser); err != nil {
				t.Fatalf("pause goal: %v", err)
			}
			assertInactiveGoalCompaction(t, engine, "paused")
		})
		t.Run("complete", func(t *testing.T) {
			if _, err := engine.SetGoalStatus(t.Context(), session.GoalStatusComplete, session.GoalActorUser); err != nil {
				t.Fatalf("complete goal: %v", err)
			}
			assertInactiveGoalCompaction(t, engine, "complete")
		})
		t.Run("cleared", func(t *testing.T) {
			if _, err := engine.ClearGoal(t.Context(), session.GoalActorUser); err != nil {
				t.Fatalf("clear goal: %v", err)
			}
			assertInactiveGoalCompaction(t, engine, "cleared")
		})
	})

	t.Run("workflow", func(t *testing.T) {
		workflowClient := &fakeCompactionClient{compactionResponses: []llm.CompactionResponse{
			remoteCompactionReplacement(1_000, 100, 200_000),
		}}
		workflowEngine := mustNewWorkflowTestEngine(
			t,
			mustCreateTestSession(t),
			workflowClient,
			&workflowruntime.CurrentNodeExecutionConfig{
				ScopeID:        runtimeids.NewExecutionScopeID(),
				CompletionMode: workflowruntime.CompletionModeTool,
				Controller:     &externallyCompletedWorkflowController{},
			},
			Config{Model: "gpt-5"},
		)
		workflowInputStepID := runtimeTestStepID("inactive-workflow-goal-compaction-input")
		workflowEngine.stepLifecycle = &stubExclusiveStepLifecycle{
			activeStepID: workflowInputStepID,
			snapshot:     &RunSnapshot{RunID: "11111111-1111-4111-8111-111111111111", StepID: workflowInputStepID},
		}
		if _, err := workflowEngine.SetGoal(t.Context(), "goal", session.GoalActorUser); err != nil {
			t.Fatalf("set workflow goal: %v", err)
		}
		assertInactiveGoalCompaction(t, workflowEngine, "workflow")
	})
}

func assertInactiveGoalCompaction(t *testing.T, engine *Engine, name string) {
	t.Helper()
	stepID := engine.stepLifecycle.Snapshot().StepID
	if err := engine.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input " + name)}})); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}
	_, receipt, err := engine.compactNow(
		context.Background(),
		stepID,
		compactionModeManual,
		compactionInstructionsInput{},
		false,
	)
	if err != nil || !receipt.Committed {
		t.Fatalf("compact inactive goal context: receipt=%+v error=%v", receipt, err)
	}
	for _, item := range engine.transcriptRuntimeState().SnapshotItems() {
		if item.Type == llm.ResponseItemTypeMessage &&
			item.MessageType != nil &&
			*item.MessageType == llm.MessageTypeActiveGoalContinuation {
			t.Fatalf("inactive-goal replacement retained continuation: %+v", item)
		}
	}
}
