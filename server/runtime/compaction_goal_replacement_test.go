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
	paused := session.GoalStatusPaused
	complete := session.GoalStatusComplete
	tests := []struct {
		name     string
		status   *session.GoalStatus
		clear    bool
		workflow bool
	}{
		{name: "absent"},
		{name: "paused", status: &paused},
		{name: "complete", status: &complete},
		{name: "cleared", clear: true},
		{name: "workflow", workflow: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			client := &fakeCompactionClient{compactionResponses: []llm.CompactionResponse{
				remoteCompactionReplacement(1_000, 100, 200_000),
			}}
			var engine *Engine
			if test.workflow {
				engine = mustNewWorkflowTestEngine(
					t,
					store,
					client,
					&workflowruntime.Config{
						ScopeID:        runtimeids.NewExecutionScopeID(),
						CompletionMode: workflowruntime.CompletionModeTool,
						Controller:     &externallyCompletedWorkflowController{},
					},
					Config{Model: "gpt-5"},
				)
			} else {
				engine = mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
			}
			if test.status != nil || test.clear || test.workflow {
				if _, err := engine.SetGoal("goal", session.GoalActorUser); err != nil {
					t.Fatalf("set goal: %v", err)
				}
			}
			if test.status != nil {
				if _, err := engine.SetGoalStatus(*test.status, session.GoalActorUser); err != nil {
					t.Fatalf("set goal status: %v", err)
				}
			}
			if test.clear {
				if _, err := engine.ClearGoal(session.GoalActorUser); err != nil {
					t.Fatalf("clear goal: %v", err)
				}
			}
			if err := engine.steer("input", steerMessagesWithPersistenceIntent(
				steeringPriorityNormal,
				steeringMessageEventNone,
				true,
				[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
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
		})
	}
}
