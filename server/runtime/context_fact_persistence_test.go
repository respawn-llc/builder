package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/sessioncontract"
	"core/shared/textutil"
)

type failingContextFactWriter struct {
	delegate          session.SessionContextFactWriter
	failuresRemaining int
	countWrites       int
	eligibilityWrites int
}

func (w *failingContextFactWriter) WriteSessionContextFacts(
	ctx context.Context,
	sessionID string,
	facts session.SessionContextFacts,
) error {
	w.countWrites++
	if w.failuresRemaining > 0 {
		w.failuresRemaining--
		return errors.New("forced Context-fact failure")
	}
	return w.delegate.WriteSessionContextFacts(ctx, sessionID, facts)
}

func (w *failingContextFactWriter) WriteManualCompactEligibility(
	ctx context.Context,
	sessionID string,
	eligible bool,
) error {
	w.eligibilityWrites++
	if w.failuresRemaining > 0 {
		w.failuresRemaining--
		return errors.New("forced Context-fact failure")
	}
	return w.delegate.WriteManualCompactEligibility(ctx, sessionID, eligible)
}

func newContextFactTestEngine(
	t *testing.T,
	writer session.SessionContextFactWriter,
	client llm.Client,
	registry *tools.Registry,
) (*Engine, *session.Store) {
	t.Helper()
	root := t.TempDir()
	store, err := session.Create(
		root,
		"workspace",
		"/workspace",
		sessioncontract.SessionCategoryMain,
		append(runtimeTestSessionPersistence.Options(), session.WithSessionContextFactWriter(writer))...,
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	initializeTestEventLog(t, store)
	engine := mustNewTestEngine(t, store, client, registry, Config{
		Model:          "gpt-5",
		CompactionMode: "local",
	})
	return engine, store
}

func TestFailedAgentStepContextFactWriteDoesNotFailStepOrRetryLater(t *testing.T) {
	var events []Event
	writer := &failingContextFactWriter{
		delegate:          runtimeTestSessionPersistence,
		failuresRemaining: 1,
	}
	engine, store := newContextFactTestEngine(
		t,
		writer,
		&fakeClient{responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done")},
		}}},
		newTestToolRegistry(t),
	)
	engine.cfg.OnEvent = func(event Event) {
		events = append(events, event)
	}

	if _, err := engine.SubmitUserMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	if writer.eligibilityWrites != 1 {
		t.Fatalf("eligibility writes = %d, want one failed best-effort write", writer.eligibilityWrites)
	}
	facts := store.ContextFacts()
	if facts.ManualCompactEligible == nil || *facts.ManualCompactEligible {
		t.Fatalf("failed eligibility became present true: %+v", facts)
	}
	if !hasEventKind(events, EventContextFactsPersistFailed) {
		t.Fatalf("events = %+v, want Context-fact persistence operational diagnostic", events)
	}
	for _, event := range events {
		if event.Kind == EventLocalEntryAdded {
			t.Fatalf("failed Context-fact write appended transcript row: %+v", event)
		}
	}
}

func TestFailedCompactionContextFactWriteDoesNotFailCompactionOrRetryLater(t *testing.T) {
	writer := &failingContextFactWriter{
		delegate:          runtimeTestSessionPersistence,
		failuresRemaining: 1,
	}
	engine, store := newContextFactTestEngine(
		t,
		writer,
		&fakeCompactionClient{responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("summary")},
		}}},
		newTestToolRegistry(t),
	)
	engine.compactionRuntimeState().SetManualCompactionEligible(true)

	if err := engine.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("CompactContext: %v", err)
	}
	if writer.countWrites != 1 {
		t.Fatalf("count writes = %d, want one failed best-effort write", writer.countWrites)
	}
	facts := store.ContextFacts()
	if facts.CompletedCompactionCount == nil || *facts.CompletedCompactionCount != 0 ||
		facts.ManualCompactEligible == nil || *facts.ManualCompactEligible {
		t.Fatalf("failed compaction facts were published: %+v", facts)
	}
}

func TestSuccessfulAgentStepEstablishesAbsentManualEligibilityFact(t *testing.T) {
	store := mustCreateTestSession(t)
	record := session.PersistedSessionRecord{
		SessionDir:   store.Dir(),
		Meta:         textutil.Value(store.Meta()),
		ContextFacts: session.SessionContextFacts{},
	}
	reopened, err := session.OpenResolved(record, runtimeTestSessionPersistence.Options()...)
	if err != nil {
		t.Fatalf("OpenResolved: %v", err)
	}
	engine := mustNewTestEngine(
		t,
		reopened,
		&fakeClient{responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done")},
		}}},
		newTestToolRegistry(t),
		Config{Model: "gpt-5"},
	)

	if _, err := engine.SubmitUserMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	facts := reopened.ContextFacts()
	if facts.CompletedCompactionCount != nil ||
		facts.ManualCompactEligible == nil || !*facts.ManualCompactEligible {
		t.Fatalf("Agent Step Context facts = %+v, want absent count and present eligibility", facts)
	}
}

func TestSuccessfulCompactionEstablishesBothAbsentContextFacts(t *testing.T) {
	store := mustCreateTestSession(t)
	record := session.PersistedSessionRecord{
		SessionDir:   store.Dir(),
		Meta:         textutil.Value(store.Meta()),
		ContextFacts: session.SessionContextFacts{},
	}
	reopened, err := session.OpenResolved(record, runtimeTestSessionPersistence.Options()...)
	if err != nil {
		t.Fatalf("OpenResolved: %v", err)
	}
	engine := mustNewTestEngine(
		t,
		reopened,
		&fakeCompactionClient{responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("summary")},
		}}},
		newTestToolRegistry(t),
		Config{Model: "gpt-5", CompactionMode: "local"},
	)
	engine.compactionRuntimeState().SetManualCompactionEligible(true)

	if err := engine.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("CompactContext: %v", err)
	}
	facts := reopened.ContextFacts()
	if facts.CompletedCompactionCount == nil || *facts.CompletedCompactionCount != 1 ||
		facts.ManualCompactEligible == nil || *facts.ManualCompactEligible {
		t.Fatalf("compaction Context facts = %+v, want present 1/false", facts)
	}
}
