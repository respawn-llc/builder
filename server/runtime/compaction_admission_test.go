package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

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

func TestManualCompactionNoBoundaryDoesNotMisreportActiveCompaction(t *testing.T) {
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeCompactionClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
	})
	compactor := engine.compactionFlow.(*defaultContextCompactor)
	compactor.steps = &stubExclusiveStepLifecycle{
		snapshot: &RunSnapshot{ActiveKind: ActiveKindUserTurn},
	}
	coordinator := engine.compactionRuntimeState().manualBoundaryCoordinator()
	coordinator.armNextGeneration()
	coordinator.abortArmedGeneration(nil)

	_, err := compactor.compactManualContext(
		context.Background(),
		compactionInstructionsInput{},
		nil,
		true,
		nil,
		nil,
	)
	assertManualCompactionAdmissionReason(t, err, ManualCompactionAdmissionReasonTooSoon)
}

func TestQueuedManualCompactionsRecheckEligibilitySerially(t *testing.T) {
	store := mustCreateTestSession(t)
	appendAgentStepBoundaryForEligibilityTest(t, store, "queued-repeat-seed")
	client := &fakeCompactionClient{compactionResponses: []llm.CompactionResponse{
		remoteCompactionReplacement(1_000, 100, 200_000),
		remoteCompactionReplacement(1_000, 100, 200_000),
	}}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	engine.compactionRuntimeState().SetManualCompactionEligible(true)
	lifecycle := engine.stepLifecycle.(*defaultExclusiveStepLifecycle)

	maintenanceStarted := make(chan struct{})
	releaseMaintenance := make(chan struct{})
	maintenanceDone := make(chan error, 1)
	go func() {
		maintenanceDone <- lifecycle.Run(
			context.Background(),
			exclusiveStepOptions{ActiveKind: ActiveKindRuntimeMaintenance},
			func(context.Context, string) error {
				close(maintenanceStarted)
				<-releaseMaintenance
				return nil
			},
		)
	}()
	select {
	case <-maintenanceStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("maintenance run did not start")
	}

	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() {
		firstDone <- engine.CompactContext(context.Background(), "")
	}()
	go func() {
		secondDone <- engine.CompactContext(context.Background(), "")
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		lifecycle.mu.Lock()
		waiters := len(lifecycle.nextWaiters)
		lifecycle.mu.Unlock()
		if waiters == 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	lifecycle.mu.Lock()
	waiters := len(lifecycle.nextWaiters)
	lifecycle.mu.Unlock()
	if waiters != 2 {
		t.Fatalf("queued manual RunNext waiters = %d, want 2", waiters)
	}
	close(releaseMaintenance)
	if err := <-maintenanceDone; err != nil {
		t.Fatalf("maintenance run: %v", err)
	}
	firstErr := <-firstDone
	secondErr := <-secondDone
	if (firstErr == nil) == (secondErr == nil) {
		t.Fatalf("queued manual results = first:%v second:%v, want exactly one success", firstErr, secondErr)
	}
	assertManualCompactionAdmissionReason(t, errors.Join(firstErr, secondErr), ManualCompactionAdmissionReasonTooSoon)
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.compactionCalls) != 1 {
		t.Fatalf("provider compaction calls = %d, want one", len(client.compactionCalls))
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
