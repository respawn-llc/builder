package runtime

import (
	"context"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
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

	t.Run("inactive goal transition sequence", func(t *testing.T) {
		t.Run("absent", func(t *testing.T) {
			assertInactiveGoalCompaction(t, engine, "absent")
		})
		if _, err := engine.SetGoal("goal", session.GoalActorUser); err != nil {
			t.Fatalf("set goal: %v", err)
		}
		t.Run("paused", func(t *testing.T) {
			if _, err := engine.SetGoalStatus(session.GoalStatusPaused, session.GoalActorUser); err != nil {
				t.Fatalf("pause goal: %v", err)
			}
			assertInactiveGoalCompaction(t, engine, "paused")
		})
		t.Run("complete", func(t *testing.T) {
			if _, err := engine.SetGoalStatus(session.GoalStatusComplete, session.GoalActorUser); err != nil {
				t.Fatalf("complete goal: %v", err)
			}
			assertInactiveGoalCompaction(t, engine, "complete")
		})
		t.Run("cleared", func(t *testing.T) {
			if _, err := engine.ClearGoal(session.GoalActorUser); err != nil {
				t.Fatalf("clear goal: %v", err)
			}
			assertInactiveGoalCompaction(t, engine, "cleared")
		})
	})

	t.Run("workflow", func(t *testing.T) {
		runID := workflow.RunID("workflow-run")
		workflowClient := &fakeCompactionClient{compactionResponses: []llm.CompactionResponse{
			remoteCompactionReplacement(1_000, 100, 200_000),
		}}
		workflowEngine := mustNewWorkflowTestEngine(
			t,
			mustCreateTestSession(t),
			workflowClient,
			&workflowruntime.Config{
				RunID:          runID,
				Contract:       workflowruntime.CompletionContract{RunID: runID},
				CompletionMode: workflowruntime.CompletionModeTool,
				Controller:     &externallyCompletedWorkflowController{},
			},
			Config{Model: "gpt-5"},
		)
		if _, err := workflowEngine.SetGoal("goal", session.GoalActorUser); err != nil {
			t.Fatalf("set workflow goal: %v", err)
		}
		assertInactiveGoalCompaction(t, workflowEngine, "workflow")
	})
}

func assertInactiveGoalCompaction(t *testing.T, engine *Engine, name string) {
	t.Helper()
	if err := engine.steer("input", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input " + name)}},
	)); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}
	_, receipt, err := engine.compactNow(
		context.Background(),
		"compact",
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
