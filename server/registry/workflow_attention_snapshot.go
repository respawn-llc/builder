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
	snapshotOut chan workflowAttentionNotificationSnapshotResult
	closeErr    error

	snapshotDone bool
	snapshotErr  error
	sequence     uint64
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
	s.start.Do(s.startSnapshotWorker)
	for !s.snapshotDone {
		if s.snapshotErr != nil {
			return s.stopWithError(s.snapshotErr)
		}
		select {
		case <-ctx.Done():
			return clientui.AttentionNotificationEvent{}, ctx.Err()
		case <-s.closed:
			return clientui.AttentionNotificationEvent{}, io.EOF
		case result := <-s.snapshotOut:
			if result.err != nil {
				if errors.Is(result.err, io.EOF) {
					s.snapshotDone = true
					continue
				}
				if errors.Is(result.err, context.Canceled) && s.worker.Err() != nil {
					return clientui.AttentionNotificationEvent{}, io.EOF
				}
				return s.stopWithError(result.err)
			}
			close(result.acknowledged)
			notification := result.notification
			return s.withLocalSequence(clientui.AttentionNotificationEvent{
				Source:  clientui.AttentionNotificationSourceSnapshot,
				Type:    clientui.AttentionNotificationEventPending,
				Pending: &notification,
			})
		}
	}
	event, err := s.live.Next(ctx)
	if err != nil {
		return clientui.AttentionNotificationEvent{}, err
	}
	return s.withLocalSequence(event)
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

func (s *workflowAttentionNotificationSubscription) startSnapshotWorker() {
	snapshot, err := s.source.OpenSnapshot(workflowAttentionNotificationSnapshotPageSize)
	if err != nil {
		s.snapshotErr = err
		return
	}
	if snapshot == nil {
		s.snapshotErr = errors.New("workflow attention notification snapshot is required")
		return
	}
	s.workers.Add(1)
	go s.pumpSnapshot(snapshot)
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

var _ serverapi.AttentionNotificationSubscription = (*workflowAttentionNotificationSubscription)(nil)
