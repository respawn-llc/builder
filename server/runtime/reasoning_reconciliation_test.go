package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/transcript"
)

func TestCompletedResponseAbortThenReasoningResetWithoutAssistantStream(t *testing.T) {
	var events []Event
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), Config{
		Model:   "gpt-5",
		OnEvent: func(event Event) { events = append(events, event) },
	})
	const stepID = "reasoning-only-discard"
	outputIndex, partIndex := int64(0), int64(0)
	if err := engine.steer(stepID, steerReasoningDeltaIntent(llm.ReasoningSummaryDelta{
		SourceCoordinate: &llm.ReasoningSourceCoordinate{OutputIndex: &outputIndex, PartIndex: &partIndex},
		Text:             "discard me",
		CurrentStatus:    &llm.ReasoningStatus{Text: "Thinking"},
	})); err != nil {
		t.Fatalf("seed reasoning trace: %v", err)
	}

	outcome, err := engine.resolveCompletedResponseStream(stepID, completedResponseAbortInstruction())
	if err != nil {
		t.Fatalf("discard completed response: %v", err)
	}
	if outcome.kind != completedResponseResolutionDiscarded || outcome.streamID != nil {
		t.Fatalf("discard outcome = %+v, want discarded without stream", outcome)
	}
	if err := (&defaultStepExecutor{engine: engine}).resetProvisionalReasoning(stepID); err != nil {
		t.Fatalf("reset provisional reasoning: %v", err)
	}
	status, traces := engine.transcriptRuntimeState().ReasoningSnapshot()
	if status == nil || status.Text != "Thinking" {
		t.Fatalf("thinking status after discard = %+v, want retained", status)
	}
	if len(traces) != 0 {
		t.Fatalf("reasoning traces after discard = %+v, want empty", traces)
	}
	resetCount := 0
	for _, event := range events {
		if event.Kind == EventReasoningDeltaReset {
			resetCount++
		}
	}
	if resetCount != 1 {
		t.Fatalf("reasoning reset events = %d, want one", resetCount)
	}
}

func TestTranscriptReasoningStateRetainsMetadataWithoutChangingPublicIdentity(t *testing.T) {
	state := newTranscriptRuntimeState("")
	outputIndex, partIndex := int64(0), int64(0)
	coordinate := &llm.ReasoningSourceCoordinate{OutputIndex: &outputIndex, PartIndex: &partIndex}
	if _, err := state.SetReasoningState("step", llm.ReasoningSummaryDelta{
		SourceCoordinate: coordinate,
		Text:             "first",
	}); err != nil {
		t.Fatalf("seed fallback reasoning trace: %v", err)
	}
	fallbackStatus, traces := state.ReasoningSnapshot()
	if fallbackStatus != nil || len(traces) != 1 || traces[0].Identity.Kent == nil {
		t.Fatalf("fallback reasoning state = status:%+v traces:%+v", fallbackStatus, traces)
	}
	providerIndex := int64(0)
	first := &llm.ReasoningItemIdentity{ItemID: "reason_a", PartIndex: &providerIndex}
	if err := state.ObserveReasoningItemIdentity("step", coordinate, first); err != nil {
		t.Fatalf("observe provider metadata: %v", err)
	}
	_, traces = state.ReasoningSnapshot()
	if traces[0].Identity.Kent == nil || traces[0].Identity.Provider != nil ||
		traces[0].ProviderMetadata == nil || traces[0].ProviderMetadata.ItemID != "reason_a" {
		t.Fatalf("reasoning identity after metadata = %+v, want immutable Kent identity plus metadata", traces[0])
	}
	second := &llm.ReasoningItemIdentity{ItemID: "reason_b", PartIndex: &providerIndex}
	if err := state.ObserveReasoningItemIdentity("step", coordinate, second); err == nil {
		t.Fatal("conflicting provider metadata was accepted")
	}
	secondOutput := int64(1)
	secondCoordinate := &llm.ReasoningSourceCoordinate{OutputIndex: &secondOutput, PartIndex: &partIndex}
	if _, err := state.SetReasoningState("step", llm.ReasoningSummaryDelta{
		SourceCoordinate: secondCoordinate,
		ItemIdentity:     first,
		Text:             "second trace",
	}); err == nil {
		t.Fatal("provider identity alias across coordinates was accepted")
	}
}

func TestReconcileReasoningRejectsInvalidCoordinateAndConsumesCommittedTrace(t *testing.T) {
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	executor := &defaultStepExecutor{engine: engine}
	invalidOutput, validPart := int64(-1), int64(0)
	if err := executor.reconcileReasoning("step", []llm.ReasoningEntry{{
		Role:             textPointer(string(transcript.EntryRoleReasoning)),
		Text:             "invalid",
		SourceCoordinate: &llm.ReasoningSourceCoordinate{OutputIndex: &invalidOutput, PartIndex: &validPart},
	}}); err == nil {
		t.Fatal("invalid completed coordinate was accepted")
	}

	outputIndex, partIndex := int64(0), int64(0)
	coordinate := &llm.ReasoningSourceCoordinate{OutputIndex: &outputIndex, PartIndex: &partIndex}
	if err := engine.steer("step", steerReasoningDeltaIntent(llm.ReasoningSummaryDelta{
		SourceCoordinate: coordinate,
		Text:             "provisional",
	})); err != nil {
		t.Fatalf("seed provisional reasoning: %v", err)
	}
	if err := executor.reconcileReasoning("step", []llm.ReasoningEntry{{
		Role:             textPointer(string(transcript.EntryRoleReasoning)),
		Text:             "completed",
		SourceCoordinate: coordinate,
	}}); err != nil {
		t.Fatalf("reconcile completed reasoning: %v", err)
	}
	_, traces := engine.transcriptRuntimeState().ReasoningSnapshot()
	if len(traces) != 0 {
		t.Fatalf("reasoning traces after committed reconciliation = %+v, want empty", traces)
	}
}

func TestReconcileReasoningConsumesTraceAfterCommittedObserverError(t *testing.T) {
	observerErr := errors.New("reasoning observer failed")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	executor := &defaultStepExecutor{engine: engine}
	outputIndex, partIndex := int64(0), int64(0)
	coordinate := &llm.ReasoningSourceCoordinate{OutputIndex: &outputIndex, PartIndex: &partIndex}
	if err := engine.steer("step", steerReasoningDeltaIntent(llm.ReasoningSummaryDelta{
		SourceCoordinate: coordinate,
		Text:             "provisional",
	})); err != nil {
		t.Fatalf("seed provisional reasoning: %v", err)
	}
	gate.FailNext(observerErr)
	err := executor.reconcileReasoning("step", []llm.ReasoningEntry{{
		Role:             textPointer(string(transcript.EntryRoleReasoning)),
		Text:             "completed",
		SourceCoordinate: coordinate,
	}})
	if !errors.Is(err, observerErr) {
		t.Fatalf("reconciliation error = %v, want observer error", err)
	}
	_, traces := engine.transcriptRuntimeState().ReasoningSnapshot()
	if len(traces) != 0 {
		t.Fatalf("reasoning traces after committed observer error = %+v, want consumed", traces)
	}
}

func TestReconcileReasoningRejectsCompletedIdentityConflictWithStream(t *testing.T) {
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	executor := &defaultStepExecutor{engine: engine}
	outputIndex, partIndex := int64(0), int64(0)
	coordinate := &llm.ReasoningSourceCoordinate{OutputIndex: &outputIndex, PartIndex: &partIndex}
	streamIdentity := &llm.ReasoningItemIdentity{ItemID: "reason_stream", PartIndex: &partIndex}
	if err := engine.steer("step", steerReasoningDeltaIntent(llm.ReasoningSummaryDelta{
		SourceCoordinate: coordinate,
		ItemIdentity:     streamIdentity,
		Text:             "streamed",
	})); err != nil {
		t.Fatalf("seed streamed reasoning: %v", err)
	}
	completedIdentity := &llm.ReasoningItemIdentity{ItemID: "reason_completed", PartIndex: &partIndex}
	err := executor.reconcileReasoning("step", []llm.ReasoningEntry{{
		Role:             textPointer(string(transcript.EntryRoleReasoning)),
		Text:             "completed",
		SourceCoordinate: coordinate,
		ItemIdentity:     completedIdentity,
	}})
	if err == nil {
		t.Fatal("completed identity conflict was accepted")
	}
	_, traces := engine.transcriptRuntimeState().ReasoningSnapshot()
	if len(traces) != 1 {
		t.Fatalf("reasoning traces after identity conflict = %+v, want retained", traces)
	}
}

func TestReconcileReasoningKeepsTraceWhenCommitIsNotDurable(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	executor := &defaultStepExecutor{engine: engine}
	outputIndex, partIndex := int64(0), int64(0)
	coordinate := &llm.ReasoningSourceCoordinate{OutputIndex: &outputIndex, PartIndex: &partIndex}
	if err := engine.steer("step", steerReasoningDeltaIntent(llm.ReasoningSummaryDelta{
		SourceCoordinate: coordinate,
		Text:             "provisional",
	})); err != nil {
		t.Fatalf("seed provisional reasoning: %v", err)
	}
	mustBlockTestEventLogAppends(t, store)
	err := executor.reconcileReasoning("step", []llm.ReasoningEntry{{
		Role:             textPointer(string(transcript.EntryRoleReasoning)),
		Text:             "completed",
		SourceCoordinate: coordinate,
	}})
	if err == nil {
		t.Fatal("uncommitted reasoning persistence was accepted")
	}
	_, traces := engine.transcriptRuntimeState().ReasoningSnapshot()
	if len(traces) != 1 {
		t.Fatalf("reasoning traces after uncommitted persistence = %+v, want retained", traces)
	}
}

func TestReconcileReasoningPersistsValidUnprovisionedCoordinateAsCompletedOnly(t *testing.T) {
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	executor := &defaultStepExecutor{engine: engine}
	outputIndex, partIndex := int64(8), int64(2)
	if err := executor.reconcileReasoning("step", []llm.ReasoningEntry{{
		Role: textPointer(string(transcript.EntryRoleReasoning)),
		Text: "completed only",
		SourceCoordinate: &llm.ReasoningSourceCoordinate{
			OutputIndex: &outputIndex,
			PartIndex:   &partIndex,
		},
	}}); err != nil {
		t.Fatalf("reconcile completed-only coordinate: %v", err)
	}
	hydration := hydrationSnapshot(t, engine)
	if len(hydration.CommittedRows) == 0 || hydration.CommittedRows[len(hydration.CommittedRows)-1].Kind != TranscriptCommittedRowFactReasoningTrace ||
		hydration.CommittedRows[len(hydration.CommittedRows)-1].ReasoningTrace.ProvisionalIdentity != nil {
		t.Fatalf("completed-only hydration rows = %+v", hydration.CommittedRows)
	}
}

func TestReconcileReasoningUsesFirstSeenProvisionalOrder(t *testing.T) {
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	executor := &defaultStepExecutor{engine: engine}
	firstOutput, secondOutput, part := int64(9), int64(1), int64(0)
	first := &llm.ReasoningSourceCoordinate{OutputIndex: &firstOutput, PartIndex: &part}
	second := &llm.ReasoningSourceCoordinate{OutputIndex: &secondOutput, PartIndex: &part}
	for _, item := range []llm.ReasoningSummaryDelta{
		{SourceCoordinate: first, Text: "first"},
		{SourceCoordinate: second, Text: "second"},
	} {
		if err := engine.steer("step", steerReasoningDeltaIntent(item)); err != nil {
			t.Fatalf("seed provisional reasoning: %v", err)
		}
	}
	if err := executor.reconcileReasoning("step", []llm.ReasoningEntry{
		{Role: textPointer(string(transcript.EntryRoleReasoning)), Text: "first", SourceCoordinate: first},
		{Role: textPointer(string(transcript.EntryRoleReasoning)), Text: "second", SourceCoordinate: second},
	}); err != nil {
		t.Fatalf("reconcile first-seen order: %v", err)
	}

	engine = mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	executor = &defaultStepExecutor{engine: engine}
	for _, item := range []llm.ReasoningSummaryDelta{
		{SourceCoordinate: first, Text: "first"},
		{SourceCoordinate: second, Text: "second"},
	} {
		if err := engine.steer("step", steerReasoningDeltaIntent(item)); err != nil {
			t.Fatalf("seed reordered reasoning: %v", err)
		}
	}
	if err := executor.reconcileReasoning("step", []llm.ReasoningEntry{
		{Role: textPointer(string(transcript.EntryRoleReasoning)), Text: "second", SourceCoordinate: second},
		{Role: textPointer(string(transcript.EntryRoleReasoning)), Text: "first", SourceCoordinate: first},
	}); err == nil {
		t.Fatal("reordered provisional traces were accepted")
	}
}

func TestReconcileReasoningRejectsMalformedCompletedOnlyIdentity(t *testing.T) {
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	executor := &defaultStepExecutor{engine: engine}
	if err := executor.reconcileReasoning("step", []llm.ReasoningEntry{{
		Role:         textPointer(string(transcript.EntryRoleReasoning)),
		Text:         "malformed",
		ItemIdentity: &llm.ReasoningItemIdentity{ItemID: "reason_1"},
	}}); err == nil {
		t.Fatal("malformed completed-only identity was accepted")
	}
}

func TestRunStepLoopResolvesCompletedOnlyReasoningAtBoundary(t *testing.T) {
	outputIndex, partIndex := int64(8), int64(2)
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Phase:   textutil.Value(llm.MessagePhaseFinal),
			Content: textutil.Value("done"),
		},
		Reasoning: []llm.ReasoningEntry{{
			Role: textPointer(string(transcript.EntryRoleReasoning)), Text: "completed",
			SourceCoordinate: &llm.ReasoningSourceCoordinate{OutputIndex: &outputIndex, PartIndex: &partIndex},
		}},
		Usage: llm.Usage{WindowTokens: 200_000},
	}}}
	engine := mustNewExecTestEngine(t, mustCreateTestSession(t), client, Config{Model: "gpt-5"})
	if _, err := engine.SubmitUserMessage(context.Background(), "turn"); err != nil {
		t.Fatalf("submit completed-only reasoning turn: %v", err)
	}
	hydration := hydrationSnapshot(t, engine)
	found := false
	for _, row := range hydration.CommittedRows {
		if row.Kind == TranscriptCommittedRowFactReasoningTrace && row.ReasoningTrace != nil &&
			row.ReasoningTrace.Text == "completed" &&
			row.ReasoningTrace.ProvisionalIdentity == nil {
			found = true
		}
	}
	if !found {
		t.Fatalf("completed-only reasoning row missing from hydration: %+v", hydration.CommittedRows)
	}
}

func TestRunStepLoopNoopAcceptanceCommitsReasoning(t *testing.T) {
	outputIndex, partIndex := int64(0), int64(0)
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Phase:   textutil.Value(llm.MessagePhaseFinal),
			Content: textutil.Value(transcript.NoopFinalToken),
		},
		Reasoning: []llm.ReasoningEntry{{
			Role: textPointer(string(transcript.EntryRoleReasoning)), Text: "noop reasoning",
			SourceCoordinate: &llm.ReasoningSourceCoordinate{OutputIndex: &outputIndex, PartIndex: &partIndex},
		}},
		Usage: llm.Usage{WindowTokens: 200_000},
	}}}
	engine := mustNewExecTestEngine(t, mustCreateTestSession(t), client, Config{Model: "gpt-5"})
	if _, err := engine.SubmitUserMessage(context.Background(), "turn"); err != nil {
		t.Fatalf("submit noop reasoning turn: %v", err)
	}
	hydration := hydrationSnapshot(t, engine)
	for _, row := range hydration.CommittedRows {
		if row.Kind == TranscriptCommittedRowFactReasoningTrace && row.ReasoningTrace != nil &&
			row.ReasoningTrace.Text == "noop reasoning" {
			return
		}
	}
	t.Fatalf("noop reasoning row missing from hydration: %+v", hydration.CommittedRows)
}

func textPointer(value string) *string {
	return &value
}
