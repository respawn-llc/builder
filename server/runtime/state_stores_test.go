package runtime

import (
	"testing"

	"core/server/llm"
	"core/server/session"
	compaction "core/shared/config"
)

func TestQueuedUserMessageStoreQueuesDiscardsAndDrainsByID(t *testing.T) {
	store := newQueuedUserMessageStore()
	first := store.Queue("same")
	store.Queue("other")
	duplicate := store.Queue("same")

	if !store.Discard(duplicate.ID) {
		t.Fatal("expected duplicate id to be discarded")
	}

	snapshot := store.Snapshot()
	if len(snapshot) != 2 || snapshot[0].ID != first.ID || snapshot[0].Text != "same" || snapshot[1].Text != "other" {
		t.Fatalf("unexpected snapshot after discard: %+v", snapshot)
	}
	if !store.HasPending() {
		t.Fatal("expected pending messages")
	}
	drained := store.Drain()
	if len(drained) != 2 || store.HasPending() {
		t.Fatalf("unexpected drain result=%+v hasPending=%v", drained, store.HasPending())
	}
}

func TestDiagnosticDedupeStoreTracksLocalPersistedAndReset(t *testing.T) {
	store := newDiagnosticDedupeStore()
	if !store.BeginLocal(" precise-token-count ") {
		t.Fatal("expected first local diagnostic to start")
	}
	if store.BeginLocal("precise-token-count") {
		t.Fatal("expected duplicate local diagnostic to be rejected")
	}
	if store.HasPersisted("precise-token-count") {
		t.Fatal("local-only diagnostic should not be persisted")
	}

	store.RestoreLocal("precise-token-count")
	if !store.HasPersisted("precise-token-count") {
		t.Fatal("restored diagnostic should be marked persisted")
	}

	store.Reset()
	if !store.BeginLocal("precise-token-count") {
		t.Fatal("reset should clear local dedupe")
	}
	if store.HasPersisted("precise-token-count") {
		t.Fatal("reset should clear persisted dedupe")
	}
}

func TestPendingToolCallStartStoreRemembersLooksUpAndForgets(t *testing.T) {
	store := newPendingToolCallStartStore()
	store.Remember(map[string]int{"call-1": 4, "": 99})

	start, ok := store.Lookup("call-1")
	if !ok || start != 4 {
		t.Fatalf("Lookup(call-1)=(%d,%v), want (4,true)", start, ok)
	}
	if _, ok := store.Lookup(""); ok {
		t.Fatal("empty call id should not be present")
	}
	if got := store.Len(); got != 1 {
		t.Fatalf("Len()=%d, want 1", got)
	}

	store.Forget("call-1")
	if got := store.Len(); got != 0 {
		t.Fatalf("Len() after forget=%d, want 0", got)
	}
}

func TestUsageTrackingStateNormalizesAndTracksCacheHit(t *testing.T) {
	state := newUsageTrackingState()
	normalized, totalInput, totalCached := state.Next(llm.Usage{
		InputTokens:          100,
		CachedInputTokens:    150,
		HasCachedInputTokens: true,
	})
	if normalized.CachedInputTokens != 100 || totalInput != 100 || totalCached != 100 {
		t.Fatalf("first Next normalized=%+v totalInput=%d totalCached=%d", normalized, totalInput, totalCached)
	}
	state.Apply(normalized, totalInput, totalCached)

	normalized, totalInput, totalCached = state.Next(llm.Usage{
		InputTokens:          300,
		CachedInputTokens:    60,
		HasCachedInputTokens: true,
	})
	if totalInput != 400 || totalCached != 160 {
		t.Fatalf("second totals input=%d cached=%d, want 400/160", totalInput, totalCached)
	}
	state.Apply(normalized, totalInput, totalCached)

	pct, ok := state.CacheHitSnapshot()
	if !ok || pct != 40 {
		t.Fatalf("CacheHitSnapshot()=(%d,%v), want (40,true)", pct, ok)
	}
	if last := state.Last(); last.InputTokens != 300 || last.CachedInputTokens != 60 {
		t.Fatalf("Last()=%+v, want second usage", last)
	}
}

func TestGoalLoopStateStartSuspendResumeFinish(t *testing.T) {
	state := newGoalLoopState()
	if !state.Start() {
		t.Fatal("first Start should acquire running state")
	}
	if state.Start() {
		t.Fatal("second Start should not acquire running state")
	}
	state.Suspend()
	if !state.Suspended() || !state.Running() {
		t.Fatalf("state after suspend suspended=%v running=%v, want true/true", state.Suspended(), state.Running())
	}
	state.Resume()
	if state.Suspended() {
		t.Fatal("Resume should clear suspended")
	}
	state.Finish(false)
	if state.Running() {
		t.Fatal("Finish should clear running")
	}
}

func TestGoalLoopStatePendingInterruptRequiresRestart(t *testing.T) {
	state := newGoalLoopState()
	if !state.Start() {
		t.Fatal("Start should acquire running state")
	}
	state.MarkInterruptPending()

	if state.ContinuationEnforced() {
		t.Fatal("pending interrupt must make continuation unenforced")
	}
	if !state.RestartNeeded() {
		t.Fatal("pending interrupt must require restart")
	}
	if state.Start() {
		t.Fatal("Start during pending interrupt should not launch a duplicate loop")
	}
	if !state.ContinuationEnforced() {
		t.Fatal("Start during pending interrupt should mark restart pending")
	}
	if !state.Finish(true) {
		t.Fatal("Finish after restart pending should request relaunch")
	}
}

func TestGoalLoopStateCommitInterruptPreservesQueuedRestart(t *testing.T) {
	state := newGoalLoopState()
	if !state.Start() {
		t.Fatal("Start should acquire running state")
	}
	state.MarkInterruptPending()
	if state.Start() {
		t.Fatal("Start during pending interrupt should not launch immediately")
	}
	state.CommitInterrupt()

	if !state.ContinuationEnforced() {
		t.Fatal("committed interrupt with queued restart should remain enforced by restart pending")
	}
	if !state.Finish(true) {
		t.Fatal("Finish should relaunch queued restart after committed interrupt")
	}
}

func TestGoalLoopStateCommitInterruptWithoutRestartSuspends(t *testing.T) {
	state := newGoalLoopState()
	if !state.Start() {
		t.Fatal("Start should acquire running state")
	}
	state.MarkInterruptPending()
	state.CommitInterrupt()

	if !state.Suspended() {
		t.Fatal("committed interrupt without queued restart should suspend")
	}
	if state.ContinuationEnforced() {
		t.Fatal("suspended loop must not report continuation enforced")
	}
}

func TestCompactionRuntimeStateTracksCountAndReminder(t *testing.T) {
	state := newCompactionRuntimeState()
	if got := state.IncrementCount(); got != 1 {
		t.Fatalf("IncrementCount()=%d, want 1", got)
	}
	state.SetCount(-10)
	if got := state.Count(); got != 0 {
		t.Fatalf("negative SetCount should clamp to 0, got %d", got)
	}
	state.SetSoonReminderIssued(true)
	if !state.SoonReminderIssued() {
		t.Fatal("expected reminder flag to be issued")
	}
	state.SetSoonReminderIssued(false)
	if state.SoonReminderIssued() {
		t.Fatal("expected reminder flag to be cleared")
	}
}

func TestCompactionPlannerDerivesLimitsFromSnapshot(t *testing.T) {
	planner := newCompactionPlanner()
	snapshot := compactionPlanningSnapshot{
		autoCompactionEnabled:         true,
		compactionMode:                "bogus",
		preSubmitCompactionLeadTokens: 35_000,
		contextWindowTokens:           1_000_000,
		effectiveContextWindowPercent: 90,
		maxOutputTokens:               4_000,
		lockedMaxOutputTokens:         8_000,
	}

	if got := planner.mode(snapshot.compactionMode); got != "native" {
		t.Fatalf("mode()=%q, want native", got)
	}
	if !planner.autoCompactionAvailable(snapshot) {
		t.Fatal("auto compaction should be available for enabled native mode")
	}
	if got := planner.contextWindowTokens(snapshot); got != 1_000_000 {
		t.Fatalf("contextWindowTokens()=%d, want 1000000", got)
	}
	if got := planner.effectiveContextTokenLimit(snapshot); got != 900_000 {
		t.Fatalf("effectiveContextTokenLimit()=%d, want 900000", got)
	}
	if got := planner.autoCompactTokenLimit(snapshot); got != 900_000 {
		t.Fatalf("autoCompactTokenLimit()=%d, want 900000", got)
	}
	if got := planner.preSubmitTokenLimit(snapshot); got != 865_000 {
		t.Fatalf("preSubmitTokenLimit()=%d, want 865000", got)
	}
	if got := planner.soonReminderLimit(snapshot); got != 765_000 {
		t.Fatalf("soonReminderLimit()=%d, want 765000", got)
	}
	// Current usage sits at the soon-reminder threshold (765000). The forced gate reserves 8000 output
	// tokens, so the runway is 900000 - 8000 - 765000 = 127000 tokens; 127000 / 1400 rounds to 91
	// estimated tool calls.
	snapshot.currentUsedTokens = 765_000
	if got := planner.estimatedToolCallsUntilForcedHandoff(snapshot); got != 91 {
		t.Fatalf("estimatedToolCallsUntilForcedHandoff()=%d, want 91", got)
	}
	if got := planner.reservedOutputTokens(snapshot); got != 8_000 {
		t.Fatalf("reservedOutputTokens()=%d, want locked max output", got)
	}
}

func TestEstimatedToolCallsUsesRemainingBudgetNearForcedLimit(t *testing.T) {
	planner := newCompactionPlanner()
	snapshot := compactionPlanningSnapshot{
		autoCompactTokenLimit: 900_000,
		// 880000 of live usage leaves 20000 before the forced limit, which rounds to 14 tool calls.
		currentUsedTokens: 880_000,
	}
	if got := planner.estimatedToolCallsUntilForcedHandoff(snapshot); got != 14 {
		t.Fatalf("estimatedToolCallsUntilForcedHandoff()=%d, want 14", got)
	}
}

func TestEstimatedToolCallsSubtractsReservedOutputFromRunway(t *testing.T) {
	planner := newCompactionPlanner()
	snapshot := compactionPlanningSnapshot{
		autoCompactTokenLimit: 900_000,
		lockedMaxOutputTokens: 8_000,
		currentUsedTokens:     891_000,
	}
	// The forced gate compares input + reservedOutput (891000 + 8000 = 899000) against 900000, so only
	// ~1000 input tokens remain; subtracting the reservation yields 1 tool call instead of the ~6 a raw
	// 900000 - 891000 = 9000 runway would overstate.
	if got := planner.estimatedToolCallsUntilForcedHandoff(snapshot); got != 1 {
		t.Fatalf("estimatedToolCallsUntilForcedHandoff()=%d, want 1", got)
	}
}

func TestEstimatedToolCallsPanicsWhenReservedOutputReachesForcedLimit(t *testing.T) {
	planner := newCompactionPlanner()
	snapshot := compactionPlanningSnapshot{
		autoCompactTokenLimit: 900_000,
		lockedMaxOutputTokens: 8_000,
		// 895000 input alone is below the forced limit, but the forced gate adds the 8000 reserved output
		// (903000 >= 900000) and would already have compacted, so reaching the estimate is unreachable.
		currentUsedTokens: 895_000,
	}
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic when consumed tokens plus reserved output reach the forced limit")
		}
	}()
	planner.estimatedToolCallsUntilForcedHandoff(snapshot)
}

func TestEstimatedToolCallsPanicsWhenUsageReachesForcedLimit(t *testing.T) {
	planner := newCompactionPlanner()
	snapshot := compactionPlanningSnapshot{
		autoCompactTokenLimit: 900_000,
		// 950000 of live usage is past the forced limit. The reminder is gated behind the same usage
		// check that triggers forced compaction, so this state is unreachable in production and must
		// fail loudly rather than report a clamped runway.
		currentUsedTokens: 950_000,
	}
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic when consumed tokens reach the forced compaction limit")
		}
	}()
	planner.estimatedToolCallsUntilForcedHandoff(snapshot)
}

func TestCompactionPlannerAppliesFallbacksAndDisableModes(t *testing.T) {
	planner := newCompactionPlanner()
	snapshot := compactionPlanningSnapshot{
		autoCompactionEnabled:         true,
		compactionMode:                "none",
		preSubmitCompactionLeadTokens: -1,
		effectiveContextWindowPercent: 101,
		lastUsage:                     llm.Usage{WindowTokens: 2_000},
	}

	if planner.autoCompactionAvailable(snapshot) {
		t.Fatal("mode=none should disable auto compaction availability")
	}
	if got := planner.contextWindowTokens(snapshot); got != 2_000 {
		t.Fatalf("contextWindowTokens()=%d, want last usage window", got)
	}
	if got := planner.effectiveContextTokenLimit(snapshot); got != 1_900 {
		t.Fatalf("effectiveContextTokenLimit()=%d, want fallback 95%% limit", got)
	}
	if got := planner.preSubmitTokenLimit(snapshot); got != compaction.EffectivePreSubmitThresholdTokens(1_900, compaction.DefaultPreSubmitRunwayTokens) {
		t.Fatalf("preSubmitTokenLimit()=%d, want default runway threshold", got)
	}
	if got := planner.soonReminderLimit(compactionPlanningSnapshot{autoCompactTokenLimit: 1}); got != 1 {
		t.Fatalf("soonReminderLimit()=%d, want minimum 1", got)
	}

	disabled := snapshot
	disabled.autoCompactionEnabled = false
	disabled.compactionMode = "native"
	if planner.autoCompactionAvailable(disabled) {
		t.Fatal("explicit auto compaction disable should make auto compaction unavailable")
	}
}

func TestCompactionPlannerSelectsExecutionEngine(t *testing.T) {
	planner := newCompactionPlanner()

	remote := planner.enginePlan(compactionPlanningSnapshot{compactionMode: "native"}, llm.ProviderCapabilities{SupportsResponsesCompact: true})
	if remote.engineKind != compactionEngineRemote || !remote.fallbackToLocalOnBadCheckpoint {
		t.Fatalf("native compact-capable plan = %+v, want remote with checkpoint fallback", remote)
	}

	local := planner.enginePlan(compactionPlanningSnapshot{compactionMode: "native"}, llm.ProviderCapabilities{})
	if local.engineKind != compactionEngineLocal || local.fallbackToLocalOnBadCheckpoint {
		t.Fatalf("native non-capable plan = %+v, want local without fallback", local)
	}

	explicitLocal := planner.enginePlan(compactionPlanningSnapshot{compactionMode: "local"}, llm.ProviderCapabilities{SupportsResponsesCompact: true})
	if explicitLocal.engineKind != compactionEngineLocal {
		t.Fatalf("local mode plan = %+v, want local", explicitLocal)
	}

	disabled := planner.enginePlan(compactionPlanningSnapshot{compactionMode: "none"}, llm.ProviderCapabilities{SupportsResponsesCompact: true})
	if disabled.engineKind != compactionEngineNone {
		t.Fatalf("none mode plan = %+v, want disabled", disabled)
	}
}

func TestHandoffRuntimeStateSnapshotsAndClears(t *testing.T) {
	state := newHandoffRuntimeState()
	state.QueueRequest(" keep API details ", " resume later ")

	req := state.RequestSnapshot()
	if req == nil || req.summarizerPrompt != "keep API details" || req.futureAgentMessage != "resume later" {
		t.Fatalf("unexpected request snapshot: %+v", req)
	}
	req.summarizerPrompt = "mutated"
	if got := state.RequestSnapshot().summarizerPrompt; got != "keep API details" {
		t.Fatalf("snapshot mutation leaked into state: %q", got)
	}

	state.QueueFutureMessage(" resume next agent ")
	if got := state.FutureMessageSnapshot(); got != "resume next agent" {
		t.Fatalf("future message = %q, want trimmed value", got)
	}
	state.ClearRequest()
	state.ClearFutureMessage()
	if state.RequestSnapshot() != nil || state.FutureMessageSnapshot() != "" {
		t.Fatalf("expected handoff state cleared, request=%+v future=%q", state.RequestSnapshot(), state.FutureMessageSnapshot())
	}
}

func TestPhaseProtocolStateResolvesOnce(t *testing.T) {
	state := newPhaseProtocolState()
	if enabled, resolved := state.Snapshot(); enabled || resolved {
		t.Fatalf("initial snapshot enabled=%v resolved=%v, want false/false", enabled, resolved)
	}
	if !state.Resolve(true) {
		t.Fatal("first resolve should store true")
	}
	if !state.Resolve(false) {
		t.Fatal("second resolve should preserve first value")
	}
	if enabled, resolved := state.Snapshot(); !enabled || !resolved {
		t.Fatalf("final snapshot enabled=%v resolved=%v, want true/true", enabled, resolved)
	}
}

func TestReviewerRuntimeStatePreservesResumeFrequencyAndInitializesClientOnce(t *testing.T) {
	state := newReviewerRuntimeState(nil)
	state.RecordResumeFrequency("all")
	if got := state.ResumeFrequency("edits"); got != "all" {
		t.Fatalf("ResumeFrequency()=%q, want all", got)
	}

	calls := 0
	client := &fakeClient{}
	factory := func() (llm.Client, error) {
		calls++
		return client, nil
	}
	if err := state.EnsureClient(factory); err != nil {
		t.Fatalf("EnsureClient first: %v", err)
	}
	if err := state.EnsureClient(factory); err != nil {
		t.Fatalf("EnsureClient second: %v", err)
	}
	if calls != 1 || state.Client() != client {
		t.Fatalf("factory calls=%d client=%p, want calls=1 client=%p", calls, state.Client(), client)
	}
}

func TestTranscriptRuntimeStateRejectsEmptyWorkingDir(t *testing.T) {
	state := newTranscriptRuntimeState(" /workspace ")
	if got := state.WorkingDir(); got != "/workspace" {
		t.Fatalf("initial working dir = %q, want /workspace", got)
	}
	if state.SetWorkingDir(" \t ") {
		t.Fatal("empty working dir should be rejected")
	}
	if got := state.WorkingDir(); got != "/workspace" {
		t.Fatalf("working dir after empty set = %q, want unchanged /workspace", got)
	}
	if !state.SetWorkingDir(" /worktree ") {
		t.Fatal("non-empty working dir should be accepted")
	}
	if got := state.WorkingDir(); got != "/worktree" {
		t.Fatalf("working dir after set = %q, want /worktree", got)
	}
}

func TestTranscriptPersistenceCoordinatorOwnsChatMutationTransitions(t *testing.T) {
	state := newTranscriptRuntimeState("/workspace")
	persistence := newTranscriptPersistenceCoordinator(state)
	persistence.AppendMessage(llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeHeadlessMode, Content: "headless mode"})
	persistence.AppendLocalEntryRecord(ChatEntry{Role: "notice", Text: "local note"})
	snap := state.chatProjection().snapshotWithMetadata().Snapshot
	if len(snap.Entries) != 2 || snap.Entries[1].Role != "notice" || snap.Entries[1].Text != "local note" {
		t.Fatalf("unexpected transcript entries after local append: %+v", snap.Entries)
	}

	persistence.ReplaceHistory(llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeCompactionSummary, Content: "summary"}}))
	items := state.SnapshotItems()
	if len(items) != 1 || items[0].MessageType != llm.MessageTypeCompactionSummary {
		t.Fatalf("unexpected provider items after replace history: %+v", items)
	}
}

func TestLockedContractStateSnapshotsAndFillsPrompts(t *testing.T) {
	state := newLockedContractState()
	state.Set(session.LockedContract{Model: " gpt-5 ", MaxOutputToken: 1024})

	if got := state.Model(); got != "gpt-5" {
		t.Fatalf("Model()=%q, want gpt-5", got)
	}
	if got := state.MaxOutputToken(); got != 1024 {
		t.Fatalf("MaxOutputToken()=%d, want 1024", got)
	}
	state.FillSystemPrompt(" system prompt ")
	state.FillReviewerPrompt(" reviewer prompt ")

	locked, ok := state.Snapshot()
	if !ok || !locked.HasSystemPrompt || locked.SystemPrompt != "system prompt" {
		t.Fatalf("system prompt snapshot = %+v ok=%v", locked, ok)
	}
	if prompt, ok := state.ReviewerPromptSnapshot(); !ok || prompt != "reviewer prompt" {
		t.Fatalf("ReviewerPromptSnapshot()=(%q,%v), want reviewer prompt/true", prompt, ok)
	}

	locked.SystemPrompt = "mutated"
	again, _ := state.Snapshot()
	if again.SystemPrompt != "system prompt" {
		t.Fatalf("snapshot mutation leaked into state: %+v", again)
	}
}

func TestModelRequestRuntimeStateInitializesTrackers(t *testing.T) {
	state := newModelRequestRuntimeState()
	if state.TokenUsage() == nil {
		t.Fatal("expected token usage tracker")
	}
	if state.RequestCache() == nil {
		t.Fatal("expected request cache tracker")
	}
	if state.TokenUsage() != state.TokenUsage() {
		t.Fatal("expected stable token usage tracker pointer")
	}
	if state.RequestCache() != state.RequestCache() {
		t.Fatal("expected stable request cache tracker pointer")
	}
}
