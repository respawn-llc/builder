package workflowsvc

import (
	"context"
	"testing"

	"core/shared/serverapi"
)

type countingWorkflowAttentionReadModel struct {
	listCalls int
}

func (m *countingWorkflowAttentionReadModel) ReadAttention(_ context.Context, req serverapi.WorkflowAttentionListRequest) (serverapi.WorkflowAttentionListResponse, error) {
	m.listCalls++
	return serverapi.WorkflowAttentionListResponse{NextPageToken: req.PageToken}, nil
}

func (*countingWorkflowAttentionReadModel) ListTaskByID(context.Context, string) (serverapi.WorkflowTaskAttentionListResponse, error) {
	return serverapi.WorkflowTaskAttentionListResponse{}, nil
}

type countingWorkflowActivityReadModel struct {
	listCalls int
	taskID    string
	window    serverapi.WorkflowOffsetWindow
}

func (m *countingWorkflowActivityReadModel) ReadActivity(_ context.Context, taskID string, window serverapi.WorkflowOffsetWindow) (serverapi.WorkflowTaskActivityListResponse, error) {
	m.listCalls++
	m.taskID = taskID
	m.window = window
	return serverapi.WorkflowTaskActivityListResponse{}, nil
}

func TestWorkflowReadModelRawIngressRejectsMalformedRequests(t *testing.T) {
	service := &Service{}
	tests := map[string]func(context.Context) error{
		"labels": func(ctx context.Context) error {
			_, err := service.ListWorkflowProjectLabels(ctx, serverapi.WorkflowProjectLabelCatalogRequest{})
			return err
		},
		"task labels": func(ctx context.Context) error {
			_, err := service.GetWorkflowTaskLabels(ctx, serverapi.WorkflowTaskLabelsGetRequest{})
			return err
		},
		"attention": func(ctx context.Context) error {
			_, err := service.ListWorkflowAttention(ctx, serverapi.WorkflowAttentionListRequest{PageSize: -1})
			return err
		},
		"task attention": func(ctx context.Context) error {
			_, err := service.ListWorkflowTaskAttention(ctx, serverapi.WorkflowTaskAttentionListRequest{})
			return err
		},
		"comments": func(ctx context.Context) error {
			_, err := service.ListWorkflowTaskComments(ctx, serverapi.WorkflowTaskOffsetPageRequest{})
			return err
		},
		"activity": func(ctx context.Context) error {
			_, err := service.ListWorkflowTaskActivity(ctx, serverapi.WorkflowTaskOffsetPageRequest{})
			return err
		},
		"sessions": func(ctx context.Context) error {
			_, err := service.ListWorkflowTaskSessions(ctx, serverapi.WorkflowTaskOffsetPageRequest{})
			return err
		},
		"task list": func(ctx context.Context) error {
			_, err := service.ListWorkflowTasks(ctx, serverapi.WorkflowTaskListRequest{})
			return err
		},
		"search": func(ctx context.Context) error {
			_, err := service.SearchWorkflowTasks(ctx, serverapi.TaskSearchRequest{})
			return err
		},
		"board": func(ctx context.Context) error {
			_, err := service.GetWorkflowBoard(ctx, serverapi.WorkflowBoardRequest{})
			return err
		},
		"board cards": func(ctx context.Context) error {
			_, err := service.ListWorkflowBoardNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{})
			return err
		},
		"task detail": func(ctx context.Context) error {
			_, err := service.GetWorkflowTask(ctx, serverapi.WorkflowTaskGetRequest{})
			return err
		},
	}
	for name, call := range tests {
		t.Run(name, func(t *testing.T) {
			if err := call(t.Context()); err == nil {
				t.Fatal("malformed raw request unexpectedly reached its read model")
			}
		})
	}
}

func TestWorkflowReadModelRawIngressValidatesOnceBeforeTrustedReadExecution(t *testing.T) {
	t.Run("attention preserves page token", func(t *testing.T) {
		readModel := &countingWorkflowAttentionReadModel{}
		service := &Service{readModels: ReadModels{Attention: readModel}}
		response, err := service.ListWorkflowAttention(t.Context(), serverapi.WorkflowAttentionListRequest{
			PageSize:  7,
			PageToken: "17:item",
		})
		if err != nil {
			t.Fatalf("ListWorkflowAttention: %v", err)
		}
		if readModel.listCalls != 1 {
			t.Fatalf("read-model calls = %d, want 1", readModel.listCalls)
		}
		if response.NextPageToken != "17:item" {
			t.Fatalf("page token = %q, want preserved token", response.NextPageToken)
		}
	})

	t.Run("activity passes resolved pagination once", func(t *testing.T) {
		readModel := &countingWorkflowActivityReadModel{}
		service := &Service{readModels: ReadModels{Activity: readModel}}
		offset, limit := 11, 23
		if _, err := service.ListWorkflowTaskActivity(t.Context(), serverapi.WorkflowTaskOffsetPageRequest{
			TaskID: "task-1",
			Offset: &offset,
			Limit:  &limit,
		}); err != nil {
			t.Fatalf("ListWorkflowTaskActivity: %v", err)
		}
		if readModel.listCalls != 1 {
			t.Fatalf("read-model calls = %d, want 1", readModel.listCalls)
		}
		if readModel.taskID != "task-1" || readModel.window.Offset != offset || readModel.window.Limit != limit {
			t.Fatalf("trusted read received task=%q window=%+v", readModel.taskID, readModel.window)
		}
	})
}
