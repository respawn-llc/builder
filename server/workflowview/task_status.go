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

func loadWorkflowTaskStatusFact(ctx context.Context, queries *sqlitegen.Queries, projector *TaskProjector, taskID string) (workflowTaskStatusFact, error) {
	row, err := queries.GetWorkflowTaskStatusRecord(ctx, taskID)
	if err != nil {
		return workflowTaskStatusFact{}, err
	}
	return projector.DecodeStatus(TaskStatusInput{
		TaskID:             row.TaskID,
		Kind:               row.Kind,
		NodeIDsJSON:        row.NodeIdsJson,
		AttentionTypesJSON: row.AttentionTypesJson,
		Done:               row.IsDone != 0,
	})
}

func loadWorkflowTaskStatusFacts(ctx context.Context, queries *sqlitegen.Queries, projector *TaskProjector, taskIDs []string) (map[string]workflowTaskStatusFact, error) {
	if len(taskIDs) == 0 {
		return map[string]workflowTaskStatusFact{}, nil
	}
	rows, err := queries.ListWorkflowTaskStatusRecordsByTasks(ctx, taskIDs)
	if err != nil {
		return nil, err
	}
	statuses := make(map[string]workflowTaskStatusFact, len(taskIDs))
	for _, row := range rows {
		if _, exists := statuses[row.TaskID]; exists {
			return nil, fmt.Errorf("workflow task status projection returned duplicate task %q", row.TaskID)
		}
		status, err := projector.DecodeStatus(TaskStatusInput{
			TaskID:             row.TaskID,
			Kind:               row.Kind,
			NodeIDsJSON:        workflowTaskStatusProjectionString(row.TaskID, "node_ids_json", row.NodeIdsJson),
			AttentionTypesJSON: workflowTaskStatusProjectionString(row.TaskID, "attention_types_json", row.AttentionTypesJson),
			Done:               row.IsDone != 0,
		})
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

func workflowTaskStatusProjectionString(taskID string, field string, value any) string {
	encoded, ok := value.(string)
	if !ok {
		panic(fmt.Sprintf("workflow task status record for task %q has non-text %s", taskID, field))
	}
	return encoded
}
