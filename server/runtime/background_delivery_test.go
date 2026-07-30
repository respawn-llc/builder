package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"core/server/llm"
	"core/server/tools"
	shelltool "core/server/tools/shell"
	"core/shared/transcript"

	"github.com/google/uuid"
)

func TestPendingBackgroundDeliveryDiagnosticBoundsAndCopiesFailureDetail(t *testing.T) {
	payload := string([]byte{0xff}) + strings.Repeat("x", maxPendingBackgroundDeliveryDiagnosticBytes*2)
	diagnostic := newPendingBackgroundDeliveryDiagnostic(
		"process-1",
		uuid.New(),
		backgroundDeliveryStageAutomaticSteering,
		1,
		errors.New(payload),
	)
	if len(diagnostic.detail) > maxPendingBackgroundDeliveryDiagnosticBytes {
		t.Fatalf("retained detail bytes = %d, limit = %d", len(diagnostic.detail), maxPendingBackgroundDeliveryDiagnosticBytes)
	}
	if !utf8.ValidString(diagnostic.detail) {
		t.Fatal("retained detail is not valid UTF-8")
	}
	if diagnostic.attempt != 1 || diagnostic.processID != "process-1" || diagnostic.activity == uuid.Nil {
		t.Fatalf("diagnostic identity = %+v", diagnostic)
	}
}

func TestPendingBackgroundDeliveryDiagnosticRejectsInvalidRequiredIdentity(t *testing.T) {
	tests := []struct {
		name string
		run  func()
	}{
		{
			name: "process",
			run: func() {
				_ = newPendingBackgroundDeliveryDiagnostic("", uuid.New(), backgroundDeliveryStageAutomaticSteering, 1, errors.New("failed"))
			},
		},
		{
			name: "activity",
			run: func() {
				_ = newPendingBackgroundDeliveryDiagnostic("process-1", uuid.Nil, backgroundDeliveryStageAutomaticSteering, 1, errors.New("failed"))
			},
		},
		{
			name: "attempt",
			run: func() {
				_ = newPendingBackgroundDeliveryDiagnostic("process-1", uuid.New(), backgroundDeliveryStageAutomaticSteering, 0, errors.New("failed"))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected constructor invariant panic")
				}
			}()
			test.run()
		})
	}
}

func TestBackgroundDeliveryDiagnosticPersistsOnlyAfterCommit(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	diagnostic := newPendingBackgroundDeliveryDiagnostic(
		"process-1",
		uuid.New(),
		backgroundDeliveryStageAutomaticSteering,
		1,
		errors.New("persistence failed"),
	)
	blocker := mustBlockTestEventLogAppends(t, store)

	receipt, err := engine.commitBackgroundDeliveryDiagnostic(diagnostic)
	if err == nil || receipt.Committed {
		t.Fatalf("uncommitted diagnostic receipt = %+v error = %v", receipt, err)
	}
	before := 0
	for _, entry := range engine.ChatSnapshot().Entries {
		if entry.Role == string(transcript.EntryRoleDeveloperErrorFeedback) {
			before++
		}
	}

	if err := blocker.Restore(); err != nil {
		t.Fatalf("restore event log appends: %v", err)
	}
	receipt, err = engine.commitBackgroundDeliveryDiagnostic(diagnostic)
	if err != nil || !receipt.Committed {
		t.Fatalf("committed diagnostic receipt = %+v error = %v", receipt, err)
	}
	after := 0
	for _, entry := range engine.ChatSnapshot().Entries {
		if entry.Role == string(transcript.EntryRoleDeveloperErrorFeedback) {
			after++
		}
	}
	if after != before+1 {
		t.Fatalf("committed background delivery diagnostics = %d, want %d", after, before+1)
	}
}

func TestPendingBackgroundDeliveryDiagnosticReturnsTypedErrorUntilCommitted(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	diagnostic := newPendingBackgroundDeliveryDiagnostic(
		"process-typed",
		uuid.New(),
		backgroundDeliveryStageAutomaticSteering,
		1,
		errors.New("persistence failed"),
	)
	blocker := mustBlockTestEventLogAppends(t, store)
	receipt, err := engine.CommitPendingBackgroundDeliveryDiagnostic(diagnostic)
	if receipt.Committed {
		t.Fatalf("uncommitted receipt = %+v", receipt)
	}
	var deliveryErr *BackgroundDeliveryError
	if !errors.As(err, &deliveryErr) {
		t.Fatalf("error = %T %v, want BackgroundDeliveryError", err, err)
	}
	if deliveryErr.ProcessID != "process-typed" || deliveryErr.Activity != diagnostic.activity {
		t.Fatalf("typed delivery error = %+v", deliveryErr)
	}
	if err := blocker.Restore(); err != nil {
		t.Fatalf("restore event log appends: %v", err)
	}
	receipt, err = engine.CommitPendingBackgroundDeliveryDiagnostic(diagnostic)
	if !receipt.Committed {
		t.Fatalf("committed receipt = %+v error=%v", receipt, err)
	}
}

func TestDiagnosticOnlyBackgroundWorkPersistsWithoutModelContinuation(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	engine.ensureOrchestrationCollaborators()
	scheduler, ok := engine.backgroundFlow.(*defaultBackgroundNoticeScheduler)
	if !ok {
		t.Fatalf("background scheduler = %T", engine.backgroundFlow)
	}
	diagnostic := newPendingBackgroundDeliveryDiagnostic(
		"process-diagnostic-only",
		uuid.New(),
		backgroundDeliveryStageAutomaticSteering,
		1,
		errors.New("delivery failed"),
	)
	scheduler.mu.Lock()
	scheduler.states = []backgroundNoticeState{newDiagnosticOnlyBackgroundNotice(diagnostic)}
	scheduler.signalChangedLocked()
	scheduler.mu.Unlock()
	scheduler.ScheduleIfIdle()

	deadline := time.After(2 * time.Second)
	for {
		found := false
		for _, entry := range engine.ChatSnapshot().Entries {
			if entry.Role == string(transcript.EntryRoleDeveloperErrorFeedback) {
				found = true
				break
			}
		}
		if found {
			break
		}
		select {
		case <-deadline:
			t.Fatal("diagnostic-only background work did not persist")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	client.mu.Lock()
	calls := len(client.calls)
	client.mu.Unlock()
	if calls != 0 {
		t.Fatalf("diagnostic-only work started model continuations: %d", calls)
	}
	if engine.BackgroundDeliveryRetirementSnapshot().Active {
		t.Fatal("committed diagnostic-only work retained the runtime")
	}
}

func TestCommittedOwnerPollSettlementDoesNotStartAutomaticContinuation(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{}
	activity := uuid.New()
	var (
		engine       *Engine
		reserved     bool
		ownerClaimed bool
	)
	engine = mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model: "gpt-5",
		BackgroundAutomaticReservation: func(processID string, gotActivity uuid.UUID) bool {
			if processID != "owner-claim" || gotActivity != activity {
				t.Fatalf("automatic reservation identity = %q %s", processID, gotActivity)
			}
			reserved = true
			return true
		},
		BackgroundAutomaticFinalizer: func(processID string, gotActivity uuid.UUID) shelltool.TerminalAutomaticFinalization {
			if processID != "owner-claim" || gotActivity != activity {
				t.Fatalf("automatic finalization identity = %q %s", processID, gotActivity)
			}
			ownerClaimed = engine.FinalizeBackgroundOwnerPoll(processID, nil).removed
			return shelltool.TerminalAlreadyFinalizedByOwnerPoll
		},
	})
	engine.AdmitBackgroundShellUpdate(BackgroundShellEvent{
		Type:       BackgroundShellEventCompleted,
		ID:         "owner-claim",
		ActivityID: activity,
		State:      "completed",
		NoticeText: "terminal notice",
	})
	scheduler, ok := engine.backgroundFlow.(*defaultBackgroundNoticeScheduler)
	if !ok {
		t.Fatalf("background scheduler = %T", engine.backgroundFlow)
	}

	if _, err := scheduler.runQueuedNotices(context.Background()); err != nil {
		t.Fatalf("run automatic notice: %v", err)
	}
	if !reserved || !ownerClaimed {
		t.Fatalf("reservation=%t owner_claimed=%t", reserved, ownerClaimed)
	}
	if countBackgroundNoticeMessages(engine) != 1 {
		t.Fatalf("persisted automatic notices = %d, want 1", countBackgroundNoticeMessages(engine))
	}
	client.mu.Lock()
	calls := len(client.calls)
	client.mu.Unlock()
	if calls != 0 {
		t.Fatalf("owner-poll settlement started automatic continuation calls=%d", calls)
	}
	if scheduler.HasPendingNotices() {
		t.Fatal("owner-poll settlement retained an automatic notice")
	}
}

func TestCommittedOwnerPollSettlementDoesNotCountCombinedAutomaticFlush(t *testing.T) {
	store := mustCreateTestSession(t)
	activity := uuid.New()
	var engine *Engine
	engine = mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		BackgroundAutomaticReservation: func(string, uuid.UUID) bool {
			return true
		},
		BackgroundAutomaticFinalizer: func(processID string, gotActivity uuid.UUID) shelltool.TerminalAutomaticFinalization {
			if processID != "owner-claim-combined" || gotActivity != activity {
				t.Fatalf("automatic finalization identity = %q %s", processID, gotActivity)
			}
			if !engine.FinalizeBackgroundOwnerPoll(processID, nil).removed {
				t.Fatal("owner claim did not remove the provisional automatic notice")
			}
			return shelltool.TerminalAlreadyFinalizedByOwnerPoll
		},
	})
	engine.AdmitBackgroundShellUpdate(BackgroundShellEvent{
		Type:       BackgroundShellEventCompleted,
		ID:         "owner-claim-combined",
		ActivityID: activity,
		State:      "completed",
		NoticeText: "terminal notice",
	})
	lifecycle, ok := engine.messageFlow.(*defaultMessageLifecycle)
	if !ok {
		t.Fatalf("message lifecycle = %T", engine.messageFlow)
	}

	result, err := lifecycle.FlushPendingUserInjections("step", allPendingUserInjectionSelection{})
	if err != nil {
		t.Fatalf("flush combined automatic notice: %v", err)
	}
	if result.flushed != 0 {
		t.Fatalf("owner-poll settlement counted as an automatic flush: %d", result.flushed)
	}
	if countBackgroundNoticeMessages(engine) != 1 {
		t.Fatalf("persisted automatic notices = %d, want 1", countBackgroundNoticeMessages(engine))
	}
	if engine.BackgroundDeliveryRetirementSnapshot().Active {
		t.Fatal("owner-poll settlement retained background delivery work")
	}
}

func countBackgroundNoticeMessages(engine *Engine) int {
	count := 0
	for _, message := range engine.transcriptRuntimeState().SnapshotMessages() {
		if message.MessageType != nil && *message.MessageType == llm.MessageTypeBackgroundNotice {
			count++
		}
	}
	return count
}
