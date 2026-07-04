package workflowrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	tools "core/server/tools"
	shelltool "core/server/tools/shell"
	"core/server/workflow"
	"core/server/workflowattention"
	"core/server/workflowruntime"
	"core/server/workflowscript"
	"core/server/workflowstore"
)

const (
	ReasonScriptValidationFailed = workflowscript.ReasonValidationFailed
	ReasonScriptExecutionFailed  = "workflow_script_execution_failed"
	ReasonScriptCompletionFailed = "workflow_script_completion_failed"
	scriptOutputLimitBytes       = 256 * 1024
	scriptCancellationGrace      = 750 * time.Millisecond
	scriptAttentionFinalizeLimit = 5 * time.Second
)

func (s *Starter) startScriptWorkflowRun(req SchedulerStartRunRequest, input workflowstore.RunStartContext) error {
	runCtx, cancel := context.WithCancel(context.Background())
	if !s.registerRun(req, cancel) {
		cancel()
		return errors.New("workflow runtime starter closed")
	}
	go s.runScript(runCtx, req, input)
	return nil
}

func (s *Starter) runScript(ctx context.Context, req SchedulerStartRunRequest, input workflowstore.RunStartContext) {
	defer s.wg.Done()
	defer s.finish(req.RunID, req.Generation)
	result, err := executeWorkflowScript(ctx, req, input)
	if err != nil {
		s.interrupt(context.Background(), req.RunID, req.Generation, scriptFailureReason(err), scriptInterruptionError{err: err, detail: scriptFailureDetailJSON(err, result)})
		return
	}
	contract, err := s.scriptCompletionContract(context.Background(), req, input)
	if err != nil {
		s.interrupt(context.Background(), req.RunID, req.Generation, ReasonScriptCompletionFailed, scriptInterruptionError{err: err, detail: scriptFailureDetailJSON(err, result)})
		return
	}
	parsed, err := workflowruntime.DecodeCompletion(json.RawMessage(result.Stdout), contract)
	if err != nil {
		s.interrupt(context.Background(), req.RunID, req.Generation, ReasonScriptCompletionFailed, scriptInterruptionError{err: err, detail: scriptFailureDetailJSON(err, result)})
		return
	}
	completed, err := s.store.CompleteRun(context.Background(), workflowstore.CompleteRunRequest{
		RunID:              req.RunID,
		TransitionID:       string(parsed.TransitionID),
		OutputValues:       parsed.OutputValues,
		Commentary:         parsed.Commentary,
		Actor:              "script",
		ExpectedGeneration: req.Generation,
		RequireGeneration:  true,
	})
	if err != nil {
		s.interrupt(context.Background(), req.RunID, req.Generation, ReasonScriptCompletionFailed, scriptInterruptionError{err: err, detail: scriptFailureDetailJSON(err, result)})
		return
	}
	s.finalizeScriptCompletionAttention(context.Background(), completed)
}

func (s *Starter) scriptCompletionContract(ctx context.Context, req SchedulerStartRunRequest, input workflowstore.RunStartContext) (workflowruntime.CompletionContract, error) {
	contract := workflowCompletionContract(req, input)
	live, err := s.store.GetRunCompletionContext(ctx, req.RunID)
	if err != nil {
		return workflowruntime.CompletionContract{}, err
	}
	contract.Transitions = workflowCompletionTransitions(live.TransitionOptions, live.TransitionIDs)
	return contract, nil
}

func (s *Starter) finalizeScriptCompletionAttention(ctx context.Context, result workflowstore.CompleteRunResult) {
	if s == nil || s.attentionFinalizer == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	finalizeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), scriptAttentionFinalizeLimit)
	defer cancel()
	s.attentionFinalizer.FinalizeTransition(finalizeCtx, workflowattention.TransitionResult{
		TransitionID:                  result.TransitionID,
		State:                         result.State,
		ResolvedApprovalTransitionIDs: append([]workflow.TransitionID(nil), result.ResolvedApprovalTransitionIDs...),
	})
	if finalizer, ok := s.attentionFinalizer.(workflowInterruptedRunFinalizer); ok {
		for _, runID := range result.InterruptedRunIDs {
			if runID == "" {
				continue
			}
			runFinalizeCtx, runCancel := context.WithTimeout(context.WithoutCancel(ctx), scriptAttentionFinalizeLimit)
			finalizer.FinalizeInterruptedRun(runFinalizeCtx, runID)
			runCancel()
		}
	}
}

type scriptInterruptionError struct {
	err    error
	detail string
}

func (e scriptInterruptionError) Error() string {
	return e.err.Error()
}

func (e scriptInterruptionError) Unwrap() error {
	return e.err
}

func (e scriptInterruptionError) InterruptionDetailJSON() string {
	return e.detail
}

type workflowScriptResult struct {
	ResolvedPath   string
	Stdout         []byte
	Stderr         []byte
	StdoutOverflow bool
	StderrOverflow bool
	ExitCode       int
	Canceled       bool
}

type workflowScriptError struct {
	Reason string
	Err    error
}

func (e workflowScriptError) Error() string {
	return e.Err.Error()
}

func (e workflowScriptError) Unwrap() error {
	return e.Err
}

func scriptFailureReason(err error) string {
	var scriptErr workflowScriptError
	if errors.As(err, &scriptErr) && scriptErr.Reason != "" {
		return scriptErr.Reason
	}
	return ReasonScriptExecutionFailed
}

func executeWorkflowScript(ctx context.Context, req SchedulerStartRunRequest, input workflowstore.RunStartContext) (workflowScriptResult, error) {
	resolvedPath, err := resolveWorkflowScriptPath(input)
	if err != nil {
		return workflowScriptResult{}, workflowScriptError{Reason: ReasonScriptValidationFailed, Err: err}
	}
	stdin, err := workflowScriptStdin(req, input)
	if err != nil {
		return workflowScriptResult{ResolvedPath: resolvedPath}, workflowScriptError{Reason: ReasonScriptExecutionFailed, Err: err}
	}
	cmd := exec.Command(resolvedPath)
	cmd.Dir = input.WorktreeRoot
	cmd.Env = workflowScriptEnv(req, input)
	cmd.Stdin = strings.NewReader(string(stdin))
	prepareScriptCommand(cmd)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return workflowScriptResult{ResolvedPath: resolvedPath}, workflowScriptError{Reason: ReasonScriptExecutionFailed, Err: err}
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return workflowScriptResult{ResolvedPath: resolvedPath}, workflowScriptError{Reason: ReasonScriptExecutionFailed, Err: err}
	}
	if err := cmd.Start(); err != nil {
		return workflowScriptResult{ResolvedPath: resolvedPath}, workflowScriptError{Reason: ReasonScriptExecutionFailed, Err: err}
	}
	stdout := shelltool.NewBoundedOutput(scriptOutputLimitBytes)
	stderr := shelltool.NewBoundedOutput(scriptOutputLimitBytes)
	var copyWG sync.WaitGroup
	copyWG.Add(2)
	go func() {
		defer copyWG.Done()
		_, _ = io.Copy(stdout, stdoutPipe)
	}()
	go func() {
		defer copyWG.Done()
		_, _ = io.Copy(stderr, stderrPipe)
	}()
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()
	canceled := false
	var waitErr error
	select {
	case waitErr = <-waitCh:
	case <-ctx.Done():
		canceled = true
		_ = terminateScriptProcess(cmd.Process)
		grace := time.NewTimer(scriptCancellationGrace)
		select {
		case waitErr = <-waitCh:
			if !grace.Stop() {
				<-grace.C
			}
		case <-grace.C:
			_ = killScriptProcess(cmd.Process)
			waitErr = <-waitCh
		}
	}
	copyWG.Wait()
	result := workflowScriptResult{
		ResolvedPath:   resolvedPath,
		Stdout:         stdout.Bytes(),
		Stderr:         stderr.Bytes(),
		StdoutOverflow: stdout.Overflow(),
		StderrOverflow: stderr.Overflow(),
		ExitCode:       processExitCode(waitErr),
		Canceled:       canceled,
	}
	switch {
	case canceled:
		return result, workflowScriptError{Reason: ReasonRuntimeCanceled, Err: context.Canceled}
	case waitErr != nil:
		return result, workflowScriptError{Reason: ReasonScriptExecutionFailed, Err: waitErr}
	case result.StdoutOverflow:
		return result, workflowScriptError{Reason: ReasonScriptCompletionFailed, Err: errors.New("script stdout exceeded workflow completion limit")}
	default:
		return result, nil
	}
}

func resolveWorkflowScriptPath(input workflowstore.RunStartContext) (string, error) {
	return workflowscript.ResolveExecutable(workflowscript.ValidationRequest{
		RawPath:             input.Node.ScriptPath,
		WorktreeRoot:        input.WorktreeRoot,
		RequireWorktreeRoot: true,
	})
}

func workflowScriptStdin(req SchedulerStartRunRequest, input workflowstore.RunStartContext) ([]byte, error) {
	payload := make(map[string]any, len(input.InputValues)+1)
	for key, value := range input.InputValues {
		payload[key] = value
	}
	payload["_kent"] = map[string]string{
		"run_id":       string(req.RunID),
		"placement_id": string(req.PlacementID),
	}
	return json.Marshal(payload)
}

func workflowScriptEnv(req SchedulerStartRunRequest, input workflowstore.RunStartContext) []string {
	env := tools.EnrichShellEnv(os.Environ())
	env = append(env,
		"KENT_WORKFLOW_RUN_ID="+string(req.RunID),
		"KENT_WORKFLOW_PLACEMENT_ID="+string(req.PlacementID),
		"KENT_WORKFLOW_TASK_ID="+string(input.Task.ID),
		"KENT_WORKFLOW_ID="+string(input.Task.WorkflowID),
		"KENT_WORKFLOW_NODE_ID="+string(input.Node.ID),
		"KENT_WORKTREE_ROOT="+input.WorktreeRoot,
	)
	return env
}

func scriptFailureDetailJSON(err error, result workflowScriptResult) string {
	detail := map[string]any{
		"error":           strings.TrimSpace(err.Error()),
		"script_path":     result.ResolvedPath,
		"exit_code":       result.ExitCode,
		"canceled":        result.Canceled,
		"stdout":          string(result.Stdout),
		"stderr":          string(result.Stderr),
		"stdout_overflow": result.StdoutOverflow,
		"stderr_overflow": result.StderrOverflow,
	}
	body, marshalErr := json.Marshal(detail)
	if marshalErr != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(body)
}
