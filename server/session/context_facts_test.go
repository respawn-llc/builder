package session

import (
	"context"
	"errors"
	"testing"
)

type recordingContextFactWriter struct {
	countWrites       int
	eligibilityWrites int
	facts             SessionContextFacts
	err               error
}

func sessionContextFactsEqual(left, right SessionContextFacts) bool {
	return optionalContextFactIntEqual(left.CompletedCompactionCount, right.CompletedCompactionCount) &&
		optionalContextFactBoolEqual(left.ManualCompactEligible, right.ManualCompactEligible)
}

func optionalContextFactIntEqual(left, right *int) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func optionalContextFactBoolEqual(left, right *bool) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func (w *recordingContextFactWriter) WriteSessionContextFacts(
	_ context.Context,
	_ string,
	facts SessionContextFacts,
) error {
	w.countWrites++
	if w.err != nil {
		return w.err
	}
	w.facts = facts.Clone()
	return nil
}

func (w *recordingContextFactWriter) WriteManualCompactEligibility(
	_ context.Context,
	_ string,
	eligible bool,
) error {
	w.eligibilityWrites++
	if w.err != nil {
		return w.err
	}
	w.facts.ManualCompactEligible = &eligible
	return nil
}

func TestIndependentSessionContextFactsStartPresentAtZeroAndFalse(t *testing.T) {
	store, err := NewLazy(
		t.TempDir(),
		"workspace",
		"/workspace",
		testSessionCategory,
		WithPersistenceObserver(&recordingPersistenceObserver{}),
	)
	if err != nil {
		t.Fatalf("NewLazy: %v", err)
	}

	facts := store.ContextFacts()
	if facts.CompletedCompactionCount == nil || *facts.CompletedCompactionCount != 0 {
		t.Fatalf("completed count = %v, want present zero", facts.CompletedCompactionCount)
	}
	if facts.ManualCompactEligible == nil || *facts.ManualCompactEligible {
		t.Fatalf("manual eligibility = %v, want present false", facts.ManualCompactEligible)
	}
}

func TestContextSnapshotClonesBoundedMetaAndFacts(t *testing.T) {
	store := newSessionTestLazyStore(t)
	if _, err := store.SetUsageState(&UsageState{InputTokens: 42}); err != nil {
		t.Fatalf("SetUsageState: %v", err)
	}
	snapshot := store.ContextSnapshot()
	if snapshot.Meta.UsageState == nil || snapshot.Meta.UsageState.InputTokens != 42 {
		t.Fatalf("snapshot usage = %+v, want 42", snapshot.Meta.UsageState)
	}
	if snapshot.Facts.CompletedCompactionCount == nil || *snapshot.Facts.CompletedCompactionCount != 0 {
		t.Fatalf("snapshot count = %v, want present zero", snapshot.Facts.CompletedCompactionCount)
	}
	snapshot.Meta.UsageState.InputTokens = 99
	*snapshot.Facts.CompletedCompactionCount = 7
	again := store.ContextSnapshot()
	if again.Meta.UsageState.InputTokens != 42 || *again.Facts.CompletedCompactionCount != 0 {
		t.Fatalf("snapshot mutated Store state: %+v", again)
	}
}

func TestChildSessionContextFactsAreStructurallyAbsent(t *testing.T) {
	parent := newSessionTestStore(t)
	child := newSessionTestLazyStoreAt(t, t.TempDir())

	if err := InitializeCreationContext(
		child,
		parent,
		SessionCreationSourcePreviousSession,
		ChildContextOptions{},
	); err != nil {
		t.Fatalf("InitializeCreationContext: %v", err)
	}

	facts := child.ContextFacts()
	if facts.CompletedCompactionCount != nil || facts.ManualCompactEligible != nil {
		t.Fatalf("child Context facts = %+v, want structurally absent", facts)
	}
}

func TestContextFactWritesUpdateMirrorOnlyAfterWriterCommit(t *testing.T) {
	writer := &recordingContextFactWriter{err: errors.New("write failed")}
	store, err := NewLazy(
		t.TempDir(),
		"workspace",
		"/workspace",
		testSessionCategory,
		WithPersistenceObserver(&recordingPersistenceObserver{}),
		WithSessionContextFactWriter(writer),
	)
	if err != nil {
		t.Fatalf("NewLazy: %v", err)
	}
	before := store.ContextFacts()

	if err := store.SetSessionContextFacts(3, true); err == nil {
		t.Fatal("SetSessionContextFacts succeeded despite writer failure")
	}
	if got := store.ContextFacts(); !sessionContextFactsEqual(got, before) {
		t.Fatalf("mirror after failed count write = %+v, want %+v", got, before)
	}
	if err := store.SetManualCompactEligibility(true); err == nil {
		t.Fatal("SetManualCompactEligibility succeeded despite writer failure")
	}
	if got := store.ContextFacts(); !sessionContextFactsEqual(got, before) {
		t.Fatalf("mirror after failed eligibility write = %+v, want %+v", got, before)
	}

	writer.err = nil
	if err := store.SetManualCompactEligibility(true); err != nil {
		t.Fatalf("successful eligibility write: %v", err)
	}
	got := store.ContextFacts()
	if got.CompletedCompactionCount == nil || *got.CompletedCompactionCount != 0 ||
		got.ManualCompactEligible == nil || !*got.ManualCompactEligible {
		t.Fatalf("mirror after successful eligibility write = %+v", got)
	}
	if writer.countWrites != 1 || writer.eligibilityWrites != 2 {
		t.Fatalf("writer calls = count:%d eligibility:%d", writer.countWrites, writer.eligibilityWrites)
	}
}

func TestFailedContextFactWriteIsNotRetriedByOrdinaryMutation(t *testing.T) {
	writer := &recordingContextFactWriter{err: errors.New("write failed")}
	observer := &recordingPersistenceObserver{}
	store, err := NewLazy(
		t.TempDir(),
		"workspace",
		"/workspace",
		testSessionCategory,
		WithPersistenceObserver(observer),
		WithSessionContextFactWriter(writer),
	)
	if err != nil {
		t.Fatalf("NewLazy: %v", err)
	}
	if err := store.SetManualCompactEligibility(true); err == nil {
		t.Fatal("eligibility write succeeded")
	}
	writer.err = nil

	if err := store.SetName("ordinary mutation"); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	if writer.eligibilityWrites != 1 {
		t.Fatalf("eligibility writes = %d, want no ordinary-mutation retry", writer.eligibilityWrites)
	}
	if got := store.ContextFacts(); got.ManualCompactEligible == nil || *got.ManualCompactEligible {
		t.Fatalf("failed eligibility was later published: %+v", got)
	}
}

func TestOpenRestoresContextFactsOutsideRecoverableMeta(t *testing.T) {
	root := t.TempDir()
	writer := &recordingContextFactWriter{}
	observer := &recordingPersistenceObserver{}
	store, err := Create(
		root,
		"workspace",
		"/workspace",
		testSessionCategory,
		WithPersistenceObserver(observer),
		WithSessionContextFactWriter(writer),
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	count := 7
	eligible := true
	record := PersistedSessionRecord{
		SessionDir: store.Dir(),
		Meta:       &observer.snapshot.Meta,
		ContextFacts: SessionContextFacts{
			CompletedCompactionCount: &count,
			ManualCompactEligible:    &eligible,
		},
	}
	resolver := stubPersistedSessionResolver{record: record}

	reopened, err := Open(
		store.Dir(),
		WithPersistenceObserver(observer),
		WithPersistedSessionResolver(resolver),
		WithSessionContextFactWriter(writer),
	)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	facts := reopened.ContextFacts()
	if facts.CompletedCompactionCount == nil || *facts.CompletedCompactionCount != 7 ||
		facts.ManualCompactEligible == nil || !*facts.ManualCompactEligible {
		t.Fatalf("reopened Context facts = %+v", facts)
	}
	if reopened.Meta().UsageState != observer.snapshot.Meta.UsageState {
		t.Fatal("opening Context facts changed recoverable Meta")
	}
}

func TestOpenNormalizesInvalidPersistedContextFactsWithoutWriting(t *testing.T) {
	root := t.TempDir()
	writer := &recordingContextFactWriter{}
	observer := &recordingPersistenceObserver{}
	store, err := Create(
		root,
		"workspace",
		"/workspace",
		testSessionCategory,
		WithPersistenceObserver(observer),
		WithSessionContextFactWriter(writer),
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	writer.countWrites = 0
	invalidCount := -7
	record := PersistedSessionRecord{
		SessionDir: store.Dir(),
		Meta:       &observer.snapshot.Meta,
		ContextFacts: SessionContextFacts{
			CompletedCompactionCount: &invalidCount,
		},
	}

	reopened, err := Open(
		store.Dir(),
		WithPersistenceObserver(observer),
		WithPersistedSessionResolver(stubPersistedSessionResolver{record: record}),
		WithSessionContextFactWriter(writer),
	)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	facts := reopened.ContextFacts()
	if facts.CompletedCompactionCount == nil || *facts.CompletedCompactionCount != 0 {
		t.Fatalf("normalized completed count = %v, want present zero", facts.CompletedCompactionCount)
	}
	if writer.countWrites != 0 {
		t.Fatalf("normalization wrote Context facts %d times, want read-only normalization", writer.countWrites)
	}
}

func TestForkAndCloneLeaveContextFactsAbsent(t *testing.T) {
	parent := newSessionTestStore(t)
	log := materializedForkEventLog(t, parent)
	target, _, err := log.AppendRecord(
		forkStringPointer("step"),
		forkUserMessageRecord("target"),
	)
	if err != nil {
		t.Fatalf("append fork target: %v", err)
	}

	forked, _, err := ForkAtUserMessage(log, target.Seq(), "fork", testSessionCategory)
	if err != nil {
		t.Fatalf("ForkAtUserMessage: %v", err)
	}
	cloned, err := CloneSession(log, "clone", testSessionCategory)
	if err != nil {
		t.Fatalf("CloneSession: %v", err)
	}
	for name, child := range map[string]*Store{"fork": forked, "clone": cloned} {
		facts := child.ContextFacts()
		if facts.CompletedCompactionCount != nil || facts.ManualCompactEligible != nil {
			t.Fatalf("%s Context facts = %+v, want absent", name, facts)
		}
	}
}
