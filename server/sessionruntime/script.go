package sessionruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	shelltool "core/server/tools/shell"
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
	stdout            *shelltool.BoundedOutput
	stderr            *shelltool.BoundedOutput
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
		err = process.cmd.Start()
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
	stdout := shelltool.NewBoundedOutput(outputLimit)
	stderr := shelltool.NewBoundedOutput(outputLimit)
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
