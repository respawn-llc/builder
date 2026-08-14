package sessionruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	Workflow *WorkflowExecutionLease
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
	if workflowRef, ok := execution.scope.Workflow(); ok {
		workflowKey, keyErr := workflowExecutionKeyFor(workflowRef)
		if keyErr != nil {
			a.mu.Unlock()
			return nil, keyErr
		}
		a.addWorkflowExecutionLocked(workflowRef, workflowKey, execution)
	}
	a.mu.Unlock()

	go func() {
		if req.Workflow != nil {
			if waitErr := req.Workflow.wait(execution.ctx); waitErr != nil {
				execution.finish(ExecutionResult{}, waitErr, nil)
				return
			}
		}
		if startErr := process.cmd.Start(); startErr != nil {
			// A failed start never becomes running. Its completion finalizer
			// retains exact ownership without publishing queued/running state.
			execution.beginWorkflowFinalization()
			var finalizeErr error
			if req.Finalize != nil {
				finalizeErr = a.runExecutionCallback("script_finalizer", execution.scope, func() error {
					return req.Finalize(context.WithoutCancel(execution.ctx), execution.scope, ScriptResult{}, startErr)
				})
			}
			execution.finish(ExecutionResult{}, errors.Join(startErr, finalizeErr), nil)
			return
		}
		if req.Workflow != nil {
			a.beginWorkflowExecution(execution)
		}
		result, runErr, stopErr := process.wait(execution.ctx)
		execution.beginWorkflowFinalization()
		var finalizeErr error
		if req.Finalize != nil {
			finalizeErr = a.runExecutionCallback("script_finalizer", execution.scope, func() error {
				return req.Finalize(context.WithoutCancel(execution.ctx), execution.scope, result.clone(), runErr)
			})
		}
		execution.finish(ExecutionResult{Script: &result}, errors.Join(runErr, finalizeErr), stopErr)
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
