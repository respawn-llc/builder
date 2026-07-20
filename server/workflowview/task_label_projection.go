package workflowview

import (
	"context"
	"fmt"

	"core/server/metadata/sqlitegen"
	"core/shared/serverapi"
)

type taskLabelAssignmentReader interface {
	ListTaskAssignedLabelIDsByTasks(context.Context, []string) ([]sqlitegen.ListTaskAssignedLabelIDsByTasksRow, error)
}

func loadTaskLabelIDsByTask(ctx context.Context, queries taskLabelAssignmentReader, taskIDs []string) (map[string][]string, error) {
	if len(taskIDs) > serverapi.WorkflowTaskListMaxPageSize {
		return nil, fmt.Errorf(
			"task label projection requires at most %d task ids, got %d",
			serverapi.WorkflowTaskListMaxPageSize,
			len(taskIDs),
		)
	}
	labelsByTaskID := make(map[string][]string, len(taskIDs))
	for _, taskID := range taskIDs {
		labelsByTaskID[taskID] = []string{}
	}
	if len(taskIDs) == 0 {
		return labelsByTaskID, nil
	}
	rows, err := queries.ListTaskAssignedLabelIDsByTasks(ctx, taskIDs)
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
		labelsByTaskID[row.TaskID] = append(labelIDs, row.LabelID)
	}
	return labelsByTaskID, nil
}
