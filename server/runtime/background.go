package runtime

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
)

type defaultBackgroundNoticeScheduler struct {
	engine *Engine
	steps  exclusiveStepLifecycle

	mu        sync.Mutex
	pending   []queuedBackgroundNotice
	scheduled bool
}

type queuedBackgroundNotice struct {
	sessionID string
	intent    steeringIntent
}

func (e *Engine) HandleBackgroundShellUpdate(evt BackgroundShellEvent, queueNotice bool) {
	e.ensureOrchestrationCollaborators()
	e.backgroundFlow.HandleBackgroundShellUpdate(evt, queueNotice)
}

func (e *Engine) RecordBackgroundShellUpdate(evt BackgroundShellEvent) error {
	e.ensureOrchestrationCollaborators()
	return e.backgroundFlow.RecordBackgroundShellUpdate(evt)
}

func (e *Engine) QueueBackgroundShellContinuation(evt BackgroundShellEvent) {
	e.ensureOrchestrationCollaborators()
	e.backgroundFlow.QueueBackgroundShellContinuation(evt)
}

func (e *Engine) RunBackgroundShellContinuation(ctx context.Context, evt BackgroundShellEvent) error {
	e.ensureOrchestrationCollaborators()
	return e.backgroundFlow.RunBackgroundShellContinuation(ctx, evt)
}

func (e *Engine) SteerBackgroundContinuationFailure(err error) error {
	if err == nil {
		return errors.New("background continuation failure is required")
	}
	_, steerErr := e.steerRuntimeErrorFeedback(
		fmt.Errorf("background continuation failed: %w", err),
	)
	return steerErr
}

func (b *defaultBackgroundNoticeScheduler) HandleBackgroundShellUpdate(evt BackgroundShellEvent, queueNotice bool) {
	if err := b.RecordBackgroundShellUpdate(evt); err != nil {
		b.engine.surfaceRunError(err)
		return
	}
	if queueNotice {
		b.QueueBackgroundShellContinuation(evt)
	}
}

func (b *defaultBackgroundNoticeScheduler) RecordBackgroundShellUpdate(evt BackgroundShellEvent) error {
	return b.engine.steer("", steerEventIntent(Event{Kind: EventBackgroundUpdated, Background: &evt}))
}

func (b *defaultBackgroundNoticeScheduler) QueueBackgroundShellContinuation(evt BackgroundShellEvent) {
	if !evt.Type.IsTerminal() {
		return
	}
	b.queueDeveloperNotice(backgroundShellDeveloperNotice(evt), true)
}

func (b *defaultBackgroundNoticeScheduler) RunBackgroundShellContinuation(ctx context.Context, evt BackgroundShellEvent) error {
	if !evt.Type.IsTerminal() {
		return nil
	}
	b.queueDeveloperNotice(backgroundShellDeveloperNotice(evt), false)
	_, err := b.runQueuedNotices(ctx)
	return err
}

func backgroundShellDeveloperNotice(evt BackgroundShellEvent) llm.Message {
	return llm.Message{
		Role:                 llm.RoleDeveloper,
		MessageType:          textutil.Value(llm.MessageTypeBackgroundNotice),
		Name:                 textutil.OptionalTrimmedString(evt.ID),
		BackgroundActivityID: textutil.Value(evt.ActivityID.String()),
		Content:              textutil.Value(formatBackgroundShellNotice(evt)),
		CompactContent:       textutil.Value(formatBackgroundShellCompact(evt)),
		BackgroundExitCode:   textutil.Pointer(evt.ExitCode),
	}
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
	b.queueDeveloperNotice(msg, true)
}

func (b *defaultBackgroundNoticeScheduler) queueDeveloperNotice(msg llm.Message, schedule bool) {
	if msg.Content == nil || strings.TrimSpace(*msg.Content) == "" {
		return
	}
	shouldSchedule := false
	sessionID, _ := textutil.OptionalTrimmed(msg.Name)
	notice := queuedBackgroundNotice{
		sessionID: sessionID,
		intent:    steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{msg}),
	}
	b.mu.Lock()
	b.pending = append(b.pending, notice)
	if schedule && !b.scheduled && (b.steps == nil || !b.steps.IsBusy()) {
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

func (b *defaultBackgroundNoticeScheduler) drainPendingNotices() []queuedBackgroundNotice {
	b.mu.Lock()
	defer b.mu.Unlock()
	pending := append([]queuedBackgroundNotice(nil), b.pending...)
	b.pending = nil
	b.scheduled = false
	return pending
}

func (b *defaultBackgroundNoticeScheduler) restorePendingNotices(notices []queuedBackgroundNotice) {
	if len(notices) == 0 {
		return
	}
	b.mu.Lock()
	b.pending = append(append([]queuedBackgroundNotice(nil), notices...), b.pending...)
	b.scheduled = false
	b.mu.Unlock()
}

func (b *defaultBackgroundNoticeScheduler) flushPendingNotices(stepID string) (int, error) {
	pending := b.drainPendingNotices()
	flushed := 0
	for index, notice := range pending {
		receipt, err := b.engine.steerWithCommitReceipt(stepID, notice.intent)
		if receipt.Committed {
			flushed++
		}
		if err != nil {
			restore := pending[index:]
			if receipt.Committed {
				restore = pending[index+1:]
			}
			b.restorePendingNotices(restore)
			return flushed, err
		}
		if !receipt.Committed {
			b.restorePendingNotices(pending[index:])
			return flushed, fmt.Errorf("background notice persistence did not commit")
		}
	}
	return flushed, nil
}

func (b *defaultBackgroundNoticeScheduler) HasPendingNotices() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending) > 0
}

func (b *defaultBackgroundNoticeScheduler) ConsumePendingBackgroundNotice(sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	removed := false
	filtered := b.pending[:0]
	for _, notice := range b.pending {
		if strings.TrimSpace(notice.sessionID) == sessionID {
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

func harvestedBackgroundCompletionSessionID(res tools.Result) (string, bool) {
	if res.Name != toolspec.ToolWriteStdin || res.CompletedBackgroundSessionID == nil {
		return "", false
	}
	if *res.CompletedBackgroundSessionID <= 0 {
		panic(fmt.Sprintf(
			"write_stdin completion provenance has invalid session ID %d",
			*res.CompletedBackgroundSessionID,
		))
	}
	return strconv.Itoa(*res.CompletedBackgroundSessionID), true
}

func (b *defaultBackgroundNoticeScheduler) processQueuedNotices(ctx context.Context) *resultGroupFatal {
	if _, err := b.runQueuedNotices(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		if fatal, abort := resultGroupFatalFromError(err); abort {
			return fatal
		}
		if steerErr := b.engine.SteerBackgroundContinuationFailure(err); steerErr != nil {
			b.engine.surfaceRunError(errors.Join(err, steerErr))
		}
	}
	return nil
}

func (b *defaultBackgroundNoticeScheduler) runQueuedNotices(ctx context.Context) (assistant llm.Message, err error) {
	if len(b.pendingSnapshot()) == 0 {
		b.clearScheduled()
		return llm.Message{}, nil
	}
	err = b.steps.Run(ctx, exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindBackground}, func(stepCtx context.Context, stepID string) error {
		if err := b.engine.ensureMetaContextForRequest(stepCtx, stepID); err != nil {
			return err
		}
		flushed, flushErr := b.flushPendingNotices(stepID)
		if flushErr != nil {
			return flushErr
		}
		if flushed == 0 {
			return nil
		}
		msg, runErr := b.engine.runStepLoop(stepCtx, stepID)
		assistant = msg
		return runErr
	})
	if err != nil && b.HasPendingNotices() {
		b.clearScheduled()
	}
	if errors.Is(err, ErrAgentBusy) {
		b.clearScheduled()
		return llm.Message{}, nil
	}
	return assistant, err
}

func (b *defaultBackgroundNoticeScheduler) pendingSnapshot() []queuedBackgroundNotice {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]queuedBackgroundNotice(nil), b.pending...)
}

func (b *defaultBackgroundNoticeScheduler) clearScheduled() {
	b.mu.Lock()
	b.scheduled = false
	b.mu.Unlock()
}
