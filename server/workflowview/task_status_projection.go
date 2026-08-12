package workflowview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type TaskStatusLiveObservationSource interface {
	ObserveWorkflowTaskExecutions([]workflow.TaskID) (workflowexecution.WorkflowTaskExecutionObservation, error)
}

type TaskStatusObservation struct {
	Live               workflowexecution.WorkflowTaskExecutionObservation
	LiveTaskStatesJSON string
}

type TaskStatusProjectionResult struct {
	Task                         sqlitegen.TaskRecord
	Definition                   definitionSnapshot
	Status                       serverapi.WorkflowTaskStatus
	Done                         bool
	CurrentNodes                 []workflow.CurrentNode
	LiveExecutions               []sessionruntime.TaskExecution
	PendingTransitionApprovalIDs []string
	Actions                      serverapi.WorkflowTaskActions
	AttentionCount               int
}

type TaskStatusProjection struct {
	metadata        *metadata.Store
	workflowStore   *workflowstore.Store
	projector       *TaskProjector
	liveObservation TaskStatusLiveObservationSource
}

func NewTaskStatusProjection(
	metadataStore *metadata.Store,
	workflowStore *workflowstore.Store,
	projector *TaskProjector,
	liveObservation TaskStatusLiveObservationSource,
) (*TaskStatusProjection, error) {
	if metadataStore == nil || metadataStore.DB() == nil || metadataStore.Queries() == nil {
		return nil, errors.New("metadata store is required")
	}
	if workflowStore == nil {
		return nil, errors.New("workflow store is required")
	}
	if projector == nil {
		return nil, errors.New("task projector is required")
	}
	if liveObservation == nil {
		return nil, errors.New("workflow task live observation source is required")
	}
	return &TaskStatusProjection{
		metadata:        metadataStore,
		workflowStore:   workflowStore,
		projector:       projector,
		liveObservation: liveObservation,
	}, nil
}

func (p *TaskStatusProjection) Observe(taskIDs []workflow.TaskID) (TaskStatusObservation, error) {
	if p == nil || p.liveObservation == nil {
		return TaskStatusObservation{}, errors.New("task status projection live observation is required")
	}
	observation, err := p.liveObservation.ObserveWorkflowTaskExecutions(taskIDs)
	if err != nil {
		return TaskStatusObservation{}, err
	}
	liveTaskStatesJSON, err := taskStatusLiveStatesJSON(observation)
	if err != nil {
		return TaskStatusObservation{}, err
	}
	return TaskStatusObservation{
		Live:               observation,
		LiveTaskStatesJSON: liveTaskStatesJSON,
	}, nil
}

func (p *TaskStatusProjection) DecodeStatus(input TaskStatusInput) (workflowTaskStatusFact, error) {
	if p == nil || p.projector == nil {
		return workflowTaskStatusFact{}, errors.New("task status projection is required")
	}
	return p.projector.DecodeStatus(input)
}

type taskStatusLiveState struct {
	TaskID             string `json:"task_id"`
	HasRunning         bool   `json:"has_running"`
	HasQueued          bool   `json:"has_queued"`
	WaitingQuestion    bool   `json:"waiting_question"`
	HasWaitingApproval bool   `json:"has_waiting_approval"`
}

func taskStatusLiveStatesJSON(observation workflowexecution.WorkflowTaskExecutionObservation) (string, error) {
	statesByTask := make(map[workflow.TaskID]taskStatusLiveState)
	for taskID, references := range observation.ConcurrencyQueued {
		if len(references) == 0 {
			continue
		}
		statesByTask[taskID] = taskStatusLiveState{
			TaskID:    string(taskID),
			HasQueued: true,
		}
	}
	for taskID, snapshot := range observation.Executions {
		if len(snapshot.Executions) == 0 {
			continue
		}
		state := statesByTask[taskID]
		state.TaskID = string(taskID)
		for _, execution := range snapshot.Executions {
			state.HasRunning = state.HasRunning || !execution.Queued
			state.HasQueued = state.HasQueued || execution.Queued
			state.WaitingQuestion = state.WaitingQuestion ||
				execution.HasPendingPromptKind(sessionruntime.PendingPromptKindQuestion)
			state.HasWaitingApproval = state.HasWaitingApproval ||
				execution.HasPendingPromptKind(sessionruntime.PendingPromptKindSessionApproval)
		}
		statesByTask[taskID] = state
	}
	states := make([]taskStatusLiveState, 0, len(statesByTask))
	for _, state := range statesByTask {
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].TaskID < states[j].TaskID })
	raw, err := json.Marshal(states)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (p *TaskStatusProjection) WithSnapshot(
	ctx context.Context,
	taskIDs []workflow.TaskID,
	operation func(TaskStatusObservation, *TaskStatusDurableSnapshot) error,
) error {
	if p == nil {
		return errors.New("task status projection is required")
	}
	if operation == nil {
		return errors.New("task status snapshot operation is required")
	}
	observation, err := p.Observe(taskIDs)
	if err != nil {
		return err
	}
	return p.WithDurableSnapshot(ctx, func(durable *TaskStatusDurableSnapshot) error {
		return operation(observation, durable)
	})
}

func (p *TaskStatusProjection) Project(
	ctx context.Context,
	observation TaskStatusObservation,
	durable *TaskStatusDurableSnapshot,
	taskIDs []workflow.TaskID,
) (map[workflow.TaskID]TaskStatusProjectionResult, error) {
	if p == nil {
		return nil, errors.New("task status projection is required")
	}
	if err := durable.validate(); err != nil {
		return nil, err
	}
	if !json.Valid([]byte(observation.LiveTaskStatesJSON)) {
		return nil, errors.New("task status observation live task states are malformed")
	}
	encodedTaskIDs, err := encodeTaskIDs(taskIDs)
	if err != nil {
		return nil, err
	}
	ids := encodedTaskIDs.values
	statuses, err := durable.projectedStatuses(ctx, encodedTaskIDs, observation.LiveTaskStatesJSON)
	if err != nil {
		return nil, err
	}
	currentNodesByTask, err := durable.CurrentNodesByTask(ctx, ids)
	if err != nil {
		return nil, err
	}
	tasksByID, err := durable.TasksByTask(ctx, ids)
	if err != nil {
		return nil, err
	}
	workflowIDs := make([]runtimeids.WorkflowID, 0, len(ids))
	workflowIDSet := make(map[runtimeids.WorkflowID]struct{}, len(ids))
	for _, taskID := range ids {
		task, exists := tasksByID[taskID]
		if !exists {
			return nil, fmt.Errorf("workflow task query omitted task %q", taskID)
		}
		workflowID := runtimeids.WorkflowID(task.WorkflowID)
		if _, exists := workflowIDSet[workflowID]; !exists {
			workflowIDSet[workflowID] = struct{}{}
			workflowIDs = append(workflowIDs, workflowID)
		}
	}
	definitions, err := durable.Definitions(ctx, workflowIDs)
	if err != nil {
		return nil, err
	}
	pendingApprovalsByTask, err := durable.PendingApprovalsByTask(ctx, ids)
	if err != nil {
		return nil, err
	}
	results := make(map[workflow.TaskID]TaskStatusProjectionResult, len(ids))
	for _, taskID := range ids {
		task := tasksByID[taskID]
		definition, exists := definitions[runtimeids.WorkflowID(task.WorkflowID)]
		if !exists {
			return nil, fmt.Errorf("workflow definition query omitted workflow %q", task.WorkflowID)
		}
		status, exists := statuses[taskID]
		if !exists {
			return nil, fmt.Errorf("workflow task status projection omitted task %q", taskID)
		}
		currentNodes, exists := currentNodesByTask[taskID]
		if !exists {
			return nil, fmt.Errorf("workflow current node projection omitted task %q", taskID)
		}
		quiescent, exists := observation.Live.Quiescence[taskID]
		if !exists {
			return nil, fmt.Errorf("workflow execution omitted Task %q from Quiescence snapshot", taskID)
		}
		liveExecutions := append([]sessionruntime.TaskExecution(nil), observation.Live.Executions[taskID].Executions...)
		concurrencyQueued := append(
			[]workflow.CurrentNodeReference(nil),
			observation.Live.ConcurrencyQueued[taskID]...,
		)
		pendingApprovals := pendingApprovalsByTask[taskID]
		pendingApprovalIDs := make([]string, 0, len(pendingApprovals))
		for _, approval := range pendingApprovals {
			if strings.TrimSpace(approval.ID) == "" {
				return nil, fmt.Errorf("workflow Task %q has a blank pending Transition Approval ID", taskID)
			}
			pendingApprovalIDs = append(pendingApprovalIDs, approval.ID)
		}
		sort.Strings(pendingApprovalIDs)
		facts := p.projector.ProjectTaskFacts(TaskFactsInput{
			Task:              task,
			Status:            status,
			CurrentNodes:      currentNodes,
			LiveExecutions:    liveExecutions,
			ConcurrencyQueued: concurrencyQueued,
			Definition:        definition,
			CanDelete:         quiescent,
		})
		results[taskID] = TaskStatusProjectionResult{
			Task:                         task,
			Definition:                   definition,
			Status:                       facts.Status,
			Done:                         facts.Done,
			CurrentNodes:                 append([]workflow.CurrentNode(nil), currentNodes...),
			LiveExecutions:               liveExecutions,
			PendingTransitionApprovalIDs: pendingApprovalIDs,
			Actions:                      facts.Actions,
			AttentionCount:               taskStatusAttentionCount(currentNodes, liveExecutions, len(pendingApprovalIDs)),
		}
	}
	return results, nil
}

func taskStatusAttentionCount(currentNodes []workflow.CurrentNode, executions []sessionruntime.TaskExecution, pendingApprovalCount int) int {
	count := pendingApprovalCount
	for _, currentNode := range currentNodes {
		if currentNode.Scheduling == nil || currentNode.Scheduling.Interruption == nil {
			continue
		}
		if workflow.IsActionableCurrentNodeInterruptionReason(currentNode.Scheduling.Interruption.Reason) {
			count++
		}
	}
	for _, execution := range executions {
		count += len(execution.PendingPrompts)
	}
	return count
}

// TaskStatusDurableSnapshot keeps all lifecycle-sensitive durable reads on
// one read-only SQLite transaction. Its methods fail after the owning
// TaskStatusProjection callback returns.
type TaskStatusDurableSnapshot struct {
	queries       *sqlitegen.Queries
	workflowStore *workflowstore.Store
	projector     *TaskProjector
	closed        bool
}

func (p *TaskStatusProjection) WithDurableSnapshot(
	ctx context.Context,
	operation func(*TaskStatusDurableSnapshot) error,
) (err error) {
	if p == nil {
		return errors.New("task status projection is required")
	}
	if operation == nil {
		return errors.New("task status durable snapshot operation is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tx, err := p.metadata.DB().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin task status durable snapshot: %w", err)
	}
	snapshot := &TaskStatusDurableSnapshot{
		queries:       p.metadata.Queries().WithTx(tx),
		workflowStore: p.workflowStore,
		projector:     p.projector,
	}
	defer func() {
		rollbackErr := tx.Rollback()
		snapshot.closed = true
		snapshot.queries = nil
		if err == nil && rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = fmt.Errorf("close task status durable snapshot: %w", rollbackErr)
		}
	}()
	return operation(snapshot)
}

func (s *TaskStatusDurableSnapshot) validate() error {
	if s == nil {
		return errors.New("task status durable snapshot is required")
	}
	if s.closed || s.queries == nil {
		return errors.New("task status durable snapshot is closed")
	}
	return nil
}

func (s *TaskStatusDurableSnapshot) Task(ctx context.Context, taskID string) (sqlitegen.TaskRecord, error) {
	if err := s.validate(); err != nil {
		return sqlitegen.TaskRecord{}, err
	}
	return s.queries.GetTask(ctx, taskID)
}

func (s *TaskStatusDurableSnapshot) TasksByTask(
	ctx context.Context,
	taskIDs []workflow.TaskID,
) (map[workflow.TaskID]sqlitegen.TaskRecord, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	encodedTaskIDs, err := encodeTaskIDs(taskIDs)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListTasksByIDs(ctx, encodedTaskIDs.json)
	if err != nil {
		return nil, err
	}
	requested := taskIDSet(encodedTaskIDs.values)
	tasks := make(map[workflow.TaskID]sqlitegen.TaskRecord, len(encodedTaskIDs.values))
	for _, row := range rows {
		taskID := workflow.TaskID(row.ID)
		if _, ok := requested[taskID]; !ok {
			return nil, fmt.Errorf("workflow task query returned unrequested task %q", taskID)
		}
		if _, duplicate := tasks[taskID]; duplicate {
			return nil, fmt.Errorf("workflow task query returned duplicate task %q", taskID)
		}
		tasks[taskID] = row
	}
	for _, taskID := range encodedTaskIDs.values {
		if _, exists := tasks[taskID]; !exists {
			return nil, fmt.Errorf("workflow task query omitted task %q", taskID)
		}
	}
	return tasks, nil
}

func (s *TaskStatusDurableSnapshot) BoardNodeTasks(
	ctx context.Context,
	params sqlitegen.ListBoardNodeTasksParams,
) ([]sqlitegen.ListBoardNodeTasksRow, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	return s.queries.ListBoardNodeTasks(ctx, params)
}

func (s *TaskStatusDurableSnapshot) ProjectedStatuses(
	ctx context.Context,
	taskIDs []workflow.TaskID,
	liveTaskStatesJSON string,
) (map[workflow.TaskID]workflowTaskStatusFact, error) {
	encodedTaskIDs, err := encodeTaskIDs(taskIDs)
	if err != nil {
		return nil, err
	}
	return s.projectedStatuses(ctx, encodedTaskIDs, liveTaskStatesJSON)
}

func (s *TaskStatusDurableSnapshot) projectedStatuses(
	ctx context.Context,
	taskIDs taskIDsEncoding,
	liveTaskStatesJSON string,
) (map[workflow.TaskID]workflowTaskStatusFact, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	if len(taskIDs.values) == 0 {
		return map[workflow.TaskID]workflowTaskStatusFact{}, nil
	}
	requested := taskIDSet(taskIDs.values)
	rows, err := s.queries.ListWorkflowTaskStatusProjectionByTasks(ctx, sqlitegen.ListWorkflowTaskStatusProjectionByTasksParams{
		TaskIdsJson:        taskIDs.json,
		LiveTaskStatesJson: liveTaskStatesJSON,
	})
	if err != nil {
		return nil, err
	}
	statuses := make(map[workflow.TaskID]workflowTaskStatusFact, len(taskIDs.values))
	for _, row := range rows {
		taskID := workflow.TaskID(row.TaskID)
		if _, ok := requested[taskID]; !ok {
			return nil, fmt.Errorf("workflow task status projection returned unrequested task %q", taskID)
		}
		if _, duplicate := statuses[taskID]; duplicate {
			return nil, fmt.Errorf("workflow task status projection returned duplicate task %q", taskID)
		}
		status, err := s.projector.DecodeStatus(TaskStatusInput{
			TaskID:             row.TaskID,
			Kind:               row.Kind,
			NodeIDsJSON:        row.NodeIdsJson,
			AttentionTypesJSON: row.AttentionTypesJson,
			Done:               row.IsDone != 0,
		})
		if err != nil {
			return nil, err
		}
		statuses[taskID] = status
	}
	for taskID := range requested {
		if _, ok := statuses[taskID]; !ok {
			return nil, fmt.Errorf("workflow task status projection omitted task %q", taskID)
		}
	}
	return statuses, nil
}

func (s *TaskStatusDurableSnapshot) PendingApprovalsByTask(
	ctx context.Context,
	taskIDs []workflow.TaskID,
) (map[workflow.TaskID][]sqlitegen.TaskPendingApproval, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	encodedTaskIDs, err := encodeTaskIDs(taskIDs)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListTaskPendingApprovalsByTasks(ctx, encodedTaskIDs.json)
	if err != nil {
		return nil, err
	}
	requested := taskIDSet(encodedTaskIDs.values)
	approvals := make(map[workflow.TaskID][]sqlitegen.TaskPendingApproval, len(encodedTaskIDs.values))
	for _, taskID := range encodedTaskIDs.values {
		approvals[taskID] = nil
	}
	for _, row := range rows {
		taskID := workflow.TaskID(row.SourceTaskID)
		if _, ok := requested[taskID]; !ok {
			return nil, fmt.Errorf("workflow pending approval query returned unrequested task %q", taskID)
		}
		approvals[taskID] = append(approvals[taskID], row)
	}
	return approvals, nil
}

func (s *TaskStatusDurableSnapshot) Definitions(
	ctx context.Context,
	workflowIDs []runtimeids.WorkflowID,
) (map[runtimeids.WorkflowID]definitionSnapshot, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	definitions := make(map[runtimeids.WorkflowID]definitionSnapshot, len(workflowIDs))
	for _, workflowID := range workflowIDs {
		if workflowID.IsZero() {
			return nil, errors.New("workflow id is required")
		}
		if _, exists := definitions[workflowID]; exists {
			continue
		}
		definition, err := s.Definition(ctx, workflowID)
		if err != nil {
			return nil, err
		}
		definitions[workflowID] = definition
	}
	return definitions, nil
}

func (s *TaskStatusDurableSnapshot) CurrentNodesByTask(
	ctx context.Context,
	taskIDs []workflow.TaskID,
) (map[workflow.TaskID][]workflow.CurrentNode, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	return s.workflowStore.ListCurrentNodesByTaskWithQueries(ctx, s.queries, taskIDs)
}

func (s *TaskStatusDurableSnapshot) Definition(
	ctx context.Context,
	workflowID runtimeids.WorkflowID,
) (definitionSnapshot, error) {
	if err := s.validate(); err != nil {
		return definitionSnapshot{}, err
	}
	domain, record, err := workflowstore.GetDefinitionWithQueries(ctx, s.queries, workflowID)
	if err != nil {
		return definitionSnapshot{}, err
	}
	api, nodeKinds := ProjectDefinition(domain, record, s.workflowStore.TargetAgentCatalog())
	return definitionSnapshot{domain: domain, api: api, nodeKinds: nodeKinds}, nil
}

type taskIDsEncoding struct {
	values []workflow.TaskID
	json   string
}

func encodeTaskIDs(taskIDs []workflow.TaskID) (taskIDsEncoding, error) {
	ids := make([]string, 0, len(taskIDs))
	values := make([]workflow.TaskID, 0, len(taskIDs))
	seen := make(map[workflow.TaskID]struct{}, len(taskIDs))
	for _, taskID := range taskIDs {
		if strings.TrimSpace(string(taskID)) == "" {
			return taskIDsEncoding{}, errors.New("workflow task id is required")
		}
		if _, exists := seen[taskID]; exists {
			return taskIDsEncoding{}, fmt.Errorf("workflow task id %q is duplicated", taskID)
		}
		seen[taskID] = struct{}{}
		values = append(values, taskID)
		ids = append(ids, string(taskID))
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return taskIDsEncoding{}, fmt.Errorf("encode workflow task ids: %w", err)
	}
	return taskIDsEncoding{values: values, json: string(raw)}, nil
}

func taskIDSet(taskIDs []workflow.TaskID) map[workflow.TaskID]struct{} {
	set := make(map[workflow.TaskID]struct{}, len(taskIDs))
	for _, taskID := range taskIDs {
		set[taskID] = struct{}{}
	}
	return set
}
