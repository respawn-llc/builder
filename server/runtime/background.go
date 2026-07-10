package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"core/server/llm"
	"core/server/tools"
	"core/shared/toolspec"
	"core/shared/valuecopy"
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

func (b *defaultBackgroundNoticeScheduler) HandleBackgroundShellUpdate(evt BackgroundShellEvent, queueNotice bool) {
	_ = b.engine.steer("", steerEventIntent(Event{Kind: EventBackgroundUpdated, Background: &evt}))
	if !queueNotice {
		return
	}
	if evt.Type != "completed" && evt.Type != "killed" {
		return
	}
	b.QueueDeveloperNotice(llm.Message{
		Role:               llm.RoleDeveloper,
		MessageType:        llm.MessageTypeBackgroundNotice,
		Name:               strings.TrimSpace(evt.ID),
		Content:            formatBackgroundShellNotice(evt),
		CompactContent:     formatBackgroundShellCompact(evt),
		BackgroundExitCode: valuecopy.Pointer(evt.ExitCode),
	})
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
	if strings.TrimSpace(msg.Content) == "" {
		return
	}
	shouldSchedule := false
	notice := queuedBackgroundNotice{
		sessionID: strings.TrimSpace(msg.Name),
		intent:    steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{msg}),
	}
	b.mu.Lock()
	b.pending = append(b.pending, notice)
	if !b.scheduled && (b.steps == nil || !b.steps.IsBusy()) {
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

func (b *defaultBackgroundNoticeScheduler) DrainPendingNotices() []steeringIntent {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) == 0 {
		b.scheduled = false
		return nil
	}
	pending := append([]queuedBackgroundNotice(nil), b.pending...)
	b.pending = nil
	b.scheduled = false
	intents := make([]steeringIntent, 0, len(pending))
	for _, notice := range pending {
		intents = append(intents, notice.intent)
	}
	return intents
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
		b.engine.AppendCommittedEntry("error", fmt.Sprintf("background continuation failed: %v", err))
	}
}

func (b *defaultBackgroundNoticeScheduler) runQueuedNotices(ctx context.Context) (assistant llm.Message, err error) {
	if len(b.pendingSnapshot()) == 0 {
		b.clearScheduled()
		return llm.Message{}, nil
	}
	err = b.steps.Run(ctx, exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindBackground}, func(stepCtx context.Context, stepID string) error {
		pending := b.DrainPendingNotices()
		if len(pending) == 0 {
			return nil
		}
		if err := b.engine.ensureMetaContextForRequest(stepCtx, stepID); err != nil {
			return err
		}
		if err := b.engine.steer(stepID, pending...); err != nil {
			return err
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
