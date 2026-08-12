package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/requestmemo"
	"core/server/workflow"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
	"github.com/google/uuid"
)

type Store struct {
	metadata     *metadata.Store
	db           *sql.DB
	queries      *sqlitegen.Queries
	roleResolver workflow.RoleResolver
	now          func() time.Time
	approvalGate chan struct{}
	graphSaves   *requestmemo.MutationLaneRegistry[runtimeids.WorkflowID]
	eventMu      sync.RWMutex
	eventSink    WorkflowEventPublisher
}

type Option func(*Store)

func WithRoleResolver(resolver workflow.RoleResolver) Option {
	return func(s *Store) {
		s.roleResolver = resolver
	}
}

func (s *Store) TargetAgentCatalog() workflow.TargetAgentCatalog {
	if s == nil {
		return nil
	}
	return s.roleResolver
}

func WithNow(now func() time.Time) Option {
	return func(s *Store) {
		if now != nil {
			s.now = now
		}
	}
}

func New(metadataStore *metadata.Store, opts ...Option) (*Store, error) {
	if metadataStore == nil || metadataStore.DB() == nil || metadataStore.Queries() == nil {
		return nil, errors.New("metadata store is required")
	}
	store := &Store{
		metadata:     metadataStore,
		db:           metadataStore.DB(),
		queries:      metadataStore.Queries(),
		now:          func() time.Time { return time.Now().UTC() },
		approvalGate: make(chan struct{}, 1),
		graphSaves:   requestmemo.NewMutationLaneRegistry[runtimeids.WorkflowID](),
		eventSink:    noopWorkflowEventPublisher{},
	}
	for _, opt := range opts {
		opt(store)
	}
	return store, nil
}

func (s *Store) ListWorkflowTaskIDs(ctx context.Context, workflowID runtimeids.WorkflowID) ([]workflow.TaskID, error) {
	if workflowID.IsZero() {
		return nil, ErrWorkflowIDRequired
	}
	rows, err := s.queries.ListWorkflowTaskIDs(ctx, workflowID)
	if err != nil {
		return nil, err
	}
	taskIDs := make([]workflow.TaskID, 0, len(rows))
	for _, row := range rows {
		taskID := workflow.TaskID(strings.TrimSpace(row))
		if taskID == "" {
			return nil, errors.New("workflow task id is blank")
		}
		taskIDs = append(taskIDs, taskID)
	}
	return taskIDs, nil
}

func (s *Store) incrementWorkflowVersion(ctx context.Context, q *sqlitegen.Queries, workflowID runtimeids.WorkflowID) (int64, error) {
	revision, err := q.IncrementWorkflowVersion(ctx, sqlitegen.IncrementWorkflowVersionParams{ID: workflowID, UpdatedAtUnixMs: s.now().UnixMilli()})
	if err != nil {
		return 0, fmt.Errorf("increment workflow version: %w", err)
	}
	return revision, nil
}

func (s *Store) withWorkflowGraphMutation(ctx context.Context, workflowID runtimeids.WorkflowID, nextGraph func(preparedWorkflowGraphSave) (preparedWorkflowGraphSave, error), apply func(context.Context, *sqlitegen.Queries, *sql.Tx) error) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	currentGraph, err := currentWorkflowGraphSavePrepared(ctx, q, workflowID)
	if err != nil {
		return 0, err
	}
	next, err := nextGraph(currentGraph)
	if err != nil {
		return 0, err
	}
	if err := enforceWorkflowGraphEditPolicy(ctx, q, workflowID, next); err != nil {
		return 0, err
	}
	if err := apply(ctx, q, tx); err != nil {
		return 0, err
	}
	revision, err := s.incrementWorkflowVersion(ctx, q, workflowID)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return revision, nil
}

type WorkflowRecord struct {
	ID                    runtimeids.WorkflowID
	Name                  string
	Description           string
	Version               int64
	ExecutionTargetPolicy workflow.ExecutionTargetPolicy
	ProjectLink           *WorkflowListProjectLink
}

type WorkflowListProjectLink struct {
	Default bool
}

type NodeRecord struct {
	ID                 workflow.NodeID
	WorkflowID         runtimeids.WorkflowID
	Key                workflow.ModelKey
	Kind               workflow.NodeKind
	DisplayName        string
	GroupID            *string
	GroupKey           string
	SubagentRole       string
	CompletionMode     string
	ScriptPath         string
	JoinInputProviders []workflow.JoinInputProvider
	SortOrder          int64
}

func workflowNodeFromRecord(node NodeRecord) (workflow.Node, error) {
	return workflow.NewNode(
		workflow.NodeIdentity{
			WorkflowID:  node.WorkflowID,
			ID:          node.ID,
			Key:         node.Key,
			DisplayName: node.DisplayName,
			GroupID:     textutil.Pointer(node.GroupID),
		},
		node.Kind,
		workflow.NodeFields{
			SubagentRole:       node.SubagentRole,
			CompletionMode:     node.CompletionMode,
			JoinInputProviders: node.JoinInputProviders,
			ScriptPath: func() workflow.OptionalScriptPath {
				if scriptPath, ok := workflow.PresentScriptPath(node.ScriptPath); ok {
					return scriptPath
				}
				return workflow.AbsentScriptPath()
			}(),
		},
	)
}

type NodeGroupRecord struct {
	ID          string
	WorkflowID  runtimeids.WorkflowID
	Key         workflow.ModelKey
	DisplayName string
	SortOrder   int64
}

type WorkflowEventRecord = serverapi.WorkflowProjectEvent

type WorkflowEventPublisher interface {
	PublishWorkflowEvent(context.Context, WorkflowEventRecord) error
}

type noopWorkflowEventPublisher struct{}

func (noopWorkflowEventPublisher) PublishWorkflowEvent(context.Context, WorkflowEventRecord) error {
	return nil
}

type TransitionGroupRecord struct {
	ID           workflow.TransitionGroupID
	WorkflowID   runtimeids.WorkflowID
	SourceNodeID workflow.NodeID
	TransitionID workflow.TransitionID
	DisplayName  string
	Description  string
	SortOrder    int64
}

type EdgeRecord struct {
	ID                 workflow.EdgeID
	WorkflowID         runtimeids.WorkflowID
	TransitionGroupID  workflow.TransitionGroupID
	Key                workflow.ModelKey
	TargetNodeID       workflow.NodeID
	AssigneeSelection  workflow.AssigneeSelection
	ThinkingSelection  workflow.ThinkingSelection
	RequiresApproval   bool
	ContextMode        workflow.ContextMode
	ContextSource      workflow.ContextSource
	InputBindings      []workflow.InputBinding
	PromptTemplate     string
	Parameters         []workflow.Parameter
	OutputRequirements []workflow.OutputRequirement
	SortOrder          int64
}

type ProjectWorkflowLinkRecord struct {
	ID         string
	ProjectID  string
	WorkflowID runtimeids.WorkflowID
	IsDefault  bool
}

type ProjectWorkflowUnlinkResult struct {
	LinkID     string
	ProjectID  string
	WorkflowID runtimeids.WorkflowID
	Unlinked   bool
	Blockers   []ProjectWorkflowUnlinkBlocker
}

type ProjectWorkflowUnlinkBlocker struct {
	Code    string
	Message string
	Count   int
	Tasks   []ProjectWorkflowUnlinkTaskReference
}

type ProjectWorkflowUnlinkTaskReference struct {
	TaskID  workflow.TaskID
	ShortID string
	Title   string
}

type TaskRecord struct {
	ID                              workflow.TaskID
	ProjectID                       string
	WorkflowID                      runtimeids.WorkflowID
	LinkID                          string
	ShortID                         string
	Title                           string
	Body                            string
	SourceURL                       string
	SourceWorkspaceID               string
	ManagedWorktreeID               *string
	PendingInitialManagedBranchName *string
	ExecutionTarget                 *ExecutionTargetSnapshot
	Version                         int64
}

// CurrentNodeStartContext is the live execution contract derived from one
// Current Node and the latest Workflow definition. It deliberately has no
// historical execution identity or frozen execution snapshot.
type CurrentNodeStartContext struct {
	Task                           TaskRecord
	Workflow                       WorkflowRecord
	Node                           NodeRecord
	CurrentNode                    workflow.CurrentNode
	EnteringEdge                   workflow.Edge
	ContextMode                    workflow.ContextMode
	SourceSessionID                *runtimeids.SessionID
	IsFanoutBranch                 bool
	AcceptedTransitionPath         AcceptedTransitionPath
	TransitionIDs                  []string
	TransitionOptions              []TransitionOption
	HasContinueSessionOutgoingEdge bool
	TransitionPrompt               string
	ParameterValues                map[string]string
	ExecutionRoot                  *ExecutionRoot
}

type AcceptedTransitionPath struct {
	SourceNodeDisplayName string
	TargetNodeDisplayName string
}

type TransitionOption struct {
	ID          string
	DisplayName string
	Description string
	Parameters  []workflow.Parameter
}

type CommentRecord struct {
	ID        string
	TaskID    workflow.TaskID
	Body      string
	Author    string
	AuthorID  string
	CreatedAt int64
	UpdatedAt int64
}

type CreateWorkflowRequest struct {
	Name        string
	Description string
}

type CreateAndLinkWorkflowRequest struct {
	Name          string
	Description   string
	ProjectID     string
	DefaultPolicy WorkflowLinkDefaultPolicy
}

type WorkflowLinkDefaultPolicy string

const (
	WorkflowLinkDefaultNever            WorkflowLinkDefaultPolicy = "never"
	WorkflowLinkDefaultAlways           WorkflowLinkDefaultPolicy = "always"
	WorkflowLinkDefaultIfProjectHasNone WorkflowLinkDefaultPolicy = "if_project_has_none"
)

type ListWorkflowsRequest struct {
	Offset     int
	Limit      int
	Query      string
	ProjectID  *string
	WorkflowID *runtimeids.WorkflowID
}

type ListWorkflowsResult struct {
	Workflows  []WorkflowRecord
	ProjectID  *string
	NextOffset *int
}

func (s *Store) CreateWorkflow(ctx context.Context, req CreateWorkflowRequest) (WorkflowRecord, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return WorkflowRecord{}, ErrWorkflowNameRequired
	}
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkflowRecord{}, fmt.Errorf("begin workflow create tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	record, err := insertWorkflow(ctx, q, now, CreateWorkflowRequest{Name: name, Description: req.Description})
	if err != nil {
		return WorkflowRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorkflowRecord{}, fmt.Errorf("commit workflow create tx: %w", err)
	}
	return record, nil
}

func (s *Store) CreateAndLinkWorkflow(ctx context.Context, req CreateAndLinkWorkflowRequest) (WorkflowRecord, ProjectWorkflowLinkRecord, error) {
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkflowRecord{}, ProjectWorkflowLinkRecord{}, fmt.Errorf("begin workflow create-and-link tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	record, err := insertWorkflow(ctx, q, now, CreateWorkflowRequest{Name: req.Name, Description: req.Description})
	if err != nil {
		return WorkflowRecord{}, ProjectWorkflowLinkRecord{}, err
	}
	link, err := s.linkWorkflowInTx(ctx, q, now, strings.TrimSpace(req.ProjectID), record.ID, req.DefaultPolicy)
	if err != nil {
		return WorkflowRecord{}, ProjectWorkflowLinkRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorkflowRecord{}, ProjectWorkflowLinkRecord{}, fmt.Errorf("commit workflow create-and-link tx: %w", err)
	}
	return record, link, nil
}

func insertWorkflow(ctx context.Context, q *sqlitegen.Queries, now int64, req CreateWorkflowRequest) (WorkflowRecord, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return WorkflowRecord{}, ErrWorkflowNameRequired
	}
	description := strings.TrimSpace(req.Description)
	workflowID := runtimeids.NewWorkflowID()
	startID := runtimeids.NewGraphEntityID()
	doneID := runtimeids.NewGraphEntityID()
	policy := workflow.DefaultExecutionTargetPolicy()
	if err := q.InsertWorkflow(ctx, sqlitegen.InsertWorkflowParams{
		ID:                    workflowID,
		Name:                  name,
		Description:           description,
		Version:               1,
		ExecutionTargetPolicy: string(policy.Mode),
		CreatedAtUnixMs:       now,
		UpdatedAtUnixMs:       now,
	}); err != nil {
		return WorkflowRecord{}, fmt.Errorf("insert workflow: %w", err)
	}
	if err := q.InsertWorkflowNode(ctx, sqlitegen.InsertWorkflowNodeParams{ID: startID, WorkflowID: workflowID, NodeKey: "backlog", Kind: string(workflow.NodeKindStart), DisplayName: "Backlog", JoinInputProvidersJson: "[]", SortOrder: 0}); err != nil {
		return WorkflowRecord{}, fmt.Errorf("insert backlog node: %w", err)
	}
	if err := q.InsertWorkflowNode(ctx, sqlitegen.InsertWorkflowNodeParams{ID: doneID, WorkflowID: workflowID, NodeKey: "done", Kind: string(workflow.NodeKindTerminal), DisplayName: "Done", JoinInputProvidersJson: "[]", SortOrder: 1000}); err != nil {
		return WorkflowRecord{}, fmt.Errorf("insert done node: %w", err)
	}
	return WorkflowRecord{ID: workflowID, Name: name, Description: description, Version: 1, ExecutionTargetPolicy: policy}, nil
}

func (s *Store) UpdateWorkflowInfo(ctx context.Context, workflowID runtimeids.WorkflowID, name string, description string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrWorkflowNameRequired
	}
	updated, err := s.queries.UpdateWorkflowInfo(ctx, sqlitegen.UpdateWorkflowInfoParams{ID: workflowID, Name: name, Description: strings.TrimSpace(description), UpdatedAtUnixMs: s.now().UnixMilli()})
	if err != nil {
		return fmt.Errorf("update workflow info: %w", err)
	}
	if updated == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ListWorkflows(ctx context.Context, req ListWorkflowsRequest) (ListWorkflowsResult, error) {
	projectID := req.ProjectID
	workflowID := req.WorkflowID
	query := sqliteLowerASCII(strings.TrimSpace(req.Query))
	if err := validateWorkflowListScopes(projectID, workflowID); err != nil {
		return ListWorkflowsResult{}, err
	}
	rows, err := s.queries.ListWorkflowRecordsPage(ctx, sqlitegen.ListWorkflowRecordsPageParams{
		PageLimit:   int64(req.Limit + 1),
		PageOffset:  int64(req.Offset),
		ProjectID:   nullableStringPointer(projectID),
		WorkflowID:  workflowID,
		SearchQuery: query,
	})
	if err != nil {
		return ListWorkflowsResult{}, err
	}
	rowsOut := make([]workflowRecordRow, 0, req.Limit+1)
	for _, row := range rows {
		rowsOut = append(rowsOut, workflowRecordRow{
			ID:                       row.ID,
			Name:                     row.Name,
			Description:              row.Description,
			Version:                  row.Version,
			ExecutionTargetPolicy:    row.ExecutionTargetPolicy,
			ExecutionTargetCustomRef: row.ExecutionTargetCustomRef,
			CreatedAtUnixMs:          row.CreatedAtUnixMs,
			UpdatedAtUnixMs:          row.UpdatedAtUnixMs,
			ActivityAtUnixMs:         workflowListActivityAtUnixMs(projectID, row.GlobalActivityAtUnixMs, row.ProjectActivityAtUnixMs),
			ProjectLinkDefault:       row.ProjectLinkDefault,
			ProjectNameOrderKey:      row.ProjectNameOrderKey,
		})
	}
	var nextOffset *int
	if len(rowsOut) > req.Limit {
		rowsOut = rowsOut[:req.Limit]
		value := req.Offset + len(rowsOut)
		nextOffset = &value
	}
	out := make([]WorkflowRecord, 0, len(rowsOut))
	for _, row := range rowsOut {
		out = append(out, workflowRecordFromRow(row))
	}
	var responseProjectID *string
	if projectID != nil {
		value := *projectID
		responseProjectID = &value
	}
	return ListWorkflowsResult{Workflows: out, ProjectID: responseProjectID, NextOffset: nextOffset}, nil
}

func validateWorkflowListScopes(projectID *string, workflowID *runtimeids.WorkflowID) error {
	if projectID != nil &&
		(strings.TrimSpace(*projectID) == "" || strings.TrimSpace(*projectID) != *projectID) {
		return errors.New("workflow list project scope must be non-blank and unpadded")
	}
	if workflowID != nil && workflowID.IsZero() {
		return errors.New("invalid workflow list workflow scope")
	}
	return nil
}

func (s *Store) AddNode(ctx context.Context, node NodeRecord) (int64, error) {
	if node.WorkflowID.IsZero() {
		return 0, ErrWorkflowIDRequired
	}
	if err := validateNodeCompletionMode(node.Kind, node.CompletionMode); err != nil {
		return 0, err
	}
	if _, err := runtimeids.GraphEntityIDBlob(string(node.ID)); err != nil {
		return 0, fmt.Errorf("node id: %w", err)
	}
	for _, provider := range node.JoinInputProviders {
		if _, err := runtimeids.GraphEntityIDBlob(string(provider.ProviderEdgeID)); err != nil {
			return 0, fmt.Errorf("join provider edge id: %w", err)
		}
	}
	node.SortOrder = 100
	return s.withWorkflowGraphMutation(ctx, node.WorkflowID, func(currentGraph preparedWorkflowGraphSave) (preparedWorkflowGraphSave, error) {
		if _, exists := workflowGraphRecordByID(currentGraph.nodes, node.ID, func(record NodeRecord) workflow.NodeID { return record.ID }); exists {
			return preparedWorkflowGraphSave{}, fmt.Errorf("insert workflow node: id %q already exists", node.ID)
		}
		if node.Kind == workflow.NodeKindStart {
			for _, existing := range currentGraph.nodes {
				if existing.Kind == workflow.NodeKindStart {
					return preparedWorkflowGraphSave{}, ErrWorkflowStartNodeExists
				}
			}
		}
		return withWorkflowGraphNode(currentGraph, node), nil
	}, func(ctx context.Context, q *sqlitegen.Queries, _ *sql.Tx) error {
		groupID, err := resolveWorkflowNodeGroupID(ctx, q, node.WorkflowID, node.GroupID, node.GroupKey)
		if err != nil {
			return err
		}
		node.GroupID = groupID
		return upsertWorkflowNode(ctx, q, node, node.SortOrder, "insert workflow node")
	})
}

func (s *Store) UpdateNode(ctx context.Context, node NodeRecord) (int64, error) {
	if strings.TrimSpace(string(node.ID)) == "" {
		return 0, errors.New("node id is required")
	}
	if node.WorkflowID.IsZero() {
		return 0, ErrWorkflowIDRequired
	}
	if err := validateNodeCompletionMode(node.Kind, node.CompletionMode); err != nil {
		return 0, err
	}
	return s.withWorkflowGraphMutation(ctx, node.WorkflowID, func(currentGraph preparedWorkflowGraphSave) (preparedWorkflowGraphSave, error) {
		current, exists := workflowGraphRecordByID(currentGraph.nodes, node.ID, func(record NodeRecord) workflow.NodeID { return record.ID })
		if !exists {
			return preparedWorkflowGraphSave{}, sql.ErrNoRows
		}
		node.SortOrder = current.SortOrder
		return withWorkflowGraphNode(currentGraph, node), nil
	}, func(ctx context.Context, q *sqlitegen.Queries, _ *sql.Tx) error {
		groupID, err := resolveWorkflowNodeGroupID(ctx, q, node.WorkflowID, node.GroupID, node.GroupKey)
		if err != nil {
			return err
		}
		node.GroupID = groupID
		return upsertWorkflowNode(ctx, q, node, node.SortOrder, "update workflow node")
	})
}

func validateNodeCompletionMode(kind workflow.NodeKind, mode string) error {
	trimmed := strings.TrimSpace(mode)
	if trimmed == "" {
		return nil
	}
	if kind != workflow.NodeKindAgent {
		return fmt.Errorf("workflow completion mode override is only valid for agent nodes")
	}
	switch trimmed {
	case "auto", "structured_output", "tool", "shell_command", "unstructured_output":
		return nil
	default:
		return fmt.Errorf("invalid workflow node completion mode %q", mode)
	}
}

func nodeCompletionMode(node NodeRecord) string {
	mode := strings.TrimSpace(node.CompletionMode)
	if node.Kind != workflow.NodeKindAgent {
		return ""
	}
	return mode
}

func executableNodeKind(kind workflow.NodeKind) bool {
	switch kind {
	case workflow.NodeKindAgent, workflow.NodeKindScript:
		return true
	default:
		return false
	}
}

func (s *Store) AddNodeGroup(ctx context.Context, group NodeGroupRecord) (NodeGroupRecord, int64, error) {
	if group.WorkflowID.IsZero() {
		return NodeGroupRecord{}, 0, ErrWorkflowIDRequired
	}
	if strings.TrimSpace(string(group.Key)) == "" {
		return NodeGroupRecord{}, 0, errors.New("group key is required")
	}
	if strings.TrimSpace(group.DisplayName) == "" {
		return NodeGroupRecord{}, 0, errors.New("group display name is required")
	}
	if _, err := runtimeids.GraphEntityIDBlob(group.ID); err != nil {
		return NodeGroupRecord{}, 0, fmt.Errorf("node group id: %w", err)
	}
	revision, err := s.withWorkflowGraphMutation(ctx, group.WorkflowID, func(currentGraph preparedWorkflowGraphSave) (preparedWorkflowGraphSave, error) {
		if _, exists := workflowGraphRecordByID(currentGraph.nodeGroups, group.ID, func(record NodeGroupRecord) string { return record.ID }); exists {
			return preparedWorkflowGraphSave{}, fmt.Errorf("insert workflow node group: id %q already exists", group.ID)
		}
		return withWorkflowGraphNodeGroup(currentGraph, group), nil
	}, func(ctx context.Context, q *sqlitegen.Queries, _ *sql.Tx) error {
		return upsertWorkflowNodeGroup(ctx, q, group, "insert workflow node group")
	})
	if err != nil {
		return NodeGroupRecord{}, 0, err
	}
	return group, revision, nil
}

func (s *Store) UpdateNodeGroup(ctx context.Context, group NodeGroupRecord) (NodeGroupRecord, int64, error) {
	if strings.TrimSpace(group.ID) == "" {
		return NodeGroupRecord{}, 0, errors.New("group id is required")
	}
	if group.WorkflowID.IsZero() {
		return NodeGroupRecord{}, 0, ErrWorkflowIDRequired
	}
	if strings.TrimSpace(string(group.Key)) == "" {
		return NodeGroupRecord{}, 0, errors.New("group key is required")
	}
	if strings.TrimSpace(group.DisplayName) == "" {
		return NodeGroupRecord{}, 0, errors.New("group display name is required")
	}
	revision, err := s.withWorkflowGraphMutation(ctx, group.WorkflowID, func(currentGraph preparedWorkflowGraphSave) (preparedWorkflowGraphSave, error) {
		if _, exists := workflowGraphRecordByID(currentGraph.nodeGroups, group.ID, func(record NodeGroupRecord) string { return record.ID }); !exists {
			return preparedWorkflowGraphSave{}, sql.ErrNoRows
		}
		return withWorkflowGraphNodeGroup(currentGraph, group), nil
	}, func(ctx context.Context, q *sqlitegen.Queries, _ *sql.Tx) error {
		return upsertWorkflowNodeGroup(ctx, q, group, "update workflow node group")
	})
	if err != nil {
		return NodeGroupRecord{}, 0, err
	}
	return group, revision, nil
}

func (s *Store) DeleteNodeGroup(ctx context.Context, workflowID runtimeids.WorkflowID, groupID string) (int64, error) {
	if workflowID.IsZero() {
		return 0, ErrWorkflowIDRequired
	}
	if strings.TrimSpace(groupID) == "" {
		return 0, errors.New("group id is required")
	}
	nodeCount, err := s.queries.CountWorkflowNodesByGroup(ctx, strings.TrimSpace(groupID))
	if err != nil {
		return 0, err
	}
	if nodeCount > 0 {
		return 0, errors.New("workflow node group is in use")
	}
	return s.withWorkflowGraphMutation(ctx, workflowID, func(currentGraph preparedWorkflowGraphSave) (preparedWorkflowGraphSave, error) {
		return withoutWorkflowGraphNodeGroup(currentGraph, strings.TrimSpace(groupID)), nil
	}, func(ctx context.Context, q *sqlitegen.Queries, _ *sql.Tx) error {
		deleted, err := q.DeleteWorkflowNodeGroup(ctx, sqlitegen.DeleteWorkflowNodeGroupParams{ID: strings.TrimSpace(groupID), WorkflowID: workflowID})
		if err != nil {
			return fmt.Errorf("delete workflow node group: %w", err)
		}
		if deleted != 1 {
			return sql.ErrNoRows
		}
		return nil
	})
}

func (s *Store) SetWorkflowEventPublisher(publisher WorkflowEventPublisher) {
	if s == nil {
		return
	}
	s.eventMu.Lock()
	if publisher == nil {
		publisher = noopWorkflowEventPublisher{}
	}
	s.eventSink = publisher
	s.eventMu.Unlock()
}

func (s *Store) PublishWorkflowEvent(ctx context.Context, event WorkflowEventRecord) error {
	occurredAt := event.OccurredAtUnixMs
	if occurredAt == 0 {
		occurredAt = s.now().UnixMilli()
	}
	normalized := WorkflowEventRecord{
		ProjectID:        cloneWorkflowEventID(event.ProjectID),
		WorkflowID:       cloneWorkflowEventWorkflowID(event.WorkflowID),
		Resource:         event.Resource,
		Action:           event.Action,
		PrimaryEntityID:  event.PrimaryEntityID,
		RelatedIDs:       append([]string(nil), event.RelatedIDs...),
		OccurredAtUnixMs: occurredAt,
	}
	if err := normalized.Validate(); err != nil {
		return err
	}
	s.eventMu.RLock()
	sink := s.eventSink
	s.eventMu.RUnlock()
	return sink.PublishWorkflowEvent(ctx, normalized)
}

func cloneWorkflowEventID(id *string) *string {
	if id == nil {
		return nil
	}
	value := *id
	return &value
}

func cloneWorkflowEventWorkflowID(id *runtimeids.WorkflowID) *runtimeids.WorkflowID {
	if id == nil {
		return nil
	}
	value := *id
	return &value
}

func (s *Store) AddTransitionGroup(ctx context.Context, group TransitionGroupRecord) (int64, error) {
	if group.WorkflowID.IsZero() {
		return 0, ErrWorkflowIDRequired
	}
	if _, err := runtimeids.GraphEntityIDBlob(string(group.ID)); err != nil {
		return 0, fmt.Errorf("transition group id: %w", err)
	}
	if _, err := runtimeids.GraphEntityIDBlob(string(group.SourceNodeID)); err != nil {
		return 0, fmt.Errorf("source node id: %w", err)
	}
	group.SortOrder = 100
	return s.withWorkflowGraphMutation(ctx, group.WorkflowID, func(currentGraph preparedWorkflowGraphSave) (preparedWorkflowGraphSave, error) {
		if _, exists := workflowGraphRecordByID(currentGraph.transitionGroups, group.ID, func(record TransitionGroupRecord) workflow.TransitionGroupID { return record.ID }); exists {
			return preparedWorkflowGraphSave{}, fmt.Errorf("insert transition group: id %q already exists", group.ID)
		}
		return withWorkflowGraphTransitionGroup(currentGraph, group), nil
	}, func(ctx context.Context, q *sqlitegen.Queries, _ *sql.Tx) error {
		if err := ensureWorkflowNodeID(ctx, q, group.WorkflowID, group.SourceNodeID); err != nil {
			return err
		}
		return upsertWorkflowTransitionGroup(ctx, q, group, group.SortOrder, "insert transition group")
	})
}

func (s *Store) UpdateTransitionGroup(ctx context.Context, group TransitionGroupRecord) (int64, error) {
	if strings.TrimSpace(string(group.ID)) == "" {
		return 0, errors.New("transition group id is required")
	}
	if group.WorkflowID.IsZero() {
		return 0, ErrWorkflowIDRequired
	}
	return s.withWorkflowGraphMutation(ctx, group.WorkflowID, func(currentGraph preparedWorkflowGraphSave) (preparedWorkflowGraphSave, error) {
		current, exists := workflowGraphRecordByID(currentGraph.transitionGroups, group.ID, func(record TransitionGroupRecord) workflow.TransitionGroupID { return record.ID })
		if !exists {
			return preparedWorkflowGraphSave{}, sql.ErrNoRows
		}
		group.SortOrder = current.SortOrder
		return withWorkflowGraphTransitionGroup(currentGraph, group), nil
	}, func(ctx context.Context, q *sqlitegen.Queries, _ *sql.Tx) error {
		if err := ensureWorkflowNodeID(ctx, q, group.WorkflowID, group.SourceNodeID); err != nil {
			return err
		}
		return upsertWorkflowTransitionGroup(ctx, q, group, group.SortOrder, "update transition group")
	})
}

func (s *Store) AddEdge(ctx context.Context, edge EdgeRecord) (int64, error) {
	if edge.WorkflowID.IsZero() {
		return 0, ErrWorkflowIDRequired
	}
	for _, identity := range []struct {
		name  string
		value string
	}{
		{"edge id", string(edge.ID)},
		{"transition group id", string(edge.TransitionGroupID)},
		{"target node id", string(edge.TargetNodeID)},
	} {
		if _, err := runtimeids.GraphEntityIDBlob(identity.value); err != nil {
			return 0, fmt.Errorf("%s: %w", identity.name, err)
		}
	}
	if err := s.validateDirectEdgeSelection(ctx, edge, false); err != nil {
		return 0, err
	}
	edge.SortOrder = 100
	return s.withWorkflowGraphMutation(ctx, edge.WorkflowID, func(currentGraph preparedWorkflowGraphSave) (preparedWorkflowGraphSave, error) {
		if _, exists := workflowGraphRecordByID(currentGraph.edges, edge.ID, func(record EdgeRecord) workflow.EdgeID { return record.ID }); exists {
			return preparedWorkflowGraphSave{}, fmt.Errorf("insert workflow edge: id %q already exists", edge.ID)
		}
		return withWorkflowGraphEdge(currentGraph, edge), nil
	}, func(ctx context.Context, q *sqlitegen.Queries, _ *sql.Tx) error {
		if err := ensureWorkflowTransitionGroupID(ctx, q, edge.WorkflowID, edge.TransitionGroupID); err != nil {
			return err
		}
		if err := ensureWorkflowNodeID(ctx, q, edge.WorkflowID, edge.TargetNodeID); err != nil {
			return err
		}
		return upsertWorkflowEdge(ctx, q, edge, edge.SortOrder, "insert workflow edge")
	})
}

func (s *Store) UpdateEdge(ctx context.Context, edge EdgeRecord) (int64, error) {
	if strings.TrimSpace(string(edge.ID)) == "" {
		return 0, errors.New("edge id is required")
	}
	if edge.WorkflowID.IsZero() {
		return 0, ErrWorkflowIDRequired
	}
	if err := s.validateDirectEdgeSelection(ctx, edge, true); err != nil {
		return 0, err
	}
	return s.withWorkflowGraphMutation(ctx, edge.WorkflowID, func(currentGraph preparedWorkflowGraphSave) (preparedWorkflowGraphSave, error) {
		current, exists := workflowGraphRecordByID(currentGraph.edges, edge.ID, func(record EdgeRecord) workflow.EdgeID { return record.ID })
		if !exists {
			return preparedWorkflowGraphSave{}, sql.ErrNoRows
		}
		edge.SortOrder = current.SortOrder
		return withWorkflowGraphEdge(currentGraph, edge), nil
	}, func(ctx context.Context, q *sqlitegen.Queries, _ *sql.Tx) error {
		if err := ensureWorkflowTransitionGroupID(ctx, q, edge.WorkflowID, edge.TransitionGroupID); err != nil {
			return err
		}
		if err := ensureWorkflowNodeID(ctx, q, edge.WorkflowID, edge.TargetNodeID); err != nil {
			return err
		}
		return upsertWorkflowEdge(ctx, q, edge, edge.SortOrder, "update workflow edge")
	})
}

func (s *Store) validateDirectEdgeSelection(ctx context.Context, candidate EdgeRecord, updating bool) error {
	if workflow.CanonicalAssigneeSelection(candidate.AssigneeSelection) != workflow.AssigneeSelectionPreviousNode &&
		workflow.CanonicalThinkingSelection(candidate.ThinkingSelection) != workflow.ThinkingSelectionPreviousNode {
		return nil
	}
	definition, _, err := s.GetDefinition(ctx, candidate.WorkflowID)
	if err != nil {
		return err
	}
	candidateValue := workflowEdgeFromRecord(candidate)
	replaced := false
	for index, edge := range definition.Edges {
		if edge.ID != candidate.ID {
			continue
		}
		if updating {
			definition.Edges[index] = candidateValue
			replaced = true
		}
	}
	if updating && !replaced {
		return nil
	}
	if !updating {
		definition.Edges = append(definition.Edges, candidateValue)
	}
	planned, err := workflow.PlanDefinitionTransitionSelection(definition, candidateValue, s.roleResolver, false)
	if err != nil {
		return err
	}
	applicability := planned.Applicability
	if workflow.CanonicalAssigneeSelection(candidate.AssigneeSelection) == workflow.AssigneeSelectionPreviousNode &&
		!applicability.Assignee.Available {
		return fmt.Errorf("edge Assignee selection is inapplicable: %s", applicability.Assignee.Reason)
	}
	if workflow.CanonicalThinkingSelection(candidate.ThinkingSelection) == workflow.ThinkingSelectionPreviousNode &&
		!applicability.Thinking.Available {
		return fmt.Errorf("edge thinking selection is inapplicable: %s", applicability.Thinking.Reason)
	}
	return nil
}

func (s *Store) DeleteNode(ctx context.Context, nodeID workflow.NodeID) error {
	if strings.TrimSpace(string(nodeID)) == "" {
		return errors.New("node id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	node, err := q.GetWorkflowNode(ctx, string(nodeID))
	if err != nil {
		return err
	}
	workflowID := node.WorkflowID
	currentGraph, err := currentWorkflowGraphSavePrepared(ctx, q, workflowID)
	if err != nil {
		return err
	}
	if err := enforceWorkflowGraphEditPolicy(ctx, q, workflowID, withoutWorkflowGraphNode(currentGraph, nodeID)); err != nil {
		return err
	}
	refs, err := q.CountCurrentTaskNodeAnchorReferences(ctx, string(nodeID))
	if err != nil {
		return err
	}
	if refs > 0 {
		return ErrNodeHasTaskHistory
	}
	if deleted, err := q.DeleteWorkflowNode(ctx, string(nodeID)); err != nil {
		return fmt.Errorf("delete workflow node: %w", err)
	} else if deleted != 1 {
		return sql.ErrNoRows
	}
	if _, err := s.incrementWorkflowVersion(ctx, q, node.WorkflowID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteEdge(ctx context.Context, edgeID workflow.EdgeID) error {
	if strings.TrimSpace(string(edgeID)) == "" {
		return errors.New("edge id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	edge, err := q.GetWorkflowEdge(ctx, string(edgeID))
	if err != nil {
		return err
	}
	workflowID := edge.WorkflowID
	currentGraph, err := currentWorkflowGraphSavePrepared(ctx, q, workflowID)
	if err != nil {
		return err
	}
	if err := enforceWorkflowGraphEditPolicy(ctx, q, workflowID, withoutWorkflowGraphEdge(currentGraph, edgeID)); err != nil {
		return err
	}
	refs, err := q.CountTaskEdgeReferences(ctx, string(edgeID))
	if err != nil {
		return err
	}
	if refs > 0 {
		return ErrEdgeHasTaskHistory
	}
	if deleted, err := q.DeleteWorkflowEdge(ctx, string(edgeID)); err != nil {
		return fmt.Errorf("delete workflow edge: %w", err)
	} else if deleted != 1 {
		return sql.ErrNoRows
	}
	if _, err := s.incrementWorkflowVersion(ctx, q, edge.WorkflowID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetDefinition(ctx context.Context, workflowID runtimeids.WorkflowID) (workflow.Definition, WorkflowRecord, error) {
	if s == nil {
		return workflow.Definition{}, WorkflowRecord{}, errors.New("workflow store is required")
	}
	return GetDefinitionWithQueries(ctx, s.queries, workflowID)
}

// GetDefinitionWithQueries reads and decodes a Workflow definition through the
// supplied generated queries so callers can keep definition reads inside one
// transaction-owned query snapshot.
func GetDefinitionWithQueries(ctx context.Context, q *sqlitegen.Queries, workflowID runtimeids.WorkflowID) (workflow.Definition, WorkflowRecord, error) {
	if q == nil {
		return workflow.Definition{}, WorkflowRecord{}, errors.New("workflow queries are required")
	}
	return workflowDefinitionFromQueries(ctx, q, workflowID)
}

func workflowDefinitionFromQueries(ctx context.Context, q *sqlitegen.Queries, workflowID runtimeids.WorkflowID) (workflow.Definition, WorkflowRecord, error) {
	row, err := q.GetWorkflow(ctx, workflowID)
	if err != nil {
		return workflow.Definition{}, WorkflowRecord{}, err
	}
	record := workflowRecordFromRow(workflowRecordRow{
		ID:                       row.ID,
		Name:                     row.Name,
		Description:              row.Description,
		Version:                  row.Version,
		ExecutionTargetPolicy:    row.ExecutionTargetPolicy,
		ExecutionTargetCustomRef: row.ExecutionTargetCustomRef,
		CreatedAtUnixMs:          row.CreatedAtUnixMs,
		UpdatedAtUnixMs:          row.UpdatedAtUnixMs,
	})
	prepared, err := currentWorkflowGraphSavePrepared(ctx, q, workflowID)
	if err != nil {
		return workflow.Definition{}, WorkflowRecord{}, err
	}
	definition, err := workflowDefinitionFromPreparedGraph(
		prepared,
		workflowID,
		row.Name,
		record.ExecutionTargetPolicy,
	)
	if err != nil {
		return workflow.Definition{}, WorkflowRecord{}, err
	}
	return definition, record, nil
}

type workflowRecordRow struct {
	ID                       runtimeids.WorkflowID
	Name                     string
	Description              string
	Version                  int64
	ExecutionTargetPolicy    string
	ExecutionTargetCustomRef sql.NullString
	CreatedAtUnixMs          int64
	UpdatedAtUnixMs          int64
	ActivityAtUnixMs         *int64
	ProjectLinkDefault       sql.NullInt64
	ProjectNameOrderKey      string
}

func workflowListActivityAtUnixMs(projectID *string, globalActivity int64, projectActivity sql.NullInt64) *int64 {
	if projectID != nil {
		return metadata.OptionalInt64(projectActivity)
	}
	value := globalActivity
	return &value
}

func workflowRecordFromRow(row workflowRecordRow) WorkflowRecord {
	record := WorkflowRecord{
		ID:          row.ID,
		Name:        row.Name,
		Description: row.Description,
		Version:     row.Version,
		ExecutionTargetPolicy: workflow.ExecutionTargetPolicy{
			Mode:      workflow.ExecutionTargetMode(row.ExecutionTargetPolicy),
			CustomRef: metadata.OptionalString(row.ExecutionTargetCustomRef),
		}.Canonical(),
	}
	if row.ProjectLinkDefault.Valid {
		record.ProjectLink = &WorkflowListProjectLink{Default: row.ProjectLinkDefault.Int64 != 0}
	}
	return record
}

func sqliteLowerASCII(value string) string {
	bytes := []byte(value)
	for index, current := range bytes {
		if current >= 'A' && current <= 'Z' {
			bytes[index] = current + ('a' - 'A')
		}
	}
	return string(bytes)
}

func resolveWorkflowNodeGroupID(ctx context.Context, q *sqlitegen.Queries, workflowID runtimeids.WorkflowID, groupID *string, groupKey string) (*string, error) {
	if groupID != nil {
		trimmedGroupID := strings.TrimSpace(*groupID)
		if trimmedGroupID == "" {
			return nil, errors.New("workflow node group id must be non-blank when present")
		}
		row, err := q.GetWorkflowNodeGroupByID(ctx, trimmedGroupID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("workflow node group %q not found", trimmedGroupID)
			}
			return nil, err
		}
		if row.WorkflowID != workflowID {
			return nil, fmt.Errorf("workflow node group %q belongs to workflow %q: %w", trimmedGroupID, row.WorkflowID, ErrBelongsToOtherWorkflow)
		}
		return &trimmedGroupID, nil
	}
	trimmedGroupKey := strings.TrimSpace(groupKey)
	if trimmedGroupKey == "" {
		return nil, nil
	}
	row, err := q.GetWorkflowNodeGroupByKey(ctx, sqlitegen.GetWorkflowNodeGroupByKeyParams{WorkflowID: workflowID, GroupKey: trimmedGroupKey})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("workflow node group %q not found", trimmedGroupKey)
		}
		return nil, err
	}
	value := row.ID
	return &value, nil
}

func ensureWorkflowNodeID(ctx context.Context, q *sqlitegen.Queries, workflowID runtimeids.WorkflowID, nodeID workflow.NodeID) error {
	trimmedNodeID := strings.TrimSpace(string(nodeID))
	if trimmedNodeID == "" {
		return errors.New("workflow node id is required")
	}
	row, err := q.GetWorkflowNode(ctx, trimmedNodeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("workflow node %q not found: %w", trimmedNodeID, sql.ErrNoRows)
		}
		return fmt.Errorf("resolve workflow node %q: %w", trimmedNodeID, err)
	}
	if row.WorkflowID != workflowID {
		return fmt.Errorf("workflow node %q belongs to workflow %q, not %q: %w", trimmedNodeID, row.WorkflowID, workflowID, ErrBelongsToOtherWorkflow)
	}
	return nil
}

func ensureWorkflowTransitionGroupID(ctx context.Context, q *sqlitegen.Queries, workflowID runtimeids.WorkflowID, groupID workflow.TransitionGroupID) error {
	trimmedGroupID := strings.TrimSpace(string(groupID))
	if trimmedGroupID == "" {
		return errors.New("workflow transition group id is required")
	}
	rowWorkflowID, err := q.GetWorkflowTransitionGroupWorkflowID(ctx, trimmedGroupID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("workflow transition group %q not found: %w", trimmedGroupID, sql.ErrNoRows)
		}
		return fmt.Errorf("resolve workflow transition group %q: %w", trimmedGroupID, err)
	}
	if rowWorkflowID != workflowID {
		return fmt.Errorf("workflow transition group %q belongs to workflow %q, not %q: %w", trimmedGroupID, rowWorkflowID, workflowID, ErrBelongsToOtherWorkflow)
	}
	return nil
}

func prefixedID(prefix string) string {
	return prefix + "-" + uuid.NewString()
}

func nullableString(value string) sql.NullString {
	trimmed := strings.TrimSpace(value)
	return sql.NullString{String: trimmed, Valid: trimmed != ""}
}

func marshalJSONArray[T any](value []T) (string, error) {
	if value == nil {
		value = []T{}
	}
	return workflow.MarshalString(value)
}
