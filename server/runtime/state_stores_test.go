package runtime

import (
	"testing"

	"core/server/llm"
	compaction "core/shared/config"
)

func TestCompactionPlannerDerivesThresholdsAndRunway(t *testing.T) {
	t.Parallel()
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
	if got := planner.reservedOutputTokens(snapshot); got != 8_000 {
		t.Fatalf("reservedOutputTokens()=%d, want locked max output", got)
	}

	tests := []struct {
		name               string
		currentUsedTokens  int
		reservedOutput     int
		estimatedToolCalls int
	}{
		{name: "at reminder threshold", currentUsedTokens: 765_000, reservedOutput: 8_000, estimatedToolCalls: 91},
		{name: "near forced limit", currentUsedTokens: 880_000, estimatedToolCalls: 14},
		{name: "reserved output reduces runway", currentUsedTokens: 891_000, reservedOutput: 8_000, estimatedToolCalls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runway := compactionPlanningSnapshot{
				autoCompactTokenLimit: 900_000,
				lockedMaxOutputTokens: test.reservedOutput,
				currentUsedTokens:     test.currentUsedTokens,
			}
			if got := planner.estimatedToolCallsUntilForcedHandoff(runway); got != test.estimatedToolCalls {
				t.Fatalf("estimatedToolCallsUntilForcedHandoff()=%d, want %d", got, test.estimatedToolCalls)
			}
		})
	}
}

func TestCompactionPlannerEagerCompactionUsesConsumedContextOnly(t *testing.T) {
	t.Parallel()
	planner := newCompactionPlanner()
	for _, test := range []struct {
		name   string
		window int
	}{
		{name: "272k", window: 272_000},
		{name: "372k", window: 372_000},
	} {
		t.Run(test.name, func(t *testing.T) {
			threshold := test.window * 88 / 100
			tests := []struct {
				name         string
				consumed     int
				reserved     int
				wantEligible bool
			}{
				{name: "exact threshold", consumed: threshold, wantEligible: true},
				{name: "one token below", consumed: threshold - 1, wantEligible: false},
				{name: "reserved output does not qualify", consumed: threshold - 1, reserved: 1, wantEligible: false},
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					if got := planner.eagerCompactionEligible(compactionPlanningSnapshot{
						contextWindowTokens:   test.window,
						currentUsedTokens:     tt.consumed,
						lockedMaxOutputTokens: tt.reserved,
					}); got != tt.wantEligible {
						t.Fatalf("eagerCompactionEligible() = %t, want %t", got, tt.wantEligible)
					}
				})
			}
		})
	}
}

func TestCompactionPlannerPanicsAtForcedLimit(t *testing.T) {
	t.Parallel()
	planner := newCompactionPlanner()
	tests := []struct {
		name              string
		currentUsedTokens int
		reservedOutput    int
	}{
		{name: "reserved output reaches limit", currentUsedTokens: 895_000, reservedOutput: 8_000},
		{name: "usage exceeds limit", currentUsedTokens: 950_000},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected forced-limit invariant panic")
				}
			}()
			planner.estimatedToolCallsUntilForcedHandoff(compactionPlanningSnapshot{
				autoCompactTokenLimit: 900_000,
				lockedMaxOutputTokens: test.reservedOutput,
				currentUsedTokens:     test.currentUsedTokens,
			})
		})
	}
}

func TestCompactionPlannerAppliesFallbacksAndSelectsEngine(t *testing.T) {
	t.Parallel()
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

	tests := []struct {
		name string
		mode string
		caps llm.ProviderCapabilities
		want compactionEnginePlan
	}{
		{
			name: "native provider compaction",
			mode: "native",
			caps: llm.ProviderCapabilities{SupportsResponsesCompact: true},
			want: compactionEnginePlan{engineKind: compactionEngineRemote, fallbackToLocalOnBadCheckpoint: true},
		},
		{name: "native local fallback", mode: "native", want: compactionEnginePlan{engineKind: compactionEngineLocal}},
		{name: "explicit local", mode: "local", caps: llm.ProviderCapabilities{SupportsResponsesCompact: true}, want: compactionEnginePlan{engineKind: compactionEngineLocal}},
		{name: "disabled", mode: "none", caps: llm.ProviderCapabilities{SupportsResponsesCompact: true}, want: compactionEnginePlan{engineKind: compactionEngineNone}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := planner.enginePlan(compactionPlanningSnapshot{compactionMode: test.mode}, test.caps); got != test.want {
				t.Fatalf("enginePlan()=%+v, want %+v", got, test.want)
			}
		})
	}
}
