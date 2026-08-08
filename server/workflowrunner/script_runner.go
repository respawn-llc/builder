package workflowrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"core/server/sessionruntime"
	tools "core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowscript"
	"core/server/workflowstore"
	"core/shared/runtimeids"
)

const (
	ReasonScriptExecutionFailed  = "workflow_script_execution_failed"
	ReasonScriptCompletionFailed = "workflow_script_completion_failed"
)

func (s *Starter) startCurrentNodeScript(
	ctx context.Context,
	input workflowstore.CurrentNodeStartContext,
	lease sessionruntime.WorkflowExecutionLease,
	controller workflowruntime.Controller,
) error {
	executionRoot, err := requireCurrentNodeExecutionRoot(input)
	if err != nil {
		return err
	}
	resolvedPath, err := workflowscript.ResolveExecutable(workflowscript.ValidationRequest{
		RawPath:     input.Node.ScriptPath,
		RootPath:    stringPointer(executionRoot.EffectiveRoot()),
		RequireRoot: true,
	})
	if err != nil {
		return err
	}
	stdin, err := currentNodeScriptStdin(input)
	if err != nil {
		return err
	}
	env := tools.EnrichShellEnv(os.Environ())
	env = append(env,
		"KENT_WORKFLOW_TASK_ID="+string(input.Task.ID),
		"KENT_WORKFLOW_ID="+input.Task.WorkflowID.String(),
		"KENT_WORKFLOW_NODE_ID="+string(input.Node.ID),
		"KENT_EXECUTION_ROOT="+executionRoot.EffectiveRoot(),
	)
	if branchKey, branchScoped := input.CurrentNode.Reference.TransitionBranchKey(); branchScoped {
		env = append(env, "KENT_WORKFLOW_TRANSITION_BRANCH_KEY="+string(branchKey))
	}
	_, err = s.runtimeAuthority.StartScriptExecution(ctx, sessionruntime.ScriptExecutionRequest{
		Workflow: &lease,
		Command: sessionruntime.ScriptCommand{
			Path:    resolvedPath,
			Workdir: stringPointer(executionRoot.EffectiveRoot()),
			Env:     env,
			Stdin:   stdin,
		},
		Finalize: func(finalizeCtx context.Context, scope sessionruntime.ExecutionScope, result sessionruntime.ScriptResult, runErr error) error {
			if runErr != nil || result.Canceled || result.StdoutOverflow {
				return s.failCurrentNodeScope(
					finalizeCtx,
					controller,
					scope,
					ReasonScriptExecutionFailed,
					scriptExecutionFailure(result, runErr),
				)
			}
			contract := workflowruntime.CompletionContract{Transitions: workflowCompletionTransitions(input.TransitionOptions, input.TransitionIDs)}
			parsed, err := workflowruntime.DecodeCompletion(json.RawMessage(result.Stdout), contract)
			if err != nil {
				return s.failCurrentNodeScope(finalizeCtx, controller, scope, ReasonScriptCompletionFailed, err)
			}
			_, err = controller.CompleteCurrentNode(finalizeCtx, workflowruntime.CompletionRequest{
				ScopeID:      scope.ID(),
				TransitionID: parsed.TransitionID,
				OutputValues: parsed.OutputValues,
				Commentary:   parsed.Commentary,
			})
			return err
		},
	})
	return err
}

func scriptExecutionFailure(result sessionruntime.ScriptResult, runErr error) error {
	cause := runErr
	stderr := bytes.TrimSpace(result.Stderr)
	if len(stderr) > 0 {
		label := "script stderr"
		if result.StderrOverflow {
			label = "script stderr (truncated)"
		}
		cause = errors.Join(cause, fmt.Errorf("%s: %s", label, stderr))
	} else if result.StderrOverflow {
		cause = errors.Join(cause, errors.New("script stderr exceeded its capture limit"))
	}
	if result.StdoutOverflow {
		cause = errors.Join(cause, errors.New("script stdout exceeded its capture limit"))
	}
	return cause
}

func currentNodeScriptStdin(input workflowstore.CurrentNodeStartContext) ([]byte, error) {
	payload := make(map[string]json.RawMessage, len(input.ParameterValues)+1)
	for key, value := range input.ParameterValues {
		encodedValue, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		payload[key] = encodedValue
	}
	identity, err := workflowscript.IdentityForCurrentNode(input.CurrentNode.Reference)
	if err != nil {
		return nil, err
	}
	encodedIdentity, err := json.Marshal(identity)
	if err != nil {
		return nil, err
	}
	payload["_kent"] = encodedIdentity
	return json.Marshal(payload)
}

func stringPointer(value string) *string {
	return &value
}

func (s *Starter) failCurrentNodeScope(
	ctx context.Context,
	controller workflowruntime.Controller,
	scope sessionruntime.ExecutionScope,
	reason string,
	cause error,
) error {
	failureController, ok := controller.(interface {
		FailCurrentNodeScope(context.Context, runtimeids.ExecutionScopeID, workflow.CurrentNodeInterruptionReason, error) error
	})
	if !ok {
		return errors.New("workflow runtime controller cannot interrupt a failed current node")
	}
	return failureController.FailCurrentNodeScope(ctx, scope.ID(), workflow.CurrentNodeInterruptionReason(reason), cause)
}
