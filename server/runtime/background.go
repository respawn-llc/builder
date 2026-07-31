package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	shelltool "core/server/tools/shell"
	"core/shared/textutil"
	"core/shared/toolspec"

	"github.com/google/uuid"
)

// BackgroundDeliveryRetirementSnapshot is the scheduler-owned answer to
// whether a resource must stay alive for background delivery. Changed closes
// whenever that answer may have changed.
type BackgroundDeliveryRetirementSnapshot struct {
	Active  bool
	Changed <-chan struct{}
}

// BackgroundDeliveryWithdrawal is the scheduler-owned classification returned
// when a Workflow Exact Execution Scope retires. CompletionPending means
// Manager must reclaim its terminal handoff; Diagnostic, when present, remains
// an explicit Resume obligation even if the completion already finalized.
type BackgroundDeliveryWithdrawal struct {
	CompletionPending bool
	Diagnostic        *PendingBackgroundDeliveryDiagnostic
}

type backgroundNoticeConsumption struct {
	removed           bool
	retainsDiagnostic bool
}

type defaultBackgroundNoticeScheduler struct {
	engine *Engine
	steps  exclusiveStepLifecycle

	mu              sync.Mutex
	states          []backgroundNoticeState
	task            backgroundLifecycleTask
	nextTask        uint64
	nextReservation uint64
	nextRetry       uint64
	retryPermit     *deferredBackgroundRetryPermit
	withdrawn       map[backgroundNoticeIdentity]BackgroundDeliveryWithdrawal
	changed         chan struct{}
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
// completion. A durable owner-poll receipt may call it after the Manager has
// accepted the owner-relative disposition. Any delivery diagnostic remains as
// diagnostic-only work and therefore continues to block retirement.
func (e *Engine) DiscardProvisionalBackgroundNotice(processID string) bool {
	e.ensureOrchestrationCollaborators()
	consumption := e.backgroundFlow.ConsumePendingBackgroundNotice(processID)
	if consumption.retainsDiagnostic {
		e.backgroundFlow.ScheduleIfIdle()
	}
	return consumption.removed
}

// FinalizeBackgroundOwnerPoll atomically removes the caller Engine's
// provisional completion and installs a Manager-transferred diagnostic as
// diagnostic-only work. It runs only after the owner poll commits durably.
func (e *Engine) FinalizeBackgroundOwnerPoll(
	processID string,
	diagnostic *PendingBackgroundDeliveryDiagnostic,
) backgroundNoticeConsumption {
	e.ensureOrchestrationCollaborators()
	return e.backgroundFlow.FinalizeOwnerPoll(processID, diagnostic)
}

func (e *Engine) ScheduleBackgroundNoticesIfIdle() {
	e.ensureOrchestrationCollaborators()
	e.backgroundFlow.ScheduleIfIdle()
}

// BackgroundDeliveryRetirementSnapshot exposes the scheduler's single source
// of truth to Session runtime retention. It deliberately does not expose the
// queue representation or let another package mutate scheduler state.
func (e *Engine) BackgroundDeliveryRetirementSnapshot() BackgroundDeliveryRetirementSnapshot {
	e.ensureOrchestrationCollaborators()
	return e.backgroundFlow.RetirementSnapshot()
}

// PermitBackgroundDeliveryRetry grants exactly one externally triggered retry
// for the current deferred generation. Lifecycle tasks never grant permits to
// themselves, which prevents a failed automatic delivery from spinning.
func (e *Engine) PermitBackgroundDeliveryRetry() bool {
	e.ensureOrchestrationCollaborators()
	return e.backgroundFlow.PermitRetry()
}

func (e *Engine) AttachBackgroundDeliveryDiagnostic(diagnostic PendingBackgroundDeliveryDiagnostic) bool {
	e.ensureOrchestrationCollaborators()
	return e.backgroundFlow.AttachDiagnostic(diagnostic)
}

// WithdrawBackgroundDelivery joins the matching receipt classification before
// returning. It is the only scheduler operation Workflow retirement uses to
// reclaim uncommitted background work from a closing Engine.
func (e *Engine) WithdrawBackgroundDelivery(
	ctx context.Context,
	processID string,
	activity uuid.UUID,
) (BackgroundDeliveryWithdrawal, bool, error) {
	e.ensureOrchestrationCollaborators()
	return e.backgroundFlow.Withdraw(ctx, processID, activity)
}

func (b *defaultBackgroundNoticeScheduler) HandleBackgroundShellUpdate(evt BackgroundShellEvent, queueNotice bool) {
	_ = b.engine.steer("", steerEventIntent(Event{Kind: EventBackgroundUpdated, Background: &evt}))
	if !queueNotice || !evt.Type.IsTerminal() {
		return
	}
	b.queueBackgroundShellNotice(evt, backgroundNoticeMessage(evt), true)
}

func (b *defaultBackgroundNoticeScheduler) AdmitBackgroundShellUpdate(evt BackgroundShellEvent) {
	if !evt.Type.IsTerminal() {
		panic(fmt.Sprintf("background admission requires terminal event: process_id=%q type=%q", evt.ID, evt.Type))
	}
	_ = b.engine.steer("", steerEventIntent(Event{Kind: EventBackgroundUpdated, Background: &evt}))
	b.queueBackgroundShellNotice(evt, backgroundNoticeMessage(evt), false)
}

func backgroundNoticeMessage(evt BackgroundShellEvent) llm.Message {
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
	if msg.Content == nil || strings.TrimSpace(*msg.Content) == "" {
		return
	}
	var scheduled backgroundLifecycleTask
	b.admitNotice(newDeveloperBackgroundNotice(
		steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{msg}),
	), true, &scheduled)
	b.launchIfScheduled(scheduled)
}

func (b *defaultBackgroundNoticeScheduler) queueBackgroundShellNotice(evt BackgroundShellEvent, msg llm.Message, schedule bool) {
	if msg.Content == nil || strings.TrimSpace(*msg.Content) == "" {
		return
	}
	var scheduled backgroundLifecycleTask
	b.admitNotice(newTerminalBackgroundNotice(
		evt.ID,
		evt.ActivityID,
		steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{msg}),
	), schedule, &scheduled)
	b.launchIfScheduled(scheduled)
}

func (b *defaultBackgroundNoticeScheduler) launchIfScheduled(task backgroundLifecycleTask) {
	if task == nil {
		return
	}
	if !b.engine.launchLifecycleTask(func(engineCtx context.Context) {
		b.processQueuedNotices(engineCtx, task)
	}) {
		b.clearScheduledTask(task)
	}
}

func (b *defaultBackgroundNoticeScheduler) admitNotice(
	notice queuedBackgroundNotice,
	schedule bool,
	scheduled *backgroundLifecycleTask,
) {
	b.mu.Lock()
	b.ensureChangedLocked()
	b.states = append(b.states, pendingBackgroundNotice{notice: notice})
	// A newly admitted notice is an external lifecycle boundary. It may carry
	// one previously deferred completion with it, but a failed task can never
	// schedule itself after it returns.
	b.permitEarliestRetryLocked()
	b.signalChangedLocked()
	if schedule && b.task == nil && (b.steps == nil || !b.steps.IsBusy()) {
		*scheduled = b.scheduleTaskLocked()
	}
	b.mu.Unlock()
}

// DrainPendingNotices reserves currently deliverable notices for the existing
// user-injection flush. The receipt is applied later by
// FinalizeCommittedBackgroundNotice; this method never treats harvesting as
// durable acceptance.
func (b *defaultBackgroundNoticeScheduler) DrainPendingNotices() []queuedBackgroundNotice {
	b.mu.Lock()
	defer b.mu.Unlock()
	reserved := b.reserveDeliverableLocked()
	return backgroundNoticesFromStates(reserved)
}

func (b *defaultBackgroundNoticeScheduler) HasPendingNotices() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.hasRetirementWorkLocked()
}

func (b *defaultBackgroundNoticeScheduler) ConsumePendingBackgroundNotice(processID string) backgroundNoticeConsumption {
	processID = strings.TrimSpace(processID)
	if processID == "" {
		return backgroundNoticeConsumption{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensureChangedLocked()
	result := backgroundNoticeConsumption{}
	next := make([]backgroundNoticeState, 0, len(b.states))
	for _, state := range b.states {
		notice, hasNotice := state.queuedBackgroundNotice()
		if !hasNotice || notice.processID() != processID {
			next = append(next, state)
			continue
		}
		result.removed = true
		if notice.diagnostic != nil {
			next = append(next, newDiagnosticOnlyBackgroundNotice(*notice.diagnostic))
			result.retainsDiagnostic = true
		}
	}
	if result.removed {
		b.states = next
		b.signalChangedLocked()
	}
	return result
}

func (b *defaultBackgroundNoticeScheduler) FinalizeOwnerPoll(
	processID string,
	diagnostic *PendingBackgroundDeliveryDiagnostic,
) backgroundNoticeConsumption {
	processID = strings.TrimSpace(processID)
	if processID == "" {
		return backgroundNoticeConsumption{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensureChangedLocked()
	result := backgroundNoticeConsumption{}
	next := make([]backgroundNoticeState, 0, len(b.states))
	for _, state := range b.states {
		notice, hasNotice := state.queuedBackgroundNotice()
		if !hasNotice || notice.processID() != processID {
			next = append(next, state)
			continue
		}
		result.removed = true
		if notice.diagnostic != nil {
			if diagnostic != nil {
				panic(fmt.Sprintf(
					"owner poll finalization has duplicate delivery diagnostics: process_id=%q",
					processID,
				))
			}
			copy := *notice.diagnostic
			diagnostic = &copy
		}
	}
	if diagnostic != nil {
		next = append(next, newDiagnosticOnlyBackgroundNotice(*diagnostic))
		result.retainsDiagnostic = true
	}
	if result.removed || diagnostic != nil {
		b.states = next
		b.signalChangedLocked()
	}
	return result
}

func (b *defaultBackgroundNoticeScheduler) Withdraw(
	ctx context.Context,
	processID string,
	activity uuid.UUID,
) (BackgroundDeliveryWithdrawal, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	identity := newBackgroundNoticeIdentity(processID, activity)
withdrawalLoop:
	for {
		b.mu.Lock()
		b.ensureChangedLocked()
		if result, withdrawn := b.withdrawn[identity]; withdrawn {
			delete(b.withdrawn, identity)
			b.mu.Unlock()
			return result, true, nil
		}
		for index, state := range b.states {
			notice, hasNotice := state.queuedBackgroundNotice()
			if hasNotice && notice.identity != nil && *notice.identity == identity {
				switch current := state.(type) {
				case pendingBackgroundNotice,
					retryDeferredBackgroundNotice,
					preparationRecoveryDiagnosticBackgroundNotice,
					preparationRecoveryBackgroundNotice:
					currentNotice, ok := state.queuedBackgroundNotice()
					if !ok {
						panic(fmt.Sprintf("withdraw background delivery has no notice state %T", state))
					}
					if b.task == nil {
						b.states = append(b.states[:index], b.states[index+1:]...)
						b.signalChangedLocked()
						b.mu.Unlock()
						return backgroundDeliveryWithdrawalForNotice(currentNotice), true, nil
					}
					b.states[index] = newWithdrawingBackgroundNotice(currentNotice, backgroundLifecycleTaskAttempt(b.task))
					task := b.task
					b.signalChangedLocked()
					b.mu.Unlock()
					if err := cancelAndJoinBackgroundLifecycleTask(ctx, task); err != nil {
						return BackgroundDeliveryWithdrawal{}, false, err
					}
					continue withdrawalLoop
				case reservedBackgroundNotice:
					b.states[index] = newWithdrawingBackgroundNotice(current.notice, current.reservation)
					b.signalChangedLocked()
					task := b.task
					b.mu.Unlock()
					if task != nil {
						if err := cancelAndJoinBackgroundLifecycleTask(ctx, task); err != nil {
							return BackgroundDeliveryWithdrawal{}, false, err
						}
					}
					continue withdrawalLoop
				case reservedPreparationRecoveryBackgroundNotice:
					b.states[index] = newWithdrawingBackgroundNotice(current.notice, current.reservation)
					b.signalChangedLocked()
					task := b.task
					b.mu.Unlock()
					if task != nil {
						if err := cancelAndJoinBackgroundLifecycleTask(ctx, task); err != nil {
							return BackgroundDeliveryWithdrawal{}, false, err
						}
					}
					continue withdrawalLoop
				case withdrawingBackgroundNotice:
					task := b.task
					if task == nil {
						b.states = append(b.states[:index], b.states[index+1:]...)
						b.signalChangedLocked()
						b.mu.Unlock()
						return backgroundDeliveryWithdrawalForNotice(current.notice), true, nil
					}
					b.mu.Unlock()
					if err := cancelAndJoinBackgroundLifecycleTask(ctx, task); err != nil {
						return BackgroundDeliveryWithdrawal{}, false, err
					}
					continue withdrawalLoop
				default:
					panic(fmt.Sprintf("withdraw background delivery has invalid notice state %T", state))
				}
			}
			diagnosticOnly, ok := state.(diagnosticOnlyBackgroundNotice)
			if ok && diagnosticOnly.diagnostic.processID == identity.processID &&
				diagnosticOnly.diagnostic.activity == identity.activity {
				if b.task != nil {
					b.states[index] = newWithdrawingDiagnosticOnlyBackgroundNotice(
						diagnosticOnly.diagnostic,
						backgroundLifecycleTaskAttempt(b.task),
					)
					task := b.task
					b.signalChangedLocked()
					b.mu.Unlock()
					if err := cancelAndJoinBackgroundLifecycleTask(ctx, task); err != nil {
						return BackgroundDeliveryWithdrawal{}, false, err
					}
					continue withdrawalLoop
				}
				b.states = append(b.states[:index], b.states[index+1:]...)
				b.signalChangedLocked()
				b.mu.Unlock()
				diagnostic := diagnosticOnly.diagnostic
				return BackgroundDeliveryWithdrawal{Diagnostic: &diagnostic}, true, nil
			}
			withdrawingDiagnosticOnly, ok := state.(withdrawingDiagnosticOnlyBackgroundNotice)
			if ok && withdrawingDiagnosticOnly.diagnostic.processID == identity.processID &&
				withdrawingDiagnosticOnly.diagnostic.activity == identity.activity {
				task := b.task
				if task == nil {
					b.states = append(b.states[:index], b.states[index+1:]...)
					b.signalChangedLocked()
					b.mu.Unlock()
					diagnostic := withdrawingDiagnosticOnly.diagnostic
					return BackgroundDeliveryWithdrawal{Diagnostic: &diagnostic}, true, nil
				}
				b.mu.Unlock()
				if err := cancelAndJoinBackgroundLifecycleTask(ctx, task); err != nil {
					return BackgroundDeliveryWithdrawal{}, false, err
				}
				continue withdrawalLoop
			}
		}
		b.mu.Unlock()
		return BackgroundDeliveryWithdrawal{}, false, nil
	}
}

func cancelAndJoinBackgroundLifecycleTask(ctx context.Context, task backgroundLifecycleTask) error {
	if ctx == nil {
		ctx = context.Background()
	}
	control := backgroundLifecycleTaskControlFor(task)
	control.cancel()
	select {
	case <-control.done:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func backgroundDeliveryWithdrawalForNotice(notice queuedBackgroundNotice) BackgroundDeliveryWithdrawal {
	result := BackgroundDeliveryWithdrawal{CompletionPending: true}
	if notice.diagnostic != nil {
		diagnostic := *notice.diagnostic
		result.Diagnostic = &diagnostic
	}
	return result
}

func (b *defaultBackgroundNoticeScheduler) ScheduleIfIdle() {
	if b.steps != nil && b.steps.IsBusy() {
		return
	}
	var scheduled backgroundLifecycleTask
	b.mu.Lock()
	if b.task == nil {
		// This call comes from an idle boundary outside a completed lifecycle
		// task. It is therefore a valid external retry trigger.
		b.permitEarliestRetryLocked()
	}
	if b.task == nil && b.hasScheduledWorkLocked() {
		scheduled = b.scheduleTaskLocked()
	}
	b.mu.Unlock()
	b.launchIfScheduled(scheduled)
}

func (b *defaultBackgroundNoticeScheduler) PermitRetry() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.permitEarliestRetryLocked()
}

func (b *defaultBackgroundNoticeScheduler) AttachDiagnostic(diagnostic PendingBackgroundDeliveryDiagnostic) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensureChangedLocked()
	for index, state := range b.states {
		notice, hasNotice := state.queuedBackgroundNotice()
		if !hasNotice || !notice.matches(diagnostic.processID, diagnostic.activity) {
			continue
		}
		if notice.diagnostic != nil {
			return false
		}
		notice.diagnostic = &diagnostic
		switch current := state.(type) {
		case pendingBackgroundNotice:
			current.notice = notice
			b.states[index] = current
		case reservedBackgroundNotice:
			current.notice = notice
			b.states[index] = current
		case retryDeferredBackgroundNotice:
			current.notice = notice
			b.states[index] = current
		case preparationRecoveryDiagnosticBackgroundNotice:
			current.notice = notice
			b.states[index] = current
		case preparationRecoveryBackgroundNotice:
			current.notice = notice
			b.states[index] = current
		case reservedPreparationRecoveryBackgroundNotice:
			current.notice = notice
			b.states[index] = current
		case withdrawingBackgroundNotice:
			current.notice = notice
			b.states[index] = current
		default:
			return false
		}
		b.signalChangedLocked()
		return true
	}
	return false
}

func (b *defaultBackgroundNoticeScheduler) permitEarliestRetryLocked() bool {
	if b.retryPermit != nil {
		return false
	}
	var generation uint64
	for _, state := range b.states {
		retry, ok := state.(retryDeferredBackgroundNotice)
		if !ok {
			continue
		}
		if generation == 0 || retry.generation < generation {
			generation = retry.generation
		}
	}
	if generation == 0 {
		return false
	}
	permit := newDeferredBackgroundRetryPermit(generation)
	b.retryPermit = &permit
	b.signalChangedLocked()
	return true
}

func (b *defaultBackgroundNoticeScheduler) RetirementSnapshot() BackgroundDeliveryRetirementSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensureChangedLocked()
	return BackgroundDeliveryRetirementSnapshot{
		Active:  b.hasRetirementWorkLocked(),
		Changed: b.changed,
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

func (b *defaultBackgroundNoticeScheduler) processQueuedNotices(
	engineCtx context.Context,
	task backgroundLifecycleTask,
) {
	running, started := b.beginTask(task)
	if !started {
		return
	}
	control := backgroundLifecycleTaskControlFor(running)
	ctx, cancel := context.WithCancel(engineCtx)
	stopCancellation := context.AfterFunc(control.ctx, cancel)
	defer stopCancellation()
	defer cancel()
	defer b.finishTask(running)
	_, _ = b.runQueuedNotices(ctx)
}

func (b *defaultBackgroundNoticeScheduler) runQueuedNotices(ctx context.Context) (assistant llm.Message, err error) {
	if !b.hasDeliverableNotice() {
		return llm.Message{}, b.runPendingDeliveryDiagnostics(ctx)
	}
	err = b.steps.Run(ctx, exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindBackground}, func(stepCtx context.Context, stepID string) error {
		reserved := b.reserveDeliverable()
		if len(reserved) == 0 {
			return nil
		}
		if err := b.engine.ensureMetaContextForRequest(stepCtx, stepID); err != nil {
			b.restorePreDeliveryReservations(reserved, err)
			return err
		}
		accepted := false
		for index, notice := range reserved {
			if err := b.reserveAutomaticDisposition(notice); err != nil {
				b.restoreUncommittedReservations(reserved[index:], err)
				return err
			}
			if notice.diagnostic != nil {
				diagnosticReceipt, diagnosticErr := b.engine.CommitPendingBackgroundDeliveryDiagnostic(*notice.diagnostic)
				if !diagnosticReceipt.Committed {
					b.restoreAutomaticDisposition(notice)
					b.restoreUncommittedReservations(reserved[index:], diagnosticErr)
					return diagnosticErr
				}
				b.clearCommittedDeliveryDiagnostic(notice.processID(), notice.activityID())
				notice.diagnostic = nil
				if diagnosticErr != nil {
					slog.Error(
						"background delivery diagnostic committed with observer error",
						"process_id", notice.processID(),
						"activity_id", notice.activityID().String(),
						"error", diagnosticErr,
					)
				}
			}
			receipt, steerErr := b.engine.steerWithCommitReceipt(stepID, notice.intent)
			if !receipt.Committed {
				b.restoreAutomaticDisposition(notice)
			}
			settlement := b.FinalizeCommittedBackgroundNotice(notice, receipt)
			switch settlement {
			case shelltool.TerminalAutomaticallyFinalized:
				accepted = true
			case shelltool.TerminalAlreadyFinalizedByOwnerPoll:
			case shelltool.TerminalAutomaticFinalizationRejected:
			default:
				panic(fmt.Sprintf("unknown automatic terminal settlement %d", settlement))
			}
			if steerErr != nil {
				if !receipt.Committed && notice.hasIdentity() {
					diagnostic := newPendingBackgroundDeliveryDiagnostic(
						notice.processID(),
						notice.activityID(),
						backgroundDeliveryStageAutomaticSteering,
						nextBackgroundDeliveryAttempt(notice.diagnostic),
						steerErr,
					)
					notice.diagnostic = &diagnostic
					slog.Error(
						"background completion delivery failed",
						"process_id", notice.processID(),
						"activity_id", notice.activityID().String(),
						"stage", backgroundDeliveryStageAutomaticSteering,
						"attempt", diagnostic.attempt,
						"committed", false,
						"error", steerErr,
					)
				}
				if !receipt.Committed {
					b.restoreUncommittedReservations(append([]queuedBackgroundNotice{notice}, reserved[index+1:]...), steerErr)
				} else {
					b.restoreUncommittedReservations(reserved[index+1:], nil)
				}
				return steerErr
			}
		}
		if !accepted {
			return nil
		}
		msg, runErr := b.engine.runStepLoop(stepCtx, stepID)
		assistant = msg
		return runErr
	})
	if errors.Is(err, ErrAgentBusy) {
		return llm.Message{}, nil
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		if diagnosticErr := b.commitPendingDeliveryDiagnostics(); diagnosticErr != nil {
			return llm.Message{}, errors.Join(err, diagnosticErr)
		}
	}
	return assistant, err
}

func (b *defaultBackgroundNoticeScheduler) reserveAutomaticDisposition(notice queuedBackgroundNotice) error {
	if !notice.hasIdentity() || b.engine.cfg.BackgroundAutomaticReservation == nil {
		return nil
	}
	if b.engine.cfg.BackgroundAutomaticReservation(notice.processID(), notice.activityID()) {
		return nil
	}
	return fmt.Errorf(
		"reserve automatic background terminal disposition: process_id=%s activity_id=%s",
		notice.processID(),
		notice.activityID(),
	)
}

func (b *defaultBackgroundNoticeScheduler) restoreAutomaticDisposition(notice queuedBackgroundNotice) {
	if !notice.hasIdentity() || b.engine.cfg.BackgroundAutomaticRollback == nil {
		return
	}
	if !b.engine.cfg.BackgroundAutomaticRollback(notice.processID(), notice.activityID()) {
		slog.Error(
			"restore automatic background terminal disposition failed",
			"process_id", notice.processID(),
			"activity_id", notice.activityID().String(),
		)
	}
}

func (b *defaultBackgroundNoticeScheduler) ReserveAutomaticDisposition(notice queuedBackgroundNotice) error {
	return b.reserveAutomaticDisposition(notice)
}

func (b *defaultBackgroundNoticeScheduler) RestoreAutomaticDisposition(notice queuedBackgroundNotice) {
	b.restoreAutomaticDisposition(notice)
}

func nextBackgroundDeliveryAttempt(previous *PendingBackgroundDeliveryDiagnostic) uint64 {
	if previous == nil {
		return 1
	}
	return previous.attempt + 1
}

func (b *defaultBackgroundNoticeScheduler) runPendingDeliveryDiagnostics(ctx context.Context) error {
	if !b.hasPendingDeliveryDiagnostics() {
		return nil
	}
	return b.steps.Run(ctx, exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindBackground}, func(context.Context, string) error {
		return b.commitPendingDeliveryDiagnostics()
	})
}

func (b *defaultBackgroundNoticeScheduler) commitPendingDeliveryDiagnostics() error {
	for _, diagnostic := range b.pendingDiagnosticsSnapshot() {
		receipt, err := b.engine.commitBackgroundDeliveryDiagnostic(diagnostic)
		if receipt.Committed {
			b.clearCommittedDeliveryDiagnostic(diagnostic.processID, diagnostic.activity)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (b *defaultBackgroundNoticeScheduler) pendingDiagnosticsSnapshot() []PendingBackgroundDeliveryDiagnostic {
	b.mu.Lock()
	defer b.mu.Unlock()
	diagnostics := make([]PendingBackgroundDeliveryDiagnostic, 0)
	for _, state := range b.states {
		switch current := state.(type) {
		case pendingBackgroundNotice:
			if current.notice.diagnostic != nil {
				diagnostics = append(diagnostics, *current.notice.diagnostic)
			}
		case reservedBackgroundNotice:
			if current.notice.diagnostic != nil {
				diagnostics = append(diagnostics, *current.notice.diagnostic)
			}
		case retryDeferredBackgroundNotice:
			if current.notice.diagnostic != nil {
				diagnostics = append(diagnostics, *current.notice.diagnostic)
			}
		case preparationRecoveryDiagnosticBackgroundNotice:
			diagnostics = append(diagnostics, *current.notice.diagnostic)
		case preparationRecoveryBackgroundNotice:
			if current.notice.diagnostic != nil {
				diagnostics = append(diagnostics, *current.notice.diagnostic)
			}
		case reservedPreparationRecoveryBackgroundNotice:
			if current.notice.diagnostic != nil {
				diagnostics = append(diagnostics, *current.notice.diagnostic)
			}
		case withdrawingBackgroundNotice:
			// Workflow withdrawal owns this diagnostic until the lifecycle
			// task is canceled and joined. It must not commit post-stop.
		case withdrawingDiagnosticOnlyBackgroundNotice:
			// Workflow withdrawal owns this diagnostic until the lifecycle task
			// is canceled and joined. It must not commit post-stop.
		case diagnosticOnlyBackgroundNotice:
			diagnostics = append(diagnostics, current.diagnostic)
		default:
			panic(fmt.Sprintf("unknown background notice state %T", state))
		}
	}
	return diagnostics
}

func (b *defaultBackgroundNoticeScheduler) clearCommittedDeliveryDiagnostic(processID string, activity uuid.UUID) {
	settled := make([]string, 0, 1)
	b.mu.Lock()
	b.ensureChangedLocked()
	next := make([]backgroundNoticeState, 0, len(b.states))
	for _, state := range b.states {
		switch current := state.(type) {
		case diagnosticOnlyBackgroundNotice:
			if current.diagnostic.processID == processID && current.diagnostic.activity == activity {
				settled = append(settled, processID)
				continue
			}
		case pendingBackgroundNotice:
			if current.notice.matches(processID, activity) {
				current.notice.diagnostic = nil
				state = current
			}
		case reservedBackgroundNotice:
			if current.notice.matches(processID, activity) {
				current.notice.diagnostic = nil
				state = current
			}
		case retryDeferredBackgroundNotice:
			if current.notice.matches(processID, activity) {
				current.notice.diagnostic = nil
				state = current
			}
		case preparationRecoveryDiagnosticBackgroundNotice:
			if current.notice.matches(processID, activity) {
				current.notice.diagnostic = nil
				state = newPreparationRecoveryBackgroundNotice(current.notice)
			}
		case preparationRecoveryBackgroundNotice:
			if current.notice.matches(processID, activity) {
				current.notice.diagnostic = nil
				state = current
			}
		case reservedPreparationRecoveryBackgroundNotice:
			if current.notice.matches(processID, activity) {
				current.notice.diagnostic = nil
				state = current
			}
		case withdrawingBackgroundNotice:
			if current.notice.matches(processID, activity) {
				current.notice.diagnostic = nil
				state = current
			}
		case withdrawingDiagnosticOnlyBackgroundNotice:
			if current.diagnostic.processID == processID && current.diagnostic.activity == activity {
				state = current
			}
		default:
			panic(fmt.Sprintf("unknown background notice state %T", state))
		}
		next = append(next, state)
	}
	b.states = next
	b.signalChangedLocked()
	b.mu.Unlock()
	for _, settledProcessID := range settled {
		b.notifyCompletionSettled(settledProcessID)
	}
}

// FinalizeCommittedBackgroundNotice applies a durable steering receipt to its
// exact reservation. The Manager finalizer remains the authority for terminal
// disposition; an accepted automatic outcome removes the notice or retains
// only its diagnostic obligation. An owner-poll outcome was already consumed
// by the owner finalizer and must not start an automatic continuation.
func (b *defaultBackgroundNoticeScheduler) FinalizeCommittedBackgroundNotice(
	notice queuedBackgroundNotice,
	receipt session.CommitReceipt,
) shelltool.TerminalAutomaticFinalization {
	if !receipt.Committed {
		return shelltool.TerminalAutomaticFinalizationRejected
	}
	settlement := shelltool.TerminalAutomaticallyFinalized
	if notice.hasIdentity() && b.engine.cfg.BackgroundAutomaticFinalizer != nil {
		settlement = b.engine.cfg.BackgroundAutomaticFinalizer(notice.processID(), notice.activityID())
	}
	if !settlement.AutomaticallyFinalized() {
		return settlement
	}
	notify := ""
	b.mu.Lock()
	b.ensureChangedLocked()
	next := make([]backgroundNoticeState, 0, len(b.states))
	for _, state := range b.states {
		current, hasNotice := state.queuedBackgroundNotice()
		if !hasNotice || !sameBackgroundNotice(current, notice) {
			next = append(next, state)
			continue
		}
		if _, withdrawing := state.(withdrawingBackgroundNotice); withdrawing {
			if b.withdrawn == nil {
				b.withdrawn = make(map[backgroundNoticeIdentity]BackgroundDeliveryWithdrawal)
			}
			b.withdrawn[*current.identity] = BackgroundDeliveryWithdrawal{
				Diagnostic: cloneBackgroundDeliveryDiagnostic(current.diagnostic),
			}
			if current.diagnostic == nil {
				notify = current.processID()
			}
			continue
		}
		if current.diagnostic != nil {
			next = append(next, newDiagnosticOnlyBackgroundNotice(*current.diagnostic))
		} else if current.hasIdentity() {
			notify = current.processID()
		}
	}
	b.states = next
	b.signalChangedLocked()
	b.mu.Unlock()
	if notify != "" {
		b.notifyCompletionSettled(notify)
	}
	return settlement
}

func sameBackgroundNotice(left queuedBackgroundNotice, right queuedBackgroundNotice) bool {
	if left.key == uuid.Nil || right.key == uuid.Nil {
		panic("background notice requires UUIDv4 key")
	}
	if left.identity == nil || right.identity == nil {
		return left.identity == nil && right.identity == nil && left.key == right.key
	}
	return left.identity.processID == right.identity.processID && left.identity.activity == right.identity.activity
}

func (b *defaultBackgroundNoticeScheduler) notifyCompletionSettled(processID string) {
	if b.engine.cfg.BackgroundCompletionSettled != nil {
		b.engine.cfg.BackgroundCompletionSettled(processID)
	}
}

func (b *defaultBackgroundNoticeScheduler) hasDeliverableNotice() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.hasDeliverableNoticeLocked()
}

func (b *defaultBackgroundNoticeScheduler) reserveDeliverable() []queuedBackgroundNotice {
	b.mu.Lock()
	defer b.mu.Unlock()
	reserved := b.reserveDeliverableLocked()
	return backgroundNoticesFromStates(reserved)
}

func (b *defaultBackgroundNoticeScheduler) reserveDeliverableLocked() []backgroundNoticeState {
	b.ensureChangedLocked()
	b.nextReservation = nextBackgroundLifecycleAttempt(b.nextReservation)
	reservation := b.nextReservation
	reserved := make([]backgroundNoticeState, 0)
	next := make([]backgroundNoticeState, 0, len(b.states))
	for _, state := range b.states {
		switch current := state.(type) {
		case pendingBackgroundNotice:
			reservationState := newReservedBackgroundNotice(current.notice, reservation)
			next = append(next, reservationState)
			reserved = append(reserved, reservationState)
		case preparationRecoveryBackgroundNotice:
			if current.notice.diagnostic != nil {
				next = append(next, state)
				continue
			}
			reservationState := newReservedPreparationRecoveryBackgroundNotice(current.notice, reservation)
			next = append(next, reservationState)
			reserved = append(reserved, reservationState)
		case retryDeferredBackgroundNotice:
			if b.retryPermit != nil && b.retryPermit.generation == current.generation {
				reservationState := newReservedBackgroundNotice(current.notice, reservation)
				next = append(next, reservationState)
				reserved = append(reserved, reservationState)
				continue
			}
			next = append(next, state)
		default:
			next = append(next, state)
		}
	}
	if len(reserved) > 0 && b.retryPermit != nil {
		b.retryPermit = nil
	}
	if len(reserved) > 0 {
		b.states = next
		b.signalChangedLocked()
	}
	return reserved
}

func (b *defaultBackgroundNoticeScheduler) restoreUncommittedReservations(notices []queuedBackgroundNotice, cause error) {
	if len(notices) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensureChangedLocked()
	b.nextRetry = nextBackgroundLifecycleAttempt(b.nextRetry)
	for _, notice := range notices {
		for index, state := range b.states {
			reserved, ok := state.(reservedBackgroundNotice)
			preparationRecoveryReserved, preparationRecoveryState := state.(reservedPreparationRecoveryBackgroundNotice)
			withdrawing, withdrawingState := state.(withdrawingBackgroundNotice)
			if !ok && !preparationRecoveryState && !withdrawingState {
				continue
			}
			current := reserved.notice
			if preparationRecoveryState {
				current = preparationRecoveryReserved.notice
			}
			if withdrawingState {
				current = withdrawing.notice
			}
			if !sameBackgroundNotice(current, notice) {
				continue
			}
			if cause != nil && notice.diagnostic != nil {
				current.diagnostic = notice.diagnostic
			}
			if withdrawingState {
				if b.withdrawn == nil {
					b.withdrawn = make(map[backgroundNoticeIdentity]BackgroundDeliveryWithdrawal)
				}
				b.withdrawn[*current.identity] = backgroundDeliveryWithdrawalForNotice(current)
				b.states = append(b.states[:index], b.states[index+1:]...)
				break
			}
			b.states[index] = newRetryDeferredBackgroundNotice(current, b.nextRetry)
			break
		}
	}
	b.signalChangedLocked()
}

// restorePreDeliveryReservations preserves the first automatic request-
// preparation failure as a durable diagnostic followed by one scheduler-owned
// retry. A failed recovery reservation falls back to the ordinary externally
// permitted retry path, so no lifecycle task can self-retry indefinitely.
func (b *defaultBackgroundNoticeScheduler) restorePreDeliveryReservations(
	notices []queuedBackgroundNotice,
	cause error,
) {
	if len(notices) == 0 {
		return
	}
	if cause == nil {
		panic("restore pre-delivery reservations requires a cause")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensureChangedLocked()
	b.nextRetry = nextBackgroundLifecycleAttempt(b.nextRetry)
	for _, notice := range notices {
		for index, state := range b.states {
			switch current := state.(type) {
			case reservedBackgroundNotice:
				if !sameBackgroundNotice(current.notice, notice) {
					continue
				}
				recovery, recoverable := preparationRecoveryNotice(current.notice, cause)
				if recoverable {
					b.states[index] = newPreparationRecoveryDiagnosticBackgroundNotice(recovery)
				} else {
					b.states[index] = newRetryDeferredBackgroundNotice(current.notice, b.nextRetry)
				}
			case reservedPreparationRecoveryBackgroundNotice:
				if !sameBackgroundNotice(current.notice, notice) {
					continue
				}
				retry, recoverable := preparationRecoveryNotice(current.notice, cause)
				if recoverable {
					b.states[index] = newRetryDeferredBackgroundNotice(retry, b.nextRetry)
				} else {
					b.states[index] = newRetryDeferredBackgroundNotice(current.notice, b.nextRetry)
				}
			case withdrawingBackgroundNotice:
				if !sameBackgroundNotice(current.notice, notice) {
					continue
				}
				withdrawn := current.notice
				if recovery, recoverable := preparationRecoveryNotice(withdrawn, cause); recoverable {
					withdrawn = recovery
				}
				if b.withdrawn == nil {
					b.withdrawn = make(map[backgroundNoticeIdentity]BackgroundDeliveryWithdrawal)
				}
				b.withdrawn[*withdrawn.identity] = backgroundDeliveryWithdrawalForNotice(withdrawn)
				b.states = append(b.states[:index], b.states[index+1:]...)
			default:
				continue
			}
			break
		}
	}
	b.signalChangedLocked()
}

func preparationRecoveryNotice(
	notice queuedBackgroundNotice,
	cause error,
) (queuedBackgroundNotice, bool) {
	if !notice.hasIdentity() || notice.identity.activity.Version() != 4 {
		return notice, false
	}
	diagnostic := newPendingBackgroundDeliveryDiagnostic(
		notice.processID(),
		notice.activityID(),
		backgroundDeliveryStagePreparation,
		nextBackgroundDeliveryAttempt(notice.diagnostic),
		cause,
	)
	notice.diagnostic = &diagnostic
	return notice, true
}

func cloneBackgroundDeliveryDiagnostic(
	diagnostic *PendingBackgroundDeliveryDiagnostic,
) *PendingBackgroundDeliveryDiagnostic {
	if diagnostic == nil {
		return nil
	}
	copy := *diagnostic
	return &copy
}

// restoreRetryDeferredNoticesFront is retained as the scheduler's deep
// combined-flush recovery operation. The message lifecycle depends only on
// the scheduler contract and never mutates the queue itself.
func (b *defaultBackgroundNoticeScheduler) restoreRetryDeferredNoticesFront(notices []queuedBackgroundNotice) {
	b.restoreUncommittedReservations(notices, nil)
}

func (b *defaultBackgroundNoticeScheduler) RestoreUncommittedBackgroundNotices(notices []queuedBackgroundNotice) {
	b.restoreUncommittedReservations(notices, nil)
}

func (b *defaultBackgroundNoticeScheduler) pendingSnapshot() []queuedBackgroundNotice {
	b.mu.Lock()
	defer b.mu.Unlock()
	return backgroundNoticesFromStates(b.states)
}

func backgroundNoticesFromStates(states []backgroundNoticeState) []queuedBackgroundNotice {
	notices := make([]queuedBackgroundNotice, 0, len(states))
	for _, state := range states {
		notice, ok := state.queuedBackgroundNotice()
		if ok {
			notices = append(notices, notice)
		}
	}
	return notices
}

func (b *defaultBackgroundNoticeScheduler) ensureChangedLocked() {
	if b.changed == nil {
		b.changed = make(chan struct{})
	}
}

func (b *defaultBackgroundNoticeScheduler) signalChangedLocked() {
	b.ensureChangedLocked()
	close(b.changed)
	b.changed = make(chan struct{})
}

func (b *defaultBackgroundNoticeScheduler) hasDeliverableNoticeLocked() bool {
	for _, state := range b.states {
		switch current := state.(type) {
		case pendingBackgroundNotice:
			return true
		case retryDeferredBackgroundNotice:
			if b.retryPermit != nil && b.retryPermit.generation == current.generation {
				return true
			}
		case preparationRecoveryBackgroundNotice:
			if current.notice.diagnostic == nil {
				return true
			}
		}
	}
	return false
}

func (b *defaultBackgroundNoticeScheduler) hasPendingDeliveryDiagnostics() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.hasPendingDeliveryDiagnosticsLocked()
}

func (b *defaultBackgroundNoticeScheduler) hasPendingDeliveryDiagnosticsLocked() bool {
	for _, state := range b.states {
		switch current := state.(type) {
		case diagnosticOnlyBackgroundNotice:
			return true
		case pendingBackgroundNotice:
			if current.notice.diagnostic != nil {
				return true
			}
		case reservedBackgroundNotice:
			if current.notice.diagnostic != nil {
				return true
			}
		case retryDeferredBackgroundNotice:
			if current.notice.diagnostic != nil {
				return true
			}
		case preparationRecoveryDiagnosticBackgroundNotice:
			return true
		case preparationRecoveryBackgroundNotice:
			if current.notice.diagnostic != nil {
				return true
			}
		case reservedPreparationRecoveryBackgroundNotice:
			if current.notice.diagnostic != nil {
				return true
			}
		case withdrawingBackgroundNotice:
			// A withdrawing Workflow obligation is intentionally not
			// deliverable. Workflow retirement transfers it after task join.
		case withdrawingDiagnosticOnlyBackgroundNotice:
			// See withdrawingBackgroundNotice above.
		default:
			panic(fmt.Sprintf("unknown background notice state %T", state))
		}
	}
	return false
}

func (b *defaultBackgroundNoticeScheduler) hasScheduledWorkLocked() bool {
	return b.hasDeliverableNoticeLocked() || b.hasPendingDeliveryDiagnosticsLocked()
}

func (b *defaultBackgroundNoticeScheduler) hasReadyPreparationRecoveryLocked() bool {
	for _, state := range b.states {
		recovery, ok := state.(preparationRecoveryBackgroundNotice)
		if ok && recovery.notice.diagnostic == nil {
			return true
		}
	}
	return false
}

func (b *defaultBackgroundNoticeScheduler) hasRetirementWorkLocked() bool {
	return len(b.states) != 0 || b.task != nil
}

func (b *defaultBackgroundNoticeScheduler) scheduleTaskLocked() backgroundLifecycleTask {
	if b.task != nil {
		panic("schedule background lifecycle task while another task is active")
	}
	b.nextTask = nextBackgroundLifecycleAttempt(b.nextTask)
	task := newScheduledBackgroundLifecycleTask(b.nextTask)
	b.task = task
	b.signalChangedLocked()
	return task
}

func (b *defaultBackgroundNoticeScheduler) clearScheduledTask(task backgroundLifecycleTask) {
	if task == nil {
		return
	}
	cleared := false
	b.mu.Lock()
	scheduled, ok := b.task.(scheduledBackgroundLifecycleTask)
	if ok && sameBackgroundLifecycleTask(scheduled, task) {
		b.task = nil
		b.signalChangedLocked()
		cleared = true
	}
	b.mu.Unlock()
	if cleared {
		backgroundLifecycleTaskControlFor(task).doneOnce.Do(func() {
			close(backgroundLifecycleTaskControlFor(task).done)
		})
	}
}

func (b *defaultBackgroundNoticeScheduler) beginTask(task backgroundLifecycleTask) (backgroundLifecycleTask, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	scheduled, ok := b.task.(scheduledBackgroundLifecycleTask)
	if !ok || !sameBackgroundLifecycleTask(scheduled, task) {
		return nil, false
	}
	running := newRunningBackgroundLifecycleTask(scheduled)
	b.task = running
	b.signalChangedLocked()
	return running, true
}

func (b *defaultBackgroundNoticeScheduler) finishTask(task backgroundLifecycleTask) {
	if task == nil {
		return
	}
	finished := false
	var scheduled backgroundLifecycleTask
	b.mu.Lock()
	running, ok := b.task.(runningBackgroundLifecycleTask)
	if ok && sameBackgroundLifecycleTask(running, task) {
		b.task = nil
		b.signalChangedLocked()
		finished = true
		if b.hasReadyPreparationRecoveryLocked() && (b.steps == nil || !b.steps.IsBusy()) {
			scheduled = b.scheduleTaskLocked()
		}
	}
	b.mu.Unlock()
	if finished {
		backgroundLifecycleTaskControlFor(task).doneOnce.Do(func() {
			close(backgroundLifecycleTaskControlFor(task).done)
		})
	}
	b.launchIfScheduled(scheduled)
}
