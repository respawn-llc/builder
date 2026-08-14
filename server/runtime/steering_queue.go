package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"

	"core/server/session"
	"core/server/tools"
	"core/server/workflow"
	"core/shared/clientui"
	"core/shared/runtimeids"
)

const (
	maxPendingSteeringIntents = 9_999
)

type steeringHumanAssociation struct {
	ordinal uint64
	scope   *runtimeids.ExecutionScopeID
}

type interruptedHumanSteering struct {
	ordinal uint64
	item    QueuedUserMessage
}

type steeringOutputReply struct {
	receipt session.CommitReceipt
	err     error
}

type steeringOutputProvenance interface {
	steeringOutputProvenance()
}

type runtimeOutputProvenance struct{}

type exactOutputProvenance struct {
	stepID string
}

type deferredHumanOutputProvenance struct{}

func (runtimeOutputProvenance) steeringOutputProvenance()       {}
func (exactOutputProvenance) steeringOutputProvenance()         {}
func (deferredHumanOutputProvenance) steeringOutputProvenance() {}

func runtimeSteeringOutputProvenance() steeringOutputProvenance {
	return runtimeOutputProvenance{}
}

func exactSteeringOutputProvenance(stepID string) steeringOutputProvenance {
	return exactOutputProvenance{stepID: stepID}
}

func deferredHumanSteeringOutputProvenance() steeringOutputProvenance {
	return deferredHumanOutputProvenance{}
}

func validateSteeringOutputProvenance(provenance steeringOutputProvenance) error {
	switch provenance := provenance.(type) {
	case runtimeOutputProvenance, deferredHumanOutputProvenance:
		return nil
	case exactOutputProvenance:
		if strings.TrimSpace(provenance.stepID) == "" {
			return errors.New("exact Runtime output requires a Step ID")
		}
		return nil
	default:
		return errors.New("Runtime output provenance is required")
	}
}

func steeringOutputStepID(provenance steeringOutputProvenance) (*string, error) {
	switch provenance := provenance.(type) {
	case runtimeOutputProvenance:
		return nil, nil
	case exactOutputProvenance:
		if strings.TrimSpace(provenance.stepID) == "" {
			return nil, errors.New("exact Runtime output requires a Step ID")
		}
		stepID := provenance.stepID
		return &stepID, nil
	case deferredHumanOutputProvenance:
		return nil, errors.New("deferred human Runtime output requires Step binding")
	default:
		return nil, errors.New("Runtime output provenance is required")
	}
}

type steeringOutputOperation struct {
	provenance    steeringOutputProvenance
	intents       []steeringIntent
	commitReceipt bool
	humanItem     *QueuedUserMessage
}

type steeringWorkflowAssignment struct {
	reference  workflow.CurrentNodeReference
	assignment WorkflowAssignment
	persisted  bool
	reply      chan steeringOutputReply
}

type pendingCompaction struct {
	mode                        compactionMode
	instructions                compactionInstructionsInput
	includePreservedUserMessage bool
	onActive                    func()
	accept                      CommandAcceptance
	requireEligibility          bool
	reply                       chan compactionReply
}

type compactionReply struct {
	receipt session.CommitReceipt
	err     error
}

type steeringUserShell struct {
	command  string
	onActive func()
	reply    chan userShellReply
}

type steeringWorktreeTransition struct {
	operation func(context.Context, func(clientui.SessionExecutionTarget, *session.WorktreeReminderState) error) error
}

type userShellReply struct {
	result tools.Result
	err    error
}

type steeringQueueEntry struct {
	output     *steeringOutputOperation
	start      *steeringWorkflowAssignment
	shell      *steeringUserShell
	worktree   *steeringWorktreeTransition
	compaction *pendingCompaction
	human      *steeringHumanAssociation

	outputReply chan steeringOutputReply
	replyOnce   sync.Once
}

func newExactOutputSteeringQueueEntry(stepID string, commitReceipt bool, intents ...steeringIntent) *steeringQueueEntry {
	return &steeringQueueEntry{
		output: &steeringOutputOperation{
			provenance:    exactSteeringOutputProvenance(stepID),
			intents:       append([]steeringIntent(nil), intents...),
			commitReceipt: commitReceipt,
		},
		outputReply: make(chan steeringOutputReply, 1),
	}
}

func newRuntimeOutputSteeringQueueEntry(commitReceipt bool, intents ...steeringIntent) *steeringQueueEntry {
	return &steeringQueueEntry{
		output: &steeringOutputOperation{
			provenance:    runtimeSteeringOutputProvenance(),
			intents:       append([]steeringIntent(nil), intents...),
			commitReceipt: commitReceipt,
		},
		outputReply: make(chan steeringOutputReply, 1),
	}
}

func newHumanSteeringQueueEntry(item QueuedUserMessage, deferUntilStepBoundary bool) *steeringQueueEntry {
	provenance := runtimeSteeringOutputProvenance()
	if deferUntilStepBoundary {
		provenance = deferredHumanSteeringOutputProvenance()
	}
	return &steeringQueueEntry{
		output: &steeringOutputOperation{
			provenance: provenance,
			intents:    []steeringIntent{steerUserMessageWithFlushIntent(item.Message)},
			humanItem:  &item,
		},
		outputReply: make(chan steeringOutputReply, 1),
	}
}

func newWorkflowAssignmentQueueEntry(
	reference workflow.CurrentNodeReference,
	assignment WorkflowAssignment,
	persisted bool,
) *steeringQueueEntry {
	return &steeringQueueEntry{
		start: &steeringWorkflowAssignment{
			reference:  reference,
			assignment: assignment,
			persisted:  persisted,
			reply:      make(chan steeringOutputReply, 1),
		},
	}
}

func newUserShellQueueEntry(
	command string,
	onActive func(),
) *steeringQueueEntry {
	return &steeringQueueEntry{shell: &steeringUserShell{
		command:  command,
		onActive: onActive,
		reply:    make(chan userShellReply, 1),
	}}
}

func newWorktreeTransitionQueueEntry(
	operation func(context.Context, func(clientui.SessionExecutionTarget, *session.WorktreeReminderState) error) error,
) *steeringQueueEntry {
	return &steeringQueueEntry{worktree: &steeringWorktreeTransition{
		operation: operation,
	}}
}

func newCompactionQueueEntry(compaction *pendingCompaction) *steeringQueueEntry {
	return &steeringQueueEntry{compaction: compaction}
}

func (e *steeringQueueEntry) validate() error {
	if e == nil {
		return errors.New("Steering queue entry is required")
	}
	kinds := 0
	if e.output != nil {
		kinds++
		if e.outputReply == nil {
			return errors.New("output Steering reply is required")
		}
		if len(e.output.intents) == 0 {
			return errors.New("output Steering operation requires at least one intent")
		}
		if err := validateSteeringOutputProvenance(e.output.provenance); err != nil {
			return err
		}
		for _, intent := range e.output.intents {
			if len(intent.items) == 0 {
				return errors.New("Runtime mutation intent requires at least one concrete mutation")
			}
			for _, mutation := range intent.items {
				if err := validateSteeringMutation(mutation); err != nil {
					return err
				}
			}
		}
		if e.output.commitReceipt &&
			(len(e.output.intents) != 1 || len(e.output.intents[0].items) != 1) {
			return errors.New("output Steering commit receipt requires exactly one item")
		}
	}
	if e.start != nil {
		kinds++
		if e.start.reply == nil || e.start.reference.Validate() != nil {
			return errors.New("Workflow assignment Steering operation is invalid")
		}
	}
	if e.shell != nil {
		kinds++
		if e.shell.command == "" || e.shell.reply == nil {
			return errors.New("user-shell Steering operation is invalid")
		}
	}
	if e.worktree != nil {
		kinds++
		if e.worktree.operation == nil {
			return errors.New("Worktree Steering operation is invalid")
		}
	}
	if e.compaction != nil {
		kinds++
		if e.compaction.reply == nil {
			return errors.New("manual compaction Steering operation is invalid")
		}
	}
	if kinds != 1 {
		return errors.New("Steering queue entry must contain exactly one operation")
	}
	if e.human != nil && e.human.ordinal == 0 {
		return errors.New("human Steering admission ordinal is required")
	}
	return nil
}

func (e *steeringQueueEntry) completeShell(reply userShellReply) error {
	if e == nil || e.shell == nil || e.shell.reply == nil {
		return errors.New("user-shell Steering queue entry is invalid")
	}
	e.replyOnce.Do(func() {
		e.shell.reply <- reply
		close(e.shell.reply)
	})
	return nil
}

func (e *steeringQueueEntry) completeOutput(reply steeringOutputReply) error {
	if e == nil || e.outputReply == nil {
		return errors.New("output Steering queue entry is invalid")
	}
	e.replyOnce.Do(func() {
		e.outputReply <- reply
		close(e.outputReply)
	})
	return nil
}

func (e *steeringQueueEntry) waitOutput(ctx context.Context) (session.CommitReceipt, error) {
	if e == nil || e.outputReply == nil {
		return session.CommitReceipt{}, errors.New("output Steering queue entry is invalid")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case reply := <-e.outputReply:
		return reply.receipt, reply.err
	case <-ctx.Done():
		return session.CommitReceipt{}, context.Cause(ctx)
	}
}

type steeringQueue struct {
	mu             sync.Mutex
	pending        []*steeringQueueEntry
	current        *steeringQueueEntry
	draining       bool
	idleReady      chan struct{}
	nextHumanOrder uint64
	closed         bool
	debug          bool
}

func newSteeringQueue(debug ...bool) *steeringQueue {
	idleReady := make(chan struct{})
	close(idleReady)
	queue := &steeringQueue{idleReady: idleReady}
	if len(debug) != 0 {
		queue.debug = debug[0]
	}
	return queue
}

func (q *steeringQueue) append(entry *steeringQueueEntry) (bool, error) {
	if q == nil {
		return false, errors.New("Steering queue is required")
	}
	if err := entry.validate(); err != nil {
		return false, err
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		entry.completeClosed()
		return false, ErrEngineClosed
	}
	defer q.mu.Unlock()
	q.requirePendingCapacityLocked()
	q.pending = append(q.pending, entry)
	wake := q.claimDrainLocked()
	return wake, nil
}

func (q *steeringQueue) appendDeferred(entry *steeringQueueEntry) (bool, error) {
	if q == nil {
		return false, errors.New("Steering queue is required")
	}
	if err := entry.validate(); err != nil {
		return false, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		entry.completeClosed()
		return false, ErrEngineClosed
	}
	q.requirePendingCapacityLocked()
	q.pending = append(q.pending, entry)
	return false, nil
}

func (q *steeringQueue) appendExactGoal(
	entry *steeringQueueEntry,
	durable *session.GoalState,
	validate func(*session.GoalState) error,
) (*session.GoalState, error) {
	if q == nil {
		return nil, errors.New("Steering queue is required")
	}
	if err := entry.validate(); err != nil {
		return nil, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		entry.completeClosed()
		return nil, ErrEngineClosed
	}
	projected := cloneRuntimeGoal(durable)
	foldEntry := func(queued *steeringQueueEntry) {
		if queued == nil || queued.output == nil {
			return
		}
		for _, intent := range queued.output.intents {
			for _, item := range intent.items {
				goal, ok := item.(*steeringGoalMutation)
				if ok {
					projected = projectGoalMutation(projected, goal.mutation)
				}
			}
		}
	}
	foldEntry(q.current)
	for _, queued := range q.pending {
		foldEntry(queued)
	}
	if validate != nil {
		if err := validate(projected); err != nil {
			return nil, err
		}
	}
	foldEntry(entry)
	q.requirePendingCapacityLocked()
	q.pending = append(q.pending, entry)
	return projected, nil
}

func (q *steeringQueue) projectGoal(durable *session.GoalState) (*session.GoalState, error) {
	if q == nil {
		return nil, errors.New("Steering queue is required")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	projected := cloneRuntimeGoal(durable)
	foldEntry := func(queued *steeringQueueEntry) {
		if queued == nil || queued.output == nil {
			return
		}
		for _, intent := range queued.output.intents {
			for _, item := range intent.items {
				if goal, ok := item.(*steeringGoalMutation); ok {
					projected = projectGoalMutation(projected, goal.mutation)
				}
			}
		}
	}
	foldEntry(q.current)
	for _, queued := range q.pending {
		foldEntry(queued)
	}
	return projected, nil
}

func (q *steeringQueue) appendHuman(
	entry *steeringQueueEntry,
	scope *runtimeids.ExecutionScopeID,
	drainImmediately bool,
) (bool, error) {
	if q == nil {
		return false, errors.New("Steering queue is required")
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		entry.completeClosed()
		return false, ErrEngineClosed
	}
	if err := entry.validate(); err != nil {
		q.mu.Unlock()
		return false, err
	}
	ordinal, err := q.nextHumanOrdinalLocked()
	if err != nil {
		q.mu.Unlock()
		return false, err
	}
	defer q.mu.Unlock()
	entry.human = &steeringHumanAssociation{
		ordinal: ordinal,
		scope:   cloneExecutionScopeID(scope),
	}
	q.requirePendingCapacityLocked()
	q.pending = append(q.pending, entry)
	wake := drainImmediately && q.claimDrainLocked()
	return wake, nil
}

func (q *steeringQueue) requirePendingCapacityLocked() {
	if len(q.pending) >= maxPendingSteeringIntents {
		panic("Runtime Steering pending Intent backlog reached 10,000")
	}
}

func (q *steeringQueue) nextHumanOrdinal() (uint64, error) {
	if q == nil {
		return 0, errors.New("Steering queue is required")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return 0, ErrEngineClosed
	}
	return q.nextHumanOrdinalLocked()
}

func (q *steeringQueue) nextHumanOrdinalLocked() (uint64, error) {
	q.nextHumanOrder++
	if q.nextHumanOrder == 0 {
		return 0, runtimeInvariant(
			q.debug,
			"allocate human Steering admission ordinal",
			errors.New("human Steering admission ordinal overflow"),
		)
	}
	return q.nextHumanOrder, nil
}

func (q *steeringQueue) claimDrainLocked() bool {
	if q.draining {
		return false
	}
	q.draining = true
	q.idleReady = make(chan struct{})
	return true
}

func (q *steeringQueue) requestWake() bool {
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	return q.claimDrainLocked()
}

func cloneExecutionScopeID(value *runtimeids.ExecutionScopeID) *runtimeids.ExecutionScopeID {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func (q *steeringQueue) beginNext(includeDeferredHuman bool) (*steeringQueueEntry, bool) {
	if q == nil {
		return nil, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.draining || q.current != nil || len(q.pending) == 0 {
		return nil, false
	}
	if !includeDeferredHuman && q.pending[0].human != nil {
		return nil, false
	}
	q.current = q.pending[0]
	q.pending = q.pending[1:]
	return q.current, true
}

func (q *steeringQueue) pauseForDeferredHuman() bool {
	if q == nil {
		return true
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.current != nil {
		return false
	}
	if len(q.pending) == 0 || q.pending[0].human == nil {
		return false
	}
	if q.draining {
		q.draining = false
		close(q.idleReady)
	}
	return true
}

func (q *steeringQueue) removeHumanByScope(scopeID runtimeids.ExecutionScopeID) []interruptedHumanSteering {
	if q == nil || scopeID.IsZero() {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	removed := make([]interruptedHumanSteering, 0)
	kept := q.pending[:0]
	for _, entry := range q.pending {
		if entry.human == nil || entry.human.scope == nil || *entry.human.scope != scopeID {
			kept = append(kept, entry)
			continue
		}
		if entry.output != nil && entry.output.humanItem != nil {
			removed = append(removed, interruptedHumanSteering{
				ordinal: entry.human.ordinal,
				item:    *entry.output.humanItem,
			})
		}
		_ = entry.completeOutput(steeringOutputReply{err: context.Canceled})
	}
	q.pending = kept
	return removed
}

func (q *steeringQueue) pendingHumanForFailure(current *steeringQueueEntry) []interruptedHumanSteering {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	items := make([]interruptedHumanSteering, 0, len(q.pending)+1)
	appendEntry := func(entry *steeringQueueEntry) {
		if entry == nil || entry.human == nil || entry.output == nil || entry.output.humanItem == nil {
			return
		}
		items = append(items, interruptedHumanSteering{
			ordinal: entry.human.ordinal,
			item:    *entry.output.humanItem,
		})
	}
	appendEntry(current)
	for _, entry := range q.pending {
		appendEntry(entry)
	}
	return items
}

func (q *steeringQueue) bindDeferredHumanProvenance(
	provenance steeringOutputProvenance,
) error {
	if q == nil {
		return nil
	}
	if err := validateSteeringOutputProvenance(provenance); err != nil {
		return err
	}
	if _, deferred := provenance.(deferredHumanOutputProvenance); deferred {
		return errors.New("deferred human output cannot bind to deferred provenance")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, entry := range q.pending {
		if entry.human == nil || entry.output == nil {
			continue
		}
		if _, deferred := entry.output.provenance.(deferredHumanOutputProvenance); !deferred {
			continue
		}
		entry.output.provenance = provenance
	}
	return nil
}

func (q *steeringQueue) finishCurrent(entry *steeringQueueEntry) error {
	if q == nil {
		return errors.New("Steering queue is required")
	}
	q.mu.Lock()
	if q.current == nil || q.current != entry {
		q.mu.Unlock()
		return errors.New("Steering queue current head does not match finalized entry")
	}
	q.current = nil
	q.mu.Unlock()
	return nil
}

func (q *steeringQueue) finishDrain(decideEmpty func()) bool {
	if q == nil {
		return true
	}
	q.mu.Lock()
	if q.current != nil {
		q.mu.Unlock()
		return false
	}
	if len(q.pending) != 0 {
		q.mu.Unlock()
		return false
	}
	if !q.draining {
		q.mu.Unlock()
		return true
	}
	q.draining = false
	close(q.idleReady)
	q.mu.Unlock()
	if decideEmpty != nil {
		decideEmpty()
	}
	return true
}

func (q *steeringQueue) pendingWork() bool {
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.draining || q.current != nil || len(q.pending) != 0
}

func (q *steeringQueue) humanAdmissionOrdinal() uint64 {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.nextHumanOrder
}

func (q *steeringQueue) pendingHumanMessages() []QueuedUserMessage {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	messages := make([]QueuedUserMessage, 0, len(q.pending)+1)
	appendHuman := func(entry *steeringQueueEntry) {
		if entry == nil || entry.human == nil || entry.output == nil || entry.output.humanItem == nil {
			return
		}
		messages = append(messages, *entry.output.humanItem)
	}
	appendHuman(q.current)
	for _, entry := range q.pending {
		appendHuman(entry)
	}
	return messages
}

func (q *steeringQueue) waitUntilMutationsApplied(ctx context.Context) error {
	if q == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		q.mu.Lock()
		if q.current == nil && len(q.pending) == 0 {
			q.mu.Unlock()
			return nil
		}
		ready := q.idleReady
		q.mu.Unlock()
		select {
		case <-ready:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
}

func (q *steeringQueue) close() {
	if q == nil {
		return
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	pending := q.pending
	q.pending = nil
	if q.current == nil && q.draining {
		q.draining = false
		close(q.idleReady)
	}
	q.mu.Unlock()
	for _, entry := range pending {
		entry.completeClosed()
	}
}

func (e *steeringQueueEntry) completeClosed() {
	switch {
	case e.output != nil:
		_ = e.completeOutput(steeringOutputReply{err: ErrEngineClosed})
	case e.start != nil:
		e.start.reply <- steeringOutputReply{err: ErrEngineClosed}
		close(e.start.reply)
	case e.shell != nil:
		_ = e.completeShell(userShellReply{err: ErrEngineClosed})
	case e.worktree != nil:
		// Runtime close drops pending process-local Steering. The Worktree
		// owner remains responsible for its own domain operation.
	case e.compaction != nil:
		e.compaction.reply <- compactionReply{err: ErrEngineClosed}
		close(e.compaction.reply)
	default:
		return
	}
}
