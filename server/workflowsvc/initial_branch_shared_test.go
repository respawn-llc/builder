package workflowsvc

import (
	"context"
	"errors"

	"core/server/session"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowruntime"
)

type initialBranchControllerRunner struct{}

func (initialBranchControllerRunner) PrepareAgentPublication(
	context.Context,
	workflow.CurrentNodeReference,
	workflowruntime.TaskPromptDelivery,
	workflowexecution.CurrentNodeAssignmentSteer,
	func(),
	workflowruntime.Controller,
) (workflowexecution.CurrentNodeAgentPublication, error) {
	return nil, errors.New("runner must not prepare publication after branch preparation failure")
}

func (initialBranchControllerRunner) PrepareScriptPublication(
	context.Context,
	workflow.CurrentNodeReference,
	workflowruntime.Controller,
) (workflowexecution.CurrentNodeScriptPublication, error) {
	return nil, errors.New("runner must not prepare publication after branch preparation failure")
}

type concurrentResumeControllerRunner struct {
	release <-chan struct{}
}

func (runner concurrentResumeControllerRunner) StartCurrentNode(
	context.Context,
	workflow.CurrentNodeReference,
	workflowruntime.TaskPromptDelivery,
	*workflowexecution.CurrentNodeClassifiedAssignment,
	sessionruntime.WorkflowExecutionLease,
	workflowruntime.Controller,
) error {
	<-runner.release
	return errors.New("concurrent Resume test runner released")
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
