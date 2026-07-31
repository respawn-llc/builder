package workflowsvc

import (
	"context"
	"errors"

	"core/server/workflow"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type WorkflowDefinitionReadModel interface {
	GetDefinition(context.Context, runtimeids.WorkflowID) (serverapi.WorkflowDefinition, map[string]workflow.NodeKind, error)
}

type WorkflowBoardReadModel interface {
	Get(context.Context, serverapi.WorkflowBoardRequest) (serverapi.WorkflowBoard, error)
	ListNodeCards(context.Context, serverapi.WorkflowBoardNodeCardsListRequest) (serverapi.WorkflowBoardNodeCardsListResponse, error)
}

type WorkflowTaskListReadModel interface {
	List(context.Context, serverapi.WorkflowTaskListRequest) (serverapi.WorkflowTaskListResponse, error)
}

type WorkflowTaskDetailReadModel interface {
	GetTask(context.Context, string) (serverapi.WorkflowTaskDetail, error)
	GetTaskByProjectShortID(context.Context, string, string) (serverapi.WorkflowTaskDetail, error)
	GetTaskByShortID(context.Context, string) (serverapi.WorkflowTaskDetail, error)
}

type WorkflowActivityReadModel interface {
	List(context.Context, serverapi.WorkflowTaskActivityListRequest) (serverapi.WorkflowTaskActivityListResponse, error)
}

type WorkflowAttentionReadModel interface {
	List(context.Context, serverapi.WorkflowAttentionListRequest) (serverapi.WorkflowAttentionListResponse, error)
	ListTask(context.Context, serverapi.WorkflowTaskAttentionListRequest) (serverapi.WorkflowTaskAttentionListResponse, error)
}

type ReadModels struct {
	Definitions WorkflowDefinitionReadModel
	Board       WorkflowBoardReadModel
	TaskList    WorkflowTaskListReadModel
	TaskDetail  WorkflowTaskDetailReadModel
	Activity    WorkflowActivityReadModel
	Attention   WorkflowAttentionReadModel
}

func (r ReadModels) validate() error {
	switch {
	case r.Definitions == nil:
		return errors.New("workflow definition read model is required")
	case r.Board == nil:
		return errors.New("workflow board read model is required")
	case r.TaskList == nil:
		return errors.New("workflow task list read model is required")
	case r.TaskDetail == nil:
		return errors.New("workflow task detail read model is required")
	case r.Activity == nil:
		return errors.New("workflow activity read model is required")
	case r.Attention == nil:
		return errors.New("workflow attention read model is required")
	default:
		return nil
	}
}
