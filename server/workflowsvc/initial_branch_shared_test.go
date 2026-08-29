package workflowsvc

import (
	"context"
	"errors"

	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowruntime"
)

type initialBranchControllerRunner struct{}

func (initialBranchControllerRunner) StartAgentCurrentNode(
	context.Context,
	workflow.CurrentNodeReference,
	workflowruntime.TaskPromptDelivery,
	workflowexecution.CurrentNodeAssignmentSteer,
	*workflowexecution.WorkflowSessionContinuation,
	func(),
	workflowruntime.Controller,
) (sessionruntime.ExecutionHandle, error) {
	return nil, errors.New("runner must not start after branch preparation failure")
}

func (initialBranchControllerRunner) PrepareScriptPublication(
	context.Context,
	workflow.CurrentNodeReference,
	workflowruntime.Controller,
) (workflowexecution.CurrentNodeScriptPublication, error) {
	return nil, errors.New("runner must not prepare publication after branch preparation failure")
}

type initialBranchControllerSteerer struct{}

func (initialBranchControllerSteerer) SteerCurrentNodeAssignment(
	context.Context,
	workflow.CurrentNodeReference,
) (workflowexecution.CurrentNodeAssignmentSteer, error) {
	return initialBranchControllerSteer{}, nil
}

type initialBranchControllerSteer struct{}

func (initialBranchControllerSteer) Wait(context.Context) (session.CommitReceipt, error) {
	return session.CommitReceipt{Committed: true}, nil
}
