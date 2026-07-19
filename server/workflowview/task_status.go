package workflowview

import (
	"context"
	"fmt"

	"core/server/metadata/sqlitegen"
	"core/shared/serverapi"
)

type workflowTaskStatusFact struct {
	Status serverapi.WorkflowTaskStatus
	Done   bool
}

func (s *Service) taskStatusFact(ctx context.Context, taskID string) (workflowTaskStatusFact, error) {
	row, err := s.queries.GetWorkflowTaskStatusRecord(ctx, taskID)
	if err != nil {
		return workflowTaskStatusFact{}, err
	}
	return s.workflowTaskStatusFactFromRecord(row)
}

func (s *Service) taskStatusFacts(ctx context.Context, taskIDs []string) (map[string]workflowTaskStatusFact, error) {
	if len(taskIDs) == 0 {
		return map[string]workflowTaskStatusFact{}, nil
	}
	rows, err := s.queries.ListWorkflowTaskStatusRecordsByTasks(ctx, taskIDs)
	if err != nil {
		return nil, err
	}
	statuses := make(map[string]workflowTaskStatusFact, len(taskIDs))
	for _, row := range rows {
		if _, exists := statuses[row.TaskID]; exists {
			return nil, fmt.Errorf("workflow task status projection returned duplicate task %q", row.TaskID)
		}
		status, err := s.workflowTaskStatusFactFromRecord(row)
		if err != nil {
			return nil, err
		}
		statuses[row.TaskID] = status
	}
	for _, taskID := range taskIDs {
		if _, exists := statuses[taskID]; !exists {
			return nil, fmt.Errorf("workflow task status projection omitted task %q", taskID)
		}
	}
	return statuses, nil
}

func (s *Service) workflowTaskStatusFactFromRecord(row sqlitegen.WorkflowTaskStatusRecord) (workflowTaskStatusFact, error) {
	nodeIDsJSON, err := workflowTaskStatusProjectionJSON(row.TaskID, "node_ids_json", row.NodeIdsJson)
	if err != nil {
		return workflowTaskStatusFact{}, err
	}
	runIDsJSON, err := workflowTaskStatusProjectionJSON(row.TaskID, "run_ids_json", row.RunIdsJson)
	if err != nil {
		return workflowTaskStatusFact{}, err
	}
	attentionTypesJSON, err := workflowTaskStatusProjectionJSON(row.TaskID, "attention_types_json", row.AttentionTypesJson)
	if err != nil {
		return workflowTaskStatusFact{}, err
	}
	return s.projector.DecodeStatus(TaskStatusInput{
		TaskID:             row.TaskID,
		Kind:               row.Kind,
		NodeIDsJSON:        nodeIDsJSON,
		RunIDsJSON:         runIDsJSON,
		AttentionTypesJSON: attentionTypesJSON,
		Done:               row.IsDone != 0,
	})
}

func workflowTaskStatusProjectionJSON(taskID string, field string, value any) (string, error) {
	encoded, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("workflow task status record for task %q has non-text %s", taskID, field)
	}
	return encoded, nil
}
