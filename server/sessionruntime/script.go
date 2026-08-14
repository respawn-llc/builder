package sessionruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"core/server/workflow"
	"core/shared/boundedio"
	"core/shared/runtimeids"
)

const (
	defaultScriptOutputLimitBytes  = 256 * 1024
	defaultScriptCancellationGrace = 750 * time.Millisecond
)

type ScriptCommand struct {
	Path              string
	Args              []string
	Workdir           *string
	Env               []string
	Stdin             []byte
	OutputLimitBytes  *int
	CancellationGrace *time.Duration
}

type ScriptExecutionRequest struct {
	Command  ScriptCommand
	Finalize func(context.Context, ExecutionScope, ScriptResult, error) error
}

type DetachedScriptExecutionRequest struct {
	Workflow WorkflowExecutionRef
	Command  ScriptCommand
	Finalize func(context.Context, ExecutionScope, ScriptResult, error) error
}

type DetachedScriptExecution struct {
	authority   *Authority
	execution   *execution
	process     *scriptProcess
	workflowKey workflow.CurrentNodeReferenceKey
	finalize    func(context.Context, ExecutionScope, ScriptResult, error) error
	mu          sync.Mutex
	settled     bool
}

func (a *Authority) PrepareDetachedScriptExecution(
	ctx context.Context,
	req DetachedScriptExecutionRequest,
) (*DetachedScriptExecution, error) {
	if a == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if err := req.Workflow.Validate(); err != nil {
		return nil, err
	}
	process, err := prepareAuthorityScriptProcess(req.Command)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, ErrAuthorityClosed
	}
	workflowKey, err := workflowExecutionKeyFor(req.Workflow)
	if err != nil {
		return nil, err
	}
	if a.workflowExecutionLocked(req.Workflow, workflowKey) != nil {
		return nil, fmt.Errorf("workflow current node %v is already live", req.Workflow.CurrentNode)
	}
	executionGeneration := a.nextExecutionGenerationLocked()
	a.nextResource++
	resourceGeneration := a.nextResource
	if resourceGeneration == 0 {
		a.closed = true
		return nil, a.invariant(
			"allocate detached Script resource generation",
			errors.New("Session Runtime resource generation overflow"),
		)
	}
	scopeID := runtimeids.NewExecutionScopeID()
	scope := newScriptExecutionScope(scopeID, executionGeneration, resourceGeneration, &req.Workflow)
	runCtx, cancel := context.WithCancel(a.lifecycleCtx)
	execution := &execution{
		authority: a, scope: scope, script: &TaskScriptExecutionTarget{Path: req.Command.Path},
		ctx: runCtx, cancel: cancel, done: make(chan struct{}),
		prompts: newExecutionPromptStore(a, scope, a.promptFeed), phase: executionPhaseQueued,
	}
	return &DetachedScriptExecution{
		authority: a, execution: execution, process: process,
		workflowKey: workflowKey, finalize: req.Finalize,
	}, nil
}

func (d *DetachedScriptExecution) Scope() (ExecutionScope, error) {
	if d == nil || d.execution == nil {
		return ExecutionScope{}, sessionRuntimeInvariant(
			d != nil && d.authority != nil && d.authority.options.debug,
			"read detached Script execution Scope",
			errors.New("detached Script execution is uninitialized"),
		)
	}
	return d.execution.scope, nil
}

func (d *DetachedScriptExecution) Publish(
	ctx context.Context,
	admit func() error,
	published func(ExecutionHandle),
) (ExecutionHandle, func(), error) {
	if d == nil || d.authority == nil || d.execution == nil || d.process == nil {
		return nil, nil, errors.New("detached Script execution is required")
	}
	if admit == nil {
		return nil, nil, errors.New("detached Script admission is required")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, nil, err
	}
	d.mu.Lock()
	if d.settled {
		d.mu.Unlock()
		return nil, nil, ErrExecutionNoLongerLive
	}
	d.settled = true
	d.mu.Unlock()
	d.authority.mu.Lock()
	d.execution.exactMu.Lock()
	workflowRef, _ := d.execution.scope.Workflow()
	if d.authority.closed || d.authority.workflowExecutionLocked(workflowRef, d.workflowKey) != nil {
		d.execution.exactMu.Unlock()
		d.authority.mu.Unlock()
		d.execution.cancel()
		return nil, nil, ErrExecutionNoLongerLive
	}
	if err := admit(); err != nil {
		d.execution.exactMu.Unlock()
		d.authority.mu.Unlock()
		d.execution.cancel()
		return nil, nil, err
	}
	d.authority.byScope[d.execution.scope.ID()] = d.execution
	d.authority.addWorkflowExecutionLocked(workflowRef, d.workflowKey, d.execution)
	d.execution.phase = executionPhaseRunning
	d.execution.exactMu.Unlock()
	d.authority.mu.Unlock()
	handle := executionHandle{execution: d.execution}
	if published != nil {
		published(handle)
	}
	return handle, func() { go d.run() }, nil
}

func (d *DetachedScriptExecution) Cancel() {
	if d == nil || d.execution == nil {
		return
	}
	d.mu.Lock()
	if d.settled {
		d.mu.Unlock()
		return
	}
	d.settled = true
	d.mu.Unlock()
	d.execution.cancel()
}

func (d *DetachedScriptExecution) run() {
	if err := context.Cause(d.execution.ctx); err != nil {
		invariantErr := d.execution.beginWorkflowFinalization()
		var finalizeErr error
		if d.finalize != nil {
			finalizeErr = d.finalize(context.WithoutCancel(d.execution.ctx), d.execution.scope, ScriptResult{}, err)
		}
		d.execution.finish(ExecutionResult{}, errors.Join(err, invariantErr, finalizeErr), nil)
		return
	}
	if startErr := d.process.cmd.Start(); startErr != nil {
		invariantErr := d.execution.beginWorkflowFinalization()
		var finalizeErr error
		if d.finalize != nil {
			finalizeErr = d.finalize(context.WithoutCancel(d.execution.ctx), d.execution.scope, ScriptResult{}, startErr)
		}
		d.execution.finish(ExecutionResult{}, errors.Join(startErr, invariantErr, finalizeErr), nil)
		return
	}
	result, runErr, stopErr := d.process.wait(d.execution.ctx)
	invariantErr := d.execution.beginWorkflowFinalization()
	var finalizeErr error
	if d.finalize != nil {
		finalizeErr = d.finalize(context.WithoutCancel(d.execution.ctx), d.execution.scope, result.clone(), runErr)
	}
	d.execution.finish(ExecutionResult{Script: &result}, errors.Join(runErr, invariantErr, finalizeErr), stopErr)
}

type ScriptResult struct {
	Stdout         []byte
	Stderr         []byte
	StdoutOverflow bool
	StderrOverflow bool
	ExitCode       *int
	Canceled       bool
}

func (r ScriptResult) clone() ScriptResult {
	r.Stdout = append([]byte(nil), r.Stdout...)
	r.Stderr = append([]byte(nil), r.Stderr...)
	if r.ExitCode != nil {
		exitCode := *r.ExitCode
		r.ExitCode = &exitCode
	}
	return r
}

type scriptProcess struct {
	cmd               *exec.Cmd
	stdout            *boundedio.Writer
	stderr            *boundedio.Writer
	cancellationGrace time.Duration
}

func (a *Authority) StartScriptExecution(ctx context.Context, req ScriptExecutionRequest) (ExecutionHandle, error) {
	if a == nil {
		return nil, fmt.Errorf("session runtime authority is required")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	process, err := prepareAuthorityScriptProcess(req.Command)
	if err != nil {
		return nil, err
	}

	a.mu.Lock()
	execution, err := a.reserveScriptExecutionLocked(req)
	if err != nil {
		a.mu.Unlock()
		return nil, err
	}
	handle := executionHandle{execution: execution}
	a.byScope[execution.scope.ID()] = execution
	a.mu.Unlock()

	go func() {
		if startErr := process.cmd.Start(); startErr != nil {
			// A failed start never becomes running. Its completion finalizer
			// retains exact ownership without publishing queued/running state.
			invariantErr := execution.beginWorkflowFinalization()
			var finalizeErr error
			if req.Finalize != nil {
				finalizeErr = req.Finalize(context.WithoutCancel(execution.ctx), execution.scope, ScriptResult{}, startErr)
			}
			execution.finish(ExecutionResult{}, errors.Join(startErr, invariantErr, finalizeErr), nil)
			return
		}
		result, runErr, stopErr := process.wait(execution.ctx)
		invariantErr := execution.beginWorkflowFinalization()
		var finalizeErr error
		if req.Finalize != nil {
			finalizeErr = req.Finalize(context.WithoutCancel(execution.ctx), execution.scope, result.clone(), runErr)
		}
		execution.finish(ExecutionResult{Script: &result}, errors.Join(runErr, invariantErr, finalizeErr), stopErr)
	}()
	return handle, nil
}

func prepareAuthorityScriptProcess(command ScriptCommand) (*scriptProcess, error) {
	if command.Path == "" {
		return nil, fmt.Errorf("script executable path is required")
	}
	outputLimit := defaultScriptOutputLimitBytes
	if command.OutputLimitBytes != nil {
		if *command.OutputLimitBytes <= 0 {
			return nil, fmt.Errorf("script output limit must be positive")
		}
		outputLimit = *command.OutputLimitBytes
	}
	cancellationGrace := defaultScriptCancellationGrace
	if command.CancellationGrace != nil {
		if *command.CancellationGrace < 0 {
			return nil, fmt.Errorf("script cancellation grace must not be negative")
		}
		cancellationGrace = *command.CancellationGrace
	}
	cmd := exec.Command(command.Path, command.Args...)
	if command.Workdir != nil {
		if *command.Workdir == "" {
			return nil, fmt.Errorf("script workdir must not be empty when present")
		}
		cmd.Dir = *command.Workdir
	}
	if command.Env != nil {
		cmd.Env = append([]string(nil), command.Env...)
	}
	if command.Stdin != nil {
		cmd.Stdin = bytes.NewReader(command.Stdin)
	}
	prepareScriptCommand(cmd)
	stdout, err := boundedio.NewWriter(outputLimit)
	if err != nil {
		return nil, fmt.Errorf("initialize script stdout capture: %w", err)
	}
	stderr, err := boundedio.NewWriter(outputLimit)
	if err != nil {
		return nil, fmt.Errorf("initialize script stderr capture: %w", err)
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return &scriptProcess{
		cmd:               cmd,
		stdout:            stdout,
		stderr:            stderr,
		cancellationGrace: cancellationGrace,
	}, nil
}

func (p *scriptProcess) wait(ctx context.Context) (ScriptResult, error, error) {
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- p.cmd.Wait()
	}()

	canceled := false
	var (
		waitErr error
		stopErr error
	)
	select {
	case waitErr = <-waitCh:
	case <-ctx.Done():
		canceled = true
		stopErr = normalizeStoppedProcessError(terminateScriptProcess(p.cmd.Process))
		timer := time.NewTimer(p.cancellationGrace)
		select {
		case waitErr = <-waitCh:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			stopErr = errors.Join(stopErr, normalizeStoppedProcessError(killScriptProcess(p.cmd.Process)))
			waitErr = <-waitCh
		}
	}
	exitCode := scriptProcessExitCode(waitErr)
	result := ScriptResult{
		Stdout:         p.stdout.Bytes(),
		Stderr:         p.stderr.Bytes(),
		StdoutOverflow: p.stdout.Overflow(),
		StderrOverflow: p.stderr.Overflow(),
		ExitCode:       exitCode,
		Canceled:       canceled,
	}
	if canceled {
		return result, context.Canceled, stopErr
	}
	return result, waitErr, stopErr
}

func normalizeStoppedProcessError(err error) error {
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func scriptProcessExitCode(err error) *int {
	if err == nil {
		exitCode := 0
		return &exitCode
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return nil
	}
	exitCode := exitErr.ExitCode()
	return &exitCode
}
