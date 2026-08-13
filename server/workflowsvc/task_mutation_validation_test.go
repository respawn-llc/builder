package workflowsvc

import (
	"context"
	"testing"

	"core/shared/serverapi"
)

func TestWorkflowTaskRawMutationIngressRejectsMalformedRequests(t *testing.T) {
	service := &Service{}
	tests := map[string]func(context.Context) error{
		"create": func(ctx context.Context) error {
			_, err := service.CreateWorkflowTask(ctx, serverapi.WorkflowTaskCreateRequest{})
			return err
		},
		"dependency add": func(ctx context.Context) error {
			_, err := service.AddWorkflowTaskDependency(ctx, serverapi.WorkflowTaskDependencyAddRequest{})
			return err
		},
		"dependency remove": func(ctx context.Context) error {
			_, err := service.RemoveWorkflowTaskDependency(ctx, serverapi.WorkflowTaskDependencyRemoveRequest{})
			return err
		},
		"update": func(ctx context.Context) error {
			_, err := service.UpdateWorkflowTask(ctx, serverapi.WorkflowTaskUpdateRequest{})
			return err
		},
		"start": func(ctx context.Context) error {
			_, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{})
			return err
		},
		"interrupt": func(ctx context.Context) error {
			_, err := service.InterruptWorkflowTask(ctx, serverapi.WorkflowTaskInterruptRequest{})
			return err
		},
		"resume": func(ctx context.Context) error {
			_, err := service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{})
			return err
		},
		"approve": func(ctx context.Context) error {
			_, err := service.ApproveWorkflowTask(ctx, serverapi.WorkflowTaskApproveRequest{})
			return err
		},
		"move preview": func(ctx context.Context) error {
			_, err := service.PreviewWorkflowTaskMove(ctx, serverapi.WorkflowTaskMovePreviewRequest{})
			return err
		},
		"move": func(ctx context.Context) error {
			_, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{})
			return err
		},
		"complete": func(ctx context.Context) error {
			_, err := service.CompleteWorkflowTask(ctx, serverapi.WorkflowTaskCompleteRequest{})
			return err
		},
		"delete": func(ctx context.Context) error {
			return service.DeleteWorkflowTask(ctx, serverapi.WorkflowTaskDeleteRequest{})
		},
		"observe": func(ctx context.Context) error {
			_, err := service.ObserveWorkflowTask(ctx, serverapi.WorkflowTaskObservationRequest{})
			return err
		},
	}
	for name, call := range tests {
		t.Run(name, func(t *testing.T) {
			if err := call(t.Context()); err == nil {
				t.Fatal("malformed raw request unexpectedly reached its owner")
			}
		})
	}
}
