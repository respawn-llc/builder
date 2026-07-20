package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"core/shared/boundedio"
	"core/shared/lifecyclecontract"
	"core/shared/ownedprocess"
)

const (
	lifecycleHookQueueCapacity    = 32
	lifecycleHookStderrLimitBytes = 4 * 1024
	lifecycleHookInvocationLimit  = 5 * time.Second
)

type lifecycleHookInvocation struct {
	argv  []string
	stdin []byte
}

type lifecycleHookDispatcher struct {
	encoder lifecyclecontract.Encoder
	argv    []string
	queue   chan []byte
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}

	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
}

func newLifecycleHookDispatcher(
	argv []string,
	encoder lifecyclecontract.Encoder,
) (*lifecycleHookDispatcher, error) {
	if len(argv) == 0 {
		return nil, errors.New("lifecycle hook dispatcher requires argv")
	}
	copiedArgv := make([]string, len(argv))
	for index, argument := range argv {
		if strings.TrimSpace(argument) == "" {
			return nil, errors.New("lifecycle hook dispatcher argv contains a blank argument")
		}
		copiedArgv[index] = strings.Clone(argument)
	}
	ctx, cancel := context.WithCancel(context.Background())
	dispatcher := &lifecycleHookDispatcher{
		encoder: encoder,
		argv:    copiedArgv,
		queue:   make(chan []byte, lifecycleHookQueueCapacity),
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
	}
	go dispatcher.run()
	return dispatcher, nil
}

func (d *lifecycleHookDispatcher) EnqueueLifecycleEnvelope(
	envelope lifecyclecontract.Envelope,
) bool {
	if d == nil {
		return false
	}
	encoded, err := d.encoder.Encode(envelope)
	if err != nil {
		return false
	}
	payload := append([]byte(nil), encoded...)

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return false
	}
	select {
	case d.queue <- payload:
		return true
	default:
		return false
	}
}

func (d *lifecycleHookDispatcher) Close() error {
	if d == nil {
		return nil
	}
	d.closeOnce.Do(func() {
		d.mu.Lock()
		d.closed = true
		d.cancel()
		d.mu.Unlock()
		<-d.done
	})
	return nil
}

func (d *lifecycleHookDispatcher) run() {
	defer close(d.done)
	for {
		select {
		case <-d.ctx.Done():
			return
		case payload := <-d.queue:
			select {
			case <-d.ctx.Done():
				return
			default:
			}
			_ = invokeLifecycleHook(d.ctx, lifecycleHookInvocation{
				argv:  d.argv,
				stdin: payload,
			})
		}
	}
}

func invokeLifecycleHook(ctx context.Context, invocation lifecycleHookInvocation) error {
	stderr, err := boundedio.NewWriter(lifecycleHookStderrLimitBytes)
	if err != nil {
		return err
	}
	owner, err := ownedprocess.Launch(ownedprocess.LaunchRequest{
		Argv:   invocation.argv,
		Stdin:  bytes.NewReader(invocation.stdin),
		Stdout: io.Discard,
		Stderr: stderr,
	})
	if err != nil {
		return err
	}

	wait := make(chan error, 1)
	go func() {
		wait <- owner.Wait()
	}()
	invocationCtx, cancel := context.WithTimeout(ctx, lifecycleHookInvocationLimit)
	defer cancel()
	select {
	case waitErr := <-wait:
		return errors.Join(waitErr, owner.Close())
	case <-invocationCtx.Done():
		closeErr := owner.Close()
		waitErr := <-wait
		return errors.Join(invocationCtx.Err(), waitErr, closeErr)
	}
}
