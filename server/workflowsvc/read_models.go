package workflowsvc

import (
	"context"
	"errors"

	"core/server/workflow"
	"core/shared/apicontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type WorkflowDefinitionReadModel interface {
	GetDefinition(context.Context, runtimeids.WorkflowID) (serverapi.WorkflowDefinition, map[string]workflow.NodeKind, error)
}

type WorkflowBoardReadModel interface {
	GetValidated(context.Context, apicontract.Validated[serverapi.WorkflowBoardRequest]) (serverapi.WorkflowBoard, error)
	ListNodeCardsValidated(context.Context, apicontract.Validated[serverapi.WorkflowBoardNodeCardsListRequest]) (serverapi.WorkflowBoardNodeCardsListResponse, error)
}

type WorkflowTaskListReadModel interface {
	ListValidated(context.Context, apicontract.Validated[serverapi.WorkflowTaskListRequest]) (serverapi.WorkflowTaskListResponse, error)
}

type WorkflowTaskSearchReadModel interface {
	SearchValidated(context.Context, apicontract.Validated[serverapi.TaskSearchRequest]) (serverapi.TaskSearchResponse, error)
}

type WorkflowTaskDetailReadModel interface {
	GetTask(context.Context, string) (serverapi.WorkflowTaskDetail, error)
	GetTaskByProjectShortID(context.Context, string, string) (serverapi.WorkflowTaskDetail, error)
	GetTaskByShortID(context.Context, string) (serverapi.WorkflowTaskDetail, error)
	ListCurrentNodes(context.Context, string) ([]workflow.CurrentNode, error)
}

type WorkflowTaskDependencyReadModel interface {
	GetTaskDependencies(context.Context, string) (serverapi.WorkflowTaskDependencies, error)
	CountUnsatisfiedBlockers(context.Context, string) (int, error)
	ListTaskDependencies(context.Context, string, *serverapi.WorkflowTaskDependencyDirection) (serverapi.WorkflowTaskDependencyListResponse, error)
}

type WorkflowActivityReadModel interface {
	ListValidated(context.Context, apicontract.Validated[serverapi.WorkflowTaskOffsetPageRequest]) (serverapi.WorkflowTaskActivityListResponse, error)
}

type WorkflowTaskSessionReadModel interface {
	ListValidated(context.Context, apicontract.Validated[serverapi.WorkflowTaskOffsetPageRequest]) (serverapi.WorkflowTaskSessionListResponse, error)
}

type WorkflowAttentionReadModel interface {
	ListValidated(context.Context, apicontract.Validated[serverapi.WorkflowAttentionListRequest]) (serverapi.WorkflowAttentionListResponse, error)
	ListTaskValidated(context.Context, apicontract.Validated[serverapi.WorkflowTaskAttentionListRequest]) (serverapi.WorkflowTaskAttentionListResponse, error)
	ListTaskByID(context.Context, string) (serverapi.WorkflowTaskAttentionListResponse, error)
}

type ReadModels struct {
	Definitions      WorkflowDefinitionReadModel
	Board            WorkflowBoardReadModel
	TaskList         WorkflowTaskListReadModel
	TaskSearch       WorkflowTaskSearchReadModel
	TaskDetail       WorkflowTaskDetailReadModel
	TaskDependencies WorkflowTaskDependencyReadModel
	TaskSessions     WorkflowTaskSessionReadModel
	Activity         WorkflowActivityReadModel
	Attention        WorkflowAttentionReadModel
	Approvals        apicontract.ApprovalViewService
}

func (r ReadModels) validate() error {
	switch {
	case r.Definitions == nil:
		return errors.New("workflow definition read model is required")
	case r.Board == nil:
		return errors.New("workflow board read model is required")
	case r.TaskList == nil:
		return errors.New("workflow task list read model is required")
	case r.TaskSearch == nil:
		return errors.New("workflow task search read model is required")
	case r.TaskDetail == nil:
		return errors.New("workflow task detail read model is required")
	case r.TaskDependencies == nil:
		return errors.New("workflow task dependency read model is required")
	case r.TaskSessions == nil:
		return errors.New("workflow Task Session read model is required")
	case r.Activity == nil:
		return errors.New("workflow activity read model is required")
	case r.Attention == nil:
		return errors.New("workflow attention read model is required")
	case r.Approvals == nil:
		return errors.New("workflow approval read model is required")
	default:
		return nil
	}
}
