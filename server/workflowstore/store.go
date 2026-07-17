package workflowstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/shared/runtimeids"
	"github.com/google/uuid"
)

type Store struct {
	metadata     *metadata.Store
	db           *sql.DB
	queries      *sqlitegen.Queries
	roleResolver workflow.RoleResolver
	now          func() time.Time
	eventMu      sync.RWMutex
	eventSink    WorkflowEventPublisher
}

type Option func(*Store)

func WithRoleResolver(resolver workflow.RoleResolver) Option {
	return func(s *Store) {
		s.roleResolver = resolver
	}
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
		metadata:  metadataStore,
		db:        metadataStore.DB(),
		queries:   metadataStore.Queries(),
		now:       func() time.Time { return time.Now().UTC() },
		eventSink: noopWorkflowEventPublisher{},
	}
	for _, opt := range opts {
		opt(store)
	}
	return store, nil
}

func (s *Store) incrementWorkflowVersion(ctx context.Context, q *sqlitegen.Queries, workflowID workflow.WorkflowID) (int64, error) {
	revision, err := q.IncrementWorkflowVersion(ctx, sqlitegen.IncrementWorkflowVersionParams{ID: string(workflowID), UpdatedAtUnixMs: s.now().UnixMilli()})
	if err != nil {
		return 0, fmt.Errorf("increment workflow version: %w", err)
	}
	return revision, nil
}

func (s *Store) withWorkflowGraphMutation(ctx context.Context, workflowID workflow.WorkflowID, nextGraph func(preparedWorkflowGraphSave) (preparedWorkflowGraphSave, error), apply func(context.Context, *sqlitegen.Queries, *sql.Tx) error) (int64, error) {
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
	ID                    workflow.WorkflowID
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
	WorkflowID         workflow.WorkflowID
	Key                workflow.ModelKey
	Kind               workflow.NodeKind
	DisplayName        string
	GroupID            string
	GroupKey           string
	SubagentRole       string
	PromptTemplate     string
	CompletionMode     string
	ScriptPath         string
	InputFields        []workflow.InputField
	JoinInputProviders []workflow.JoinInputProvider
	OutputFields       []workflow.OutputField
	SortOrder          int64
}

func workflowNodeFromRecord(node NodeRecord) (workflow.Node, error) {
	return workflow.NewNode(
		workflow.NodeIdentity{
			WorkflowID:  node.WorkflowID,
			ID:          node.ID,
			Key:         node.Key,
			DisplayName: node.DisplayName,
			GroupID:     node.GroupID,
		},
		node.Kind,
		workflow.NodeFields{
			SubagentRole:       node.SubagentRole,
			PromptTemplate:     node.PromptTemplate,
			CompletionMode:     node.CompletionMode,
			InputFields:        node.InputFields,
			JoinInputProviders: node.JoinInputProviders,
			OutputFields:       node.OutputFields,
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
	WorkflowID  workflow.WorkflowID
	Key         workflow.ModelKey
	DisplayName string
	SortOrder   int64
}

type WorkflowEventRecord struct {
	ProjectID        string
	WorkflowID       string
	Resource         string
	Action           string
	ChangedIDs       []string
	OccurredAtUnixMs int64
}

type WorkflowEventPublisher interface {
	PublishWorkflowEvent(context.Context, WorkflowEventRecord) error
}

type noopWorkflowEventPublisher struct{}

func (noopWorkflowEventPublisher) PublishWorkflowEvent(context.Context, WorkflowEventRecord) error {
	return nil
}

type TransitionGroupRecord struct {
	ID           workflow.TransitionGroupID
	WorkflowID   workflow.WorkflowID
	SourceNodeID workflow.NodeID
	TransitionID workflow.TransitionID
	DisplayName  string
	Description  string
	SortOrder    int64
}

type EdgeRecord struct {
	ID                 workflow.EdgeID
	WorkflowID         workflow.WorkflowID
	TransitionGroupID  workflow.TransitionGroupID
	Key                workflow.ModelKey
	TargetNodeID       workflow.NodeID
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
	WorkflowID workflow.WorkflowID
	IsDefault  bool
}

type ProjectWorkflowUnlinkResult struct {
	LinkID     string
	ProjectID  string
	WorkflowID workflow.WorkflowID
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
	ID                workflow.TaskID
	ProjectID         string
	WorkflowID        workflow.WorkflowID
	LinkID            string
	ShortID           string
	Title             string
	Body              string
	SourceURL         string
	SourceWorkspaceID string
	ManagedWorktreeID string
	ExecutionTarget   *ExecutionTargetSnapshot
	CanceledAt        *int64
	CancelReason      *string
	Version           int64
}

type PlacementRecord struct {
	ID     workflow.PlacementID
	TaskID workflow.TaskID
	NodeID workflow.NodeID
	State  string
}

type RunRecord struct {
	ID                      workflow.RunID
	TaskID                  workflow.TaskID
	PlacementID             workflow.PlacementID
	NodeID                  workflow.NodeID
	SessionID               string
	Generation              int64
	AutomationRequestedAt   *int64
	StartedAt               *int64
	CompletedAt             *int64
	InterruptedAt           *int64
	InterruptionReason      *string
	WaitingAskID            *string
	EffectiveCompletionMode *string
	InvalidCompletions      int64
}

type ActiveRunCompletionTargetSelector struct {
	RunID     workflow.RunID
	SessionID string
	TaskID    workflow.TaskID
	ProjectID string
	ShortID   string
}

type ActiveRunCompletionTarget struct {
	Run RunRecord
}

type RunnableRunRecord struct {
	RunRecord
	WorkflowRevisionSeen int64
}

type RunStartContext struct {
	Run                            RunRecord
	Task                           TaskRecord
	Workflow                       WorkflowRecord
	Node                           NodeRecord
	ContextMode                    workflow.ContextMode
	WorkflowHasContinueSessionEdge bool
	SourceRunID                    workflow.RunID
	SourceSessionID                string
	SourceNode                     NodeRecord
	AcceptedTransitionPath         AcceptedTransitionPath
	// IsFanoutBranch is true when this run's placement is one branch of a
	// parallel fan-out transition group. Continuation modes must isolate such
	// runs (fork the source session) instead of sharing/mutating it.
	IsFanoutBranch       bool
	TransitionIDs        []string
	TransitionOptions    []TransitionOption
	PromptTemplate       string
	Parameters           []workflow.Parameter
	ParameterValues      map[string]string
	PriorParameterValues map[string]map[string]string
	InputValues          map[string]string
	NodeOutputValues     map[string]map[string]string
	ExecutionRoot        *ExecutionRoot
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

type TransitionRecord struct {
	ID           workflow.TransitionID
	TaskID       workflow.TaskID
	TransitionID string
	State        string
	Commentary   string
	OutputValues map[string]string
	CreatedAt    int64
}

type TransitionEdgeRecord struct {
	ID                   string
	TaskTransitionID     workflow.TransitionID
	WorkflowEdgeID       workflow.EdgeID
	EdgeKey              string
	TargetNodeID         workflow.NodeID
	TargetPlacementID    workflow.PlacementID
	State                string
	WorkflowRevisionSeen int64
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
	PageSize   int
	PageToken  string
	Query      string
	ProjectID  *string
	WorkflowID *workflow.WorkflowID
}

type ListWorkflowsResult struct {
	Workflows     []WorkflowRecord
	ProjectID     *string
	NextPageToken string
}

type workflowListPageCursor struct {
	activityAtUnixMs  int64
	workflowID        string
	projectDefault    *int64
	projectName       *string
	projectID         *string
	filterWorkflowID  *string
	searchQuery       string
	filterFingerprint string
	hasValue          bool
}

type workflowListPageTokenPayload struct {
	Version           int     `json:"version"`
	ActivityAtUnixMs  int64   `json:"activity_at_unix_ms"`
	WorkflowID        string  `json:"workflow_id"`
	ProjectDefault    *int64  `json:"project_default,omitempty"`
	ProjectName       *string `json:"project_name,omitempty"`
	ProjectID         *string `json:"project_id,omitempty"`
	FilterWorkflowID  *string `json:"filter_workflow_id,omitempty"`
	SearchQuery       string  `json:"search_query"`
	FilterFingerprint string  `json:"filter_fingerprint"`
}

const (
	defaultWorkflowListPageSize = 50
	maxWorkflowListPageSize     = 100
)

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
	workflowID := prefixedID("workflow")
	startID := prefixedID("node")
	doneID := prefixedID("node")
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
	if err := q.InsertWorkflowNode(ctx, sqlitegen.InsertWorkflowNodeParams{ID: startID, WorkflowID: workflowID, NodeKey: "backlog", Kind: string(workflow.NodeKindStart), DisplayName: "Backlog", InputFieldsJson: "[]", JoinInputProvidersJson: "[]", OutputFieldsJson: "[]", SortOrder: 0}); err != nil {
		return WorkflowRecord{}, fmt.Errorf("insert backlog node: %w", err)
	}
	if err := q.InsertWorkflowNode(ctx, sqlitegen.InsertWorkflowNodeParams{ID: doneID, WorkflowID: workflowID, NodeKey: "done", Kind: string(workflow.NodeKindTerminal), DisplayName: "Done", InputFieldsJson: "[]", JoinInputProvidersJson: "[]", OutputFieldsJson: "[]", SortOrder: 1000}); err != nil {
		return WorkflowRecord{}, fmt.Errorf("insert done node: %w", err)
	}
	return WorkflowRecord{ID: workflow.WorkflowID(workflowID), Name: name, Description: description, Version: 1, ExecutionTargetPolicy: policy}, nil
}

func (s *Store) UpdateWorkflowInfo(ctx context.Context, workflowID workflow.WorkflowID, name string, description string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrWorkflowNameRequired
	}
	updated, err := s.queries.UpdateWorkflowInfo(ctx, sqlitegen.UpdateWorkflowInfoParams{ID: string(workflowID), Name: name, Description: strings.TrimSpace(description), UpdatedAtUnixMs: s.now().UnixMilli()})
	if err != nil {
		return fmt.Errorf("update workflow info: %w", err)
	}
	if updated == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ListWorkflows(ctx context.Context, req ListWorkflowsRequest) (ListWorkflowsResult, error) {
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = defaultWorkflowListPageSize
	}
	if pageSize > maxWorkflowListPageSize {
		pageSize = maxWorkflowListPageSize
	}
	cursor, err := parseWorkflowListPageToken(req.PageToken)
	if err != nil {
		return ListWorkflowsResult{}, err
	}
	projectID := req.ProjectID
	workflowID := workflowIDString(req.WorkflowID)
	query := strings.TrimSpace(req.Query)
	if cursor.hasValue {
		if projectID != nil && !optionalStringEqual(projectID, cursor.projectID) {
			return ListWorkflowsResult{}, errors.New("workflow list page token project scope conflict")
		}
		if workflowID != nil && !optionalStringEqual(workflowID, cursor.filterWorkflowID) {
			return ListWorkflowsResult{}, errors.New("workflow list page token workflow scope conflict")
		}
		if query != "" && query != cursor.searchQuery {
			return ListWorkflowsResult{}, errors.New("workflow list page token search scope conflict")
		}
		if projectID == nil {
			projectID = cursor.projectID
		}
		if workflowID == nil {
			workflowID = cursor.filterWorkflowID
		}
		if query == "" {
			query = cursor.searchQuery
		}
		fingerprint, fingerprintErr := workflowListFilterFingerprint(projectID, workflowID, query)
		if fingerprintErr != nil {
			return ListWorkflowsResult{}, fingerprintErr
		}
		if fingerprint != cursor.filterFingerprint {
			return ListWorkflowsResult{}, errors.New("workflow list page token filter fingerprint conflict")
		}
	}
	cursorActive := int64(0)
	if cursor.hasValue {
		cursorActive = 1
	}
	cursorProjectDefault := sql.NullInt64{}
	if cursor.projectDefault != nil {
		cursorProjectDefault = sql.NullInt64{Int64: *cursor.projectDefault, Valid: true}
	}
	cursorProjectName := sql.NullString{}
	if cursor.projectName != nil {
		cursorProjectName = sql.NullString{String: *cursor.projectName, Valid: true}
	}
	rows, err := s.queries.ListWorkflowRecordsPage(ctx, sqlitegen.ListWorkflowRecordsPageParams{
		PageLimit:              int64(pageSize + 1),
		ProjectID:              nullableWorkflowFilter(projectID),
		WorkflowID:             nullableWorkflowFilter(workflowID),
		SearchQuery:            query,
		CursorActive:           cursorActive,
		CursorActivityAtUnixMs: cursor.activityAtUnixMs,
		CursorWorkflowID:       cursor.workflowID,
		CursorProjectDefault:   cursorProjectDefault,
		CursorProjectName:      cursorProjectName,
	})
	if err != nil {
		return ListWorkflowsResult{}, err
	}
	rowsOut := make([]workflowRecordRow, 0, pageSize+1)
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
			ActivityAtUnixMs:         row.ActivityAtUnixMs,
			ProjectLinkDefault:       row.ProjectLinkDefault,
		})
	}
	nextPageToken := ""
	if len(rowsOut) > pageSize {
		nextPageToken, err = workflowListPageToken(rowsOut[pageSize-1], projectID, workflowID, query)
		if err != nil {
			return ListWorkflowsResult{}, err
		}
		rowsOut = rowsOut[:pageSize]
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
	return ListWorkflowsResult{Workflows: out, ProjectID: responseProjectID, NextPageToken: nextPageToken}, nil
}

func (s *Store) AddNode(ctx context.Context, node NodeRecord) (int64, error) {
	if strings.TrimSpace(string(node.WorkflowID)) == "" {
		return 0, errors.New("workflow id is required")
	}
	if err := validateNodeCompletionMode(node.Kind, node.CompletionMode); err != nil {
		return 0, err
	}
	if node.ID == "" {
		node.ID = workflow.NodeID(prefixedID("node"))
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
		groupID, err := resolveWorkflowNodeGroupID(ctx, q, string(node.WorkflowID), node.GroupID, node.GroupKey)
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
	if strings.TrimSpace(string(node.WorkflowID)) == "" {
		return 0, errors.New("workflow id is required")
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
		groupID, err := resolveWorkflowNodeGroupID(ctx, q, string(node.WorkflowID), node.GroupID, node.GroupKey)
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
	if strings.TrimSpace(string(group.WorkflowID)) == "" {
		return NodeGroupRecord{}, 0, errors.New("workflow id is required")
	}
	if strings.TrimSpace(string(group.Key)) == "" {
		return NodeGroupRecord{}, 0, errors.New("group key is required")
	}
	if strings.TrimSpace(group.DisplayName) == "" {
		return NodeGroupRecord{}, 0, errors.New("group display name is required")
	}
	if strings.TrimSpace(group.ID) == "" {
		group.ID = prefixedID("workflow-node-group")
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
	if strings.TrimSpace(string(group.WorkflowID)) == "" {
		return NodeGroupRecord{}, 0, errors.New("workflow id is required")
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

func (s *Store) DeleteNodeGroup(ctx context.Context, workflowID workflow.WorkflowID, groupID string) (int64, error) {
	if strings.TrimSpace(string(workflowID)) == "" {
		return 0, errors.New("workflow id is required")
	}
	if strings.TrimSpace(groupID) == "" {
		return 0, errors.New("group id is required")
	}
	nodeCount, err := s.queries.CountWorkflowNodesByGroup(ctx, nullableString(groupID))
	if err != nil {
		return 0, err
	}
	if nodeCount > 0 {
		return 0, errors.New("workflow node group is in use")
	}
	return s.withWorkflowGraphMutation(ctx, workflowID, func(currentGraph preparedWorkflowGraphSave) (preparedWorkflowGraphSave, error) {
		return withoutWorkflowGraphNodeGroup(currentGraph, strings.TrimSpace(groupID)), nil
	}, func(ctx context.Context, q *sqlitegen.Queries, _ *sql.Tx) error {
		deleted, err := q.DeleteWorkflowNodeGroup(ctx, sqlitegen.DeleteWorkflowNodeGroupParams{ID: strings.TrimSpace(groupID), WorkflowID: string(workflowID)})
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
	if strings.TrimSpace(event.Resource) == "" {
		return ErrEventResourceRequired
	}
	if strings.TrimSpace(event.Action) == "" {
		return ErrEventActionRequired
	}
	occurredAt := event.OccurredAtUnixMs
	if occurredAt == 0 {
		occurredAt = s.now().UnixMilli()
	}
	normalized := WorkflowEventRecord{
		ProjectID:        strings.TrimSpace(event.ProjectID),
		WorkflowID:       strings.TrimSpace(event.WorkflowID),
		Resource:         strings.TrimSpace(event.Resource),
		Action:           strings.TrimSpace(event.Action),
		ChangedIDs:       append([]string(nil), event.ChangedIDs...),
		OccurredAtUnixMs: occurredAt,
	}
	s.eventMu.RLock()
	sink := s.eventSink
	s.eventMu.RUnlock()
	return sink.PublishWorkflowEvent(ctx, normalized)
}

func (s *Store) AddTransitionGroup(ctx context.Context, group TransitionGroupRecord) (int64, error) {
	if group.ID == "" {
		group.ID = workflow.TransitionGroupID(prefixedID("group"))
	}
	group.SortOrder = 100
	return s.withWorkflowGraphMutation(ctx, group.WorkflowID, func(currentGraph preparedWorkflowGraphSave) (preparedWorkflowGraphSave, error) {
		if _, exists := workflowGraphRecordByID(currentGraph.transitionGroups, group.ID, func(record TransitionGroupRecord) workflow.TransitionGroupID { return record.ID }); exists {
			return preparedWorkflowGraphSave{}, fmt.Errorf("insert transition group: id %q already exists", group.ID)
		}
		return withWorkflowGraphTransitionGroup(currentGraph, group), nil
	}, func(ctx context.Context, q *sqlitegen.Queries, _ *sql.Tx) error {
		if err := ensureWorkflowNodeID(ctx, q, string(group.WorkflowID), group.SourceNodeID); err != nil {
			return err
		}
		return upsertWorkflowTransitionGroup(ctx, q, group, group.SortOrder, "insert transition group")
	})
}

func (s *Store) UpdateTransitionGroup(ctx context.Context, group TransitionGroupRecord) (int64, error) {
	if strings.TrimSpace(string(group.ID)) == "" {
		return 0, errors.New("transition group id is required")
	}
	if strings.TrimSpace(string(group.WorkflowID)) == "" {
		return 0, errors.New("workflow id is required")
	}
	return s.withWorkflowGraphMutation(ctx, group.WorkflowID, func(currentGraph preparedWorkflowGraphSave) (preparedWorkflowGraphSave, error) {
		current, exists := workflowGraphRecordByID(currentGraph.transitionGroups, group.ID, func(record TransitionGroupRecord) workflow.TransitionGroupID { return record.ID })
		if !exists {
			return preparedWorkflowGraphSave{}, sql.ErrNoRows
		}
		group.SortOrder = current.SortOrder
		return withWorkflowGraphTransitionGroup(currentGraph, group), nil
	}, func(ctx context.Context, q *sqlitegen.Queries, _ *sql.Tx) error {
		if err := ensureWorkflowNodeID(ctx, q, string(group.WorkflowID), group.SourceNodeID); err != nil {
			return err
		}
		return upsertWorkflowTransitionGroup(ctx, q, group, group.SortOrder, "update transition group")
	})
}

func (s *Store) AddEdge(ctx context.Context, edge EdgeRecord) (int64, error) {
	if edge.ID == "" {
		edge.ID = workflow.EdgeID(prefixedID("edge"))
	}
	edge.SortOrder = 100
	return s.withWorkflowGraphMutation(ctx, edge.WorkflowID, func(currentGraph preparedWorkflowGraphSave) (preparedWorkflowGraphSave, error) {
		if _, exists := workflowGraphRecordByID(currentGraph.edges, edge.ID, func(record EdgeRecord) workflow.EdgeID { return record.ID }); exists {
			return preparedWorkflowGraphSave{}, fmt.Errorf("insert workflow edge: id %q already exists", edge.ID)
		}
		return withWorkflowGraphEdge(currentGraph, edge), nil
	}, func(ctx context.Context, q *sqlitegen.Queries, _ *sql.Tx) error {
		if err := ensureWorkflowTransitionGroupID(ctx, q, string(edge.WorkflowID), edge.TransitionGroupID); err != nil {
			return err
		}
		if err := ensureWorkflowNodeID(ctx, q, string(edge.WorkflowID), edge.TargetNodeID); err != nil {
			return err
		}
		return upsertWorkflowEdge(ctx, q, edge, edge.SortOrder, "insert workflow edge")
	})
}

func (s *Store) UpdateEdge(ctx context.Context, edge EdgeRecord) (int64, error) {
	if strings.TrimSpace(string(edge.ID)) == "" {
		return 0, errors.New("edge id is required")
	}
	if strings.TrimSpace(string(edge.WorkflowID)) == "" {
		return 0, errors.New("workflow id is required")
	}
	return s.withWorkflowGraphMutation(ctx, edge.WorkflowID, func(currentGraph preparedWorkflowGraphSave) (preparedWorkflowGraphSave, error) {
		current, exists := workflowGraphRecordByID(currentGraph.edges, edge.ID, func(record EdgeRecord) workflow.EdgeID { return record.ID })
		if !exists {
			return preparedWorkflowGraphSave{}, sql.ErrNoRows
		}
		edge.SortOrder = current.SortOrder
		return withWorkflowGraphEdge(currentGraph, edge), nil
	}, func(ctx context.Context, q *sqlitegen.Queries, _ *sql.Tx) error {
		if err := ensureWorkflowTransitionGroupID(ctx, q, string(edge.WorkflowID), edge.TransitionGroupID); err != nil {
			return err
		}
		if err := ensureWorkflowNodeID(ctx, q, string(edge.WorkflowID), edge.TargetNodeID); err != nil {
			return err
		}
		return upsertWorkflowEdge(ctx, q, edge, edge.SortOrder, "update workflow edge")
	})
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
	workflowID := workflow.WorkflowID(node.WorkflowID)
	currentGraph, err := currentWorkflowGraphSavePrepared(ctx, q, workflowID)
	if err != nil {
		return err
	}
	if err := enforceWorkflowGraphEditPolicy(ctx, q, workflowID, withoutWorkflowGraphNode(currentGraph, nodeID)); err != nil {
		return err
	}
	refs, err := q.CountCurrentTaskNodeAnchorReferences(ctx, nullableString(string(nodeID)))
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
	if _, err := s.incrementWorkflowVersion(ctx, q, workflow.WorkflowID(node.WorkflowID)); err != nil {
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
	workflowID := workflow.WorkflowID(edge.WorkflowID)
	currentGraph, err := currentWorkflowGraphSavePrepared(ctx, q, workflowID)
	if err != nil {
		return err
	}
	if err := enforceWorkflowGraphEditPolicy(ctx, q, workflowID, withoutWorkflowGraphEdge(currentGraph, edgeID)); err != nil {
		return err
	}
	refs, err := q.CountTaskEdgeReferences(ctx, sql.NullString{String: string(edgeID), Valid: true})
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
	if _, err := s.incrementWorkflowVersion(ctx, q, workflow.WorkflowID(edge.WorkflowID)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetDefinition(ctx context.Context, workflowID workflow.WorkflowID) (workflow.Definition, WorkflowRecord, error) {
	return workflowDefinitionFromQueries(ctx, s.queries, workflowID)
}

func workflowDefinitionFromQueries(ctx context.Context, q *sqlitegen.Queries, workflowID workflow.WorkflowID) (workflow.Definition, WorkflowRecord, error) {
	row, err := q.GetWorkflow(ctx, string(workflowID))
	if err != nil {
		return workflow.Definition{}, WorkflowRecord{}, err
	}
	nodes, err := q.ListWorkflowNodes(ctx, string(workflowID))
	if err != nil {
		return workflow.Definition{}, WorkflowRecord{}, err
	}
	nodeGroups, err := q.ListWorkflowNodeGroups(ctx, string(workflowID))
	if err != nil {
		return workflow.Definition{}, WorkflowRecord{}, err
	}
	groups, err := q.ListWorkflowTransitionGroups(ctx, string(workflowID))
	if err != nil {
		return workflow.Definition{}, WorkflowRecord{}, err
	}
	edges, err := q.ListWorkflowEdges(ctx, string(workflowID))
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
	def := workflow.Definition{ID: workflow.WorkflowID(row.ID), DisplayName: row.Name, ExecutionTargetPolicy: record.ExecutionTargetPolicy}
	groupMemberIDs := map[string][]workflow.NodeID{}
	for _, group := range nodeGroups {
		def.NodeGroups = append(def.NodeGroups, workflow.NodeGroup{WorkflowID: workflow.WorkflowID(group.WorkflowID), ID: group.ID, Key: workflow.ModelKey(group.GroupKey), DisplayName: group.DisplayName})
	}
	for _, node := range nodes {
		inputFields := []workflow.InputField{}
		joinProviders := []workflow.JoinInputProvider{}
		outputFields := []workflow.OutputField{}
		if err := workflow.UnmarshalString(node.InputFieldsJson, &inputFields); err != nil {
			return workflow.Definition{}, WorkflowRecord{}, err
		}
		if err := workflow.UnmarshalString(node.JoinInputProvidersJson, &joinProviders); err != nil {
			return workflow.Definition{}, WorkflowRecord{}, err
		}
		if err := workflow.UnmarshalString(node.OutputFieldsJson, &outputFields); err != nil {
			return workflow.Definition{}, WorkflowRecord{}, err
		}
		groupID := ""
		if node.GroupID.Valid {
			groupID = node.GroupID.String
			groupMemberIDs[groupID] = append(groupMemberIDs[groupID], workflow.NodeID(node.ID))
		}
		scriptPath := ""
		if node.ScriptPath.Valid {
			scriptPath = node.ScriptPath.String
		}
		workflowNode, err := workflowNodeFromRecord(NodeRecord{
			ID:                 workflow.NodeID(node.ID),
			WorkflowID:         workflow.WorkflowID(node.WorkflowID),
			Key:                workflow.ModelKey(node.NodeKey),
			Kind:               workflow.NodeKind(node.Kind),
			DisplayName:        node.DisplayName,
			GroupID:            groupID,
			SubagentRole:       node.SubagentRole,
			PromptTemplate:     node.PromptTemplate,
			CompletionMode:     node.CompletionMode,
			ScriptPath:         scriptPath,
			InputFields:        inputFields,
			JoinInputProviders: joinProviders,
			OutputFields:       outputFields,
		})
		if err != nil {
			return workflow.Definition{}, WorkflowRecord{}, err
		}
		def.Nodes = append(def.Nodes, workflowNode)
	}
	for index := range def.NodeGroups {
		def.NodeGroups[index].MemberNodeIDs = groupMemberIDs[def.NodeGroups[index].ID]
	}
	for _, group := range groups {
		def.TransitionGroups = append(def.TransitionGroups, workflow.TransitionGroup{WorkflowID: workflow.WorkflowID(group.WorkflowID), ID: workflow.TransitionGroupID(group.ID), SourceNodeID: workflow.NodeID(group.SourceNodeID), TransitionID: workflow.TransitionID(group.TransitionID), DisplayName: group.DisplayName, Description: group.Description})
	}
	for _, edge := range edges {
		inputs := []workflow.InputBinding{}
		parameters := []workflow.Parameter{}
		requirements := []workflow.OutputRequirement{}
		if err := workflow.UnmarshalString(edge.ParametersJson, &parameters); err != nil {
			return workflow.Definition{}, WorkflowRecord{}, err
		}
		if err := workflow.UnmarshalString(edge.InputBindingsJson, &inputs); err != nil {
			return workflow.Definition{}, WorkflowRecord{}, err
		}
		if err := workflow.UnmarshalString(edge.OutputRequirementsJson, &requirements); err != nil {
			return workflow.Definition{}, WorkflowRecord{}, err
		}
		def.Edges = append(def.Edges, workflow.Edge{WorkflowID: workflow.WorkflowID(edge.WorkflowID), ID: workflow.EdgeID(edge.ID), Key: workflow.ModelKey(edge.EdgeKey), TransitionGroupID: workflow.TransitionGroupID(edge.TransitionGroupID), TargetNodeID: workflow.NodeID(edge.TargetNodeID), RequiresApproval: edge.RequiresApproval != 0, ContextMode: workflow.ContextMode(edge.ContextMode), ContextSource: workflow.CanonicalContextSource(workflow.ContextSource{Kind: workflow.ContextSourceKind(edge.ContextSourceKind), NodeKey: workflow.ModelKey(edge.ContextSourceNodeKey)}), PromptTemplate: edge.PromptTemplate, Parameters: parameters, InputBindings: inputs, OutputRequirements: requirements})
	}
	return def, record, nil
}

type workflowRecordRow struct {
	ID                       string
	Name                     string
	Description              string
	Version                  int64
	ExecutionTargetPolicy    string
	ExecutionTargetCustomRef sql.NullString
	CreatedAtUnixMs          int64
	UpdatedAtUnixMs          int64
	ActivityAtUnixMs         int64
	ProjectLinkDefault       interface{}
}

func workflowRecordFromRow(row workflowRecordRow) WorkflowRecord {
	record := WorkflowRecord{
		ID:          workflow.WorkflowID(row.ID),
		Name:        row.Name,
		Description: row.Description,
		Version:     row.Version,
		ExecutionTargetPolicy: workflow.ExecutionTargetPolicy{
			Mode:      workflow.ExecutionTargetMode(row.ExecutionTargetPolicy),
			CustomRef: metadata.OptionalString(row.ExecutionTargetCustomRef),
		}.Canonical(),
	}
	if row.ProjectLinkDefault != nil {
		defaultValue, ok := row.ProjectLinkDefault.(int64)
		if !ok {
			panic(fmt.Sprintf("workflow list returned project_link_default with unexpected type %T for workflow %q", row.ProjectLinkDefault, row.ID))
		}
		record.ProjectLink = &WorkflowListProjectLink{Default: defaultValue != 0}
	}
	return record
}

func parseWorkflowListPageToken(token string) (workflowListPageCursor, error) {
	if strings.TrimSpace(token) == "" {
		return workflowListPageCursor{}, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return workflowListPageCursor{}, fmt.Errorf("invalid workflow list page token")
	}
	var payload workflowListPageTokenPayload
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return workflowListPageCursor{}, fmt.Errorf("invalid workflow list page token")
	}
	if payload.Version != 2 || payload.ActivityAtUnixMs < 0 || strings.TrimSpace(payload.FilterFingerprint) == "" {
		return workflowListPageCursor{}, fmt.Errorf("invalid workflow list page token")
	}
	if _, err := runtimeids.ParseCanonicalPrefixedUUIDv4(payload.WorkflowID, "workflow-", "workflow id"); err != nil {
		return workflowListPageCursor{}, fmt.Errorf("invalid workflow list page token")
	}
	if payload.ProjectID == nil && (payload.ProjectDefault != nil || payload.ProjectName != nil) {
		return workflowListPageCursor{}, fmt.Errorf("invalid workflow list page token")
	}
	if payload.ProjectID != nil {
		if strings.TrimSpace(*payload.ProjectID) == "" ||
			strings.TrimSpace(*payload.ProjectID) != *payload.ProjectID ||
			payload.ProjectDefault == nil ||
			*payload.ProjectDefault < 0 ||
			*payload.ProjectDefault > 1 ||
			payload.ProjectName == nil ||
			strings.TrimSpace(*payload.ProjectName) == "" {
			return workflowListPageCursor{}, fmt.Errorf("invalid workflow list page token")
		}
	}
	if payload.FilterWorkflowID != nil {
		if _, err := runtimeids.ParseCanonicalPrefixedUUIDv4(*payload.FilterWorkflowID, "workflow-", "workflow id"); err != nil {
			return workflowListPageCursor{}, fmt.Errorf("invalid workflow list page token")
		}
	}
	return workflowListPageCursor{
		activityAtUnixMs:  payload.ActivityAtUnixMs,
		workflowID:        payload.WorkflowID,
		projectDefault:    payload.ProjectDefault,
		projectName:       payload.ProjectName,
		projectID:         payload.ProjectID,
		filterWorkflowID:  payload.FilterWorkflowID,
		searchQuery:       payload.SearchQuery,
		filterFingerprint: payload.FilterFingerprint,
		hasValue:          true,
	}, nil
}

func workflowListPageToken(row workflowRecordRow, projectID *string, workflowID *string, query string) (string, error) {
	payload := workflowListPageTokenPayload{
		Version:          2,
		ActivityAtUnixMs: row.ActivityAtUnixMs,
		WorkflowID:       row.ID,
		ProjectID:        projectID,
		FilterWorkflowID: workflowID,
		SearchQuery:      query,
	}
	fingerprint, err := workflowListFilterFingerprint(projectID, workflowID, query)
	if err != nil {
		return "", err
	}
	payload.FilterFingerprint = fingerprint
	if projectID != nil {
		projectDefault, ok := row.ProjectLinkDefault.(int64)
		if !ok {
			return "", fmt.Errorf("workflow list project default has unexpected type %T for workflow %q", row.ProjectLinkDefault, row.ID)
		}
		payload.ProjectDefault = &projectDefault
		projectName := strings.ToLower(row.Name)
		payload.ProjectName = &projectName
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal workflow list page token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func workflowListFilterFingerprint(projectID *string, workflowID *string, query string) (string, error) {
	scope := struct {
		ProjectID  *string `json:"project_id,omitempty"`
		WorkflowID *string `json:"workflow_id,omitempty"`
		Query      string  `json:"query"`
	}{
		ProjectID:  projectID,
		WorkflowID: workflowID,
		Query:      strings.ToLower(strings.TrimSpace(query)),
	}
	encoded, err := json.Marshal(scope)
	if err != nil {
		return "", fmt.Errorf("marshal workflow list filter scope: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", sum[:]), nil
}

func workflowIDString(id *workflow.WorkflowID) *string {
	if id == nil {
		return nil
	}
	value := string(*id)
	return &value
}

func nullableWorkflowFilter(value *string) interface{} {
	if value == nil {
		return nil
	}
	return *value
}

func optionalStringEqual(left *string, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func resolveWorkflowNodeGroupID(ctx context.Context, q *sqlitegen.Queries, workflowID string, groupID string, groupKey string) (string, error) {
	trimmedGroupID := strings.TrimSpace(groupID)
	if trimmedGroupID != "" {
		row, err := q.GetWorkflowNodeGroupByID(ctx, trimmedGroupID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", fmt.Errorf("workflow node group %q not found", trimmedGroupID)
			}
			return "", err
		}
		if row.WorkflowID != strings.TrimSpace(workflowID) {
			return "", fmt.Errorf("workflow node group %q belongs to workflow %q: %w", trimmedGroupID, row.WorkflowID, ErrBelongsToOtherWorkflow)
		}
		return trimmedGroupID, nil
	}
	trimmedGroupKey := strings.TrimSpace(groupKey)
	if trimmedGroupKey == "" {
		return "", nil
	}
	row, err := q.GetWorkflowNodeGroupByKey(ctx, sqlitegen.GetWorkflowNodeGroupByKeyParams{WorkflowID: strings.TrimSpace(workflowID), GroupKey: trimmedGroupKey})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("workflow node group %q not found", trimmedGroupKey)
		}
		return "", err
	}
	return row.ID, nil
}

func ensureWorkflowNodeID(ctx context.Context, q *sqlitegen.Queries, workflowID string, nodeID workflow.NodeID) error {
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
	if row.WorkflowID != strings.TrimSpace(workflowID) {
		return fmt.Errorf("workflow node %q belongs to workflow %q, not %q: %w", trimmedNodeID, row.WorkflowID, strings.TrimSpace(workflowID), ErrBelongsToOtherWorkflow)
	}
	return nil
}

func ensureWorkflowTransitionGroupID(ctx context.Context, q *sqlitegen.Queries, workflowID string, groupID workflow.TransitionGroupID) error {
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
	if rowWorkflowID != strings.TrimSpace(workflowID) {
		return fmt.Errorf("workflow transition group %q belongs to workflow %q, not %q: %w", trimmedGroupID, rowWorkflowID, strings.TrimSpace(workflowID), ErrBelongsToOtherWorkflow)
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
