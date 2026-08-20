package workflowview

import (
	"context"
	"fmt"

	"core/server/metadata/sqlitegen"
	"core/shared/serverapi"
)

type taskLabelAssignmentReader interface {
	ListTaskAssignedLabelsByTasks(context.Context, []string) ([]sqlitegen.ListTaskAssignedLabelsByTasksRow, error)
}

func loadTaskLabelsByTask(ctx context.Context, queries taskLabelAssignmentReader, taskIDs []string) (map[string][]serverapi.WorkflowProjectLabel, error) {
	if len(taskIDs) > serverapi.WorkflowPaginationMaxLimit {
		return nil, fmt.Errorf(
			"task label projection requires at most %d task ids, got %d",
			serverapi.WorkflowPaginationMaxLimit,
			len(taskIDs),
		)
	}
	labelsByTaskID := make(map[string][]serverapi.WorkflowProjectLabel, len(taskIDs))
	for _, taskID := range taskIDs {
		labelsByTaskID[taskID] = []serverapi.WorkflowProjectLabel{}
	}
	if len(taskIDs) == 0 {
		return labelsByTaskID, nil
	}
	rows, err := queries.ListTaskAssignedLabelsByTasks(ctx, taskIDs)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		labelIDs, ok := labelsByTaskID[row.TaskID]
		if !ok {
			return nil, fmt.Errorf(
				"task label projection returned unrequested task_id %q",
				row.TaskID,
			)
		}
		labelsByTaskID[row.TaskID] = append(labelIDs, serverapi.WorkflowProjectLabel{
			ID:   row.LabelID,
			Name: row.LabelName,
		})
	}
	return labelsByTaskID, nil
}

func taskLabelIDsByTask(labelsByTaskID map[string][]serverapi.WorkflowProjectLabel) map[string][]string {
	labelIDsByTaskID := make(map[string][]string, len(labelsByTaskID))
	for taskID, labels := range labelsByTaskID {
		labelIDs := make([]string, 0, len(labels))
		for _, label := range labels {
			labelIDs = append(labelIDs, label.ID)
		}
		labelIDsByTaskID[taskID] = labelIDs
	}
	return labelIDsByTaskID
}
