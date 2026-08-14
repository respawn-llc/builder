package runtime

import (
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
)

type defaultBackgroundNoticeScheduler struct {
	engine *Engine

	mu      sync.Mutex
	pending []queuedBackgroundNotice
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
	if queueNotice {
		b.QueueBackgroundShellContinuation(evt)
	}
	if err := b.RecordBackgroundShellUpdate(evt); err != nil {
		b.engine.surfaceRunError(err)
	}
}

func (b *defaultBackgroundNoticeScheduler) RecordBackgroundShellUpdate(evt BackgroundShellEvent) error {
	return b.engine.steerCurrentStepOrRuntime(
		steerEventIntent(Event{Kind: EventBackgroundUpdated, Background: &evt}),
	)
}

func (b *defaultBackgroundNoticeScheduler) QueueBackgroundShellContinuation(evt BackgroundShellEvent) {
	if !evt.Type.IsTerminal() {
		return
	}
	b.queueDeveloperNotice(backgroundShellDeveloperNotice(evt))
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
	b.queueDeveloperNotice(msg)
}

func (b *defaultBackgroundNoticeScheduler) queueDeveloperNotice(msg llm.Message) {
	if msg.Content == nil || strings.TrimSpace(*msg.Content) == "" {
		return
	}
	sessionID, _ := textutil.OptionalTrimmed(msg.Name)
	notice := queuedBackgroundNotice{
		sessionID: sessionID,
		intent:    steerMessagesWithPersistenceIntent(steeringMessageEventDefault, true, []llm.Message{msg}),
	}
	b.mu.Lock()
	b.pending = append(b.pending, notice)
	b.mu.Unlock()
	if b.engine.stepLifecycle.Snapshot() == nil {
		if _, err := b.flushPendingNotices(nil); err != nil {
			b.engine.surfaceRunError(err)
		}
	}
}

func (b *defaultBackgroundNoticeScheduler) drainPendingNotices() []queuedBackgroundNotice {
	b.mu.Lock()
	defer b.mu.Unlock()
	pending := append([]queuedBackgroundNotice(nil), b.pending...)
	b.pending = nil
	return pending
}

func (b *defaultBackgroundNoticeScheduler) restorePendingNotices(notices []queuedBackgroundNotice) {
	if len(notices) == 0 {
		return
	}
	b.mu.Lock()
	b.pending = append(append([]queuedBackgroundNotice(nil), notices...), b.pending...)
	b.mu.Unlock()
}

func (b *defaultBackgroundNoticeScheduler) flushPendingNotices(stepID *string) (int, error) {
	pending := b.drainPendingNotices()
	flushed := 0
	for index, notice := range pending {
		var receipt session.CommitReceipt
		var err error
		if stepID == nil {
			receipt, err = b.engine.steerRuntimeWithCommitReceipt(notice.intent)
		} else {
			receipt, err = b.engine.steerWithCommitReceipt(*stepID, notice.intent)
		}
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
	return removed
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

func (b *defaultBackgroundNoticeScheduler) pendingSnapshot() []queuedBackgroundNotice {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]queuedBackgroundNotice(nil), b.pending...)
}
