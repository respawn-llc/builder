package workflowrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"core/server/sessionruntime"
	tools "core/server/tools"
	"core/server/workflowattention"
	"core/server/workflowruntime"
	"core/server/workflowscript"
	"core/server/workflowstore"
)

const (
	ReasonScriptValidationFailed = workflowscript.ReasonValidationFailed
	ReasonScriptExecutionFailed  = "workflow_script_execution_failed"
	ReasonScriptCompletionFailed = "workflow_script_completion_failed"
	scriptAttentionFinalizeLimit = 5 * time.Second
)

func (s *Starter) startScriptWorkflowRun(req SchedulerStartRunRequest, input workflowstore.RunStartContext) error {
	scriptReq, resolvedPath, err := workflowScriptExecutionRequest(req, input)
	if err != nil {
		return err
	}
	scriptReq.Finalize = func(_ context.Context, _ sessionruntime.ExecutionScope, result sessionruntime.ScriptResult, runErr error) error {
		return s.finalizeWorkflowScript(req, input, workflowScriptResultFromExecution(resolvedPath, result), runErr)
	}
	_, err = s.runtimeAuthority.StartScriptExecution(context.Background(), scriptReq)
	return err
}

func (s *Starter) finalizeWorkflowScript(req SchedulerStartRunRequest, input workflowstore.RunStartContext, result workflowScriptResult, runErr error) error {
	err := workflowScriptRunError(result, runErr)
	if err != nil {
		s.interrupt(context.Background(), req.RunID, req.Generation, scriptFailureReason(err), scriptInterruptionError{err: err, detail: scriptFailureDetailJSON(err, result)})
		return err
	}
	contract, err := s.scriptCompletionContract(context.Background(), req, input)
	if err != nil {
		s.interrupt(context.Background(), req.RunID, req.Generation, ReasonScriptCompletionFailed, scriptInterruptionError{err: err, detail: scriptFailureDetailJSON(err, result)})
		return err
	}
	parsed, err := workflowruntime.DecodeCompletion(json.RawMessage(result.Stdout), contract)
	if err != nil {
		s.interrupt(context.Background(), req.RunID, req.Generation, ReasonScriptCompletionFailed, scriptInterruptionError{err: err, detail: scriptFailureDetailJSON(err, result)})
		return err
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
		return err
	}
	s.finalizeScriptCompletionAttention(context.Background(), completed.Result)
	return nil
}

func (s *Starter) scriptCompletionContract(ctx context.Context, req SchedulerStartRunRequest, input workflowstore.RunStartContext) (workflowruntime.CompletionContract, error) {
	contract := workflowCompletionContractForRun(input.Run, input)
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
		TransitionID:                      result.TransitionID,
		State:                             result.State,
		ResolvedApprovalProjections:       workflowattention.ApprovalProjections(result.ResolvedApprovalTransitionProjections),
		ResolvedInterruptedRunProjections: workflowattention.InterruptedRunProjections(result.ResolvedInterruptedRunProjections),
	})
	if finalizer, ok := s.attentionFinalizer.(workflowInterruptedRunFinalizer); ok {
		for _, runID := range result.InterruptedRunIDs {
			if runID == "" {
				continue
			}
			runFinalizeCtx, runCancel := context.WithTimeout(context.WithoutCancel(ctx), scriptAttentionFinalizeLimit)
			finalizer.PublishPendingInterruptedRun(runFinalizeCtx, runID)
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

func workflowScriptRunError(result workflowScriptResult, err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return workflowScriptError{Reason: ReasonRuntimeCanceled, Err: err}
	case err != nil:
		return workflowScriptError{Reason: ReasonScriptExecutionFailed, Err: err}
	case result.StdoutOverflow:
		return workflowScriptError{Reason: ReasonScriptCompletionFailed, Err: errors.New("script stdout exceeded workflow completion limit")}
	default:
		return nil
	}
}

func workflowScriptExecutionRequest(req SchedulerStartRunRequest, input workflowstore.RunStartContext) (sessionruntime.ScriptExecutionRequest, string, error) {
	executionRoot, err := requireRunExecutionRoot(input)
	if err != nil {
		return sessionruntime.ScriptExecutionRequest{}, "", workflowScriptError{Reason: ReasonScriptValidationFailed, Err: err}
	}
	resolvedPath, err := resolveWorkflowScriptPath(input)
	if err != nil {
		return sessionruntime.ScriptExecutionRequest{}, "", workflowScriptError{Reason: ReasonScriptValidationFailed, Err: err}
	}
	stdin, err := workflowScriptStdin(req, input)
	if err != nil {
		return sessionruntime.ScriptExecutionRequest{}, resolvedPath, workflowScriptError{Reason: ReasonScriptExecutionFailed, Err: err}
	}
	env, err := workflowScriptEnv(req, input)
	if err != nil {
		return sessionruntime.ScriptExecutionRequest{}, resolvedPath, workflowScriptError{Reason: ReasonScriptExecutionFailed, Err: err}
	}
	workdir := executionRoot.EffectiveRoot()
	return sessionruntime.ScriptExecutionRequest{
		Workflow: &sessionruntime.WorkflowExecutionRef{
			TaskID:     req.TaskID,
			RunID:      req.RunID,
			Generation: req.Generation,
		},
		Command: sessionruntime.ScriptCommand{
			Path:    resolvedPath,
			Workdir: &workdir,
			Env:     env,
			Stdin:   stdin,
		},
	}, resolvedPath, nil
}

func workflowScriptResultFromExecution(resolvedPath string, execution sessionruntime.ScriptResult) workflowScriptResult {
	result := workflowScriptResult{ResolvedPath: resolvedPath}
	result.Stdout = execution.Stdout
	result.Stderr = execution.Stderr
	result.StdoutOverflow = execution.StdoutOverflow
	result.StderrOverflow = execution.StderrOverflow
	result.Canceled = execution.Canceled
	if execution.ExitCode != nil && *execution.ExitCode >= 0 {
		result.ExitCode = *execution.ExitCode
	} else {
		result.ExitCode = 1
	}
	return result
}

func resolveWorkflowScriptPath(input workflowstore.RunStartContext) (string, error) {
	executionRoot, err := requireRunExecutionRoot(input)
	if err != nil {
		return "", err
	}
	rootPath := executionRoot.EffectiveRoot()
	return workflowscript.ResolveExecutable(workflowscript.ValidationRequest{
		RawPath:     input.Node.ScriptPath,
		RootPath:    &rootPath,
		RequireRoot: true,
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

func workflowScriptEnv(req SchedulerStartRunRequest, input workflowstore.RunStartContext) ([]string, error) {
	env := tools.EnrichShellEnv(os.Environ())
	executionRoot, err := requireRunExecutionRoot(input)
	if err != nil {
		return nil, err
	}
	env = append(env,
		"KENT_WORKFLOW_RUN_ID="+string(req.RunID),
		"KENT_WORKFLOW_PLACEMENT_ID="+string(req.PlacementID),
		"KENT_WORKFLOW_TASK_ID="+string(input.Task.ID),
		"KENT_WORKFLOW_ID="+string(input.Task.WorkflowID),
		"KENT_WORKFLOW_NODE_ID="+string(input.Node.ID),
		"KENT_EXECUTION_ROOT="+executionRoot.EffectiveRoot(),
	)
	if executionRoot.Managed != nil {
		env = append(env, "KENT_WORKTREE_ROOT="+executionRoot.Managed.Root)
	}
	return env, nil
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
