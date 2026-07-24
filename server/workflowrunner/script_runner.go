package workflowrunner

import (
	"context"
	"encoding/json"
	"errors"
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
		"KENT_WORKFLOW_ID="+string(input.Task.WorkflowID),
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
				return s.failCurrentNodeScope(finalizeCtx, controller, scope, ReasonScriptExecutionFailed, runErr)
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

func currentNodeScriptStdin(input workflowstore.CurrentNodeStartContext) ([]byte, error) {
	payload := make(map[string]any, len(input.ParameterValues)+1)
	for key, value := range input.ParameterValues {
		payload[key] = value
	}
	kent := map[string]any{
		"task_id": string(input.Task.ID),
		"node_id": string(input.Node.ID),
	}
	if branchKey, branchScoped := input.CurrentNode.Reference.TransitionBranchKey(); branchScoped {
		kent["transition_branch_key"] = string(branchKey)
	}
	payload["_kent"] = kent
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
