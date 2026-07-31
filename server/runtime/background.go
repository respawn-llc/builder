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
	"core/shared/textutil"
	"core/shared/toolspec"

	"github.com/google/uuid"
)

type defaultBackgroundNoticeScheduler struct {
	engine *Engine
	steps  exclusiveStepLifecycle

	mu      sync.Mutex
	entries map[uuid.UUID]*backgroundNoticeEntry
	order   []uuid.UUID
	task    bool
}

type queuedBackgroundNotice struct {
	id        uuid.UUID
	processID string
	intent    steeringIntent
}

type backgroundNoticeClaim uint8

const (
	backgroundNoticeClaimStandalone backgroundNoticeClaim = iota + 1
	backgroundNoticeClaimCombinedFlush
)

type backgroundNoticeState uint8

const (
	backgroundNoticePending backgroundNoticeState = iota + 1
	backgroundNoticeClaimedStandalone
	backgroundNoticeClaimedCombinedFlush
	backgroundNoticeApplying
)

type backgroundNoticeDisposition uint8

const (
	backgroundNoticeApplied backgroundNoticeDisposition = iota + 1
	backgroundNoticeSuppressed
	backgroundNoticeFailed
	backgroundNoticeNotAppliedAfterPriorFailure
	backgroundNoticeCanceled
)

func (d backgroundNoticeDisposition) String() string {
	switch d {
	case backgroundNoticeApplied:
		return "applied"
	case backgroundNoticeSuppressed:
		return "suppressed"
	case backgroundNoticeFailed:
		return "failed"
	case backgroundNoticeNotAppliedAfterPriorFailure:
		return "not_applied_after_prior_failure"
	case backgroundNoticeCanceled:
		return "canceled"
	default:
		return fmt.Sprintf("unknown_background_notice_disposition_%d", d)
	}
}

// backgroundNoticeCapacityState records who is responsible for releasing the
// entry's scheduler slot. Runtime-command permits are wired by the resource
// owner; the scheduler still makes the local ownership handoff explicit.
type backgroundNoticeCapacityState uint8

const (
	backgroundNoticeCapacityPending backgroundNoticeCapacityState = iota + 1
	backgroundNoticeCapacityBatchOwned
	backgroundNoticeCapacityApplying
	backgroundNoticeCapacityReleased
)

type backgroundNoticeEntry struct {
	notice   queuedBackgroundNotice
	state    backgroundNoticeState
	capacity backgroundNoticeCapacityState
	lease    OrderedMutationLease
	future   *BackgroundNoticeFuture
}

type BackgroundNoticeResult struct {
	Disposition string
	Err         error
}

type BackgroundNoticeFuture struct {
	result  chan BackgroundNoticeResult
	once    sync.Once
	mu      sync.Mutex
	ready   *BackgroundNoticeResult
	observe func(BackgroundNoticeResult)
}

func newBackgroundNoticeFuture() *BackgroundNoticeFuture {
	return &BackgroundNoticeFuture{result: make(chan BackgroundNoticeResult, 1)}
}

func (f *BackgroundNoticeFuture) Await(ctx context.Context) (BackgroundNoticeResult, error) {
	if f == nil {
		return BackgroundNoticeResult{}, errors.New("background notice future is required")
	}
	select {
	case result := <-f.result:
		return result, result.Err
	case <-ctx.Done():
		return BackgroundNoticeResult{}, context.Cause(ctx)
	}
}

func (f *BackgroundNoticeFuture) resolve(result BackgroundNoticeResult) {
	if f == nil {
		return
	}
	f.once.Do(func() {
		f.mu.Lock()
		f.ready = &result
		observe := f.observe
		f.mu.Unlock()
		if observe != nil {
			observe(result)
		}
		f.result <- result
	})
}

func (f *BackgroundNoticeFuture) Observe(observe func(BackgroundNoticeResult)) {
	if f == nil || observe == nil {
		return
	}
	f.mu.Lock()
	if f.ready != nil {
		result := *f.ready
		f.mu.Unlock()
		observe(result)
		return
	}
	f.observe = observe
	f.mu.Unlock()
}

// backgroundNoticeBatch is the sole owner of claimed entries. Callers cannot
// receive a bare notice slice and therefore cannot independently settle,
// restore, or suppress an entry.
type backgroundNoticeBatch struct {
	scheduler *defaultBackgroundNoticeScheduler
	claim     backgroundNoticeClaim
	ids       []uuid.UUID

	begun        bool
	dispositions []backgroundNoticeDisposition
}

func (e *Engine) HandleBackgroundShellUpdate(evt BackgroundShellEvent, queueNotice bool) {
	e.ensureOrchestrationCollaborators()
	e.backgroundFlow.HandleBackgroundShellUpdate(evt, queueNotice)
	e.backgroundFlow.ScheduleIfIdle()
}

func (e *Engine) HandleBackgroundShellUpdateWithOrderedTurn(turn OrderedMutationTurn, evt BackgroundShellEvent, queueNotice bool) error {
	_, err := e.HandleBackgroundShellUpdateWithOrderedTurnResult(turn, evt, queueNotice)
	return err
}

func (e *Engine) HandleBackgroundShellUpdateWithOrderedTurnResult(turn OrderedMutationTurn, evt BackgroundShellEvent, queueNotice bool) (*BackgroundNoticeFuture, error) {
	e.ensureOrchestrationCollaborators()
	return e.backgroundFlow.(*defaultBackgroundNoticeScheduler).handleBackgroundShellUpdateWithOrderedTurnResult(evt, queueNotice, turn)
}

func (e *Engine) ScheduleBackgroundNoticesIfIdle() {
	if e == nil {
		return
	}
	e.ensureOrchestrationCollaborators()
	e.backgroundFlow.ScheduleIfIdle()
}

func (b *defaultBackgroundNoticeScheduler) HandleBackgroundShellUpdate(evt BackgroundShellEvent, queueNotice bool) {
	_ = b.HandleBackgroundShellUpdateWithOrderedTurn(evt, queueNotice, nil)
}

func (b *defaultBackgroundNoticeScheduler) HandleBackgroundShellUpdateWithOrderedTurn(evt BackgroundShellEvent, queueNotice bool, turn OrderedMutationTurn) error {
	_, err := b.handleBackgroundShellUpdateWithOrderedTurnResult(evt, queueNotice, turn)
	return err
}

func (b *defaultBackgroundNoticeScheduler) handleBackgroundShellUpdateWithOrderedTurnResult(evt BackgroundShellEvent, queueNotice bool, turn OrderedMutationTurn) (*BackgroundNoticeFuture, error) {
	var lease OrderedMutationLease
	if queueNotice && evt.Type.IsTerminal() && turn != nil {
		var err error
		lease, err = turn.RetainLease()
		if err != nil {
			return nil, err
		}
	}
	if err := b.engine.steerAndTurn(turn, "", steerEventIntent(Event{Kind: EventBackgroundUpdated, Background: &evt})); err != nil {
		if lease != nil {
			return nil, errors.Join(err, lease.Release())
		}
		return nil, err
	}
	if !queueNotice {
		return nil, nil
	}
	if !evt.Type.IsTerminal() {
		return nil, nil
	}
	future := b.queueDeveloperNotice(llm.Message{
		Role:                 llm.RoleDeveloper,
		MessageType:          textutil.Value(llm.MessageTypeBackgroundNotice),
		Name:                 textutil.OptionalTrimmedString(evt.ID),
		BackgroundActivityID: textutil.Value(evt.ActivityID.String()),
		Content:              textutil.Value(formatBackgroundShellNotice(evt)),
		CompactContent:       textutil.Value(formatBackgroundShellCompact(evt)),
		BackgroundExitCode:   textutil.Pointer(evt.ExitCode),
	}, turn == nil, lease)
	return future, nil
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
	b.queueDeveloperNotice(msg, true, nil)
}

func (b *defaultBackgroundNoticeScheduler) queueDeveloperNotice(msg llm.Message, schedule bool, lease OrderedMutationLease) *BackgroundNoticeFuture {
	if msg.Content == nil || strings.TrimSpace(*msg.Content) == "" {
		return nil
	}
	processID, _ := textutil.OptionalTrimmed(msg.Name)
	notice := queuedBackgroundNotice{
		id:        uuid.New(),
		processID: processID,
		intent:    steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{msg}),
	}
	shouldSchedule := false
	future := newBackgroundNoticeFuture()
	b.mu.Lock()
	b.ensureEntriesLocked()
	b.entries[notice.id] = &backgroundNoticeEntry{
		notice:   notice,
		state:    backgroundNoticePending,
		capacity: backgroundNoticeCapacityPending,
		lease:    lease,
		future:   future,
	}
	b.order = append(b.order, notice.id)
	if !b.task && (b.steps == nil || !b.steps.IsBusy()) {
		shouldSchedule = true
	}
	b.mu.Unlock()
	if shouldSchedule && schedule {
		b.scheduleStandalone()
	}
	return future
}

func (b *defaultBackgroundNoticeScheduler) scheduleStandalone() {
	batch := b.ClaimPendingNotices(backgroundNoticeClaimStandalone)
	if batch.Empty() {
		return
	}
	b.mu.Lock()
	if b.task {
		b.mu.Unlock()
		b.Restore(batch)
		return
	}
	b.task = true
	b.mu.Unlock()
	leaderLease := batch.detachLeaderLease()
	launch := func(ctx context.Context) {
		b.processQueuedNotices(ctx, batch)
	}
	launched := false
	if leaderLease != nil {
		launched = b.engine.launchLifecycleTaskWithLease(launch, leaderLease)
		if !launched {
			_ = leaderLease.Release()
		}
	} else {
		launched = b.engine.launchLifecycleTask(launch)
	}
	if !launched {
		b.mu.Lock()
		b.task = false
		b.mu.Unlock()
		b.Restore(batch)
	}
}

func (b *defaultBackgroundNoticeScheduler) ClaimPendingNotices(claim backgroundNoticeClaim) backgroundNoticeBatch {
	if claim != backgroundNoticeClaimStandalone && claim != backgroundNoticeClaimCombinedFlush {
		b.invariantFailure(fmt.Errorf("background notice claim is invalid: %d", claim))
		return backgroundNoticeBatch{scheduler: b}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensureEntriesLocked()
	batch := backgroundNoticeBatch{scheduler: b, claim: claim}
	for _, id := range b.order {
		entry := b.entries[id]
		if entry == nil || entry.state != backgroundNoticePending {
			continue
		}
		switch claim {
		case backgroundNoticeClaimStandalone:
			entry.state = backgroundNoticeClaimedStandalone
		case backgroundNoticeClaimCombinedFlush:
			entry.state = backgroundNoticeClaimedCombinedFlush
		}
		entry.capacity = backgroundNoticeCapacityBatchOwned
		batch.ids = append(batch.ids, id)
	}
	return batch
}

func (batch backgroundNoticeBatch) detachLeaderLease() OrderedMutationLease {
	if batch.scheduler == nil || len(batch.ids) == 0 {
		return nil
	}
	batch.scheduler.mu.Lock()
	defer batch.scheduler.mu.Unlock()
	entry := batch.scheduler.entries[batch.ids[0]]
	if entry == nil {
		return nil
	}
	lease := entry.lease
	entry.lease = nil
	return lease
}

func (b *defaultBackgroundNoticeScheduler) HasPendingNotices() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.entries) > 0
}

func (b *defaultBackgroundNoticeScheduler) SuppressPendingBackgroundNotice(processID string) backgroundNoticeSuppressionResult {
	processID = strings.TrimSpace(processID)
	if processID == "" {
		return backgroundNoticeSuppressionResult{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, id := range append([]uuid.UUID(nil), b.order...) {
		entry := b.entries[id]
		if entry == nil || entry.notice.processID != processID {
			continue
		}
		switch entry.state {
		case backgroundNoticePending, backgroundNoticeClaimedStandalone, backgroundNoticeClaimedCombinedFlush:
			b.settleLocked(id, backgroundNoticeSuppressed)
			return backgroundNoticeSuppressionResult{
				disposition: backgroundNoticeSuppressed,
				matched:     true,
			}
		case backgroundNoticeApplying:
			return backgroundNoticeSuppressionResult{
				matched:         true,
				alreadyApplying: true,
			}
		default:
			b.invariantFailure(fmt.Errorf("background notice %s has invalid state %d", id, entry.state))
			return backgroundNoticeSuppressionResult{matched: true}
		}
	}
	return backgroundNoticeSuppressionResult{}
}

func (b *defaultBackgroundNoticeScheduler) ScheduleIfIdle() {
	if b.steps != nil && b.steps.IsBusy() {
		return
	}
	b.mu.Lock()
	shouldSchedule := !b.task && b.hasPendingLocked()
	b.mu.Unlock()
	if shouldSchedule {
		b.scheduleStandalone()
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

func (b *defaultBackgroundNoticeScheduler) processQueuedNotices(ctx context.Context, batch backgroundNoticeBatch) {
	defer func() {
		b.mu.Lock()
		b.task = false
		b.mu.Unlock()
	}()
	if _, err := b.runQueuedNotices(ctx, batch); err != nil {
		if errors.Is(err, context.Canceled) {
			b.Cancel(batch)
			return
		}
		b.Cancel(batch)
		b.engine.AppendCommittedEntry("error", fmt.Sprintf("background continuation failed: %v", err))
	}
}

func (b *defaultBackgroundNoticeScheduler) runQueuedNotices(ctx context.Context, batch backgroundNoticeBatch) (assistant llm.Message, err error) {
	if batch.Empty() {
		return llm.Message{}, nil
	}
	err = b.steps.Run(ctx, exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindBackground}, func(stepCtx context.Context, stepID string) error {
		if !batch.BeginApply() {
			return nil
		}
		if err := b.engine.ensureMetaContextForRequest(stepCtx, stepID); err != nil {
			b.Cancel(batch)
			return err
		}
		applied, err := batch.Apply(func(intent steeringIntent) error {
			return b.engine.steer(stepID, intent)
		})
		if err != nil {
			return err
		}
		if applied == 0 {
			return nil
		}
		msg, runErr := b.engine.runStepLoop(stepCtx, stepID)
		assistant = msg
		return runErr
	})
	if errors.Is(err, ErrAgentBusy) {
		b.Restore(batch)
		return llm.Message{}, nil
	}
	return assistant, err
}

func (b *defaultBackgroundNoticeScheduler) ensureEntriesLocked() {
	if b.entries == nil {
		b.entries = make(map[uuid.UUID]*backgroundNoticeEntry)
	}
}

func (b *defaultBackgroundNoticeScheduler) invariantFailure(err error) {
	if b != nil && b.engine != nil && err != nil {
		if b.engine.cfg.Debug {
			panic(fmt.Sprintf("background notice scheduler invariant: %v", err))
		}
		b.engine.surfaceRunError(err)
	}
}

func (b *defaultBackgroundNoticeScheduler) hasPendingLocked() bool {
	for _, entry := range b.entries {
		if entry.state == backgroundNoticePending {
			return true
		}
	}
	return false
}

func (b *defaultBackgroundNoticeScheduler) settleLocked(id uuid.UUID, disposition backgroundNoticeDisposition) {
	b.settleLockedWithError(id, disposition, nil)
}

func (b *defaultBackgroundNoticeScheduler) settleLockedWithError(id uuid.UUID, disposition backgroundNoticeDisposition, cause error) {
	entry := b.entries[id]
	if entry == nil {
		return
	}
	if entry.capacity == backgroundNoticeCapacityReleased {
		b.invariantFailure(fmt.Errorf("background notice %s capacity released twice", id))
		return
	}
	entry.capacity = backgroundNoticeCapacityReleased
	if entry.future != nil {
		entry.future.resolve(BackgroundNoticeResult{Disposition: disposition.String(), Err: cause})
	}
	lease := entry.lease
	entry.lease = nil
	delete(b.entries, id)
	for index, candidate := range b.order {
		if candidate == id {
			b.order = append(b.order[:index], b.order[index+1:]...)
			if lease != nil {
				if err := lease.Release(); err != nil {
					b.invariantFailure(fmt.Errorf("release background notice %s capacity: %w", id, err))
				}
			}
			return
		}
	}
	b.invariantFailure(fmt.Errorf("background notice %s missing from scheduler order", id))
}

func (b *defaultBackgroundNoticeScheduler) restore(batch backgroundNoticeBatch) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, id := range batch.ids {
		entry := b.entries[id]
		if entry == nil {
			continue
		}
		switch entry.state {
		case backgroundNoticeClaimedStandalone, backgroundNoticeClaimedCombinedFlush:
			entry.state = backgroundNoticePending
			entry.capacity = backgroundNoticeCapacityPending
		case backgroundNoticePending:
		case backgroundNoticeApplying:
			b.invariantFailure(fmt.Errorf("restore background notice %s after apply began", id))
		default:
			b.invariantFailure(fmt.Errorf("restore background notice %s has invalid state %d", id, entry.state))
		}
	}
}

func (b *defaultBackgroundNoticeScheduler) cancel(batch backgroundNoticeBatch) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, id := range batch.ids {
		entry := b.entries[id]
		if entry == nil {
			continue
		}
		switch entry.state {
		case backgroundNoticeClaimedStandalone, backgroundNoticeClaimedCombinedFlush, backgroundNoticeApplying:
			b.settleLocked(id, backgroundNoticeCanceled)
		case backgroundNoticePending:
		default:
			b.invariantFailure(fmt.Errorf("cancel background notice %s has invalid state %d", id, entry.state))
		}
	}
}

func (batch backgroundNoticeBatch) Empty() bool {
	return len(batch.ids) == 0
}

func (batch *backgroundNoticeBatch) BeginApply() bool {
	if batch == nil || batch.scheduler == nil {
		return false
	}
	batch.scheduler.mu.Lock()
	defer batch.scheduler.mu.Unlock()
	if batch.begun {
		batch.scheduler.invariantFailure(errors.New("background notice batch began application twice"))
		return false
	}
	batch.begun = true
	for _, id := range batch.ids {
		entry := batch.scheduler.entries[id]
		if entry == nil {
			continue
		}
		switch entry.state {
		case backgroundNoticeClaimedStandalone:
			if batch.claim != backgroundNoticeClaimStandalone {
				batch.scheduler.invariantFailure(errors.New("standalone notice claimed by combined batch"))
				continue
			}
		case backgroundNoticeClaimedCombinedFlush:
			if batch.claim != backgroundNoticeClaimCombinedFlush {
				batch.scheduler.invariantFailure(errors.New("combined notice claimed by standalone batch"))
				continue
			}
		default:
			batch.scheduler.invariantFailure(fmt.Errorf("background notice %s cannot begin apply from state %d", id, entry.state))
			continue
		}
		entry.state = backgroundNoticeApplying
		entry.capacity = backgroundNoticeCapacityApplying
	}
	return batch.hasSurvivorsLocked()
}

func (batch *backgroundNoticeBatch) hasSurvivorsLocked() bool {
	for _, id := range batch.ids {
		if _, ok := batch.scheduler.entries[id]; ok {
			return true
		}
	}
	return false
}

func (batch *backgroundNoticeBatch) Apply(apply func(steeringIntent) error) (int, error) {
	if batch == nil || batch.scheduler == nil {
		return 0, nil
	}
	if !batch.begun {
		batch.scheduler.invariantFailure(errors.New("apply background notice batch before BeginApply"))
		return 0, errors.New("background notice batch has not begun application")
	}
	if apply == nil {
		batch.scheduler.invariantFailure(errors.New("background notice batch apply function is nil"))
		return 0, errors.New("background notice batch apply function is required")
	}
	applied := 0
	for index, id := range batch.ids {
		batch.scheduler.mu.Lock()
		entry := batch.scheduler.entries[id]
		if entry == nil {
			batch.scheduler.mu.Unlock()
			continue
		}
		if entry.state != backgroundNoticeApplying {
			batch.scheduler.mu.Unlock()
			err := fmt.Errorf("background notice %s is not applying", id)
			batch.scheduler.invariantFailure(err)
			return applied, err
		}
		intent := entry.notice.intent
		batch.scheduler.mu.Unlock()

		if err := apply(intent); err != nil {
			batch.scheduler.mu.Lock()
			batch.scheduler.settleLockedWithError(id, backgroundNoticeFailed, err)
			batch.dispositions = append(batch.dispositions, backgroundNoticeFailed)
			for _, laterID := range batch.ids[index+1:] {
				if later := batch.scheduler.entries[laterID]; later != nil {
					if later.state != backgroundNoticeApplying {
						batch.scheduler.mu.Unlock()
						err := fmt.Errorf("background notice %s is not applying after prior failure", laterID)
						batch.scheduler.invariantFailure(err)
						return applied, err
					}
					batch.scheduler.settleLocked(laterID, backgroundNoticeNotAppliedAfterPriorFailure)
					batch.dispositions = append(batch.dispositions, backgroundNoticeNotAppliedAfterPriorFailure)
				}
			}
			batch.scheduler.mu.Unlock()
			return applied, err
		}
		batch.scheduler.mu.Lock()
		batch.scheduler.settleLocked(id, backgroundNoticeApplied)
		batch.dispositions = append(batch.dispositions, backgroundNoticeApplied)
		batch.scheduler.mu.Unlock()
		applied++
	}
	return applied, nil
}

func (batch *backgroundNoticeBatch) Dispositions() []backgroundNoticeDisposition {
	if batch == nil {
		return nil
	}
	return append([]backgroundNoticeDisposition(nil), batch.dispositions...)
}

func (b *defaultBackgroundNoticeScheduler) Restore(batch backgroundNoticeBatch) {
	b.restore(batch)
}

func (b *defaultBackgroundNoticeScheduler) Cancel(batch backgroundNoticeBatch) {
	b.cancel(batch)
}

func (b *defaultBackgroundNoticeScheduler) CancelPendingBackgroundNotices() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, id := range append([]uuid.UUID(nil), b.order...) {
		b.settleLocked(id, backgroundNoticeCanceled)
	}
}
