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
	"core/server/workflowexecution"
	"core/server/workflowruntime"
	"core/server/workflowscript"
	"core/server/workflowstore"
	"core/shared/runtimeids"
)

const (
	ReasonScriptExecutionFailed  = "workflow_script_execution_failed"
	ReasonScriptCompletionFailed = "workflow_script_completion_failed"
)

type currentNodeScriptPublication struct {
	detached *sessionruntime.DetachedScriptExecution
}

func (p *currentNodeScriptPublication) Publish(
	ctx context.Context,
	admit func() error,
	published func(sessionruntime.ExecutionHandle),
) (sessionruntime.ExecutionHandle, func(), error) {
	if p == nil || p.detached == nil {
		return nil, nil, errors.New("detached Script publication is required")
	}
	return p.detached.Publish(ctx, admit, published)
}

func (p *currentNodeScriptPublication) Cancel() {
	if p != nil && p.detached != nil {
		p.detached.Cancel()
	}
}

func (s *Starter) PrepareScriptPublication(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
	operationID runtimeids.CurrentNodeOperationID,
	controller workflowruntime.Controller,
) (workflowexecution.CurrentNodeScriptPublication, error) {
	if s.closed.Load() {
		return nil, errors.New("workflow runtime starter closed")
	}
	input, err := s.store.ResolveCurrentNodeStartContext(ctx, reference)
	if err != nil {
		return nil, err
	}
	if input.Node.Kind != workflow.NodeKindScript {
		return nil, nil
	}
	command, err := currentNodeScriptCommand(input)
	if err != nil {
		return nil, err
	}
	detached, err := s.runtimeAuthority.PrepareDetachedScriptExecution(ctx, sessionruntime.DetachedScriptExecutionRequest{
		Workflow: sessionruntime.WorkflowExecutionRef{
			ProjectID:   input.Task.ProjectID,
			WorkflowID:  input.Workflow.ID,
			OperationID: operationID,
			CurrentNode: input.CurrentNode.Reference,
		},
		Command: command,
		Finalize: func(finalizeCtx context.Context, scope sessionruntime.ExecutionScope, result sessionruntime.ScriptResult, runErr error) error {
			return s.finalizeCurrentNodeScript(finalizeCtx, controller, input, scope, result, runErr)
		},
	})
	if err != nil {
		return nil, err
	}
	return &currentNodeScriptPublication{detached: detached}, nil
}

func currentNodeScriptCommand(input workflowstore.CurrentNodeStartContext) (sessionruntime.ScriptCommand, error) {
	executionRoot, err := requireCurrentNodeExecutionRoot(input)
	if err != nil {
		return sessionruntime.ScriptCommand{}, err
	}
	resolvedPath, err := workflowscript.ResolveExecutable(workflowscript.ValidationRequest{
		RawPath:     input.Node.ScriptPath,
		RootPath:    stringPointer(executionRoot.EffectiveRoot()),
		RequireRoot: true,
	})
	if err != nil {
		return sessionruntime.ScriptCommand{}, err
	}
	stdin, err := currentNodeScriptStdin(input)
	if err != nil {
		return sessionruntime.ScriptCommand{}, err
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
	return sessionruntime.ScriptCommand{
		Path: resolvedPath, Workdir: stringPointer(executionRoot.EffectiveRoot()),
		Env: env, Stdin: stdin,
	}, nil
}

func (s *Starter) finalizeCurrentNodeScript(
	ctx context.Context,
	controller workflowruntime.Controller,
	input workflowstore.CurrentNodeStartContext,
	scope sessionruntime.ExecutionScope,
	result sessionruntime.ScriptResult,
	runErr error,
) error {
	if runErr != nil || result.Canceled || result.StdoutOverflow {
		return s.failCurrentNodeScope(
			ctx, controller, scope, ReasonScriptExecutionFailed,
			scriptExecutionFailure(result, runErr),
		)
	}
	contract := workflowruntime.CompletionContract{Transitions: workflowCompletionTransitions(input.TransitionOptions, input.TransitionIDs)}
	parsed, err := workflowruntime.DecodeCompletion(json.RawMessage(result.Stdout), contract)
	if err != nil {
		return s.failCurrentNodeScope(ctx, controller, scope, ReasonScriptCompletionFailed, err)
	}
	_, err = controller.CompleteScriptCurrentNode(ctx, workflowruntime.ScriptCompletionRequest{
		ScopeID: scope.ID(), TransitionID: parsed.TransitionID,
		OutputValues: parsed.OutputValues, Commentary: parsed.Commentary,
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
