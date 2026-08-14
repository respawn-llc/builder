package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/runtimeids"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestAcceptedResponsePersistsProviderModelMismatchAndAdjustedUsage(t *testing.T) {
	for _, test := range []struct {
		name       string
		debug      bool
		visibility transcript.EntryVisibility
	}{
		{name: "detail by default", visibility: transcript.EntryVisibilityDetail},
		{name: "ongoing in debug", debug: true, visibility: transcript.EntryVisibilityOngoing},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			response := finalTextResponse("done")
			response.ServedModel = stringPointer("served-model")
			response.Usage = llm.Usage{InputTokens: 42, OutputTokens: 3, WindowTokens: 200000}
			engine := mustNewTestEngine(t, store, &fakeClient{responses: []llm.Response{response}}, newTestToolRegistry(t), Config{
				Model: "requested-model",
				Debug: test.debug,
			})
			if _, err := runStepLoopInActiveTestRun(t, context.Background(), engine); err != nil {
				t.Fatal(err)
			}
			warnings := providerModelMismatchWarnings(t, store)
			if len(warnings) != 1 {
				t.Fatalf("provider-model mismatch warning count = %d, want one", len(warnings))
			}
			warning := warnings[0]
			if warning.Visibility != test.visibility ||
				warning.ProviderModelMismatch.RequestedModel != "requested-model" ||
				warning.ProviderModelMismatch.ServedModel != "served-model" {
				t.Fatalf("warning = %+v", warning)
			}
			if usage := store.Meta().UsageState; usage == nil || usage.InputTokens != 42 {
				t.Fatalf("usage state = %+v, want accepted response usage", store.Meta().UsageState)
			}
		})
	}
}

func TestConsecutiveAcceptedToolLoopResponsesPersistOneMismatchEach(t *testing.T) {
	store := mustCreateTestSession(t)
	first := commentaryResponse("working", llm.ToolCall{ID: "call-1", Name: "exec_command", Input: json.RawMessage(`{"cmd":"true"}`)})
	first.ServedModel = stringPointer("served-model")
	second := finalTextResponse("done")
	second.ServedModel = stringPointer("served-model")
	engine := mustNewTestEngine(t, store, &fakeClient{responses: []llm.Response{first, second}}, newTestToolRegistry(t,
		tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "requested-model"})
	if _, err := runStepLoopInActiveTestRun(t, context.Background(), engine); err != nil {
		t.Fatal(err)
	}
	if warnings := providerModelMismatchWarnings(t, store); len(warnings) != 1 {
		t.Fatalf("provider-model mismatch warning count = %d, want one", len(warnings))
	}
}
func TestQueuedAgentSteerStartsNewMismatchWarningStep(t *testing.T) {
	first := commentaryResponse("working", llm.ToolCall{ID: "call-1", Name: "exec_command", Input: json.RawMessage(`{"cmd":"true"}`)})
	first.ServedModel = stringPointer("served-model")
	second := finalTextResponse("done")
	second.ServedModel = stringPointer("served-model")
	client, started, release := newGatedHookClient(first, second)
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client,
		newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}),
		Config{Model: "requested-model"})
	done := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(context.Background(), "start")
		done <- err
	}()
	<-started
	steer, err := NewAgentSteer(runtimeids.NewSessionID(), "new instructions")
	if err != nil {
		t.Fatalf("NewAgentSteer: %v", err)
	}
	if _, err := engine.AcceptAgentSteering(steer, nil); err != nil {
		t.Fatalf("accept Agent Steering: %v", err)
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if warnings := providerModelMismatchWarnings(t, engine.store); len(warnings) != 2 {
		t.Fatalf("provider-model mismatch warning count = %d, want one per Agent Step", len(warnings))
	}
}
func TestAcceptedResponsePersistenceAttemptsWarningAndUsageIndependently(t *testing.T) {
	t.Run("warning failure still attempts usage", func(t *testing.T) {
		warningErr := errors.New("warning observer failed")
		store, engine, gate := newAcceptedPersistenceTest(t)
		baselineSequence := store.Meta().LastSequence
		gate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
			return snapshot.Meta.LastSequence > baselineSequence && snapshot.Meta.UsageState == nil
		}, warningErr)
		_, err := engine.commitAcceptedResponseCandidate("step-1", acceptedMismatchCandidate(), false)
		requireErrorIs(t, err, warningErr)
		if store.Meta().UsageState == nil {
			t.Fatal("usage checkpoint was not attempted after warning failure")
		}
	})
	t.Run("usage failure follows warning", func(t *testing.T) {
		usageErr := errors.New("usage observer failed")
		store, engine, gate := newAcceptedPersistenceTest(t)
		gate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
			return snapshot.Meta.UsageState != nil
		}, usageErr)
		_, err := engine.commitAcceptedResponseCandidate("step-1", acceptedMismatchCandidate(), false)
		requireErrorIs(t, err, usageErr)
		if warnings := providerModelMismatchWarnings(t, store); len(warnings) != 1 {
			t.Fatalf("provider-model mismatch warning count = %d, want one", len(warnings))
		}
	})
	t.Run("both failures surface", func(t *testing.T) {
		usageErr := errors.New("usage observer failed")
		store, engine, gate := newAcceptedPersistenceTest(t)
		gate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
			return snapshot.Meta.UsageState != nil
		}, usageErr)
		blocker := mustBlockTestEventLogAppends(t, store)
		_, err := engine.commitAcceptedResponseCandidate("step-1", acceptedMismatchCandidate(), false)
		requireErrorIs(t, err, usageErr)
		joined, ok := err.(interface{ Unwrap() []error })
		if !ok || len(joined.Unwrap()) != 2 {
			t.Fatalf("commit error = %v, want both independent persistence failures", err)
		}
		if restoreErr := blocker.Restore(); restoreErr != nil {
			t.Fatalf("restore event log appends: %v", restoreErr)
		}
	})
}
func newAcceptedPersistenceTest(t *testing.T) (*session.Store, *Engine, *sessiontest.PersistenceGate) {
	t.Helper()
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	return store, mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), Config{Model: "requested-model"}), gate
}
func requireErrorIs(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("error = %v, want %v", got, want)
	}
}
func acceptedMismatchCandidate() successfulRequestCandidate {
	return successfulRequestCandidate{
		response: llm.Response{
			ServedModel: stringPointer("served-model"),
			Usage:       llm.Usage{InputTokens: 42, OutputTokens: 3, WindowTokens: 200000},
		},
		requestedModel:          "requested-model",
		estimatedProviderTokens: 17,
	}
}
func providerModelMismatchWarnings(t *testing.T, store *session.Store) []storedLocalEntry {
	t.Helper()
	events, err := collectTestEventRecords(store)
	if err != nil {
		t.Fatalf("collect events: %v", err)
	}
	var warnings []storedLocalEntry
	for _, event := range events {
		if event.Kind == "local_entry" {
			entry := persistedLocalEntryForTest(t, event)
			if entry.ProviderModelMismatch != nil {
				warnings = append(warnings, entry)
			}
		}
	}
	return warnings
}
