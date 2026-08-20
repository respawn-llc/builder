package workflowsvc

import (
	"context"
	"errors"

	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowruntime"
	"core/server/workflowstore"
)

type initialBranchControllerRunner struct{}

func (initialBranchControllerRunner) StartCurrentNode(
	context.Context,
	workflow.CurrentNodeReference,
	workflowruntime.TaskPromptDelivery,
	*workflowexecution.CurrentNodeClassifiedAssignment,
	sessionruntime.WorkflowExecutionLease,
	workflowruntime.Controller,
) error {
	return errors.New("runner must not start after branch preparation failure")
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

func (initialBranchControllerSteerer) PrepareManualMoveAssignments(
	context.Context,
	[]workflowstore.CurrentNodeStartContext,
) (
	workflowstore.ManualMoveTargetAssignmentPreparation,
	map[workflow.CurrentNodeReferenceKey]workflowexecution.CurrentNodeAssignmentSteer,
	error,
) {
	return workflowstore.ManualMoveTargetAssignmentPreparation{}, nil, errors.New("Manual Move assignment preparation must not run")
}

type initialBranchControllerSteer struct{}

func (initialBranchControllerSteer) Wait(context.Context) (session.CommitReceipt, error) {
	return session.CommitReceipt{Committed: true}, nil
}
