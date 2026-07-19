package workflowview

import (
	"context"
	"errors"
	"strings"

	"core/internal/testharness/testsetup"
	"core/server/metadata"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/server/worktree"
	"core/shared/serverapi"
)

type workflowViewTestService struct {
	metadata    *metadata.Store
	definitions *DefinitionProjection
	projector   *TaskProjector
	taskList    *TaskList
	taskDetail  *TaskDetail
	activity    *Activity
	transcripts SessionActiveTranscriptProvider
	prompts     PendingPromptSource
}

func newWorkflowViewTestReadModels(metadataStore *metadata.Store, transcripts SessionActiveTranscriptProvider, prompts PendingPromptSource) (*workflowViewTestService, error) {
	if metadataStore == nil {
		return nil, errors.New("metadata store is required")
	}
	workflowStore, err := workflowstore.New(metadataStore)
	if err != nil {
		return nil, err
	}
	definitions, err := NewDefinitionProjection(workflowStore)
	if err != nil {
		return nil, err
	}
	projector := NewTaskProjector()
	taskList, err := NewTaskList(metadataStore, definitions, projector)
	if err != nil {
		return nil, err
	}
	taskDetail, err := NewTaskDetail(metadataStore, definitions, projector, worktree.NewGitInspector(nil))
	if err != nil {
		return nil, err
	}
	activity, err := NewActivity(metadataStore, definitions, projector)
	if err != nil {
		return nil, err
	}
	return &workflowViewTestService{
		metadata:    metadataStore,
		definitions: definitions,
		projector:   projector,
		taskList:    taskList,
		taskDetail:  taskDetail,
		activity:    activity,
		transcripts: transcripts,
		prompts:     prompts,
	}, nil
}

func (s *workflowViewTestService) GetDefinition(ctx context.Context, workflowID string) (serverapi.WorkflowDefinition, map[string]workflow.NodeKind, error) {
	if s == nil {
		return serverapi.WorkflowDefinition{}, nil, errors.New("workflow view test service is required")
	}
	if strings.TrimSpace(workflowID) == "" {
		return serverapi.WorkflowDefinition{}, nil, errors.New("workflow_id is required")
	}
	return s.definitions.GetDefinition(ctx, workflowID)
}

func (s *workflowViewTestService) GetBoard(ctx context.Context, req serverapi.WorkflowBoardRequest, roleResolver workflow.RoleResolver) (serverapi.WorkflowBoard, error) {
	board, err := NewBoard(s.metadata, s.definitions, roleResolver, s.projector)
	if err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	return board.Get(ctx, req)
}

func (s *workflowViewTestService) ListTasks(ctx context.Context, req serverapi.WorkflowTaskListRequest, _ workflow.RoleResolver) (serverapi.WorkflowTaskListResponse, error) {
	return s.taskList.List(ctx, req)
}

func (s *workflowViewTestService) ListBoardNodeCards(ctx context.Context, req serverapi.WorkflowBoardNodeCardsListRequest, roleResolver workflow.RoleResolver) (serverapi.WorkflowBoardNodeCardsListResponse, error) {
	board, err := NewBoard(s.metadata, s.definitions, roleResolver, s.projector)
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	return board.ListNodeCards(ctx, req)
}

func (s *workflowViewTestService) GetTask(ctx context.Context, taskID string) (serverapi.WorkflowTaskDetail, error) {
	return s.taskDetail.GetTask(ctx, taskID)
}

func (s *workflowViewTestService) GetTaskByProjectShortID(ctx context.Context, projectID string, shortID string) (serverapi.WorkflowTaskDetail, error) {
	return s.taskDetail.GetTaskByProjectShortID(ctx, projectID, shortID)
}

func (s *workflowViewTestService) GetTaskByShortID(ctx context.Context, shortID string) (serverapi.WorkflowTaskDetail, error) {
	return s.taskDetail.GetTaskByShortID(ctx, shortID)
}

func (s *workflowViewTestService) ListTaskActivity(ctx context.Context, req serverapi.WorkflowTaskActivityListRequest) (serverapi.WorkflowTaskActivityListResponse, error) {
	return s.activity.List(ctx, req)
}

func (s *workflowViewTestService) ListAttention(ctx context.Context, req serverapi.WorkflowAttentionListRequest, roleResolver workflow.RoleResolver) (serverapi.WorkflowAttentionListResponse, error) {
	attention, err := NewAttention(s.metadata, s.definitions, roleResolver, s.transcripts, s.prompts)
	if err != nil {
		return serverapi.WorkflowAttentionListResponse{}, err
	}
	return attention.List(ctx, req)
}

func (s *workflowViewTestService) ListTaskAttention(ctx context.Context, req serverapi.WorkflowTaskAttentionListRequest, roleResolver workflow.RoleResolver) (serverapi.WorkflowTaskAttentionListResponse, error) {
	attention, err := NewAttention(s.metadata, s.definitions, roleResolver, s.transcripts, s.prompts)
	if err != nil {
		return serverapi.WorkflowTaskAttentionListResponse{}, err
	}
	return attention.ListTask(ctx, req)
}

func newDefaultWorkflowViewTestReadModels(metadataStore *metadata.Store) (*workflowViewTestService, error) {
	return newWorkflowViewTestReadModels(metadataStore, nil, nil)
}

func workflowViewTestRoleResolver() workflow.RoleResolver {
	return testsetup.QuestionsEnabled("coder")
}
