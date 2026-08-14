package runtime

import (
	"encoding/json"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestSteerBackgroundContinuationFailureUsesDeveloperErrorFeedback(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})

	if err := engine.SteerBackgroundContinuationFailure(errors.New("provider unavailable")); err != nil {
		t.Fatalf("steer background continuation failure: %v", err)
	}
	entries := engine.ChatSnapshot().Entries
	if len(entries) != 1 ||
		entries[0].Role != string(transcript.EntryRoleDeveloperErrorFeedback) ||
		entries[0].Text == "" {
		t.Fatalf("background failure entries = %+v, want one developer error feedback entry", entries)
	}

	mustBlockTestEventLogAppends(t, store)
	if err := engine.SteerBackgroundContinuationFailure(errors.New("retry failed")); err == nil {
		t.Fatal("background continuation failure steering swallowed persistence error")
	}
}

func TestBackgroundNoticeOwnershipFollowsWriteStdinCompletionCommitReceipt(t *testing.T) {
	for _, tt := range []struct {
		name        string
		block       bool
		wantPending bool
	}{
		{name: "committed"},
		{name: "uncommitted append failure", block: true, wantPending: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
			steps := &stubExclusiveStepLifecycle{
				busy:     true,
				snapshot: &RunSnapshot{RunID: "11111111-1111-4111-8111-111111111111", StepID: "step"},
			}
			scheduler := &defaultBackgroundNoticeScheduler{engine: engine}
			engine.stepLifecycle = &stubExclusiveStepLifecycle{busy: true}
			engine.stepLifecycle = steps
			engine.backgroundFlow = scheduler

			scheduler.QueueDeveloperNotice(llm.Message{
				Role:    llm.RoleDeveloper,
				Name:    textutil.Value("42"),
				Content: textutil.Value("queued background notice"),
			})
			if tt.block {
				mustBlockTestEventLogAppends(t, store)
			}

			presentation := transcript.NormalizeToolCallMeta(transcript.ToolCallMeta{ToolName: string(toolspec.ToolWriteStdin)})
			receipt, _, err := engine.persistToolCompletionRaw("step", tools.Result{
				CallID:       "write-stdin-call",
				Name:         toolspec.ToolWriteStdin,
				Output:       json.RawMessage(`{"background_session_id":42,"background_running":false,"backgrounded":true}`),
				Presentation: &presentation,
			})
			if receipt.Committed == tt.wantPending {
				t.Fatalf("completion receipt = %+v, want committed=%t", receipt, !tt.wantPending)
			}
			if tt.wantPending && err == nil {
				t.Fatal("uncommitted completion did not surface append failure")
			}
			if !tt.wantPending && err != nil {
				t.Fatalf("persist committed completion: %v", err)
			}
			if got := scheduler.HasPendingNotices(); got != tt.wantPending {
				t.Fatalf("pending notice after completion = %t, want %t", got, tt.wantPending)
			}
		})
	}
}

func TestFlushPendingUserInjectionsRestoresOnlyLaterNoticeAfterCommittedObserverFailure(t *testing.T) {
	observerErr := errors.New("background notice observer failed")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	scheduler := &defaultBackgroundNoticeScheduler{engine: engine}
	engine.stepLifecycle = &stubExclusiveStepLifecycle{
		busy:         true,
		snapshot:     &RunSnapshot{RunID: "11111111-1111-4111-8111-111111111111", StepID: "step"},
		activeStepID: "step",
	}
	engine.backgroundFlow = scheduler
	lifecycle := newDefaultMessageLifecycle(engine, scheduler)
	for _, sessionID := range []string{"first", "second"} {
		scheduler.QueueDeveloperNotice(llm.Message{
			Role:    llm.RoleDeveloper,
			Name:    textutil.Value(sessionID),
			Content: textutil.Value(sessionID + " notice"),
		})
	}
	gate.FailNext(observerErr)

	_, err := lifecycle.FlushPendingUserInjections("step", allPendingUserInjectionSelection{})
	if !errors.Is(err, observerErr) {
		t.Fatalf("flush error = %v, want observer failure", err)
	}
	pending := scheduler.pendingSnapshot()
	if len(pending) != 1 || pending[0].sessionID != "second" {
		t.Fatalf("pending notices after committed failure = %+v", pending)
	}
}
