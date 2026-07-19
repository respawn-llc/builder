package workflowview

import (
	"context"
	"errors"
	"strings"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/server/workflowscript"
	"core/server/workflowstore"
	"core/server/worktree"
	"core/shared/serverapi"
)

type Service struct {
	metadata    *metadata.Store
	queries     *sqlitegen.Queries
	definitions *DefinitionProjection
	projector   *TaskProjector
	taskList    *TaskList
	taskDetail  *TaskDetail
	activity    *Activity
	attention   *Attention
}

const attentionKindInterruptedRun = "interrupted_run"

const interruptedRunAttentionMessage = "This task's run was stopped."

// Sentinel errors returned by the workflow view service. Callers and tests must
// match these with errors.Is/errors.As rather than comparing rendered message
// text. Dynamic context is wrapped via fmt.Errorf("... %w", Err...).
var (
	// ErrTaskIDRequired is returned when a task id is required but blank.
	ErrTaskIDRequired = errors.New("task_id is required")
	// ErrInvalidPageToken is returned when a pagination page_token fails to
	// decode or does not match its issuing query.
	ErrInvalidPageToken = errors.New("page_token is invalid")
)

type serviceOptions struct {
	attentionTranscripts SessionActiveTranscriptProvider
	attentionPrompts     PendingPromptSource
}

type Option func(*serviceOptions)

func WithSessionTranscriptProvider(provider SessionActiveTranscriptProvider) Option {
	return func(options *serviceOptions) {
		options.attentionTranscripts = provider
	}
}

func WithPendingPromptSource(source PendingPromptSource) Option {
	return func(options *serviceOptions) {
		options.attentionPrompts = source
	}
}

func New(metadataStore *metadata.Store, opts ...Option) (*Service, error) {
	if metadataStore == nil || metadataStore.Queries() == nil {
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
	options := serviceOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	attention, err := NewAttention(metadataStore, definitions, options.attentionTranscripts, options.attentionPrompts)
	if err != nil {
		return nil, err
	}
	return &Service{
		metadata:    metadataStore,
		queries:     metadataStore.Queries(),
		definitions: definitions,
		projector:   projector,
		taskList:    taskList,
		taskDetail:  taskDetail,
		activity:    activity,
		attention:   attention,
	}, nil
}

func (s *Service) GetDefinition(ctx context.Context, workflowID string) (serverapi.WorkflowDefinition, map[string]workflow.NodeKind, error) {
	if s == nil {
		return serverapi.WorkflowDefinition{}, nil, errors.New("workflow view service is required")
	}
	if strings.TrimSpace(workflowID) == "" {
		return serverapi.WorkflowDefinition{}, nil, errors.New("workflow_id is required")
	}
	return s.definitions.GetDefinition(ctx, workflowID)
}

func (s *Service) GetBoard(ctx context.Context, req serverapi.WorkflowBoardRequest, roleResolver workflow.RoleResolver) (serverapi.WorkflowBoard, error) {
	if s == nil {
		return serverapi.WorkflowBoard{}, errors.New("workflow view service is required")
	}
	board, err := NewBoard(s.metadata, s.definitions, roleResolver, s.projector)
	if err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	return board.Get(ctx, req)
}

func (s *Service) ListTasks(ctx context.Context, req serverapi.WorkflowTaskListRequest, roleResolver workflow.RoleResolver) (serverapi.WorkflowTaskListResponse, error) {
	if s == nil {
		return serverapi.WorkflowTaskListResponse{}, errors.New("workflow view service is required")
	}
	return s.taskList.List(ctx, req)
}

func (s *Service) ListBoardNodeCards(ctx context.Context, req serverapi.WorkflowBoardNodeCardsListRequest, roleResolver workflow.RoleResolver) (serverapi.WorkflowBoardNodeCardsListResponse, error) {
	if s == nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, errors.New("workflow view service is required")
	}
	board, err := NewBoard(s.metadata, s.definitions, roleResolver, s.projector)
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	return board.ListNodeCards(ctx, req)
}

func (s *Service) GetTask(ctx context.Context, taskID string) (serverapi.WorkflowTaskDetail, error) {
	if s == nil {
		return serverapi.WorkflowTaskDetail{}, errors.New("workflow view service is required")
	}
	return s.taskDetail.GetTask(ctx, taskID)
}

func (s *Service) GetTaskByProjectShortID(ctx context.Context, projectID string, shortID string) (serverapi.WorkflowTaskDetail, error) {
	if s == nil {
		return serverapi.WorkflowTaskDetail{}, errors.New("workflow view service is required")
	}
	return s.taskDetail.GetTaskByProjectShortID(ctx, projectID, shortID)
}

func (s *Service) GetTaskByShortID(ctx context.Context, shortID string) (serverapi.WorkflowTaskDetail, error) {
	if s == nil {
		return serverapi.WorkflowTaskDetail{}, errors.New("workflow view service is required")
	}
	return s.taskDetail.GetTaskByShortID(ctx, shortID)
}

func (s *Service) ListTaskActivity(ctx context.Context, req serverapi.WorkflowTaskActivityListRequest) (serverapi.WorkflowTaskActivityListResponse, error) {
	if s == nil {
		return serverapi.WorkflowTaskActivityListResponse{}, errors.New("workflow view service is required")
	}
	return s.activity.List(ctx, req)
}

func (s *Service) ListAttention(ctx context.Context, req serverapi.WorkflowAttentionListRequest, roleResolver workflow.RoleResolver) (serverapi.WorkflowAttentionListResponse, error) {
	if s == nil {
		return serverapi.WorkflowAttentionListResponse{}, errors.New("workflow view service is required")
	}
	return s.attention.List(ctx, req, roleResolver)
}

func (s *Service) ListTaskAttention(ctx context.Context, req serverapi.WorkflowTaskAttentionListRequest, roleResolver workflow.RoleResolver) (serverapi.WorkflowTaskAttentionListResponse, error) {
	if s == nil {
		return serverapi.WorkflowTaskAttentionListResponse{}, errors.New("workflow view service is required")
	}
	return s.attention.ListTask(ctx, req, roleResolver)
}

func (s *Service) definition(ctx context.Context, workflowID string) (serverapi.WorkflowDefinition, map[string]workflow.NodeKind, error) {
	return s.definitions.GetDefinition(ctx, workflowID)
}

func bodyPreview(body string) string {
	trimmed := strings.TrimSpace(body)
	const limit = 96
	if len(trimmed) <= limit {
		return trimmed
	}
	return trimmed[:limit]
}

func markdownPreview(body string) serverapi.MarkdownPreview {
	trimmed := strings.TrimSpace(body)
	const codePointLimit = 512
	codePointCount := 0
	for byteIndex := range trimmed {
		if codePointCount == codePointLimit {
			return serverapi.MarkdownPreview{Markdown: trimmed[:byteIndex], Truncated: true}
		}
		codePointCount++
	}
	return serverapi.MarkdownPreview{Markdown: trimmed}
}

func definitionExecutionValidation(def workflow.Definition, roleResolver workflow.RoleResolver) *workflow.ValidationResult {
	result := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: workflow.ValidationContextExecution, RoleResolver: roleResolver})
	result.Errors = append(result.Errors, scriptPathDefinitionValidationErrors(def, nil)...)
	return &result
}

func scriptPathDefinitionValidationErrors(def workflow.Definition, rootPath *string) []workflow.ValidationError {
	out := []workflow.ValidationError{}
	for _, node := range def.Nodes {
		if node.Kind() != workflow.NodeKindScript {
			continue
		}
		diagnostics := workflowscript.Validate(workflowscript.ValidationRequest{
			RawPath:  workflow.NodeScriptPath(node).String(),
			RootPath: rootPath,
		})
		for _, diagnostic := range diagnostics {
			out = append(out, workflow.ValidationError{
				Code:          workflow.ValidationErrorCode(diagnostic.Code),
				Message:       diagnostic.Message,
				WorkflowID:    def.ID,
				NodeID:        workflow.NodeIDOf(node),
				BlocksContext: diagnostic.Blocking,
			})
		}
	}
	return out
}

func apiContextSource(in workflow.ContextSource) serverapi.WorkflowContextSource {
	source := workflow.CanonicalContextSource(in)
	return serverapi.WorkflowContextSource{Kind: string(source.Kind), NodeKey: string(source.NodeKey)}
}
