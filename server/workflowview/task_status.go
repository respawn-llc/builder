package workflowview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/shared/serverapi"
)

type workflowTaskStatusFact struct {
	Status serverapi.WorkflowTaskStatus
	Done   bool
}

const workflowTaskStatusModelVersion = 2

type taskStatusAuthorityObservation struct {
	TaskID          string `json:"task_id"`
	RunID           string `json:"run_id"`
	Generation      int64  `json:"generation"`
	WaitingQuestion bool   `json:"waiting_question"`
}

type taskStatusCurrentRunFact struct {
	TaskID          string `json:"task_id"`
	RunID           string `json:"run_id"`
	Generation      int64  `json:"generation"`
	WaitingQuestion bool   `json:"waiting_question"`
}

type taskStatusProjectionArguments struct {
	authorityObservationsJSON string
	currentRunFactsJSON       string
	currentRuns               map[sessionruntime.WorkflowExecutionRef]taskStatusCurrentRunFact
}

func (s *TaskStatusSnapshot) canonicalStatusFact(ctx context.Context, projector *TaskProjector, taskID string) (workflowTaskStatusFact, error) {
	if s == nil || s.queries == nil {
		return workflowTaskStatusFact{}, errors.New("task status snapshot is required")
	}
	if projector == nil {
		return workflowTaskStatusFact{}, errors.New("task projector is required")
	}
	if strings.TrimSpace(taskID) == "" {
		return workflowTaskStatusFact{}, errors.New("workflow task id is required")
	}
	arguments, err := s.statusProjectionArguments()
	if err != nil {
		return workflowTaskStatusFact{}, err
	}
	row, err := s.queries.GetCanonicalWorkflowTaskStatusRecord(ctx, sqlitegen.GetCanonicalWorkflowTaskStatusRecordParams{
		TaskID:                    taskID,
		AuthorityObservationsJson: arguments.authorityObservationsJSON,
		CurrentRunFactsJson:       arguments.currentRunFactsJSON,
	})
	if err != nil {
		return workflowTaskStatusFact{}, err
	}
	return workflowTaskStatusFactFromValues(
		projector,
		row.TaskID,
		row.IsDone != 0,
		row.Kind,
		row.NodeIdsJson,
		row.RunIdsJson,
		row.AttentionTypesJson,
	)
}

func (s *TaskStatusSnapshot) canonicalStatusFacts(ctx context.Context, projector *TaskProjector, taskIDs []string) (map[string]workflowTaskStatusFact, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("task status snapshot is required")
	}
	if projector == nil {
		return nil, errors.New("task projector is required")
	}
	if len(taskIDs) == 0 {
		return map[string]workflowTaskStatusFact{}, nil
	}
	arguments, err := s.statusProjectionArguments()
	if err != nil {
		return nil, err
	}
	encodedTaskIDs, err := json.Marshal(taskIDs)
	if err != nil {
		return nil, fmt.Errorf("encode canonical task status task ids: %w", err)
	}
	rows, err := s.queries.ListCanonicalWorkflowTaskStatusRecordsByTasks(ctx, sqlitegen.ListCanonicalWorkflowTaskStatusRecordsByTasksParams{
		TaskIdsJson:               string(encodedTaskIDs),
		AuthorityObservationsJson: arguments.authorityObservationsJSON,
		CurrentRunFactsJson:       arguments.currentRunFactsJSON,
	})
	if err != nil {
		return nil, err
	}
	statuses := make(map[string]workflowTaskStatusFact, len(taskIDs))
	for _, row := range rows {
		if _, exists := statuses[row.TaskID]; exists {
			return nil, fmt.Errorf("canonical workflow task status projection returned duplicate task %q", row.TaskID)
		}
		status, err := workflowTaskStatusFactFromValues(
			projector,
			row.TaskID,
			row.IsDone != 0,
			row.Kind,
			row.NodeIdsJson,
			row.RunIdsJson,
			row.AttentionTypesJson,
		)
		if err != nil {
			return nil, err
		}
		statuses[row.TaskID] = status
	}
	for _, taskID := range taskIDs {
		if _, exists := statuses[taskID]; !exists {
			return nil, fmt.Errorf("canonical workflow task status projection omitted task %q", taskID)
		}
	}
	return statuses, nil
}

func (s *TaskStatusSnapshot) currentExecutionsByTask(taskIDs []string) (map[string][]sessionruntime.TaskExecution, error) {
	if s == nil {
		return nil, errors.New("task status snapshot is required")
	}
	arguments, err := s.statusProjectionArguments()
	if err != nil {
		return nil, err
	}
	wanted := make(map[workflow.TaskID]struct{}, len(taskIDs))
	current := make(map[string][]sessionruntime.TaskExecution, len(taskIDs))
	for _, taskID := range taskIDs {
		if strings.TrimSpace(taskID) == "" {
			return nil, errors.New("workflow task id is required")
		}
		domainTaskID := workflow.TaskID(taskID)
		if _, exists := wanted[domainTaskID]; exists {
			return nil, fmt.Errorf("workflow task id %q is duplicated", taskID)
		}
		wanted[domainTaskID] = struct{}{}
		current[taskID] = []sessionruntime.TaskExecution{}
	}
	for _, observed := range s.authority.Executions {
		execution := observed.Execution
		if _, exists := wanted[execution.Ref.TaskID]; !exists {
			continue
		}
		durable, exists := arguments.currentRuns[execution.Ref]
		if !exists {
			continue
		}
		execution.WaitingQuestion = execution.WaitingQuestion && durable.WaitingQuestion
		taskID := string(execution.Ref.TaskID)
		current[taskID] = append(current[taskID], execution)
	}
	for taskID, executions := range current {
		sort.Slice(executions, func(i, j int) bool {
			if executions[i].Ref.RunID != executions[j].Ref.RunID {
				return executions[i].Ref.RunID < executions[j].Ref.RunID
			}
			return executions[i].Ref.Generation < executions[j].Ref.Generation
		})
		current[taskID] = executions
	}
	return current, nil
}

func (s *TaskStatusSnapshot) statusProjectionArguments() (taskStatusProjectionArguments, error) {
	if s == nil {
		return taskStatusProjectionArguments{}, errors.New("task status snapshot is required")
	}
	authority := make([]taskStatusAuthorityObservation, 0, len(s.authority.Executions))
	for _, observed := range s.authority.Executions {
		execution := observed.Execution
		if err := execution.Ref.Validate(); err != nil {
			return taskStatusProjectionArguments{}, fmt.Errorf("canonical task status authority execution: %w", err)
		}
		authority = append(authority, taskStatusAuthorityObservation{
			TaskID:          string(execution.Ref.TaskID),
			RunID:           string(execution.Ref.RunID),
			Generation:      execution.Ref.Generation,
			WaitingQuestion: execution.WaitingQuestion,
		})
	}
	currentRuns := make(map[sessionruntime.WorkflowExecutionRef]taskStatusCurrentRunFact, len(s.currentRunFacts))
	currentRunFacts := make([]taskStatusCurrentRunFact, 0, len(s.currentRunFacts))
	for _, row := range s.currentRunFacts {
		fact, current, err := taskStatusCurrentRunFactFromAnchor(row)
		if err != nil {
			return taskStatusProjectionArguments{}, err
		}
		if !current {
			continue
		}
		ref := sessionruntime.WorkflowExecutionRef{
			TaskID:     workflow.TaskID(fact.TaskID),
			RunID:      workflow.RunID(fact.RunID),
			Generation: fact.Generation,
		}
		if _, exists := currentRuns[ref]; exists {
			return taskStatusProjectionArguments{}, fmt.Errorf("canonical task status snapshot has duplicate current run %q generation %d", fact.RunID, fact.Generation)
		}
		currentRuns[ref] = fact
		currentRunFacts = append(currentRunFacts, fact)
	}
	encodedAuthority, err := json.Marshal(authority)
	if err != nil {
		return taskStatusProjectionArguments{}, fmt.Errorf("encode canonical task status authority observations: %w", err)
	}
	encodedCurrentRunFacts, err := json.Marshal(currentRunFacts)
	if err != nil {
		return taskStatusProjectionArguments{}, fmt.Errorf("encode canonical task status current run facts: %w", err)
	}
	return taskStatusProjectionArguments{
		authorityObservationsJSON: string(encodedAuthority),
		currentRunFactsJSON:       string(encodedCurrentRunFacts),
		currentRuns:               currentRuns,
	}, nil
}

func taskStatusCurrentRunFactFromAnchor(row sqlitegen.AnchorWorkflowTaskStatusSnapshotRow) (taskStatusCurrentRunFact, bool, error) {
	if !row.DurableRunID.Valid {
		return taskStatusCurrentRunFact{}, false, nil
	}
	if !row.TaskID.Valid || !row.RunGeneration.Valid || !row.StartedAtUnixMs.Valid ||
		row.CompletedAtUnixMs.Valid || row.InterruptedAtUnixMs.Valid ||
		!row.PlacementState.Valid || row.PlacementState.String != "active" {
		return taskStatusCurrentRunFact{}, false, nil
	}
	fact := taskStatusCurrentRunFact{
		TaskID:          row.TaskID.String,
		RunID:           row.DurableRunID.String,
		Generation:      row.RunGeneration.Int64,
		WaitingQuestion: row.WaitingAskID.Valid,
	}
	ref := sessionruntime.WorkflowExecutionRef{
		TaskID:     workflow.TaskID(fact.TaskID),
		RunID:      workflow.RunID(fact.RunID),
		Generation: fact.Generation,
	}
	if err := ref.Validate(); err != nil {
		return taskStatusCurrentRunFact{}, false, fmt.Errorf("canonical task status current run fact: %w", err)
	}
	return fact, true, nil
}

func workflowTaskStatusFactFromValues(
	projector *TaskProjector,
	taskID string,
	done bool,
	kind string,
	nodeIDsJSON string,
	runIDsJSON string,
	attentionTypesJSON string,
) (workflowTaskStatusFact, error) {
	if projector == nil {
		return workflowTaskStatusFact{}, errors.New("task projector is required")
	}
	return projector.DecodeStatus(TaskStatusInput{
		TaskID:             taskID,
		Kind:               kind,
		NodeIDsJSON:        nodeIDsJSON,
		RunIDsJSON:         runIDsJSON,
		AttentionTypesJSON: attentionTypesJSON,
		Done:               done,
	})
}
