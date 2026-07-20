package session

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	"core/internal/testharness/filemode"
)

func TestSetGoalPersistsOnlyMetadata(t *testing.T) {
	store := newSessionTestStore(t)

	goal, err := store.SetGoal("  ship goal mode\nwith docs  ", GoalActorUser)
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if goal.ID == "" {
		t.Fatalf("goal id is empty")
	}
	if goal.Objective != "ship goal mode\nwith docs" {
		t.Fatalf("objective = %q", goal.Objective)
	}
	if goal.Status != GoalStatusActive {
		t.Fatalf("status = %q, want active", goal.Status)
	}

	reopened, err := openSessionTestStore(store)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	persisted := reopened.Metadata().Goal
	if persisted == nil {
		t.Fatalf("persisted goal is nil")
	}
	if *persisted != goal {
		t.Fatalf("persisted goal = %+v, want %+v", *persisted, goal)
	}

	events, err := collectEvents(reopened)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("goal metadata mutation emitted events: %+v", events)
	}
}

func TestGoalTranscriptMutationRequiresMaterializedCapability(t *testing.T) {
	store := newSessionTestStore(t)
	beforeRevision := storeTestMeta(store).LastSequence

	_, receipt, err := store.SetGoalWithMessage(
		MaterializedEventLog{},
		nil,
		"ship goal mode",
		GoalActorUser,
		goalTestMessageRecord(t, "goal feedback"),
	)
	if err == nil {
		t.Fatal("goal transcript mutation accepted a missing event-log capability")
	}
	if receipt.Committed {
		t.Fatalf("missing-capability receipt = %+v, want uncommitted", receipt)
	}
	if goal := store.Metadata().Goal; goal != nil {
		t.Fatalf("missing-capability goal mutation persisted metadata: %+v", goal)
	}
	if got := storeTestMeta(store).LastSequence; got != beforeRevision {
		t.Fatalf(
			"missing-capability goal mutation changed event revision: got %d want %d",
			got,
			beforeRevision,
		)
	}
}

func TestGoalTranscriptMutationsRollBackMetadataWhenRecordAppendFails(t *testing.T) {
	store := newSessionTestStore(t)
	eventLog := materializeGoalTestEventLog(t, store)

	blocker := filemode.MustBlockEventLogAppends(t, store.eventsFP)
	_, receipt, err := store.SetGoalWithMessage(
		eventLog,
		nil,
		"ship goal mode",
		GoalActorUser,
		goalTestMessageRecord(t, "goal feedback"),
	)
	if err == nil {
		t.Fatal("expected SetGoalWithMessage to fail when event append fails")
	}
	if receipt.Committed {
		t.Fatal("failed goal transcript append reported committed")
	}
	if goal := store.Metadata().Goal; goal != nil {
		t.Fatalf("goal after failed atomic set = %+v, want nil", goal)
	}
	if err := blocker.Restore(); err != nil {
		t.Fatalf("restore event log after failed goal set: %v", err)
	}
	events, err := collectEvents(store)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events after failed atomic set = %+v, want none", events)
	}

	goal, err := store.SetGoal("ship goal mode", GoalActorUser)
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	previous := *store.Metadata().Goal
	blocker = filemode.MustBlockEventLogAppends(t, store.eventsFP)
	if _, receipt, err := store.SetGoalStatusWithMessage(
		eventLog,
		nil,
		GoalStatusPaused,
		GoalActorUser,
		goalTestMessageRecord(t, "goal feedback"),
	); err == nil {
		t.Fatal("expected SetGoalStatusWithMessage to fail when event append fails")
	} else if receipt.Committed {
		t.Fatal("failed status transcript append reported committed")
	}
	if got := store.Metadata().Goal; got == nil || !reflect.DeepEqual(*got, previous) {
		t.Fatalf("goal after failed status update = %+v, want %+v", got, previous)
	}

	if err := blocker.Restore(); err != nil {
		t.Fatalf("restore event log after failed goal status: %v", err)
	}
	previous = goal
	filemode.MustBlockEventLogAppends(t, store.eventsFP)
	if _, receipt, err := store.ClearGoalWithMessage(
		eventLog,
		nil,
		GoalActorUser,
		goalTestMessageRecord(t, "goal feedback"),
	); err == nil {
		t.Fatal("expected ClearGoalWithMessage to fail when event append fails")
	} else if receipt.Committed {
		t.Fatal("failed clear transcript append reported committed")
	}
	if got := store.Metadata().Goal; got == nil || !reflect.DeepEqual(*got, previous) {
		t.Fatalf("goal after failed clear = %+v, want %+v", got, previous)
	}
}

func TestGoalTranscriptMutationsCommitMetadataAndMessageTogether(t *testing.T) {
	store := newSessionTestStore(t)
	eventLog := materializeGoalTestEventLog(t, store)

	goal, receipt, err := store.SetGoalWithMessage(
		eventLog,
		nil,
		"ship goal mode",
		GoalActorUser,
		goalTestMessageRecord(t, "goal set"),
	)
	if err != nil || !receipt.Committed {
		t.Fatalf("SetGoalWithMessage goal=%+v receipt=%+v err=%v", goal, receipt, err)
	}
	if persisted := store.Metadata().Goal; persisted == nil || persisted.ID != goal.ID {
		t.Fatalf("persisted goal after set = %+v, want %+v", persisted, goal)
	}

	paused, receipt, err := store.SetGoalStatusWithMessage(
		eventLog,
		nil,
		GoalStatusPaused,
		GoalActorUser,
		goalTestMessageRecord(t, "goal paused"),
	)
	if err != nil || !receipt.Committed || paused.Status != GoalStatusPaused {
		t.Fatalf("SetGoalStatusWithMessage goal=%+v receipt=%+v err=%v", paused, receipt, err)
	}

	if _, transitioned, receipt, err := store.CompleteGoalIfActiveWithMessage(
		eventLog,
		nil,
		goal.ID,
		GoalActorSystem,
		goalTestMessageRecord(t, "should not persist"),
	); err != nil || transitioned || receipt.Committed {
		t.Fatalf("guarded completion transitioned=%t receipt=%+v err=%v, want no mutation", transitioned, receipt, err)
	}

	cleared, receipt, err := store.ClearGoalWithMessage(
		eventLog,
		nil,
		GoalActorUser,
		goalTestMessageRecord(t, "goal cleared"),
	)
	if err != nil || !receipt.Committed || cleared.ID != goal.ID {
		t.Fatalf("ClearGoalWithMessage goal=%+v receipt=%+v err=%v", cleared, receipt, err)
	}
	if store.Metadata().Goal != nil {
		t.Fatalf("goal after committed clear = %+v, want nil", store.Metadata().Goal)
	}

	records, err := collectEvents(store)
	if err != nil {
		t.Fatalf("collect committed goal records: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("committed goal records = %d, want 3", len(records))
	}
	for index, record := range records {
		message, ok := mustEventRecordPayload(record).(MessageRecord)
		if !ok || message.Role != MessageRoleDeveloper {
			t.Fatalf("goal record %d = %#v, want developer MessageRecord", index, mustEventRecordPayload(record))
		}
	}
}

func materializeGoalTestEventLog(t *testing.T, store *Store) MaterializedEventLog {
	t.Helper()
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize goal event log: %v", err)
	}
	return eventLog
}

func goalTestMessageRecord(t *testing.T, content string) MessageRecord {
	t.Helper()
	messageType := MessageTypeGoal
	record := MessageRecord{
		Role:        MessageRoleDeveloper,
		MessageType: &messageType,
		Content:     &content,
	}
	if _, err := NewEventRecord(1, nil, record); err != nil {
		t.Fatalf("build goal message record: %v", err)
	}
	return record
}

func TestGoalStatusAndClearPersistOnlyMetadata(t *testing.T) {
	store := newSessionTestStore(t)
	first, err := store.SetGoal("first goal", GoalActorUser)
	if err != nil {
		t.Fatalf("SetGoal first: %v", err)
	}
	second, err := store.SetGoal("second goal", GoalActorUser)
	if err != nil {
		t.Fatalf("SetGoal second: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("replacement reused goal id %q", second.ID)
	}

	paused, err := store.SetGoalStatus(GoalStatusPaused, GoalActorAgent)
	if err != nil {
		t.Fatalf("SetGoalStatus paused: %v", err)
	}
	if paused.Status != GoalStatusPaused {
		t.Fatalf("paused status = %q", paused.Status)
	}
	cleared, err := store.ClearGoal(GoalActorUser)
	if err != nil {
		t.Fatalf("ClearGoal: %v", err)
	}
	if cleared.ID != second.ID || cleared.Status != GoalStatusPaused {
		t.Fatalf("cleared goal = %+v, want second paused goal", cleared)
	}
	if store.Metadata().Goal != nil {
		t.Fatalf("meta goal after clear = %+v, want nil", store.Metadata().Goal)
	}

	events, err := collectEvents(store)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("goal metadata mutations emitted events: %+v", events)
	}
}

func TestSetGoalRejectsAgentOverwriteOfActiveOrPausedGoal(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status GoalStatus
	}{
		{name: "active", status: GoalStatusActive},
		{name: "paused", status: GoalStatusPaused},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newSessionTestStore(t)
			existing, err := store.SetGoal("existing goal", GoalActorUser)
			if err != nil {
				t.Fatalf("SetGoal initial: %v", err)
			}
			if tt.status == GoalStatusPaused {
				existing, err = store.SetGoalStatus(GoalStatusPaused, GoalActorUser)
				if err != nil {
					t.Fatalf("SetGoalStatus paused: %v", err)
				}
			}

			_, err = store.SetGoal("agent replacement", GoalActorAgent)
			var blocked GoalAgentOverwriteBlockedError
			if !errors.As(err, &blocked) {
				t.Fatalf("SetGoal agent overwrite error = %v, want GoalAgentOverwriteBlockedError", err)
			}
			if blocked.Goal.ID != existing.ID || blocked.Goal.Objective != existing.Objective || blocked.Goal.Status != tt.status {
				t.Fatalf("blocked goal = %+v, want existing %+v status %q", blocked.Goal, existing, tt.status)
			}
			if goal := store.Metadata().Goal; goal == nil || goal.ID != existing.ID || goal.Objective != existing.Objective || goal.Status != tt.status {
				t.Fatalf("persisted goal after rejected overwrite = %+v", goal)
			}
		})
	}
}

func TestSetGoalAllowsOnlyOneConcurrentAgentGoal(t *testing.T) {
	store := newSessionTestStore(t)
	start := make(chan struct{})
	type result struct {
		goal GoalState
		err  error
	}
	results := make(chan result, 2)

	var ready sync.WaitGroup
	ready.Add(2)
	for _, objective := range []string{"first agent goal", "second agent goal"} {
		objective := objective
		go func() {
			ready.Done()
			<-start
			goal, err := store.SetGoal(objective, GoalActorAgent)
			results <- result{goal: goal, err: err}
		}()
	}
	ready.Wait()
	close(start)

	successes := make([]GoalState, 0, 1)
	blocked := 0
	for i := 0; i < 2; i++ {
		got := <-results
		if got.err == nil {
			successes = append(successes, got.goal)
			continue
		}
		var blockedErr GoalAgentOverwriteBlockedError
		if !errors.As(got.err, &blockedErr) {
			t.Fatalf("SetGoal concurrent error = %v, want blocked error", got.err)
		}
		blocked++
	}
	if len(successes) != 1 || blocked != 1 {
		t.Fatalf("concurrent agent goals successes=%d blocked=%d, want 1/1", len(successes), blocked)
	}
	if goal := store.Metadata().Goal; goal == nil || goal.ID != successes[0].ID || goal.Status != GoalStatusActive {
		t.Fatalf("persisted concurrent goal = %+v, want success %+v", goal, successes[0])
	}
}

func TestGoalValidationRejectsInvalidValues(t *testing.T) {
	store := newSessionTestStore(t)
	if _, err := store.SetGoal(" \n\t ", GoalActorUser); err == nil {
		t.Fatalf("SetGoal empty objective error = nil")
	}
	if _, err := store.SetGoal("objective", GoalActor("robot")); err == nil {
		t.Fatalf("SetGoal invalid actor error = nil")
	}
	if _, err := store.SetGoal("objective", GoalActorUser); err != nil {
		t.Fatalf("SetGoal valid: %v", err)
	}
	if _, err := store.SetGoalStatus(GoalStatus("blocked"), GoalActorUser); err == nil {
		t.Fatalf("SetGoalStatus invalid status error = nil")
	}
}
