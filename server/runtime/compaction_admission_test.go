package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
)

func TestManualCompactionAdmissionRejectsFreshDisabledAndTooSoonRequestsWithoutSideEffects(t *testing.T) {
	t.Run("fresh session is too soon", func(t *testing.T) {
		client := &fakeCompactionClient{}
		engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{})
		before := compactionAdmissionEventCount(t, engine)

		err := engine.CompactContext(context.Background(), "")
		assertManualCompactionAdmissionReason(t, err, ManualCompactionAdmissionReasonTooSoon)
		assertCompactionAdmissionSideEffectsUnchanged(t, engine, client, before, 0, 0, 0)
	})

	t.Run("disabled policy precedes too soon", func(t *testing.T) {
		client := &fakeCompactionClient{}
		engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{
			CompactionMode: "none",
		})
		engine.compactionRuntimeState().SetManualCompactionEligible(true)
		before := compactionAdmissionEventCount(t, engine)

		err := engine.CompactContext(context.Background(), "")
		assertManualCompactionAdmissionReason(t, err, ManualCompactionAdmissionReasonDisabled)
		assertCompactionAdmissionSideEffectsUnchanged(t, engine, client, before, 0, 0, 0)
	})

	t.Run("successful compaction resets eligibility for immediate repeat", func(t *testing.T) {
		client := &fakeCompactionClient{
			responses: []llm.Response{{
				Assistant: llm.Message{
					Role:    llm.RoleAssistant,
					Content: textutil.Value("summary"),
				},
			}},
		}
		engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{})
		if err := engine.steer("step-1", steerMessagesWithPersistenceIntent(
			steeringPriorityNormal,
			steeringMessageEventNone,
			true,
			[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
		)); err != nil {
			t.Fatalf("persist input: %v", err)
		}
		engine.compactionRuntimeState().SetManualCompactionEligible(true)

		if err := engine.CompactContext(context.Background(), ""); err != nil {
			t.Fatalf("first compaction: %v", err)
		}
		before := compactionAdmissionEventCount(t, engine)
		count := engine.compactionRuntimeState().Count()
		client.mu.Lock()
		beforeCalls, beforeCompactions := len(client.calls), len(client.compactionCalls)
		client.mu.Unlock()
		err := engine.CompactContext(context.Background(), "")
		assertManualCompactionAdmissionReason(t, err, ManualCompactionAdmissionReasonTooSoon)
		assertCompactionAdmissionSideEffectsUnchanged(t, engine, client, before, count, beforeCalls, beforeCompactions)
	})
}

func TestManualCompactionAdmissionPrecedenceReturnsActiveDuringAnyCompactionMode(t *testing.T) {
	for _, mode := range []compactionMode{
		compactionModeAuto,
		compactionModeHandoff,
		compactionModeManual,
	} {
		t.Run(string(mode), func(t *testing.T) {
			client := &fakeCompactionClient{}
			engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{})
			before := compactionAdmissionEventCount(t, engine)
			lease, err := engine.compactionRuntimeState().acquireCompactionGate(false, false)
			if err != nil {
				t.Fatalf("acquire compaction gate: %v", err)
			}
			defer lease.release()

			err = engine.CompactContext(context.Background(), "")
			assertManualCompactionAdmissionReason(t, err, ManualCompactionAdmissionReasonActive)
			assertCompactionAdmissionSideEffectsUnchanged(t, engine, client, before, 0, 0, 0)
		})
	}
}

func TestManualCompactionRechecksEligibilityAfterQueuedRunNextOwnership(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Engine)
		want   ManualCompactionAdmissionReason
	}{
		{
			name: "eligibility reset",
			mutate: func(engine *Engine) {
				engine.compactionRuntimeState().SetManualCompactionEligible(false)
			},
			want: ManualCompactionAdmissionReasonTooSoon,
		},
		{
			name: "policy disabled",
			mutate: func(engine *Engine) {
				engine.cfg.CompactionMode = "none"
			},
			want: ManualCompactionAdmissionReasonDisabled,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeCompactionClient{}
			engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{})
			steps := &stubExclusiveStepLifecycle{
				snapshot: &RunSnapshot{ActiveKind: ActiveKindRuntimeMaintenance},
			}
			steps.runFn = func(ctx context.Context, options exclusiveStepOptions, fn func(stepCtx context.Context, stepID string) error) error {
				test.mutate(engine)
				return fn(ctx, "queued-manual-compaction")
			}
			compactor := engine.compactionFlow.(*defaultContextCompactor)
			compactor.steps = steps
			engine.compactionRuntimeState().SetManualCompactionEligible(true)

			_, err := engine.CompactContextWithActiveHook(context.Background(), "", nil)
			assertManualCompactionAdmissionReason(t, err, test.want)
			client.mu.Lock()
			defer client.mu.Unlock()
			if len(client.compactionCalls) != 0 {
				t.Fatalf("provider compaction calls = %d, want zero after ownership recheck", len(client.compactionCalls))
			}
		})
	}
}

func TestManualCompactionAdmissionAllowsASecondCompactAfterALaterAgentStep(t *testing.T) {
	store := mustCreateTestSession(t)
	appendAgentStepBoundaryForEligibilityTest(t, store, "initial-step")
	client := &fakeCompactionClient{
		responses: []llm.Response{
			{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("first summary")}},
			{Assistant: llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseFinal), Content: textutil.Value("turn complete")}},
			{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("second summary")}},
		},
	}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model:          "gpt-5",
		CompactionMode: "local",
	})

	if err := engine.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("first compact: %v", err)
	}
	if engine.compactionRuntimeState().ManualCompactionEligible() {
		t.Fatal("first committed compact retained eligibility")
	}
	if _, err := engine.SubmitUserMessage(context.Background(), "create later boundary"); err != nil {
		t.Fatalf("later agent step: %v", err)
	}
	if !engine.compactionRuntimeState().ManualCompactionEligible() {
		t.Fatal("later completed agent step did not restore eligibility")
	}
	if err := engine.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("second compact after later boundary: %v", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.calls) != 3 {
		t.Fatalf("provider calls = %d, want user step plus two compactions", len(client.calls))
	}
}

func TestCompactionGateRejectsImpossibleInternalOverlapAndPanicsInDebug(t *testing.T) {
	t.Run("release returns invariant error", func(t *testing.T) {
		engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeCompactionClient{}, tools.NewRegistry(), Config{})
		lease, err := engine.compactionRuntimeState().acquireCompactionGate(false, false)
		if err != nil {
			t.Fatalf("acquire compaction gate: %v", err)
		}
		defer lease.release()

		_, receipt, err := engine.compactNow(context.Background(), "step-1", compactionModeAuto, compactionInstructionsInput{}, false)
		if !errors.Is(err, ErrCompactionInvariantViolation) {
			t.Fatalf("internal overlap error = %v, want invariant violation", err)
		}
		if receipt.Committed {
			t.Fatalf("internal overlap receipt = %+v, want uncommitted", receipt)
		}
	})

	t.Run("debug panics", func(t *testing.T) {
		engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeCompactionClient{}, tools.NewRegistry(), Config{
			Debug: true,
		})
		lease, err := engine.compactionRuntimeState().acquireCompactionGate(false, false)
		if err != nil {
			t.Fatalf("acquire compaction gate: %v", err)
		}
		defer lease.release()

		defer func() {
			if recover() == nil {
				t.Fatal("debug internal overlap did not panic")
			}
		}()
		_, _, _ = engine.compactNow(context.Background(), "step-1", compactionModeAuto, compactionInstructionsInput{}, false)
	})
}

func assertManualCompactionAdmissionReason(t *testing.T, err error, want ManualCompactionAdmissionReason) {
	t.Helper()
	var admissionErr *ManualCompactionAdmissionError
	if !errors.As(err, &admissionErr) || admissionErr.Reason != want {
		t.Fatalf("manual compaction admission error = %T %v, want reason %q", err, err, want)
	}
}

func compactionAdmissionEventCount(t *testing.T, engine *Engine) int {
	t.Helper()
	window, err := engine.eventLog.ReadRecentRecords(10_000)
	if err != nil {
		t.Fatalf("read compaction admission events: %v", err)
	}
	return len(window.Records)
}

func assertCompactionAdmissionSideEffectsUnchanged(
	t *testing.T,
	engine *Engine,
	client *fakeCompactionClient,
	beforeEvents int,
	beforeCount int,
	beforeCalls int,
	beforeCompactions int,
) {
	t.Helper()
	if got := compactionAdmissionEventCount(t, engine); got != beforeEvents {
		t.Fatalf("event count after rejected compaction = %d, want %d", got, beforeEvents)
	}
	if got := engine.compactionRuntimeState().Count(); got != beforeCount {
		t.Fatalf("compaction count after rejected compaction = %d, want %d", got, beforeCount)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.calls) != beforeCalls || len(client.compactionCalls) != beforeCompactions {
		t.Fatalf("provider calls after rejected compaction = %d/%d, want %d/%d", len(client.calls), len(client.compactionCalls), beforeCalls, beforeCompactions)
	}
}
