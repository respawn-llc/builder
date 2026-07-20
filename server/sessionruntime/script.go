package sessionruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"core/shared/boundedio"
	"core/shared/ownedprocess"
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
	launchRequest     ownedprocess.LaunchRequest
	owner             *ownedprocess.Owner
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
	if err == nil {
		err = process.start()
	}
	if err != nil {
		a.mu.Unlock()
		return nil, err
	}
	handle := executionHandle{execution: execution}
	a.byScope[execution.scope.ID()] = execution
	if workflowRef, ok := execution.scope.Workflow(); ok {
		a.byWorkflow[workflowRef] = execution
	}
	a.mu.Unlock()

	go func() {
		result, runErr, stopErr := process.wait(execution.ctx)
		var finalizeErr error
		if req.Finalize != nil {
			finalizeErr = req.Finalize(context.WithoutCancel(execution.ctx), execution.scope, result.clone(), runErr)
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
	var workdir *string
	if command.Workdir != nil {
		if *command.Workdir == "" {
			return nil, fmt.Errorf("script workdir must not be empty when present")
		}
		value := *command.Workdir
		workdir = &value
	}
	var stdin io.Reader
	if command.Stdin != nil {
		stdin = bytes.NewReader(command.Stdin)
	}
	stdout, err := boundedio.NewWriter(outputLimit)
	if err != nil {
		return nil, fmt.Errorf("initialize script stdout capture: %w", err)
	}
	stderr, err := boundedio.NewWriter(outputLimit)
	if err != nil {
		return nil, fmt.Errorf("initialize script stderr capture: %w", err)
	}
	return &scriptProcess{
		launchRequest: ownedprocess.LaunchRequest{
			Argv:   append([]string{command.Path}, command.Args...),
			Cwd:    workdir,
			Env:    append([]string(nil), command.Env...),
			Stdin:  stdin,
			Stdout: stdout,
			Stderr: stderr,
		},
		stdout:            stdout,
		stderr:            stderr,
		cancellationGrace: cancellationGrace,
	}, nil
}

func (p *scriptProcess) start() error {
	owner, err := ownedprocess.Launch(p.launchRequest)
	if err != nil {
		return err
	}
	p.owner = owner
	return nil
}

func (p *scriptProcess) wait(ctx context.Context) (ScriptResult, error, error) {
	wait := make(chan error, 1)
	go func() {
		wait <- p.owner.Wait()
	}()
	if ctx.Err() != nil {
		return p.cancel(wait)
	}
	select {
	case <-ctx.Done():
		return p.cancel(wait)
	case waitErr := <-wait:
		if ctx.Err() != nil {
			return p.cancel(readyScriptWait(waitErr))
		}
		closeErr := normalizeStoppedProcessError(p.owner.Close())
		return p.result(waitErr, false), waitErr, closeErr
	}
}

func (p *scriptProcess) cancel(wait <-chan error) (ScriptResult, error, error) {
	terminationErr := normalizeStoppedProcessError(p.owner.Terminate())
	grace := time.NewTimer(p.cancellationGrace)
	var (
		waitErr  error
		closeErr error
	)
	select {
	case waitErr = <-wait:
		<-grace.C
		closeErr = normalizeStoppedProcessError(p.owner.Close())
	case <-grace.C:
		closeErr = normalizeStoppedProcessError(p.owner.Close())
		waitErr = <-wait
	}
	return p.result(waitErr, true), context.Canceled, errors.Join(terminationErr, closeErr)
}

func (p *scriptProcess) result(waitErr error, canceled bool) ScriptResult {
	return ScriptResult{
		Stdout:         p.stdout.Bytes(),
		Stderr:         p.stderr.Bytes(),
		StdoutOverflow: p.stdout.Overflow(),
		StderrOverflow: p.stderr.Overflow(),
		ExitCode:       scriptProcessExitCode(waitErr),
		Canceled:       canceled,
	}
}

func readyScriptWait(waitErr error) <-chan error {
	wait := make(chan error, 1)
	wait <- waitErr
	return wait
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
