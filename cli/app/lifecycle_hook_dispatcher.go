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
	issues  *lifecycleHookIssueMailbox

	mu        sync.Mutex
	closed    bool
	disabled  bool
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
		issues:  newLifecycleHookIssueMailbox(),
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
		d.issues.Report(lifecycleHookIssue{
			Kind:  lifecycleHookIssueEncoding,
			Cause: err,
		})
		return false
	}
	payload := append([]byte(nil), encoded...)

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.disabled {
		return false
	}
	select {
	case d.queue <- payload:
		return true
	default:
		d.issues.ReportOverload()
		return false
	}
}

func (d *lifecycleHookDispatcher) Issues() *lifecycleHookIssueMailbox {
	if d == nil {
		return nil
	}
	return d.issues
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
		d.issues.Close()
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
			result := invokeLifecycleHook(d.ctx, lifecycleHookInvocation{
				argv:  d.argv,
				stdin: payload,
			})
			if d.handleInvocationResult(result) {
				return
			}
		}
	}
}

type lifecycleHookInvocationResult struct {
	startErr            error
	waitErr             error
	closeErr            error
	timedOut            bool
	stderr              *string
	stderrOverflowBytes *int64
}

func invokeLifecycleHook(
	ctx context.Context,
	invocation lifecycleHookInvocation,
) lifecycleHookInvocationResult {
	stderr, err := boundedio.NewWriter(lifecycleHookStderrLimitBytes)
	if err != nil {
		return lifecycleHookInvocationResult{startErr: err}
	}
	owner, err := ownedprocess.Launch(ownedprocess.LaunchRequest{
		Argv:   invocation.argv,
		Stdin:  bytes.NewReader(invocation.stdin),
		Stdout: io.Discard,
		Stderr: stderr,
	})
	if err != nil {
		return lifecycleHookInvocationResult{startErr: err}
	}

	wait := make(chan error, 1)
	go func() {
		wait <- owner.Wait()
	}()
	invocationCtx, cancel := context.WithTimeout(ctx, lifecycleHookInvocationLimit)
	defer cancel()
	result := lifecycleHookInvocationResult{}
	select {
	case waitErr := <-wait:
		result.waitErr = waitErr
		result.closeErr = owner.Close()
	case <-invocationCtx.Done():
		result.timedOut = errors.Is(invocationCtx.Err(), context.DeadlineExceeded)
		result.closeErr = owner.Close()
		result.waitErr = <-wait
	}
	if value := stderr.String(); value != "" {
		result.stderr = &value
	}
	if overflow := stderr.OverflowBytes(); overflow > 0 {
		result.stderrOverflowBytes = &overflow
	}
	return result
}

func (d *lifecycleHookDispatcher) handleInvocationResult(
	result lifecycleHookInvocationResult,
) (disabled bool) {
	if result.startErr != nil {
		if failure, deterministic := classifyLifecycleHookLaunchFailure(result.startErr); deterministic {
			d.mu.Lock()
			d.disabled = true
			d.mu.Unlock()
			d.issues.Report(lifecycleHookIssue{
				Kind:          lifecycleHookIssueLaunchDisabled,
				Cause:         result.startErr,
				LaunchFailure: &failure,
			})
			return true
		}
		d.issues.Report(lifecycleHookIssue{
			Kind:  lifecycleHookIssueLaunchFailed,
			Cause: result.startErr,
		})
		return false
	}
	if result.timedOut {
		d.issues.Report(lifecycleHookIssue{
			Kind:                lifecycleHookIssueTimeout,
			Cause:               context.DeadlineExceeded,
			Stderr:              result.stderr,
			StderrOverflowBytes: result.stderrOverflowBytes,
		})
		return false
	}
	if result.waitErr != nil {
		issue := lifecycleHookIssue{
			Kind:                lifecycleHookIssueNonzeroExit,
			Cause:               errors.Join(result.waitErr, result.closeErr),
			Stderr:              result.stderr,
			StderrOverflowBytes: result.stderrOverflowBytes,
		}
		if code, ok := lifecycleHookExitCode(result.waitErr); ok {
			issue.ExitCode = &code
		}
		d.issues.Report(issue)
		return false
	}
	if result.closeErr != nil {
		d.issues.Report(lifecycleHookIssue{
			Kind:                lifecycleHookIssueLaunchFailed,
			Cause:               result.closeErr,
			Stderr:              result.stderr,
			StderrOverflowBytes: result.stderrOverflowBytes,
		})
	}
	return false
}

func lifecycleHookExitCode(err error) (int, bool) {
	type exitCoder interface {
		ExitCode() int
	}
	var coded exitCoder
	if !errors.As(err, &coded) {
		return 0, false
	}
	code := coded.ExitCode()
	if code < 0 {
		return 0, false
	}
	return code, true
}
