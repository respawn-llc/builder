package main

import (
	"errors"
	"fmt"
	"strings"

	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type taskListOutput struct {
	ProjectID                   string                                                `json:"project_id"`
	WorkflowID                  *runtimeids.WorkflowID                                `json:"workflow_id,omitempty"`
	MatchingWorkflowCardinality serverapi.WorkflowTaskListMatchingWorkflowCardinality `json:"matching_workflow_cardinality"`
	NextOffset                  *int                                                  `json:"next_offset,omitempty"`
	Tasks                       []taskListItem                                        `json:"tasks"`
}

type taskListItem struct {
	ShortID         string                       `json:"short_id"`
	TaskID          string                       `json:"task_id"`
	WorkflowID      runtimeids.WorkflowID        `json:"workflow_id"`
	Status          serverapi.WorkflowTaskStatus `json:"status"`
	ColumnKeys      *[]string                    `json:"column_keys,omitempty"`
	Title           string                       `json:"title"`
	CreatedAtUnixMs int64                        `json:"created_at_unix_ms"`
	UpdatedAtUnixMs int64                        `json:"updated_at_unix_ms"`
	LabelIDs        []string                     `json:"label_ids"`
}

type taskListProjection struct {
	Output taskListOutput
	Rows   []taskListRenderItem
}

type taskListRenderItem struct {
	Item         taskListItem
	WorkflowName string
	LabelNames   []string
	ShowWorkflow bool
	ShowColumns  bool
}

type taskListExpectedScope struct {
	ProjectID     string
	WorkflowID    *runtimeids.WorkflowID
	WorkflowOwner taskListExpectedWorkflowOwner
}

type taskListExpectedWorkflowOwner uint8

const (
	taskListExpectedWorkflowFromRequest taskListExpectedWorkflowOwner = iota
	taskListExpectedWorkflowFromToken
)

func taskListProjectionFromResponse(resp serverapi.WorkflowTaskListResponse, expectedScope taskListExpectedScope) (taskListProjection, error) {
	if strings.TrimSpace(expectedScope.ProjectID) == "" || strings.TrimSpace(expectedScope.ProjectID) != expectedScope.ProjectID {
		return taskListProjection{}, errors.New("task list request scope is missing project_id")
	}
	if strings.TrimSpace(resp.Scope.ProjectID) == "" || strings.TrimSpace(resp.Scope.ProjectID) != resp.Scope.ProjectID {
		return taskListProjection{}, errors.New("task list response scope is missing project_id")
	}
	if resp.Scope.ProjectID != expectedScope.ProjectID {
		return taskListProjection{}, fmt.Errorf("task list response project %q does not match requested project %q", resp.Scope.ProjectID, expectedScope.ProjectID)
	}
	switch expectedScope.WorkflowOwner {
	case taskListExpectedWorkflowFromRequest:
		if (resp.Scope.WorkflowID == nil) != (expectedScope.WorkflowID == nil) {
			return taskListProjection{}, errors.New("task list response workflow scope does not match requested scope")
		}
		if resp.Scope.WorkflowID != nil && *resp.Scope.WorkflowID != *expectedScope.WorkflowID {
			return taskListProjection{}, fmt.Errorf("task list response workflow %q does not match requested workflow %q", *resp.Scope.WorkflowID, *expectedScope.WorkflowID)
		}
	case taskListExpectedWorkflowFromToken:
		if expectedScope.WorkflowID != nil {
			return taskListProjection{}, errors.New("task list token-owned workflow scope cannot include an explicit workflow_id")
		}
	default:
		return taskListProjection{}, errors.New("task list request has invalid workflow scope ownership")
	}
	switch resp.MatchingWorkflowCardinality {
	case serverapi.WorkflowTaskListMatchingWorkflowCardinalityNone,
		serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne,
		serverapi.WorkflowTaskListMatchingWorkflowCardinalityMultiple:
	default:
		return taskListProjection{}, fmt.Errorf("task list response has invalid matching_workflow_cardinality %q", resp.MatchingWorkflowCardinality)
	}
	if resp.MatchingWorkflowCardinality == serverapi.WorkflowTaskListMatchingWorkflowCardinalityNone && len(resp.Tasks) != 0 {
		return taskListProjection{}, errors.New("task list response with no matching workflows cannot contain tasks")
	}
	var selectedWorkflowID *runtimeids.WorkflowID
	if resp.Scope.WorkflowID != nil {
		workflowID := *resp.Scope.WorkflowID
		if workflowID.IsZero() {
			return taskListProjection{}, errors.New("workflow_id is required")
		}
		selectedWorkflowID = &workflowID
		if resp.MatchingWorkflowCardinality == serverapi.WorkflowTaskListMatchingWorkflowCardinalityMultiple {
			return taskListProjection{}, errors.New("task list response narrowed to one workflow cannot have multiple matching workflows")
		}
	}
	showWorkflow := resp.MatchingWorkflowCardinality == serverapi.WorkflowTaskListMatchingWorkflowCardinalityMultiple
	showColumns := selectedWorkflowID != nil
	items := make([]taskListItem, 0, len(resp.Tasks))
	rows := make([]taskListRenderItem, 0, len(resp.Tasks))
	var soleWorkflowID *runtimeids.WorkflowID
	for _, task := range resp.Tasks {
		workflowID := task.WorkflowID
		if workflowID.IsZero() {
			return taskListProjection{}, errors.New("workflow_id is required")
		}
		if selectedWorkflowID != nil && workflowID != *selectedWorkflowID {
			return taskListProjection{}, fmt.Errorf("task list response task %q workflow %q does not match selected workflow %q", task.TaskID, workflowID, *selectedWorkflowID)
		}
		if resp.MatchingWorkflowCardinality == serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne {
			if soleWorkflowID == nil {
				value := workflowID
				soleWorkflowID = &value
			} else if workflowID != *soleWorkflowID {
				return taskListProjection{}, fmt.Errorf("single-workflow task list response mixes workflows %q and %q", *soleWorkflowID, workflowID)
			}
		}
		expectedNativeState, validStatus := task.Status.Kind.NativeState()
		if !validStatus || task.Status.NativeState != expectedNativeState {
			return taskListProjection{}, fmt.Errorf("task list response task %q has invalid status %q/%q", task.TaskID, task.Status.Kind, task.Status.NativeState)
		}
		var columnKeys *[]string
		if showColumns {
			if task.ColumnKeys == nil {
				return taskListProjection{}, fmt.Errorf("narrowed task list response task %q is missing workflow-relative columns", task.TaskID)
			}
			values := append([]string(nil), (*task.ColumnKeys)...)
			columnKeys = &values
		} else if task.ColumnKeys != nil {
			return taskListProjection{}, fmt.Errorf("project-wide task list response task %q contains workflow-relative columns", task.TaskID)
		}
		workflowName := ""
		if showWorkflow {
			if task.WorkflowName == nil || strings.TrimSpace(*task.WorkflowName) == "" || strings.TrimSpace(*task.WorkflowName) != *task.WorkflowName {
				return taskListProjection{}, fmt.Errorf("task list response task %q is missing an exact workflow_name for multiple-workflow rendering", task.TaskID)
			}
			workflowName = *task.WorkflowName
		}
		labelIDs := make([]string, len(task.Labels))
		labelNames := make([]string, len(task.Labels))
		for index, label := range task.Labels {
			labelIDs[index], labelNames[index] = label.ID, label.Name
		}
		item := taskListItem{
			ShortID:         task.ShortID,
			TaskID:          task.TaskID,
			WorkflowID:      workflowID,
			Status:          task.Status,
			ColumnKeys:      columnKeys,
			Title:           task.Title,
			CreatedAtUnixMs: task.CreatedAtUnixMs,
			UpdatedAtUnixMs: task.UpdatedAtUnixMs,
			LabelIDs:        labelIDs,
		}
		items = append(items, item)
		rows = append(rows, taskListRenderItem{
			Item:         item,
			WorkflowName: workflowName,
			LabelNames:   labelNames,
			ShowWorkflow: showWorkflow,
			ShowColumns:  showColumns,
		})
	}
	return taskListProjection{
		Output: taskListOutput{
			ProjectID:                   resp.Scope.ProjectID,
			WorkflowID:                  selectedWorkflowID,
			MatchingWorkflowCardinality: resp.MatchingWorkflowCardinality,
			NextOffset:                  resp.NextOffset,
			Tasks:                       items,
		},
		Rows: rows,
	}, nil
}
