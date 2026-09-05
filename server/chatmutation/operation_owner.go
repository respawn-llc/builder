package chatmutation

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrOperationOwnerClosed = errors.New("Chat operation owner is closed")

const DefaultAttachmentFinalizationTimeout = 15 * time.Second

type OperationOwner struct {
	mu                  sync.Mutex
	closing             bool
	nextID              uint64
	operations          map[uint64]context.CancelCauseFunc
	wait                sync.WaitGroup
	finalizationTimeout time.Duration
	closeErr            error
}

type OperationScope struct {
	ctx   context.Context
	owner *OperationOwner
}

func (s OperationScope) Context() context.Context {
	return s.ctx
}

func (s OperationScope) FinalizeAttachment(finalize func(context.Context) error) error {
	if s.owner == nil {
		return errors.New("Chat operation owner is required")
	}
	if finalize == nil {
		return errors.New("Chat attachment finalizer is required")
	}
	ctx, cancel := context.WithTimeout(
		context.WithoutCancel(s.ctx),
		s.owner.finalizationTimeout,
	)
	defer cancel()
	err := finalize(ctx)
	if err != nil {
		s.owner.recordFinalizationError(err)
	}
	return err
}

type Operation struct {
	done chan error
}

func NewOperationOwner(finalizationTimeout time.Duration) (*OperationOwner, error) {
	if finalizationTimeout <= 0 {
		return nil, errors.New("Chat attachment finalization timeout must be positive")
	}
	return &OperationOwner{
		operations:          make(map[uint64]context.CancelCauseFunc),
		finalizationTimeout: finalizationTimeout,
	}, nil
}

func (o *OperationOwner) Start(
	callerCtx context.Context,
	run func(OperationScope) error,
) (*Operation, error) {
	if o == nil {
		return nil, errors.New("Chat operation owner is required")
	}
	if run == nil {
		return nil, errors.New("Chat operation is required")
	}
	if callerCtx == nil {
		callerCtx = context.Background()
	}
	ctx, cancel := context.WithCancelCause(context.WithoutCancel(callerCtx))
	operation := &Operation{done: make(chan error, 1)}

	o.mu.Lock()
	if o.closing {
		o.mu.Unlock()
		cancel(ErrOperationOwnerClosed)
		return nil, ErrOperationOwnerClosed
	}
	o.nextID++
	id := o.nextID
	o.operations[id] = cancel
	o.wait.Add(1)
	o.mu.Unlock()

	go func() {
		defer func() {
			cancel(nil)
			o.mu.Lock()
			delete(o.operations, id)
			o.mu.Unlock()
			o.wait.Done()
		}()
		operation.done <- run(OperationScope{ctx: ctx, owner: o})
		close(operation.done)
	}()
	return operation, nil
}

func (o *Operation) Await(ctx context.Context) error {
	if o == nil {
		return errors.New("Chat operation is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case err := <-o.done:
		return err
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (o *OperationOwner) Close() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	if !o.closing {
		o.closing = true
		for _, cancel := range o.operations {
			cancel(ErrOperationOwnerClosed)
		}
	}
	o.mu.Unlock()
	o.wait.Wait()
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.closeErr
}

func (o *OperationOwner) recordFinalizationError(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closing {
		o.closeErr = errors.Join(o.closeErr, err)
	}
}
