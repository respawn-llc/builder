package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"

	"github.com/google/uuid"
)

type defaultBackgroundNoticeScheduler struct {
	engine *Engine
	steps  exclusiveStepLifecycle

	mu        sync.Mutex
	pending   []queuedBackgroundNotice
	scheduled bool
}

type queuedBackgroundNotice struct {
	processID  string
	activity   uuid.UUID
	intent     steeringIntent
	diagnostic *PendingBackgroundDeliveryDiagnostic
}

func (e *Engine) HandleBackgroundShellUpdate(evt BackgroundShellEvent, queueNotice bool) {
	e.ensureOrchestrationCollaborators()
	e.backgroundFlow.HandleBackgroundShellUpdate(evt, queueNotice)
}

// AdmitBackgroundShellUpdate adds a terminal notice without scheduling it.
// Authority uses this during the Manager acknowledgement boundary so automatic
// delivery cannot overtake the handoff that owns the notice.
func (e *Engine) AdmitBackgroundShellUpdate(evt BackgroundShellEvent) {
	e.ensureOrchestrationCollaborators()
	e.backgroundFlow.AdmitBackgroundShellUpdate(evt)
}

// DiscardProvisionalBackgroundNotice removes only an uncommitted automatic
// notice. It is used when a Manager owner-poll receipt supersedes a terminal
// handoff while the Authority callback is still returning.
func (e *Engine) DiscardProvisionalBackgroundNotice(processID string) bool {
	e.ensureOrchestrationCollaborators()
	return e.backgroundFlow.ConsumePendingBackgroundNotice(processID)
}

func (e *Engine) ScheduleBackgroundNoticesIfIdle() {
	e.ensureOrchestrationCollaborators()
	e.backgroundFlow.ScheduleIfIdle()
}

func (b *defaultBackgroundNoticeScheduler) HandleBackgroundShellUpdate(evt BackgroundShellEvent, queueNotice bool) {
	_ = b.engine.steer("", steerEventIntent(Event{Kind: EventBackgroundUpdated, Background: &evt}))
	if !queueNotice {
		return
	}
	if !evt.Type.IsTerminal() {
		return
	}
	b.queueBackgroundShellNotice(evt, llm.Message{
		Role:                 llm.RoleDeveloper,
		MessageType:          textutil.Value(llm.MessageTypeBackgroundNotice),
		Name:                 textutil.OptionalTrimmedString(evt.ID),
		BackgroundActivityID: textutil.Value(evt.ActivityID.String()),
		Content:              textutil.Value(formatBackgroundShellNotice(evt)),
		CompactContent:       textutil.Value(formatBackgroundShellCompact(evt)),
		BackgroundExitCode:   textutil.Pointer(evt.ExitCode),
	}, true)
}

func (b *defaultBackgroundNoticeScheduler) AdmitBackgroundShellUpdate(evt BackgroundShellEvent) {
	if !evt.Type.IsTerminal() {
		panic(fmt.Sprintf("background admission requires terminal event: process_id=%q type=%q", evt.ID, evt.Type))
	}
	_ = b.engine.steer("", steerEventIntent(Event{Kind: EventBackgroundUpdated, Background: &evt}))
	b.queueBackgroundShellNotice(evt, llm.Message{
		Role:                 llm.RoleDeveloper,
		MessageType:          textutil.Value(llm.MessageTypeBackgroundNotice),
		Name:                 textutil.OptionalTrimmedString(evt.ID),
		BackgroundActivityID: textutil.Value(evt.ActivityID.String()),
		Content:              textutil.Value(formatBackgroundShellNotice(evt)),
		CompactContent:       textutil.Value(formatBackgroundShellCompact(evt)),
		BackgroundExitCode:   textutil.Pointer(evt.ExitCode),
	}, false)
}

func formatBackgroundShellNotice(evt BackgroundShellEvent) string {
	if strings.TrimSpace(evt.NoticeText) != "" {
		return strings.TrimSpace(evt.NoticeText)
	}
	parts := []string{fmt.Sprintf("Background shell %s %s.", evt.ID, evt.State)}
	if code := evt.ExitCode; code != nil {
		parts = append(parts, fmt.Sprintf("Exit code: %d", *code))
	}
	preview := strings.TrimSpace(evt.Preview)
	if preview != "" {
		parts = append(parts, "Output:")
		parts = append(parts, preview)
	} else {
		parts = append(parts, "No output")
	}
	return strings.Join(parts, "\n")
}

func formatBackgroundShellCompact(evt BackgroundShellEvent) string {
	if strings.TrimSpace(evt.CompactText) != "" {
		return strings.TrimSpace(evt.CompactText)
	}
	text := fmt.Sprintf("Background shell %s %s", evt.ID, evt.State)
	if code := evt.ExitCode; code != nil {
		text = fmt.Sprintf("%s (exit %d)", text, *code)
	}
	return text
}

func (b *defaultBackgroundNoticeScheduler) QueueDeveloperNotice(msg llm.Message) {
	if msg.Content == nil || strings.TrimSpace(*msg.Content) == "" {
		return
	}
	shouldSchedule := false
	notice := queuedBackgroundNotice{
		intent: steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{msg}),
	}
	b.admitNotice(notice, true, &shouldSchedule)
	if shouldSchedule {
		if !b.engine.launchLifecycleTask(b.processQueuedNotices) {
			b.clearScheduled()
		}
	}
}

func (b *defaultBackgroundNoticeScheduler) queueBackgroundShellNotice(evt BackgroundShellEvent, msg llm.Message, schedule bool) {
	if msg.Content == nil || strings.TrimSpace(*msg.Content) == "" {
		return
	}
	shouldSchedule := false
	b.admitNotice(queuedBackgroundNotice{
		processID: strings.TrimSpace(evt.ID),
		activity:  evt.ActivityID,
		intent:    steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{msg}),
	}, schedule, &shouldSchedule)
	if shouldSchedule {
		if !b.engine.launchLifecycleTask(b.processQueuedNotices) {
			b.clearScheduled()
		}
	}
}

func (b *defaultBackgroundNoticeScheduler) admitNotice(notice queuedBackgroundNotice, schedule bool, shouldSchedule *bool) {
	b.mu.Lock()
	b.pending = append(b.pending, notice)
	if schedule && !b.scheduled && (b.steps == nil || !b.steps.IsBusy()) {
		b.scheduled = true
		*shouldSchedule = true
	}
	b.mu.Unlock()
}

func (b *defaultBackgroundNoticeScheduler) DrainPendingNotices() []queuedBackgroundNotice {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) == 0 {
		b.scheduled = false
		return nil
	}
	pending := append([]queuedBackgroundNotice(nil), b.pending...)
	b.pending = nil
	b.scheduled = false
	return pending
}

func (b *defaultBackgroundNoticeScheduler) HasPendingNotices() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending) > 0
}

func (b *defaultBackgroundNoticeScheduler) ConsumePendingBackgroundNotice(sessionID string) bool {
	processID := strings.TrimSpace(sessionID)
	if processID == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	removed := false
	filtered := b.pending[:0]
	for _, notice := range b.pending {
		if notice.processID == processID {
			removed = true
			continue
		}
		filtered = append(filtered, notice)
	}
	b.pending = filtered
	if len(b.pending) == 0 {
		b.scheduled = false
	}
	return removed
}

func (b *defaultBackgroundNoticeScheduler) ScheduleIfIdle() {
	if b.steps != nil && b.steps.IsBusy() {
		return
	}
	shouldSchedule := false
	b.mu.Lock()
	if len(b.pending) > 0 && !b.scheduled {
		b.scheduled = true
		shouldSchedule = true
	}
	b.mu.Unlock()
	if shouldSchedule {
		if !b.engine.launchLifecycleTask(b.processQueuedNotices) {
			b.clearScheduled()
		}
	}
}

type harvestedBackgroundCompletion struct {
	SessionID  int  `json:"background_session_id"`
	Running    bool `json:"background_running"`
	Background bool `json:"backgrounded"`
}

func harvestedBackgroundCompletionSessionID(res tools.Result) (string, bool) {
	if res.IsError || res.Name != toolspec.ToolWriteStdin {
		return "", false
	}
	var out harvestedBackgroundCompletion
	if err := json.Unmarshal(res.Output, &out); err != nil {
		return "", false
	}
	if out.SessionID <= 0 || out.Running || !out.Background {
		return "", false
	}
	return fmt.Sprintf("%d", out.SessionID), true
}

func (b *defaultBackgroundNoticeScheduler) processQueuedNotices(ctx context.Context) {
	if _, err := b.runQueuedNotices(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		b.commitPendingDeliveryDiagnostics()
	}
}

func (b *defaultBackgroundNoticeScheduler) runQueuedNotices(ctx context.Context) (assistant llm.Message, err error) {
	if len(b.pendingSnapshot()) == 0 {
		b.clearScheduled()
		return llm.Message{}, nil
	}
	err = b.steps.Run(ctx, exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindBackground}, func(stepCtx context.Context, stepID string) error {
		pending := b.drainPending()
		if len(pending) == 0 {
			return nil
		}
		if err := b.engine.ensureMetaContextForRequest(stepCtx, stepID); err != nil {
			b.restorePendingFront(pending)
			return err
		}
		for index, notice := range pending {
			receipt, steerErr := b.engine.steerWithCommitReceipt(stepID, notice.intent)
			b.FinalizeCommittedBackgroundNotice(notice, receipt)
			if steerErr != nil {
				if !receipt.Committed && notice.processID != "" {
					diagnostic := newPendingBackgroundDeliveryDiagnostic(
						notice.processID,
						notice.activity,
						backgroundDeliveryStageAutomaticSteering,
						nextBackgroundDeliveryAttempt(notice.diagnostic),
						steerErr,
					)
					pending[index].diagnostic = &diagnostic
				}
				if !receipt.Committed {
					b.restorePendingFront(pending[index:])
				} else {
					b.restorePendingFront(pending[index+1:])
				}
				return steerErr
			}
		}
		msg, runErr := b.engine.runStepLoop(stepCtx, stepID)
		assistant = msg
		return runErr
	})
	if errors.Is(err, ErrAgentBusy) {
		b.clearScheduled()
		return llm.Message{}, nil
	}
	return assistant, err
}

func nextBackgroundDeliveryAttempt(previous *PendingBackgroundDeliveryDiagnostic) uint64 {
	if previous == nil {
		return 1
	}
	return previous.attempt + 1
}

func (b *defaultBackgroundNoticeScheduler) commitPendingDeliveryDiagnostics() {
	for _, notice := range b.pendingSnapshot() {
		if notice.diagnostic == nil {
			continue
		}
		receipt, err := b.engine.commitBackgroundDeliveryDiagnostic(*notice.diagnostic)
		if receipt.Committed {
			b.clearCommittedDeliveryDiagnostic(notice.processID, notice.activity)
		}
		if err != nil {
			return
		}
	}
}

func (b *defaultBackgroundNoticeScheduler) clearCommittedDeliveryDiagnostic(processID string, activity uuid.UUID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for index := range b.pending {
		notice := &b.pending[index]
		if notice.processID == processID && notice.activity == activity {
			notice.diagnostic = nil
		}
	}
}

func (b *defaultBackgroundNoticeScheduler) FinalizeCommittedBackgroundNotice(notice queuedBackgroundNotice, receipt session.CommitReceipt) {
	if !receipt.Committed || notice.processID == "" || b.engine.cfg.BackgroundAutomaticFinalizer == nil {
		return
	}
	if b.engine.cfg.BackgroundAutomaticFinalizer(notice.processID, notice.activity) &&
		b.engine.cfg.BackgroundCompletionSettled != nil {
		b.engine.cfg.BackgroundCompletionSettled(notice.processID)
	}
}

func (b *defaultBackgroundNoticeScheduler) pendingSnapshot() []queuedBackgroundNotice {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]queuedBackgroundNotice(nil), b.pending...)
}

func (b *defaultBackgroundNoticeScheduler) drainPending() []queuedBackgroundNotice {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) == 0 {
		b.scheduled = false
		return nil
	}
	pending := append([]queuedBackgroundNotice(nil), b.pending...)
	b.pending = nil
	b.scheduled = false
	return pending
}

func (b *defaultBackgroundNoticeScheduler) restorePendingFront(notices []queuedBackgroundNotice) {
	if len(notices) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	restored := make([]queuedBackgroundNotice, 0, len(notices)+len(b.pending))
	restored = append(restored, notices...)
	restored = append(restored, b.pending...)
	b.pending = restored
	b.scheduled = false
}

func (b *defaultBackgroundNoticeScheduler) clearScheduled() {
	b.mu.Lock()
	b.scheduled = false
	b.mu.Unlock()
}
