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

	"core/shared/boundedio"
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
	Workflow *WorkflowExecutionRef
	Command  ScriptCommand
	Finalize func(context.Context, ExecutionScope, ScriptResult, error) error
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

type preparedScriptExecutionState uint8

const (
	preparedScriptExecutionPrepared preparedScriptExecutionState = iota + 1
	preparedScriptExecutionRunning
	preparedScriptExecutionCommitFailed
	preparedScriptExecutionAborted
)

type PreparedScriptExecution struct {
	request   ScriptExecutionRequest
	process   *scriptProcess
	execution *execution

	mu         sync.Mutex
	state      preparedScriptExecutionState
	commitErr  error
	activation chan struct{}
	activate   sync.Once
}

func (p *PreparedScriptExecution) Handle() ExecutionHandle {
	if p == nil || p.execution == nil {
		panic("prepared script execution is uninitialized")
	}
	return executionHandle{execution: p.execution}
}

func (p *PreparedScriptExecution) Commit() error {
	if p == nil || p.execution == nil || p.process == nil {
		return errors.New("prepared script execution is uninitialized")
	}
	p.mu.Lock()
	if p.state != preparedScriptExecutionPrepared {
		state := p.state
		p.mu.Unlock()
		return fmt.Errorf("prepared script execution cannot commit from state %d", state)
	}
	if err := p.process.cmd.Start(); err != nil {
		p.state = preparedScriptExecutionCommitFailed
		p.commitErr = err
		p.mu.Unlock()
		return err
	}
	p.state = preparedScriptExecutionRunning
	p.mu.Unlock()
	go func() {
		result, runErr, stopErr := p.process.wait(p.execution.ctx)
		p.execution.beginFinalization()
		select {
		case <-p.activation:
		case <-p.execution.ctx.Done():
			p.execution.finish(ExecutionResult{Script: &result}, runErr, stopErr)
			return
		}
		var finalizeErr error
		if p.request.Finalize != nil {
			finalizeErr = p.request.Finalize(context.WithoutCancel(p.execution.ctx), p.execution.scope, result.clone(), runErr)
		}
		p.execution.finish(ExecutionResult{Script: &result}, errors.Join(runErr, finalizeErr), stopErr)
	}()
	return nil
}

func (p *PreparedScriptExecution) Activate() {
	if p == nil || p.execution == nil || p.process == nil {
		panic("prepared script execution is uninitialized")
	}
	p.mu.Lock()
	state := p.state
	p.mu.Unlock()
	if state != preparedScriptExecutionRunning {
		panic(fmt.Sprintf("prepared script execution cannot activate from state %d", state))
	}
	p.execution.authority.activateExecution(p.execution)
	p.activate.Do(func() { close(p.activation) })
}

func (p *PreparedScriptExecution) Abort() error {
	if p == nil {
		return nil
	}
	if p.execution == nil {
		return errors.New("prepared script execution is uninitialized")
	}
	p.mu.Lock()
	var runErr error
	switch p.state {
	case preparedScriptExecutionPrepared:
	case preparedScriptExecutionCommitFailed:
		runErr = p.commitErr
	case preparedScriptExecutionAborted:
		p.mu.Unlock()
		return nil
	case preparedScriptExecutionRunning:
		p.mu.Unlock()
		return errors.New("running script execution cannot be aborted")
	default:
		state := p.state
		p.mu.Unlock()
		return fmt.Errorf("prepared script execution cannot abort from state %d", state)
	}
	p.state = preparedScriptExecutionAborted
	p.mu.Unlock()
	p.execution.finish(ExecutionResult{}, runErr, nil)
	return nil
}

func (a *Authority) PrepareScriptExecution(ctx context.Context, req ScriptExecutionRequest) (*PreparedScriptExecution, error) {
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
	a.byScope[execution.scope.ID()] = execution
	if workflowRef, ok := execution.scope.Workflow(); ok {
		a.byWorkflow[workflowRef] = execution
		a.recordWorkflowExecutionMapMutationLocked()
	}
	a.mu.Unlock()
	return &PreparedScriptExecution{
		request:    req,
		process:    process,
		execution:  execution,
		state:      preparedScriptExecutionPrepared,
		activation: make(chan struct{}),
	}, nil
}

func (a *Authority) StartScriptExecution(ctx context.Context, req ScriptExecutionRequest) (ExecutionHandle, error) {
	prepared, err := a.PrepareScriptExecution(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := prepared.Commit(); err != nil {
		return nil, errors.Join(err, prepared.Abort())
	}
	prepared.Activate()
	return prepared.Handle(), nil
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
