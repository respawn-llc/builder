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

	if _, err := engine.SubmitUserMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	if writer.eligibilityWrites != 1 {
		t.Fatalf("eligibility writes = %d, want one failed best-effort write", writer.eligibilityWrites)
	}
	if err := store.SetName("later ordinary mutation"); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	if writer.eligibilityWrites != 1 {
		t.Fatalf("ordinary mutation retried eligibility write: %d", writer.eligibilityWrites)
	}
	facts := store.ContextFacts()
	if facts.ManualCompactEligible == nil || *facts.ManualCompactEligible {
		t.Fatalf("failed eligibility became present true: %+v", facts)
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
	if err := store.SetName("later ordinary mutation"); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	if writer.countWrites != 1 {
		t.Fatalf("ordinary mutation retried count write: %d", writer.countWrites)
	}
	facts := store.ContextFacts()
	if facts.CompletedCompactionCount == nil || *facts.CompletedCompactionCount != 0 ||
		facts.ManualCompactEligible == nil || *facts.ManualCompactEligible {
		t.Fatalf("failed compaction facts were published: %+v", facts)
	}
}

func TestRestoredRuntimeAuthorityDoesNotOpenAbsentPresentationGates(t *testing.T) {
	store := mustCreateTestSession(t)
	count := 2
	eligible := true
	record := session.PersistedSessionRecord{
		SessionDir: store.Dir(),
		Meta:       textutil.Value(store.Meta()),
		ContextFacts: session.SessionContextFacts{
			CompletedCompactionCount: nil,
			ManualCompactEligible:    nil,
		},
	}
	reopened, err := session.OpenResolved(
		record,
		append(
			runtimeTestSessionPersistence.Options(),
			session.WithSessionContextFactWriter(runtimeTestSessionPersistence),
		)...,
	)
	if err != nil {
		t.Fatalf("OpenResolved: %v", err)
	}
	engine := mustNewTestEngine(t, reopened, &fakeClient{}, newTestToolRegistry(t), Config{Model: "gpt-5"})
	engine.compactionRuntimeState().SetCount(count)
	engine.compactionRuntimeState().SetManualCompactionEligible(eligible)

	facts := engine.compactionRuntimeState().ContextFacts()
	if facts.CompletedCompactionCount != nil || facts.ManualCompactEligible != nil {
		t.Fatalf("restored runtime authority opened presentation gates: %+v", facts)
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

func TestReservedCompactionInterpositionReplacesAgentStepEligibilityFact(t *testing.T) {
	engine, store := newContextFactTestEngine(
		t,
		runtimeTestSessionPersistence,
		&fakeClient{},
		newTestToolRegistry(t),
	)
	lifecycle := &stubExclusiveStepLifecycle{}
	lifecycle.drainFn = func(context.Context) error {
		engine.compactionRuntimeState().SetCount(1)
		engine.compactionRuntimeState().SetManualCompactionEligible(false)
		engine.persistCompletedCompactionFactsBestEffort("reserved-compaction", 1)
		return nil
	}
	engine.stepLifecycle = lifecycle
	executor := &defaultStepExecutor{engine: engine}

	if err := executor.completeAgentStepBoundary(context.Background(), "agent-step"); err != nil {
		t.Fatalf("completeAgentStepBoundary: %v", err)
	}
	facts := store.ContextFacts()
	if facts.CompletedCompactionCount == nil || *facts.CompletedCompactionCount != 1 ||
		facts.ManualCompactEligible == nil || *facts.ManualCompactEligible {
		t.Fatalf("post-interposition Context facts = %+v, want compaction 1/false", facts)
	}
}
