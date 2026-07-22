package workflowsvc

import (
	"context"
	"testing"

	"core/shared/serverapi"
)

func TestServiceRejectsMalformedAttentionAndActivityReadModelOutput(t *testing.T) {
	t.Run("global attention", func(t *testing.T) {
		ctx, service, _ := newWorkflowServiceTestContext(t)
		service.readModels.Attention = malformedWorkflowAttentionReadModel{
			global: serverapi.WorkflowAttentionListResponse{
				Items: []serverapi.WorkflowAttentionItem{{Kind: "validation_blocker"}},
			},
		}
		if _, err := service.ListWorkflowAttention(ctx, serverapi.WorkflowAttentionListRequest{}); err == nil {
			t.Fatal("ListWorkflowAttention accepted malformed read-model output")
		}
	})

	t.Run("task attention task mismatch", func(t *testing.T) {
		ctx, service, _ := newWorkflowServiceTestContext(t)
		service.readModels.Attention = malformedWorkflowAttentionReadModel{
			task: serverapi.WorkflowTaskAttentionListResponse{
				Items: []serverapi.WorkflowAttentionItem{workflowAttentionItemForServiceTest("task-other")},
			},
		}
		if _, err := service.ListWorkflowTaskAttention(ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: "task-requested"}); err == nil {
			t.Fatal("ListWorkflowTaskAttention accepted an item for another task")
		}
	})

	t.Run("activity nesting", func(t *testing.T) {
		ctx, service, _ := newWorkflowServiceTestContext(t)
		service.readModels.Activity = malformedWorkflowActivityReadModel{
			response: serverapi.WorkflowTaskActivityListResponse{
				Items: []serverapi.WorkflowTaskActivityItem{{
					Type:   "comment",
					TaskID: "task-requested",
					Attention: &serverapi.WorkflowAttentionItem{
						Kind:       "interrupted_run",
						TaskID:     "task-requested",
						WorkflowID: workflowIDPointerForServiceTest(),
						RunID:      workflowAttentionStringForServiceTest("run-1"),
					},
				}},
			},
		}
		if _, err := service.ListWorkflowTaskActivity(ctx, serverapi.WorkflowTaskActivityListRequest{TaskID: "task-requested"}); err == nil {
			t.Fatal("ListWorkflowTaskActivity accepted incoherent nested attention")
		}
	})
}

type malformedWorkflowAttentionReadModel struct {
	global serverapi.WorkflowAttentionListResponse
	task   serverapi.WorkflowTaskAttentionListResponse
}

func (m malformedWorkflowAttentionReadModel) List(context.Context, serverapi.WorkflowAttentionListRequest) (serverapi.WorkflowAttentionListResponse, error) {
	return m.global, nil
}

func (m malformedWorkflowAttentionReadModel) ListTask(context.Context, serverapi.WorkflowTaskAttentionListRequest) (serverapi.WorkflowTaskAttentionListResponse, error) {
	return m.task, nil
}

type malformedWorkflowActivityReadModel struct {
	response serverapi.WorkflowTaskActivityListResponse
}

func (m malformedWorkflowActivityReadModel) List(context.Context, serverapi.WorkflowTaskActivityListRequest) (serverapi.WorkflowTaskActivityListResponse, error) {
	return m.response, nil
}

func workflowAttentionItemForServiceTest(taskID string) serverapi.WorkflowAttentionItem {
	return serverapi.WorkflowAttentionItem{
		Kind:       "interrupted_run",
		TaskID:     taskID,
		WorkflowID: workflowIDPointerForServiceTest(),
		RunID:      workflowAttentionStringForServiceTest("run-1"),
	}
}

func workflowIDPointerForServiceTest() *string {
	workflowID := "workflow-1"
	return &workflowID
}

func workflowAttentionStringForServiceTest(value string) *string {
	return &value
}
