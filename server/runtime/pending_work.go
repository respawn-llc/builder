package runtime

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
)

type orderedPendingWorkItem struct {
	order uint64
	item  runtimeinput.PendingWorkItem
}

var errPendingManualCompactionRemoved = errors.New("pending manual compaction was removed")

func (e *Engine) PendingWorkSnapshot() (runtimeinput.PendingWork, error) {
	if e == nil {
		return runtimeinput.PendingWork{}, nil
	}
	e.ensureOrchestrationCollaborators()

	autoDrainIDs := e.queuedUserAutoDrainIDSnapshot()
	messages := e.messageFlow.PendingUserMessageEntries()
	steer := make([]orderedPendingWorkItem, 0, len(messages))
	queue := make([]runtimeinput.PendingWorkItem, 0, len(messages))
	for _, message := range messages {
		lane := runtimeinput.PendingWorkLaneQueue
		if _, autoDrain := autoDrainIDs[message.message.ID]; autoDrain {
			lane = runtimeinput.PendingWorkLaneSteer
		}
		item, err := pendingWorkMessage(message.message, lane)
		if err != nil {
			return runtimeinput.PendingWork{}, err
		}
		if lane == runtimeinput.PendingWorkLaneSteer {
			steer = append(steer, orderedPendingWorkItem{order: message.admission, item: item})
		} else {
			queue = append(queue, item)
		}
	}
	if lifecycle, ok := e.stepLifecycle.(*defaultExclusiveStepLifecycle); ok {
		for _, compaction := range lifecycle.pendingManualCompactions() {
			steer = append(steer, orderedPendingWorkItem{
				order: compaction.order,
				item: runtimeinput.PendingWorkItem{
					ID:               compaction.itemID,
					Lane:             runtimeinput.PendingWorkLaneSteer,
					Kind:             runtimeinput.PendingWorkItemKindManualCompaction,
					State:            runtimeinput.PendingWorkItemStatePending,
					ManualCompaction: &compaction.admission,
				},
			})
		}
	}
	sort.SliceStable(steer, func(i, j int) bool {
		return steer[i].order < steer[j].order
	})
	items := make([]runtimeinput.PendingWorkItem, 0, len(steer)+len(queue))
	for _, pending := range steer {
		items = append(items, pending.item)
	}
	items = append(items, queue...)
	collection := runtimeinput.PendingWork{Items: items}
	if err := collection.Validate(); err != nil {
		return runtimeinput.PendingWork{}, fmt.Errorf("project Pending Work: %w", err)
	}
	return collection, nil
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

func (e *Engine) publishPendingWorkSnapshot() {
	snapshot, err := e.PendingWorkSnapshot()
	if err != nil {
		e.surfaceRunError(err)
		return
	}
	e.publishPendingWork(snapshot)
}

func (e *Engine) publishPendingWork(snapshot runtimeinput.PendingWork) {
	if err := e.emitRaw(Event{Kind: EventPendingWorkReplaced, PendingWork: &snapshot}); err != nil {
		e.surfaceRunError(fmt.Errorf("publish Pending Work replacement: %w", err))
	}
}

func (e *Engine) RemovePendingWork(ctx context.Context, id runtimeids.QueueItemID) (runtimeinput.PendingWorkRestoration, error) {
	if id.IsZero() {
		return runtimeinput.PendingWorkRestoration{}, &runtimeinput.PendingWorkRemovalError{ItemID: id}
	}
	return awaitEngineRuntimeOperation(ctx, e, func(context.Context) (runtimeinput.PendingWorkRestoration, error) {
		return e.removePendingWork(id)
	})
}

func (e *Engine) removePendingWork(id runtimeids.QueueItemID) (runtimeinput.PendingWorkRestoration, error) {
	e.ensureOrchestrationCollaborators()
	if lifecycle, ok := e.stepLifecycle.(*defaultExclusiveStepLifecycle); ok {
		if compaction, removed := lifecycle.removePendingManualCompaction(id); removed {
			item := runtimeinput.PendingWorkItem{
				ID:               compaction.itemID,
				Lane:             runtimeinput.PendingWorkLaneSteer,
				Kind:             runtimeinput.PendingWorkItemKindManualCompaction,
				State:            runtimeinput.PendingWorkItemStatePending,
				ManualCompaction: &compaction.admission,
			}
			restoration, err := item.Restoration()
			if err != nil {
				return runtimeinput.PendingWorkRestoration{}, err
			}
			e.publishPendingWorkSnapshot()
			return restoration, nil
		}
	}

	autoDrainIDs := e.queuedUserAutoDrainIDSnapshot()
	message, removed := e.messageFlow.DiscardQueuedUserMessage(id.String())
	if !removed {
		return runtimeinput.PendingWorkRestoration{}, &runtimeinput.PendingWorkRemovalError{ItemID: id}
	}
	lane := runtimeinput.PendingWorkLaneQueue
	if _, autoDrain := autoDrainIDs[id.String()]; autoDrain {
		lane = runtimeinput.PendingWorkLaneSteer
		e.unmarkQueuedUserInjectionForAutoDrain(id.String())
		e.completeLiveRunQueueItems(map[string]struct{}{id.String(): {}})
	}
	item, err := pendingWorkMessage(message, lane)
	if err != nil {
		return runtimeinput.PendingWorkRestoration{}, err
	}
	restoration, err := item.Restoration()
	if err != nil {
		return runtimeinput.PendingWorkRestoration{}, err
	}
	e.emitQueuedUserMessageStatus(message, QueuedUserMessageDiscarded, "", false, nil)
	e.publishPendingWorkSnapshot()
	return restoration, nil
}

func (s *defaultExclusiveStepLifecycle) pendingManualCompactions() []pendingManualCompaction {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]pendingManualCompaction, 0)
	for _, waiter := range s.nextWaiters {
		if waiter == nil || waiter.reservation == nil || waiter.reservation.pendingManualCompaction == nil {
			continue
		}
		items = append(items, *waiter.reservation.pendingManualCompaction)
	}
	return items
}

func (s *defaultExclusiveStepLifecycle) removePendingManualCompaction(id runtimeids.QueueItemID) (pendingManualCompaction, bool) {
	if s == nil || id.IsZero() {
		return pendingManualCompaction{}, false
	}
	s.mu.Lock()
	var removed *pendingManualCompaction
	var cancel context.CancelCauseFunc
	for index, waiter := range s.nextWaiters {
		if waiter == nil || waiter.reservation == nil || waiter.reservation.pendingManualCompaction == nil ||
			waiter.reservation.pendingManualCompaction.itemID != id {
			continue
		}
		removed = waiter.reservation.pendingManualCompaction
		cancel = waiter.reservation.cancelPendingCompaction
		s.nextWaiters = append(s.nextWaiters[:index], s.nextWaiters[index+1:]...)
		s.notifyNextWaiterLocked()
		break
	}
	s.mu.Unlock()
	if removed == nil {
		return pendingManualCompaction{}, false
	}
	if cancel != nil {
		cancel(errPendingManualCompactionRemoved)
	}
	return *removed, true
}

func pendingWorkMessage(message QueuedUserMessage, lane runtimeinput.PendingWorkLane) (runtimeinput.PendingWorkItem, error) {
	id, err := runtimeids.ParseQueueItemID(message.ID)
	if err != nil {
		return runtimeinput.PendingWorkItem{}, err
	}
	text, err := message.DisplayText()
	if err != nil {
		return runtimeinput.PendingWorkItem{}, err
	}
	return runtimeinput.PendingWorkItem{
		ID:      id,
		Lane:    lane,
		Kind:    runtimeinput.PendingWorkItemKindMessage,
		State:   runtimeinput.PendingWorkItemStatePending,
		Message: &runtimeinput.PendingWorkMessage{Text: text},
	}, nil
}
