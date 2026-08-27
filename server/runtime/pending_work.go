package runtime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
)

type pendingWorkSteerAdmission uint64

type orderedPendingWorkItem struct {
	order pendingWorkSteerAdmission
	item  runtimeinput.PendingWorkItem
}

type pendingOperationalWork struct {
	order  pendingWorkSteerAdmission
	item   runtimeinput.PendingWorkItem
	cancel context.CancelCauseFunc
}

type pendingWorkCoordinator struct {
	mu                 sync.Mutex
	nextSteerAdmission pendingWorkSteerAdmission
	latest             atomic.Pointer[runtimeinput.PendingWork]
}

var errPendingOperationalWorkRemoved = errors.New("pending operational work was removed")

func newPendingWorkCoordinator() *pendingWorkCoordinator {
	coordinator := &pendingWorkCoordinator{}
	empty := runtimeinput.PendingWork{}
	coordinator.latest.Store(&empty)
	return coordinator
}

func (e *Engine) PendingWorkSnapshot() (runtimeinput.PendingWork, error) {
	if e == nil {
		return runtimeinput.PendingWork{}, nil
	}
	e.ensureOrchestrationCollaborators()
	return clonePendingWork(*e.pendingWork.latest.Load()), nil
}

func (e *Engine) requirePendingWorkCapacity() error {
	snapshot, err := e.PendingWorkSnapshot()
	if err != nil {
		return err
	}
	if len(snapshot.Items) >= runtimeinput.PendingWorkCapacity {
		return &serverapi.PendingWorkCapacityError{}
	}
	return nil
}

func (e *Engine) mutatePendingWork(
	assignSteerAdmission bool,
	mutate func(*pendingWorkSteerAdmission) (bool, error),
	beforeReplacement func(),
) error {
	if mutate == nil {
		return errors.New("Pending Work mutation is required")
	}
	e.ensureOrchestrationCollaborators()
	coordinator := e.pendingWork
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()

	var admission *pendingWorkSteerAdmission
	if assignSteerAdmission {
		next := coordinator.nextSteerAdmission + 1
		admission = &next
	}
	changed, err := mutate(admission)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	if admission != nil {
		coordinator.nextSteerAdmission = *admission
	}
	snapshot, err := e.projectPendingWork()
	if err != nil {
		panic(fmt.Sprintf("project Pending Work after accepted mutation: %v", err))
	}
	stored := clonePendingWork(snapshot)
	coordinator.latest.Store(&stored)
	if beforeReplacement != nil {
		beforeReplacement()
	}
	e.publishPendingWork(snapshot)
	return nil
}

func (e *Engine) projectPendingWork() (runtimeinput.PendingWork, error) {
	messages := e.messageFlow.PendingUserMessageEntries()
	queue := make([]runtimeinput.PendingWorkItem, 0, len(messages))
	steer := make([]orderedPendingWorkItem, 0, len(messages))
	for _, message := range messages {
		item, err := pendingWorkMessage(message)
		if err != nil {
			return runtimeinput.PendingWork{}, err
		}
		switch message.lane {
		case runtimeinput.PendingWorkLaneQueue:
			queue = append(queue, item)
		case runtimeinput.PendingWorkLaneSteer:
			if message.steerAdmission == nil {
				return runtimeinput.PendingWork{}, errors.New("pending Steer message admission is required")
			}
			steer = append(steer, orderedPendingWorkItem{order: *message.steerAdmission, item: item})
		default:
			return runtimeinput.PendingWork{}, fmt.Errorf("pending message lane %q is invalid", message.lane)
		}
	}
	if lifecycle, ok := e.stepLifecycle.(*defaultExclusiveStepLifecycle); ok {
		for _, operational := range lifecycle.pendingOperationalWork() {
			steer = append(steer, orderedPendingWorkItem{
				order: operational.order,
				item:  operational.item,
			})
		}
	}
	sort.SliceStable(steer, func(i, j int) bool {
		return steer[i].order < steer[j].order
	})
	items := make([]runtimeinput.PendingWorkItem, 0, len(queue)+len(steer))
	items = append(items, queue...)
	for _, pending := range steer {
		items = append(items, pending.item)
	}
	collection := runtimeinput.PendingWork{Items: items}
	if err := collection.Validate(); err != nil {
		return runtimeinput.PendingWork{}, err
	}
	return collection, nil
}

func (e *Engine) publishPendingWork(snapshot runtimeinput.PendingWork) {
	published := clonePendingWork(snapshot)
	if err := e.emitRaw(Event{Kind: EventPendingWorkReplaced, PendingWork: &published}); err != nil {
		e.surfaceRunError(fmt.Errorf("publish Pending Work replacement: %w", err))
	}
}

func (e *Engine) RemovePendingWork(ctx context.Context, id runtimeids.QueueItemID) (runtimeinput.PendingWorkRestoration, error) {
	if id.IsZero() {
		return runtimeinput.PendingWorkRestoration{}, &runtimeinput.PendingWorkRemovalError{ItemID: id}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := context.Cause(ctx); err != nil {
		return runtimeinput.PendingWorkRestoration{}, err
	}
	return e.removePendingWork(id)
}

func (e *Engine) removePendingWork(id runtimeids.QueueItemID) (runtimeinput.PendingWorkRestoration, error) {
	e.ensureOrchestrationCollaborators()
	var restoration runtimeinput.PendingWorkRestoration
	err := e.mutatePendingWork(false, func(*pendingWorkSteerAdmission) (bool, error) {
		if lifecycle, ok := e.stepLifecycle.(*defaultExclusiveStepLifecycle); ok {
			if operational, removed := lifecycle.removePendingOperationalWork(id); removed {
				var err error
				restoration, err = operational.item.Restoration()
				if err != nil {
					return false, err
				}
				return true, nil
			}
		}

		message, removed := e.messageFlow.DiscardQueuedUserMessage(id.String())
		if !removed {
			return false, &runtimeinput.PendingWorkRemovalError{ItemID: id}
		}
		if message.lane == runtimeinput.PendingWorkLaneSteer {
			e.unmarkQueuedUserInjectionForAutoDrain(id.String())
			e.completeLiveRunQueueItems(map[string]struct{}{id.String(): {}})
		}
		item, err := pendingWorkMessage(message)
		if err != nil {
			return false, err
		}
		restoration, err = item.Restoration()
		if err != nil {
			return false, err
		}
		e.emitQueuedUserMessageStatus(message.message, QueuedUserMessageDiscarded, "", false)
		return true, nil
	}, nil)
	return restoration, err
}

func (s *defaultExclusiveStepLifecycle) pendingOperationalWork() []pendingOperationalWork {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]pendingOperationalWork, 0)
	for _, waiter := range s.nextWaiters {
		if waiter == nil || waiter.reservation == nil || waiter.reservation.pendingWork == nil {
			continue
		}
		items = append(items, *waiter.reservation.pendingWork)
	}
	return items
}

func (s *defaultExclusiveStepLifecycle) removePendingOperationalWork(id runtimeids.QueueItemID) (pendingOperationalWork, bool) {
	if s == nil || id.IsZero() {
		return pendingOperationalWork{}, false
	}
	s.mu.Lock()
	var removed *pendingOperationalWork
	for index, waiter := range s.nextWaiters {
		if waiter == nil || waiter.reservation == nil || waiter.reservation.pendingWork == nil ||
			waiter.reservation.pendingWork.item.ID != id {
			continue
		}
		removed = waiter.reservation.pendingWork
		waiter.reservation.pendingWork = nil
		s.nextWaiters = append(s.nextWaiters[:index], s.nextWaiters[index+1:]...)
		s.notifyNextWaiterLocked()
		break
	}
	s.mu.Unlock()
	if removed == nil {
		return pendingOperationalWork{}, false
	}
	if removed.cancel != nil {
		removed.cancel(errPendingOperationalWorkRemoved)
	}
	return *removed, true
}

func pendingWorkMessage(message queuedUserMessage) (runtimeinput.PendingWorkItem, error) {
	id, err := runtimeids.ParseQueueItemID(message.message.ID)
	if err != nil {
		return runtimeinput.PendingWorkItem{}, err
	}
	text, err := message.message.DisplayText()
	if err != nil {
		return runtimeinput.PendingWorkItem{}, err
	}
	return runtimeinput.PendingWorkItem{
		ID:             id,
		Lane:           message.lane,
		Kind:           runtimeinput.PendingWorkItemKindMessage,
		State:          runtimeinput.PendingWorkItemStatePending,
		CanonicalInput: text,
		Message:        &runtimeinput.PendingWorkMessage{Text: text},
	}, nil
}

func clonePendingWork(source runtimeinput.PendingWork) runtimeinput.PendingWork {
	cloned := runtimeinput.PendingWork{Items: make([]runtimeinput.PendingWorkItem, len(source.Items))}
	for index, item := range source.Items {
		cloned.Items[index] = item
		if item.Message != nil {
			message := *item.Message
			cloned.Items[index].Message = &message
		}
		if item.ManualCompaction != nil {
			compaction := *item.ManualCompaction
			if item.ManualCompaction.Guidance != nil {
				guidance := *item.ManualCompaction.Guidance
				compaction.Guidance = &guidance
			}
			cloned.Items[index].ManualCompaction = &compaction
		}
		if item.WorktreeTransition != nil {
			transition := *item.WorktreeTransition
			if item.WorktreeTransition.Selector != nil {
				selector := *item.WorktreeTransition.Selector
				transition.Selector = &selector
			}
			cloned.Items[index].WorktreeTransition = &transition
		}
	}
	return cloned
}
