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
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestAcceptedResponsePersistsProviderModelMismatchAndAdjustedUsage(t *testing.T) {
	tests := []struct {
		name       string
		debug      bool
		visibility transcript.EntryVisibility
	}{
		{name: "detail by default", visibility: transcript.EntryVisibilityDetail},
		{name: "ongoing in debug", debug: true, visibility: transcript.EntryVisibilityOngoing},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			response := finalTextResponse("done")
			response.ServedModel = stringPointer("served-model")
			response.Usage = llm.Usage{InputTokens: 42, OutputTokens: 3, WindowTokens: 200000}
			engine := mustNewTestEngine(t, store, &fakeClient{responses: []llm.Response{response}}, newTestToolRegistry(t), Config{
				Model: "requested-model",
				Debug: test.debug,
			})

			if _, err := engine.runStepLoop(context.Background(), "step-1"); err != nil {
				t.Fatalf("run step: %v", err)
			}

			events, err := collectTestEventRecords(store)
			if err != nil {
				t.Fatalf("collect events: %v", err)
			}
			var warnings []storedLocalEntry
			for _, event := range events {
				if event.Kind != "local_entry" {
					continue
				}
				entry := persistedLocalEntryForTest(t, event)
				if entry.ProviderModelMismatch != nil {
					warnings = append(warnings, entry)
				}
			}
			if len(warnings) != 1 {
				t.Fatalf("provider-model mismatch warnings = %+v, want one", warnings)
			}
			warning := warnings[0]
			if warning.Visibility != test.visibility ||
				warning.ProviderModelMismatch.RequestedModel != "requested-model" ||
				warning.ProviderModelMismatch.ServedModel != "served-model" {
				t.Fatalf("warning = %+v", warning)
			}
			if store.Meta().UsageState == nil || store.Meta().UsageState.InputTokens != 42 {
				t.Fatalf("usage state = %+v, want accepted response usage", store.Meta().UsageState)
			}

			providerItems := engine.transcriptRuntimeState().SnapshotItems()
			for _, item := range providerItems {
				if item.Type == llm.ResponseItemTypeMessage && item.Role != nil && *item.Role == llm.RoleDeveloper &&
					item.Content != nil && *item.Content == "served-model" {
					t.Fatalf("provider history contains model mismatch warning: %+v", item)
				}
			}
		})
	}
}

func TestAcceptedResponseSkipsProviderModelWarningForMissingOrEqualModel(t *testing.T) {
	for _, servedModel := range []*string{nil, stringPointer("requested-model")} {
		store := mustCreateTestSession(t)
		response := finalTextResponse("done")
		response.ServedModel = servedModel
		engine := mustNewTestEngine(t, store, &fakeClient{responses: []llm.Response{response}}, newTestToolRegistry(t), Config{Model: "requested-model"})

		if _, err := engine.runStepLoop(context.Background(), "step-1"); err != nil {
			t.Fatalf("run step: %v", err)
		}
		events, err := collectTestEventRecords(store)
		if err != nil {
			t.Fatalf("collect events: %v", err)
		}
		for _, event := range events {
			if event.Kind == "local_entry" && persistedLocalEntryForTest(t, event).ProviderModelMismatch != nil {
				t.Fatalf("unexpected mismatch warning for served model %#v", servedModel)
			}
		}
	}
}

func TestConsecutiveAcceptedToolLoopResponsesPersistOneMismatchEach(t *testing.T) {
	store := mustCreateTestSession(t)
	first := commentaryResponse("working", llm.ToolCall{
		ID:    "call-1",
		Name:  "exec_command",
		Input: json.RawMessage(`{"cmd":"true"}`),
	})
	first.ServedModel = stringPointer("served-model")
	second := finalTextResponse("done")
	second.ServedModel = stringPointer("served-model")
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{responses: []llm.Response{first, second}},
		newTestToolRegistry(t, tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: fakeTool{name: toolspec.ToolExecCommand},
		}),
		Config{Model: "requested-model"},
	)

	if _, err := engine.runStepLoop(context.Background(), "shared-step"); err != nil {
		t.Fatalf("run tool loop: %v", err)
	}

	events, err := collectTestEventRecords(store)
	if err != nil {
		t.Fatalf("collect events: %v", err)
	}
	warnings := 0
	for _, event := range events {
		if event.Kind == "local_entry" && persistedLocalEntryForTest(t, event).ProviderModelMismatch != nil {
			warnings++
		}
	}
	if warnings != 2 {
		t.Fatalf("provider-model mismatch warnings = %d, want one per accepted generation", warnings)
	}
}

func TestAcceptedResponsePersistenceAttemptsWarningAndUsageIndependently(t *testing.T) {
	t.Run("committed warning observer failure still attempts usage", func(t *testing.T) {
		warningErr := errors.New("warning observer failed")
		gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
		store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
		engine := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), Config{Model: "requested-model"})
		baselineSequence := store.Meta().LastSequence
		gate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
			return snapshot.Meta.LastSequence > baselineSequence && snapshot.Meta.UsageState == nil
		}, warningErr)

		err := engine.commitAcceptedResponseCandidate("step-1", acceptedMismatchCandidate())
		if !errors.Is(err, warningErr) {
			t.Fatalf("commit error = %v, want warning observer failure", err)
		}
		if usage := store.Meta().UsageState; usage == nil ||
			usage.InputTokens != 42 ||
			usage.EstimatedProviderTokens != 17 {
			t.Fatalf("usage state = %+v, want independently committed adjusted checkpoint", usage)
		}
	})

	t.Run("usage observer failure follows successful warning", func(t *testing.T) {
		usageErr := errors.New("usage observer failed")
		gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
		store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
		engine := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), Config{Model: "requested-model"})
		gate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
			return snapshot.Meta.UsageState != nil
		}, usageErr)

		err := engine.commitAcceptedResponseCandidate("step-1", acceptedMismatchCandidate())
		if !errors.Is(err, usageErr) {
			t.Fatalf("commit error = %v, want usage observer failure", err)
		}
		if usage := store.Meta().UsageState; usage == nil ||
			usage.InputTokens != 42 ||
			usage.EstimatedProviderTokens != 17 {
			t.Fatalf("usage state = %+v, want committed adjusted checkpoint", usage)
		}
	})

	t.Run("uncommitted warning and usage observer failures both surface", func(t *testing.T) {
		usageErr := errors.New("usage observer failed")
		gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
		store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
		engine := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), Config{Model: "requested-model"})
		gate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
			return snapshot.Meta.UsageState != nil
		}, usageErr)
		blocker := mustBlockTestEventLogAppends(t, store)

		err := engine.commitAcceptedResponseCandidate("step-1", acceptedMismatchCandidate())
		if !errors.Is(err, usageErr) {
			t.Fatalf("commit error = %v, want usage observer failure", err)
		}
		joined, ok := err.(interface{ Unwrap() []error })
		if !ok || len(joined.Unwrap()) != 2 {
			t.Fatalf("commit error = %v, want both independent persistence failures", err)
		}
		if restoreErr := blocker.Restore(); restoreErr != nil {
			t.Fatalf("restore event log appends: %v", restoreErr)
		}
	})
}

func TestAdjustedUsageBaselineSurvivesSessionReopen(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), Config{Model: "requested-model"})
	if err := engine.commitAcceptedResponseCandidate("step-1", successfulRequestCandidate{
		response:                llm.Response{Usage: llm.Usage{InputTokens: 9, WindowTokens: 200000}},
		requestedModel:          "requested-model",
		estimatedProviderTokens: 23,
	}); err != nil {
		t.Fatalf("commit accepted candidate: %v", err)
	}

	reopened := mustOpenTestSession(t, store.Dir())
	if usage := reopened.Meta().UsageState; usage == nil || usage.EstimatedProviderTokens != 23 {
		t.Fatalf("reopened usage state = %+v, want adjusted baseline 23", usage)
	}
}

func TestProviderModelMismatchHydrationKeepsHistoricalAbsenceAndNewFact(t *testing.T) {
	store := mustCreateTestSession(t)
	if _, _, err := appendTestEvent(t, store, "historical-step", storedLocalEntry{
		Visibility: transcript.EntryVisibilityDetail,
		Role:       string(transcript.EntryRoleWarning),
		Text:       "historical notice",
	}); err != nil {
		t.Fatalf("append historical local entry: %v", err)
	}

	engine := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), Config{Model: "requested-model"})
	if err := engine.commitAcceptedResponseCandidate("new-step", acceptedMismatchCandidate()); err != nil {
		t.Fatalf("commit new mismatch: %v", err)
	}
	if err := engine.Close(); err != nil {
		t.Fatalf("close engine: %v", err)
	}

	reopened := mustNewTestEngine(
		t,
		mustOpenTestSession(t, store.Dir()),
		&fakeClient{},
		newTestToolRegistry(t),
		Config{Model: "requested-model"},
	)
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("close reopened engine: %v", err)
		}
	})
	snapshot := hydrationSnapshot(t, reopened)
	var historical, current *TranscriptNoticeRowFact
	for index := range snapshot.CommittedRows {
		row := &snapshot.CommittedRows[index]
		if row.Kind != TranscriptCommittedRowFactNotice {
			continue
		}
		switch row.StepID {
		case "historical-step":
			historical = row.Notice
		case "new-step":
			current = row.Notice
		}
	}
	if historical == nil || historical.ProviderModelMismatch != nil {
		t.Fatalf("historical hydrated notice = %+v, want mismatch facts absent", historical)
	}
	if current == nil ||
		current.ProviderModelMismatch == nil ||
		current.ProviderModelMismatch.RequestedModel != "requested-model" ||
		current.ProviderModelMismatch.ServedModel != "served-model" {
		t.Fatalf("new hydrated mismatch notice = %+v", current)
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
