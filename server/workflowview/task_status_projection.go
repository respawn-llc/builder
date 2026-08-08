package workflowview

import (
	"context"
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

type TaskStatusCaptureSource interface {
	CaptureWorkflowTaskExecutions(
		context.Context,
		[]workflow.TaskID,
		func(workflowexecution.WorkflowTaskExecutionObservation, *sqlitegen.Queries) error,
	) error
}

type TaskStatusLifecycleQuerySource interface {
	CaptureWorkflowTaskLifecycleQuery(
		context.Context,
		func(string, *sqlitegen.Queries) error,
	) error
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
	LiveSessionIDs               []string
	CurrentScripts               []serverapi.WorkflowTaskCurrentScript
	AttentionCount               int
}

type TaskStatusProjection struct {
	workflowStore *workflowstore.Store
	projector     *TaskProjector
	capture       TaskStatusCaptureSource
	queryCapture  TaskStatusLifecycleQuerySource
}

func NewTaskStatusProjection(
	metadataStore *metadata.Store,
	workflowStore *workflowstore.Store,
	projector *TaskProjector,
	capture TaskStatusCaptureSource,
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
	if capture == nil {
		return nil, errors.New("workflow Task lifecycle capture source is required")
	}
	return &TaskStatusProjection{
		workflowStore: workflowStore,
		projector:     projector,
		capture:       capture,
		queryCapture: func() TaskStatusLifecycleQuerySource {
			source, _ := capture.(TaskStatusLifecycleQuerySource)
			return source
		}(),
	}, nil
}

func (p *TaskStatusProjection) DecodeStatus(input TaskStatusInput) (workflowTaskStatusFact, error) {
	if p == nil || p.projector == nil {
		return workflowTaskStatusFact{}, errors.New("task status projection is required")
	}
	return p.projector.DecodeStatus(input)
}

type taskStatusLiveState struct {
	TaskID               string   `json:"task_id"`
	HasLifecycleOverride bool     `json:"has_lifecycle_override"`
	CurrentNodeIDs       []string `json:"current_node_ids"`
	HasRunning           bool     `json:"has_running"`
	HasQueued            bool     `json:"has_queued"`
	WaitingQuestion      bool     `json:"waiting_question"`
	HasWaitingApproval   bool     `json:"has_waiting_approval"`
}

func taskStatusLiveStatesJSON(observation workflowexecution.WorkflowTaskExecutionObservation) (string, error) {
	states := make([]taskStatusLiveState, 0, len(observation.Lifecycle))
	for taskID, lifecycle := range observation.Lifecycle {
		if taskID == "" {
			return "", errors.New("workflow lifecycle observation has a blank Task id")
		}
		currentNodeKeys := make(map[workflow.CurrentNodeReferenceKey]struct{}, len(lifecycle.CurrentNodes))
		nodeIDs := make(map[workflow.NodeID]struct{}, len(lifecycle.CurrentNodes))
		for _, currentNode := range lifecycle.CurrentNodes {
			if currentNode.Reference.TaskID != taskID {
				return "", fmt.Errorf("workflow lifecycle observation Current Node belongs to another Task: %v", currentNode.Reference)
			}
			key, err := currentNode.Reference.Key()
			if err != nil {
				return "", err
			}
			if _, duplicate := currentNodeKeys[key]; duplicate {
				return "", fmt.Errorf("workflow lifecycle observation contains duplicate Current Node %v", currentNode.Reference)
			}
			currentNodeKeys[key] = struct{}{}
			nodeIDs[currentNode.Reference.NodeID] = struct{}{}
		}
		if len(currentNodeKeys) == 0 {
			return "", fmt.Errorf("workflow lifecycle observation for Task %q has no Current Nodes", taskID)
		}
		exactScopes := make(map[runtimeids.ExecutionScopeID]workflow.CurrentNodeReferenceKey, len(lifecycle.ExactExecutions))
		for _, exact := range lifecycle.ExactExecutions {
			if exact.CurrentNode.TaskID != taskID {
				return "", fmt.Errorf("workflow lifecycle Exact execution belongs to another Task: %v", exact.CurrentNode)
			}
			key, err := exact.CurrentNode.Key()
			if err != nil {
				return "", err
			}
			if _, exists := currentNodeKeys[key]; !exists {
				return "", fmt.Errorf("workflow lifecycle Exact execution has no Current Node override: %v", exact.CurrentNode)
			}
			if exact.ScopeID.IsZero() {
				return "", fmt.Errorf("workflow lifecycle Exact execution for %v has no scope id", exact.CurrentNode)
			}
			if _, duplicate := exactScopes[exact.ScopeID]; duplicate {
				return "", fmt.Errorf("workflow lifecycle observation contains duplicate Exact execution %v", exact.CurrentNode)
			}
			exactScopes[exact.ScopeID] = key
		}
		state := taskStatusLiveState{
			TaskID:               string(taskID),
			HasLifecycleOverride: true,
			CurrentNodeIDs:       make([]string, 0, len(nodeIDs)),
		}
		for nodeID := range nodeIDs {
			state.CurrentNodeIDs = append(state.CurrentNodeIDs, string(nodeID))
		}
		sort.Strings(state.CurrentNodeIDs)
		for _, queued := range lifecycle.QueuedCurrentNodes {
			if queued.TaskID != taskID {
				return "", fmt.Errorf("workflow lifecycle queued Run belongs to another Task: %v", queued)
			}
			key, err := queued.Key()
			if err != nil {
				return "", err
			}
			if _, exists := currentNodeKeys[key]; !exists {
				return "", fmt.Errorf("workflow lifecycle queued Run has no Current Node override: %v", queued)
			}
		}
		matchedExact := make(map[runtimeids.ExecutionScopeID]struct{}, len(exactScopes))
		executionStatuses := make([]workflowstore.LifecycleTaskExecutionStatus, 0, len(exactScopes))
		for _, execution := range observation.Executions[taskID].Executions {
			key, err := execution.Ref.CurrentNode.Key()
			if err != nil {
				return "", err
			}
			exactKey, exact := exactScopes[execution.ScopeID]
			if !exact {
				continue
			}
			if exactKey != key {
				return "", fmt.Errorf(
					"workflow lifecycle Exact Scope %s references %v but Authority references %v",
					execution.ScopeID,
					lifecycle.ExactExecutions,
					execution.Ref.CurrentNode,
				)
			}
			matchedExact[execution.ScopeID] = struct{}{}
			executionStatuses = append(executionStatuses, workflowstore.LifecycleTaskExecutionStatus{
				CurrentNode:     execution.Ref.CurrentNode,
				WaitingQuestion: execution.HasPendingPromptKind(sessionruntime.PendingPromptKindQuestion),
				WaitingApproval: execution.HasPendingPromptKind(sessionruntime.PendingPromptKindSessionApproval),
			})
		}
		if len(matchedExact) != len(exactScopes) {
			return "", fmt.Errorf(
				"workflow lifecycle observation for Task %q has %d Exact facts but %d matching Authority executions",
				taskID,
				len(exactScopes),
				len(matchedExact),
			)
		}
		status, err := workflowstore.DeriveLifecycleTaskStatus(taskID, lifecycle.QueuedCurrentNodes, executionStatuses)
		if err != nil {
			return "", err
		}
		state.HasRunning = status.HasRunning
		state.HasQueued = status.HasQueued
		state.WaitingQuestion = status.WaitingQuestion
		state.HasWaitingApproval = status.WaitingApproval
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
	return p.capture.CaptureWorkflowTaskExecutions(
		ctx,
		taskIDs,
		func(live workflowexecution.WorkflowTaskExecutionObservation, queries *sqlitegen.Queries) error {
			liveTaskStatesJSON, err := taskStatusLiveStatesJSON(live)
			if err != nil {
				return err
			}
			durable := &TaskStatusDurableSnapshot{
				queries:       queries,
				workflowStore: p.workflowStore,
				projector:     p.projector,
			}
			defer func() {
				durable.closed = true
				durable.queries = nil
			}()
			return operation(TaskStatusObservation{
				Live:               live,
				LiveTaskStatesJSON: liveTaskStatesJSON,
			}, durable)
		},
	)
}

func (p *TaskStatusProjection) WithLifecycleQuery(
	ctx context.Context,
	operation func(string, *TaskStatusDurableSnapshot) error,
) error {
	if p == nil {
		return errors.New("task status projection is required")
	}
	if operation == nil {
		return errors.New("task status lifecycle query operation is required")
	}
	if p.queryCapture == nil {
		return errors.New("task status lifecycle query source is required")
	}
	return p.queryCapture.CaptureWorkflowTaskLifecycleQuery(
		ctx,
		func(token string, queries *sqlitegen.Queries) error {
			if strings.TrimSpace(token) == "" {
				return errors.New("task status lifecycle query token is required")
			}
			durable := &TaskStatusDurableSnapshot{
				queries:       queries,
				workflowStore: p.workflowStore,
				projector:     p.projector,
			}
			defer func() {
				durable.closed = true
				durable.queries = nil
			}()
			return operation(token, durable)
		},
	)
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
			if _, lifecycleOwned := observation.Live.Lifecycle[taskID]; lifecycleOwned {
				return nil, fmt.Errorf("workflow execution omitted lifecycle-owned Task %q from Quiescence snapshot", taskID)
			}
			quiescent = true
		}
		liveExecutions := append([]sessionruntime.TaskExecution(nil), observation.Live.Executions[taskID].Executions...)
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
			Task:           task,
			Status:         status,
			CurrentNodes:   currentNodes,
			LiveExecutions: liveExecutions,
			Definition:     definition,
			CanDelete:      quiescent,
		})
		liveSessionIDs, currentScripts, err := taskStatusLiveTargets(taskID, liveExecutions)
		if err != nil {
			return nil, err
		}
		results[taskID] = TaskStatusProjectionResult{
			Task:                         task,
			Definition:                   definition,
			Status:                       facts.Status,
			Done:                         facts.Done,
			CurrentNodes:                 append([]workflow.CurrentNode(nil), currentNodes...),
			LiveExecutions:               liveExecutions,
			PendingTransitionApprovalIDs: pendingApprovalIDs,
			Actions:                      facts.Actions,
			LiveSessionIDs:               liveSessionIDs,
			CurrentScripts:               currentScripts,
			AttentionCount:               taskStatusAttentionCount(currentNodes, liveExecutions, len(pendingApprovalIDs)),
		}
	}
	return results, nil
}

func taskStatusLiveTargets(taskID workflow.TaskID, executions []sessionruntime.TaskExecution) ([]string, []serverapi.WorkflowTaskCurrentScript, error) {
	sessionIDs := make([]string, 0, len(executions))
	scripts := make([]serverapi.WorkflowTaskCurrentScript, 0, len(executions))
	for _, execution := range executions {
		switch {
		case execution.Agent != nil:
			sessionIDs = append(sessionIDs, execution.Agent.SessionID.String())
		case execution.Script != nil:
			if strings.TrimSpace(execution.Script.Path) == "" {
				return nil, nil, fmt.Errorf("task %q live Script execution has a blank target path", taskID)
			}
			scripts = append(scripts, serverapi.WorkflowTaskCurrentScript{
				CurrentNode: workflowCurrentNodeReference(execution.Ref.CurrentNode),
				Path:        execution.Script.Path,
			})
		default:
			return nil, nil, fmt.Errorf("task %q live workflow execution has no target", taskID)
		}
	}
	sort.Strings(sessionIDs)
	sort.Slice(scripts, func(i, j int) bool {
		if scripts[i].CurrentNode.NodeID != scripts[j].CurrentNode.NodeID {
			return scripts[i].CurrentNode.NodeID < scripts[j].CurrentNode.NodeID
		}
		return optionalStringValue(scripts[i].CurrentNode.TransitionBranchKey) < optionalStringValue(scripts[j].CurrentNode.TransitionBranchKey)
	})
	return sessionIDs, scripts, nil
}

func optionalStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
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
	return workflowstore.ListCurrentNodesByTaskWithQueries(ctx, s.queries, taskIDs)
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
