package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"core/server/llm"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/toolspec"
)

func TestPreciseRefreshCheckpointsFollowThresholdBands(t *testing.T) {
	threshold := 300
	if got := firstPreciseRefreshCheckpoint(threshold); got != 150 {
		t.Fatalf("first checkpoint=%d, want 150", got)
	}
	if got := preciseRefreshAlwaysThreshold(threshold); got != 270 {
		t.Fatalf("always-precise threshold=%d, want 270", got)
	}
	for _, tc := range []struct {
		last int
		want int
	}{
		{last: 50, want: 150},
		{last: 150, want: 190},
		{last: 190, want: 216},
		{last: 216, want: 234},
		{last: 240, want: 250},
		{last: 269, want: 270},
	} {
		if got := nextPreciseRefreshCheckpoint(tc.last, threshold, nil); got != tc.want {
			t.Fatalf("next checkpoint after %d = %d, want %d", tc.last, got, tc.want)
		}
	}
}

func TestPreciseRefreshCheckpointsUseRecentSignificantGrowth(t *testing.T) {
	threshold := 300
	recentGrowth := []int{6, 6, 6, 6}
	if got := nextPreciseRefreshCheckpoint(150, threshold, recentGrowth); got != 168 {
		t.Fatalf("next checkpoint with significant growth=%d, want 168", got)
	}
	if got := nextPreciseRefreshCheckpoint(240, threshold, recentGrowth); got != 255 {
		t.Fatalf("next checkpoint near always-precise band=%d, want 255", got)
	}
}

func TestTokenUsageTrackerUsesFallbackScheduleAcrossPlainInvalidation(t *testing.T) {
	tracker := newTokenUsageTracker()
	threshold := 300
	if tracker.currentCheckpointDue(149, threshold, false) {
		t.Fatal("did not expect refresh below first checkpoint")
	}
	if !tracker.currentCheckpointDue(150, threshold, false) {
		t.Fatal("expected refresh at first checkpoint")
	}
	tracker.store("req-1", 150, true)
	tracker.invalidateCurrent(tokenUsageMutationPlain)
	if tracker.currentCheckpointDue(189, threshold, false) {
		t.Fatal("did not expect refresh before second checkpoint")
	}
	if !tracker.currentCheckpointDue(190, threshold, false) {
		t.Fatal("expected refresh at second checkpoint after invalidation")
	}
	tracker.store("req-2", 190, true)
	tracker.invalidateCurrent(tokenUsageMutationPlain)
	if tracker.currentCheckpointDue(215, threshold, false) {
		t.Fatal("did not expect refresh before third fallback checkpoint")
	}
	if !tracker.currentCheckpointDue(216, threshold, false) {
		t.Fatal("expected refresh at third fallback checkpoint")
	}
	tracker.store("req-3", 216, true)
	tracker.invalidateCurrent(tokenUsageMutationPlain)
	if !tracker.currentCheckpointDue(270, threshold, false) {
		t.Fatal("expected every request at or above 90% to require precise counting")
	}
}

func TestTokenUsageTrackerForcesCriticalRefreshAfterSignificantMutation(t *testing.T) {
	tracker := newTokenUsageTracker()
	threshold := 300
	tracker.store("req-1", 120, true)
	tracker.invalidateCurrent(tokenUsageMutationSignificant)
	if tracker.currentCheckpointDue(121, threshold, false) {
		t.Fatal("did not expect scheduled refresh below 50% without a critical operation")
	}
	if !tracker.currentCheckpointDue(121, threshold, true) {
		t.Fatal("expected critical path to force an exact recount after significant mutation")
	}
	tracker.store("req-2", 138, true)
	if tracker.forceCurrentPreciseCheck {
		t.Fatal("expected successful exact recount to clear forced refresh flag")
	}
	if len(tracker.recentSignificantGrowth) != 1 || tracker.recentSignificantGrowth[0] != 18 {
		t.Fatalf("recent significant growth=%v, want [18]", tracker.recentSignificantGrowth)
	}
}

func TestTokenUsageTrackerHardResetClearsAdaptiveGrowth(t *testing.T) {
	tracker := newTokenUsageTracker()
	tracker.storeUsageBaseline(120, 100)
	tracker.store("req-1", 150, true)
	tracker.invalidateCurrent(tokenUsageMutationSignificant)
	tracker.store("req-2", 168, true)
	tracker.invalidateCurrent(tokenUsageMutationHardReset)
	if tracker.lastPreciseInputTokens != 0 {
		t.Fatalf("last precise input tokens=%d, want 0 after hard reset", tracker.lastPreciseInputTokens)
	}
	if tracker.forceCurrentPreciseCheck {
		t.Fatal("expected hard reset to clear forced precise refresh state")
	}
	if len(tracker.recentSignificantGrowth) != 0 {
		t.Fatalf("recent significant growth=%v, want cleared history", tracker.recentSignificantGrowth)
	}
	if estimated, ok := tracker.estimateCurrentInputTokens(140); !ok || estimated != 140 {
		t.Fatalf("expected hard reset to fall back to fresh estimate, got (%d, %v)", estimated, ok)
	}
}

func TestTokenUsageTrackerEstimatesCurrentInputTokensFromUsageBaselineDelta(t *testing.T) {
	tracker := newTokenUsageTracker()
	tracker.storeUsageBaseline(900, 180)

	if estimated, ok := tracker.estimateCurrentInputTokens(150); !ok || estimated != 900 {
		t.Fatalf("estimate below checkpoint = (%d, %v), want (900, true)", estimated, ok)
	}
	if estimated, ok := tracker.estimateCurrentInputTokens(240); !ok || estimated != 960 {
		t.Fatalf("estimate above checkpoint = (%d, %v), want (960, true)", estimated, ok)
	}
}

func TestCurrentInputTokensPreciselyRechecksAfterTranscriptMutation(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &preciseCompactionClient{inputTokenCount: 240, contextWindow: 400000}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", ContextWindowTokens: 400_000})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "hello"}})); err != nil {
		t.Fatalf("append message: %v", err)
	}

	if precise, ok := eng.currentInputTokensPrecisely(context.Background()); !ok || precise != 240 {
		t.Fatalf("first precise count = (%d, %v), want (240, true)", precise, ok)
	}
	if client.countCalls != 1 {
		t.Fatalf("count calls=%d, want 1", client.countCalls)
	}
	if precise, ok := eng.currentInputTokensPrecisely(context.Background()); !ok || precise != 240 {
		t.Fatalf("cached precise count = (%d, %v), want (240, true)", precise, ok)
	}
	if client.countCalls != 1 {
		t.Fatalf("expected unchanged request to reuse precise cache, got %d calls", client.countCalls)
	}

	client.inputTokenCount = 360
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, Content: "world"}})); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}
	if precise, ok := eng.currentInputTokensPrecisely(context.Background()); !ok || precise != 360 {
		t.Fatalf("second precise count = (%d, %v), want (360, true)", precise, ok)
	}
	if client.countCalls != 2 {
		t.Fatalf("expected transcript mutation to force a new precise count, got %d calls", client.countCalls)
	}
}

func TestContextUsagePrefersFreshPreciseCurrentTokens(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &preciseCompactionClient{inputTokenCount: 180, contextWindow: 400000}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", ContextWindowTokens: 400_000})
	eng.setLastUsage(llm.Usage{InputTokens: 900, OutputTokens: 100, WindowTokens: 400_000})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "precise me"}})); err != nil {
		t.Fatalf("append message: %v", err)
	}

	if _, ok := eng.currentInputTokensPrecisely(context.Background()); !ok {
		t.Fatal("expected exact token count to succeed")
	}
	usage := eng.ContextUsage()
	if usage.UsedTokens != 180 {
		t.Fatalf("used tokens=%d, want exact 180", usage.UsedTokens)
	}
}

func TestCurrentInputTokensPreciselyRechecksAfterFastModeToggle(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &preciseCompactionClient{inputTokenCount: 180, contextWindow: 400000}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", ContextWindowTokens: 400_000})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "hello"}})); err != nil {
		t.Fatalf("append message: %v", err)
	}
	if _, ok := eng.currentInputTokensPrecisely(context.Background()); !ok {
		t.Fatal("expected first exact token count to succeed")
	}
	if client.countCalls != 1 {
		t.Fatalf("count calls=%d, want 1", client.countCalls)
	}
	changed, err := eng.SetFastModeEnabled(true)
	if err != nil {
		t.Fatalf("enable fast mode: %v", err)
	}
	if !changed {
		t.Fatal("expected fast mode toggle to report changed")
	}
	client.inputTokenCount = 220
	if precise, ok := eng.currentInputTokensPrecisely(context.Background()); !ok || precise != 220 {
		t.Fatalf("post-toggle precise count = (%d, %v), want (220, true)", precise, ok)
	}
	if client.countCalls != 2 {
		t.Fatalf("expected request-shape toggle to force recount, got %d calls", client.countCalls)
	}
}

func TestCurrentInputTokensPreciselyIfDueSkipsBackendFarBelowCheckpoint(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &preciseCompactionClient{inputTokenCount: 999, contextWindow: 400000}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", ContextWindowTokens: 400_000})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "short"}})); err != nil {
		t.Fatalf("append message: %v", err)
	}

	if precise, ok := eng.currentInputTokensPreciselyIfDueWithPriority(context.Background(), 100_000, false); ok || precise != 0 {
		t.Fatalf("currentInputTokensPreciselyIfDue = (%d, %v), want no refresh", precise, ok)
	}
	if client.countCalls != 0 {
		t.Fatalf("expected no backend count below checkpoint, got %d calls", client.countCalls)
	}
}

func TestCurrentInputTokensPreciselyIfCriticalForcesRefreshAfterSignificantMutation(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &preciseCompactionClient{inputTokenCount: 180, contextWindow: 400000}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", ContextWindowTokens: 400_000})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "hello"}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if _, ok := eng.currentInputTokensPrecisely(context.Background()); !ok {
		t.Fatal("expected initial exact token count to succeed")
	}
	if client.countCalls != 1 {
		t.Fatalf("count calls=%d, want 1", client.countCalls)
	}
	client.inputTokenCount = 220
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, Content: "background shell completed"}})); err != nil {
		t.Fatalf("append developer mutation: %v", err)
	}
	if precise, ok := eng.currentInputTokensPreciselyIfDueWithPriority(context.Background(), 1_000, false); ok || precise != 0 {
		t.Fatalf("scheduled exact recount = (%d, %v), want no refresh below 50%%", precise, ok)
	}
	if precise, ok := eng.currentInputTokensPreciselyIfDueWithPriority(context.Background(), 1_000, true); !ok || precise != 220 {
		t.Fatalf("critical exact recount = (%d, %v), want (220, true)", precise, ok)
	}
	if client.countCalls != 2 {
		t.Fatalf("expected significant provider-visible mutation to force a critical recount, got %d calls", client.countCalls)
	}
}

func TestCurrentInputTokensPreciselyPersistsTranscriptErrorOnceOnCountFailure(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &preciseCompactionClient{countErr: errors.New("chatgpt-codex status 404"), contextWindow: 400000}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", ContextWindowTokens: 400_000})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "hello"}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if precise, ok := eng.currentInputTokensPrecisely(context.Background()); ok || precise != 0 {
		t.Fatalf("currentInputTokensPrecisely = (%d, %v), want no precise count", precise, ok)
	}
	if precise, ok := eng.currentInputTokensPrecisely(context.Background()); ok || precise != 0 {
		t.Fatalf("second currentInputTokensPrecisely = (%d, %v), want no precise count", precise, ok)
	}
	if client.countCalls != 2 {
		t.Fatalf("count calls=%d, want 2 repeated backend attempts", client.countCalls)
	}

	reopenedStore, err := runtimeTestSessionPersistence.Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	reopened, err := New(reopenedStore, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", ContextWindowTokens: 400_000})
	if err != nil {
		t.Fatalf("reopen engine: %v", err)
	}
	if precise, ok := reopened.currentInputTokensPrecisely(context.Background()); ok || precise != 0 {
		t.Fatalf("reopened currentInputTokensPrecisely = (%d, %v), want no precise count", precise, ok)
	}
	if client.countCalls != 2 {
		t.Fatalf("expected reopened engine to reuse persisted failure marker without retrying backend, got %d count attempts", client.countCalls)
	}

	events, err := sessiontest.CollectEvents(reopenedStore)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	diagnosticEntries := 0
	for _, evt := range events {
		if evt.Kind != "local_entry" {
			continue
		}
		var entry storedLocalEntry
		if err := json.Unmarshal(evt.Payload, &entry); err != nil {
			t.Fatalf("decode local_entry: %v", err)
		}
		if entry.DiagnosticKey != preciseTokenCountFailureDiagnostic {
			continue
		}
		diagnosticEntries++
		if entry.Role != "error" {
			t.Fatalf("diagnostic role = %q, want error", entry.Role)
		}
		if !strings.Contains(entry.Text, "Exact token counting failed:") {
			t.Fatalf("expected exact count failure text, got %q", entry.Text)
		}
		if !strings.Contains(entry.Text, "Falling back to a local token estimate.") {
			t.Fatalf("expected fallback note, got %q", entry.Text)
		}
		if !strings.Contains(entry.Text, "chatgpt-codex status 404") {
			t.Fatalf("expected backend error details, got %q", entry.Text)
		}
	}
	if diagnosticEntries != 1 {
		t.Fatalf("diagnostic local_entry count = %d, want 1", diagnosticEntries)
	}
}

// A dangling-tool-call 400 from the precise token-count probe is repairable by
// the model request path, so it must not persist a permanent failure diagnostic
// that would disable exact counting for the rest of the active list. The probe
// falls back to an estimate transiently and keeps retrying the backend.
func TestCurrentInputTokensPreciselyDoesNotPersistFailureForRepairable400(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &preciseCompactionClient{
		countErr:      &llm.APIStatusError{StatusCode: 400, Body: "tool call without output"},
		contextWindow: 400000,
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5", ContextWindowTokens: 400_000})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "missing", Name: "exec", Input: json.RawMessage(`{}`)}},
	}})); err != nil {
		t.Fatalf("append dangling tool call: %v", err)
	}

	if precise, ok := eng.currentInputTokensPrecisely(context.Background()); ok || precise != 0 {
		t.Fatalf("currentInputTokensPrecisely = (%d, %v), want no precise count", precise, ok)
	}
	if precise, ok := eng.currentInputTokensPrecisely(context.Background()); ok || precise != 0 {
		t.Fatalf("second currentInputTokensPrecisely = (%d, %v), want no precise count", precise, ok)
	}
	if client.countCalls != 2 {
		t.Fatalf("count calls=%d, want 2 repeated backend attempts (no permanent failure marker)", client.countCalls)
	}

	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	for _, evt := range events {
		if evt.Kind != "local_entry" {
			continue
		}
		var entry storedLocalEntry
		if err := json.Unmarshal(evt.Payload, &entry); err != nil {
			t.Fatalf("decode local_entry: %v", err)
		}
		if entry.DiagnosticKey == preciseTokenCountFailureDiagnostic {
			t.Fatalf("did not expect a persisted precise-token failure diagnostic for a repairable 400: %+v", entry)
		}
	}
}

func TestCurrentInputTokensPreciselySkipsUnsupportedCountClient(t *testing.T) {
	store := mustCreateTestSession(t)

	supported := false
	client := &preciseCompactionClient{inputTokenCount: 123, contextWindow: 400000, countSupported: &supported}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", ContextWindowTokens: 400_000})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "hello"}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}

	if precise, ok := eng.currentInputTokensPrecisely(context.Background()); ok || precise != 0 {
		t.Fatalf("currentInputTokensPrecisely = (%d, %v), want no precise count", precise, ok)
	}
	if client.countCalls != 0 {
		t.Fatalf("count calls=%d, want 0 for unsupported exact counting", client.countCalls)
	}

	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	for _, evt := range events {
		if evt.Kind != "local_entry" {
			continue
		}
		var entry storedLocalEntry
		if err := json.Unmarshal(evt.Payload, &entry); err != nil {
			t.Fatalf("decode local_entry: %v", err)
		}
		if entry.DiagnosticKey == preciseTokenCountFailureDiagnostic {
			t.Fatalf("did not expect precise-token diagnostic for unsupported provider: %+v", entry)
		}
	}
}

func TestCurrentInputTokensPreciselyPersistsTranscriptErrorOnSupportProbeFailure(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &preciseCompactionClient{inputTokenCount: 123, contextWindow: 400000, supportErr: errors.New("oauth metadata unavailable")}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", ContextWindowTokens: 400_000})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "hello"}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}

	if precise, ok := eng.currentInputTokensPrecisely(context.Background()); ok || precise != 0 {
		t.Fatalf("currentInputTokensPrecisely = (%d, %v), want no precise count", precise, ok)
	}
	if client.countCalls != 0 {
		t.Fatalf("count calls=%d, want 0 when support probe fails closed", client.countCalls)
	}

	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	entries := 0
	for _, evt := range events {
		if evt.Kind != "local_entry" {
			continue
		}
		var entry storedLocalEntry
		if err := json.Unmarshal(evt.Payload, &entry); err != nil {
			t.Fatalf("decode local_entry: %v", err)
		}
		if entry.DiagnosticKey != preciseTokenCountSupportDiagnostic {
			continue
		}
		entries++
		if !strings.Contains(entry.Text, "Exact token counting availability check failed:") {
			t.Fatalf("unexpected diagnostic text: %q", entry.Text)
		}
	}
	if entries != 1 {
		t.Fatalf("support diagnostic count=%d, want 1", entries)
	}
}
