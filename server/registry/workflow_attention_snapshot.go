package registry

import (
	"context"
	"errors"
	"io"
	"sync"

	"core/shared/clientui"
	"core/shared/serverapi"
)

const workflowAttentionNotificationSnapshotPageSize = 32

type WorkflowAttentionNotificationSnapshot interface {
	Next(context.Context, func(clientui.AttentionNotification) error) error
}

type WorkflowAttentionNotificationSnapshotSource interface {
	OpenSnapshot(int) (WorkflowAttentionNotificationSnapshot, error)
}

func (r *RuntimeRegistry) WithWorkflowAttentionNotificationSnapshot(source WorkflowAttentionNotificationSnapshotSource) *RuntimeRegistry {
	if r == nil {
		return r
	}
	r.workflowAttentionSnapshot = source
	return r
}

type attentionNotificationStreamResult struct {
	event clientui.AttentionNotificationEvent
	err   error
}

type workflowAttentionNotificationSnapshotResult struct {
	notification clientui.AttentionNotification
	acknowledged chan struct{}
	err          error
}

type workflowAttentionNotificationSubscription struct {
	live        serverapi.AttentionNotificationSubscription
	source      WorkflowAttentionNotificationSnapshotSource
	worker      context.Context
	cancel      context.CancelFunc
	closed      chan struct{}
	start       sync.Once
	close       sync.Once
	workers     sync.WaitGroup
	liveOut     chan attentionNotificationStreamResult
	snapshotOut chan workflowAttentionNotificationSnapshotResult
	closeErr    error

	pendingLive         *attentionNotificationStreamResult
	pendingSnapshot     *workflowAttentionNotificationSnapshotResult
	snapshotDone        bool
	snapshotErr         error
	liveAheadOfSnapshot bool
	sequence            uint64
}

func newWorkflowAttentionNotificationSubscription(
	live serverapi.AttentionNotificationSubscription,
	source WorkflowAttentionNotificationSnapshotSource,
) serverapi.AttentionNotificationSubscription {
	if source == nil {
		return live
	}
	worker, cancel := context.WithCancel(context.Background())
	return &workflowAttentionNotificationSubscription{
		live:        live,
		source:      source,
		worker:      worker,
		cancel:      cancel,
		closed:      make(chan struct{}),
		liveOut:     make(chan attentionNotificationStreamResult, 1),
		snapshotOut: make(chan workflowAttentionNotificationSnapshotResult, 1),
	}
}

func (s *workflowAttentionNotificationSubscription) Next(ctx context.Context) (clientui.AttentionNotificationEvent, error) {
	if s == nil {
		return clientui.AttentionNotificationEvent{}, io.EOF
	}
	select {
	case <-s.closed:
		return clientui.AttentionNotificationEvent{}, io.EOF
	default:
	}
	s.start.Do(s.startWorkers)
	for {
		s.collectReady()
		if s.snapshotErr != nil {
			return s.stopWithError(s.snapshotErr)
		}
		if s.pendingLive != nil && s.pendingSnapshot != nil {
			snapshotFirst, discardLive := snapshotMustPrecedeLive(
				s.pendingSnapshot.notification,
				s.pendingLive.event,
			)
			if snapshotFirst {
				if discardLive {
					s.pendingLive = nil
				}
				return s.emitSnapshot()
			}
			if s.liveAheadOfSnapshot {
				return s.emitSnapshot()
			}
			s.liveAheadOfSnapshot = true
			return s.emitLive()
		}
		if s.pendingLive != nil {
			return s.emitLive()
		}
		if s.pendingSnapshot != nil {
			return s.emitSnapshot()
		}
		if err := s.waitForReady(ctx); err != nil {
			return clientui.AttentionNotificationEvent{}, err
		}
	}
}

func (s *workflowAttentionNotificationSubscription) Close() error {
	if s == nil {
		return nil
	}
	var closeErr error
	s.close.Do(func() {
		close(s.closed)
		s.cancel()
		s.closeErr = s.live.Close()
		s.workers.Wait()
	})
	closeErr = s.closeErr
	return closeErr
}

func (s *workflowAttentionNotificationSubscription) startWorkers() {
	snapshot, err := s.source.OpenSnapshot(workflowAttentionNotificationSnapshotPageSize)
	if err != nil {
		s.snapshotErr = err
		return
	}
	if snapshot == nil {
		s.snapshotErr = errors.New("workflow attention notification snapshot is required")
		return
	}
	s.workers.Add(2)
	go s.pumpLive()
	go s.pumpSnapshot(snapshot)
}

func (s *workflowAttentionNotificationSubscription) pumpLive() {
	defer s.workers.Done()
	for {
		event, err := s.live.Next(s.worker)
		result := attentionNotificationStreamResult{event: event, err: err}
		select {
		case s.liveOut <- result:
		case <-s.worker.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func (s *workflowAttentionNotificationSubscription) pumpSnapshot(snapshot WorkflowAttentionNotificationSnapshot) {
	defer s.workers.Done()
	for {
		acknowledged := make(chan struct{})
		enqueued := false
		err := snapshot.Next(s.worker, func(notification clientui.AttentionNotification) error {
			if enqueued {
				return errors.New("workflow attention notification snapshot emitted more than one item from Next")
			}
			enqueued = true
			select {
			case s.snapshotOut <- workflowAttentionNotificationSnapshotResult{
				notification: notification,
				acknowledged: acknowledged,
			}:
				return nil
			case <-s.worker.Done():
				return context.Cause(s.worker)
			}
		})
		if err != nil {
			select {
			case s.snapshotOut <- workflowAttentionNotificationSnapshotResult{err: err}:
			case <-s.worker.Done():
			}
			return
		}
		if !enqueued {
			select {
			case s.snapshotOut <- workflowAttentionNotificationSnapshotResult{
				err: errors.New("workflow attention notification snapshot Next returned without an item"),
			}:
			case <-s.worker.Done():
			}
			return
		}
		select {
		case <-acknowledged:
		case <-s.worker.Done():
			return
		}
	}
}

func (s *workflowAttentionNotificationSubscription) collectReady() {
	if s.pendingSnapshot == nil && !s.snapshotDone && s.snapshotErr == nil {
		select {
		case result := <-s.snapshotOut:
			s.acceptSnapshotResult(result)
		default:
		}
	}
	if s.pendingLive == nil {
		select {
		case result := <-s.liveOut:
			s.pendingLive = &result
		default:
		}
	}
}

func (s *workflowAttentionNotificationSubscription) waitForReady(ctx context.Context) error {
	if s.snapshotDone {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.closed:
			return io.EOF
		case result := <-s.liveOut:
			s.pendingLive = &result
			return nil
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.closed:
		return io.EOF
	case result := <-s.liveOut:
		s.pendingLive = &result
		return nil
	case result := <-s.snapshotOut:
		s.acceptSnapshotResult(result)
		return nil
	}
}

func (s *workflowAttentionNotificationSubscription) acceptSnapshotResult(result workflowAttentionNotificationSnapshotResult) {
	if result.err == nil {
		s.pendingSnapshot = &result
		return
	}
	if errors.Is(result.err, io.EOF) {
		s.snapshotDone = true
		return
	}
	if errors.Is(result.err, context.Canceled) && s.worker.Err() != nil {
		s.snapshotDone = true
		return
	}
	s.snapshotErr = result.err
}

func (s *workflowAttentionNotificationSubscription) emitLive() (clientui.AttentionNotificationEvent, error) {
	result := *s.pendingLive
	s.pendingLive = nil
	if result.err != nil {
		return s.stopWithError(result.err)
	}
	return s.withLocalSequence(result.event)
}

func (s *workflowAttentionNotificationSubscription) emitSnapshot() (clientui.AttentionNotificationEvent, error) {
	result := *s.pendingSnapshot
	s.pendingSnapshot = nil
	s.liveAheadOfSnapshot = false
	close(result.acknowledged)
	notification := result.notification
	return s.withLocalSequence(clientui.AttentionNotificationEvent{
		Source:  clientui.AttentionNotificationSourceSnapshot,
		Type:    clientui.AttentionNotificationEventPending,
		Pending: &notification,
	})
}

func (s *workflowAttentionNotificationSubscription) stopWithError(err error) (clientui.AttentionNotificationEvent, error) {
	_ = s.Close()
	return clientui.AttentionNotificationEvent{}, err
}

func (s *workflowAttentionNotificationSubscription) withLocalSequence(event clientui.AttentionNotificationEvent) (clientui.AttentionNotificationEvent, error) {
	s.sequence++
	event.Sequence = s.sequence
	if err := serverapi.ValidateAttentionNotificationEvent(event); err != nil {
		return s.stopWithError(err)
	}
	return event, nil
}

func snapshotMustPrecedeLive(
	snapshot clientui.AttentionNotification,
	live clientui.AttentionNotificationEvent,
) (precede bool, discardLive bool) {
	switch live.Type {
	case clientui.AttentionNotificationEventResolved:
		return live.ID != nil && *live.ID == snapshot.ID, false
	case clientui.AttentionNotificationEventPending:
		if live.Pending == nil || live.Pending.ID != snapshot.ID {
			return false, false
		}
		return true, live.Pending.Revision <= snapshot.Revision
	default:
		return false, false
	}
}

var _ serverapi.AttentionNotificationSubscription = (*workflowAttentionNotificationSubscription)(nil)
