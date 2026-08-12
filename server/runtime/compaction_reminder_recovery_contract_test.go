package runtime

import (
	"context"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/shared/sessioncontract"
	"core/shared/textutil"
)

func TestCompactionSoonReminderRemainsSingleShotAcrossAdmissionToggle(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	var events []Event
	engine := newReminderRecoveryEngine(t, store, &fakeClient{}, func(event Event) {
		events = append(events, event)
	})
	seedReminderUsage(t, engine)

	engine.SetAutoCompactionEnabled(false)
	if err := newCompactionReminderCoordinator(engine).maybeAppend(context.Background(), "disabled"); err != nil {
		t.Fatalf("append disabled reminder: %v", err)
	}
	engine.SetAutoCompactionEnabled(true)
	if err := newCompactionReminderCoordinator(engine).maybeAppend(context.Background(), "enabled"); err != nil {
		t.Fatalf("append enabled reminder: %v", err)
	}
	if err := newCompactionReminderCoordinator(engine).maybeAppend(context.Background(), "duplicate"); err != nil {
		t.Fatalf("append duplicate reminder: %v", err)
	}

	if reminders := typedReminderEvents(events); reminders != 1 {
		t.Fatalf("typed reminder events = %d, want one; events=%+v", reminders, events)
	}
	if reminders := boundedReminderRecords(t, store); reminders != 1 {
		t.Fatalf("bounded reminder records = %d, want one", reminders)
	}
}

func TestReopenPreservesCompactionSoonReminderAdmission(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := newReminderRecoveryEngine(t, store, &fakeClient{}, nil)
	seedReminderUsage(t, engine)
	if err := newCompactionReminderCoordinator(engine).maybeAppend(context.Background(), "issued"); err != nil {
		t.Fatalf("append reminder: %v", err)
	}

	reopenedStore := mustOpenTestSession(t, store.Dir())
	reopened := newReminderRecoveryEngine(t, reopenedStore, &fakeClient{}, nil)
	seedReminderUsage(t, reopened)
	if !reopened.compactionRuntimeState().SoonReminderIssued() || !reopenedStore.Meta().CompactionSoonReminderIssued {
		t.Fatalf("reopened reminder state = runtime:%v persisted:%v", reopened.compactionRuntimeState().SoonReminderIssued(), reopenedStore.Meta().CompactionSoonReminderIssued)
	}
	if err := newCompactionReminderCoordinator(reopened).maybeAppend(context.Background(), "reopened"); err != nil {
		t.Fatalf("append reopened reminder: %v", err)
	}
	if reminders := boundedReminderRecords(t, reopenedStore); reminders != 1 {
		t.Fatalf("reopened bounded reminder records = %d, want one", reminders)
	}
}

func TestForkBeforeReminderDoesNotInheritReminderAdmission(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := newReminderRecoveryEngine(t, store, &fakeClient{}, nil)
	if err := engine.steer("seed", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist fork anchor: %v", err)
	}
	if err := engine.persistCompactionSoonReminderIssued(true); err != nil {
		t.Fatalf("persist reminder admission: %v", err)
	}

	forkedStore, _, err := session.ForkAtUserMessage(
		mustMaterializeTestEventLog(t, store),
		boundedLatestUserSequence(t, store),
		"fork",
		sessioncontract.SessionCategoryMain,
	)
	if err != nil {
		t.Fatalf("fork before reminder: %v", err)
	}
	if forkedStore.Meta().CompactionSoonReminderIssued {
		t.Fatal("fork before reminder inherited reminder admission")
	}
}

func TestForkAfterReminderPreservesReminderAdmission(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := newReminderRecoveryEngine(t, store, &fakeClient{}, nil)
	if err := engine.steer("seed", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("before")}},
	)); err != nil {
		t.Fatalf("persist seed: %v", err)
	}
	seedReminderUsage(t, engine)
	if err := newCompactionReminderCoordinator(engine).maybeAppend(context.Background(), "issued"); err != nil {
		t.Fatalf("append reminder: %v", err)
	}
	if err := engine.steer("anchor", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("after")}},
	)); err != nil {
		t.Fatalf("persist fork anchor: %v", err)
	}

	forkedStore, _, err := session.ForkAtUserMessage(
		mustMaterializeTestEventLog(t, store),
		boundedLatestUserSequence(t, store),
		"fork",
		sessioncontract.SessionCategoryMain,
	)
	if err != nil {
		t.Fatalf("fork after reminder: %v", err)
	}
	if !forkedStore.Meta().CompactionSoonReminderIssued {
		t.Fatal("fork after reminder lost reminder admission")
	}
	if reminders := boundedReminderRecords(t, forkedStore); reminders != 1 {
		t.Fatalf("forked bounded reminder records = %d, want one", reminders)
	}
}

func newReminderRecoveryEngine(t *testing.T, store *session.Store, client llm.Client, onEvent func(Event)) *Engine {
	t.Helper()
	return mustNewTestEngine(t, store, client, newTestToolRegistry(t), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 1_000,
		CompactionMode:        "local",
		OnEvent:               onEvent,
	})
}

func seedReminderUsage(t *testing.T, engine *Engine) {
	t.Helper()
	engine.setLastUsage(llm.Usage{InputTokens: 890, WindowTokens: 2_000})
}

func typedReminderEvents(events []Event) int {
	count := 0
	for _, event := range events {
		if event.Kind == EventConversationUpdated &&
			event.Message.MessageType != nil &&
			*event.Message.MessageType == llm.MessageTypeCompactionSoonReminder {
			count++
		}
	}
	return count
}

func boundedReminderRecords(t *testing.T, store *session.Store) int {
	t.Helper()
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded reminder records: %v", err)
	}
	count := 0
	for _, record := range window.Records {
		message, ok := mustSessionEventPayload(record).(session.MessageRecord)
		if ok && message.MessageType != nil && *message.MessageType == session.MessageTypeCompactionSoonReminder {
			count++
		}
	}
	return count
}

func boundedLatestUserSequence(t *testing.T, store *session.Store) int64 {
	t.Helper()
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded fork records: %v", err)
	}
	for index := len(window.Records) - 1; index >= 0; index-- {
		record := window.Records[index]
		message, ok := mustSessionEventPayload(record).(session.MessageRecord)
		if ok && message.Role == session.MessageRoleUser {
			return record.Seq()
		}
	}
	t.Fatal("bounded fork records contain no user message")
	return 0
}
