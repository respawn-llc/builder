package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type workflowTaskSelectorKind uint8

const (
	workflowTaskSelectorShortID workflowTaskSelectorKind = iota
	workflowTaskSelectorPersistentID
)

type workflowTaskSelector struct {
	kind  workflowTaskSelectorKind
	value string
}

func classifyWorkflowTaskSelector(raw string) workflowTaskSelector {
	if taskID, err := runtimeids.ParseCanonicalTaskID(raw); err == nil {
		return workflowTaskSelector{kind: workflowTaskSelectorPersistentID, value: taskID}
	}
	return workflowTaskSelector{kind: workflowTaskSelectorShortID, value: raw}
}

func workflowTaskList(ctx context.Context, remote workflowCommandRemote, req serverapi.WorkflowTaskListRequest) (serverapi.WorkflowTaskListResponse, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, workflowCommandTimeout)
	defer cancel()
	return remote.ListWorkflowTasks(rpcCtx, req)
}

func resolveWorkflowTaskID(ctx context.Context, cfg config.App, remote workflowCommandRemote, projectRef string, ref string) (string, error) {
	task, err := resolveWorkflowTask(ctx, cfg, remote, projectRef, ref)
	if err != nil {
		return "", err
	}
	return task.Summary.ID, nil
}

func resolveWorkflowTask(ctx context.Context, cfg config.App, remote workflowCommandRemote, projectRef string, ref string) (serverapi.WorkflowTaskDetail, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return serverapi.WorkflowTaskDetail{}, errors.New("task id is required")
	}
	selector := classifyWorkflowTaskSelector(trimmed)
	if selector.kind == workflowTaskSelectorPersistentID {
		detail, err := getWorkflowTaskByID(ctx, remote, selector.value)
		if err != nil && isWorkflowTaskNotFound(err) {
			return serverapi.WorkflowTaskDetail{}, fmt.Errorf("%s: %w", err, serverapi.ErrWorkflowTaskNotFound)
		}
		return detail, err
	}
	projectID, err := resolveWorkflowProjectID(ctx, cfg, remote, projectRef)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	detail, err := getWorkflowTaskByProjectShortID(ctx, remote, projectID, trimmed)
	if err != nil {
		if errors.Is(err, serverapi.ErrWorkflowTaskNotFound) || errors.Is(err, sql.ErrNoRows) {
			return serverapi.WorkflowTaskDetail{}, fmt.Errorf("task %q not found in project %s: %w", trimmed, projectID, serverapi.ErrWorkflowTaskNotFound)
		}
		return serverapi.WorkflowTaskDetail{}, err
	}
	return detail, nil
}

func isWorkflowTaskNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, serverapi.ErrWorkflowTaskNotFound)
}

func getWorkflowTaskByID(ctx context.Context, remote workflowCommandRemote, taskID string) (serverapi.WorkflowTaskDetail, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, workflowCommandTimeout)
	defer cancel()
	resp, err := remote.GetWorkflowTask(rpcCtx, serverapi.WorkflowTaskGetRequest{TaskID: taskID})
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	return resp.Task, nil
}

func getWorkflowTaskByProjectShortID(ctx context.Context, remote workflowCommandRemote, projectID string, shortID string) (serverapi.WorkflowTaskDetail, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, workflowCommandTimeout)
	defer cancel()
	resp, err := remote.GetWorkflowTask(rpcCtx, serverapi.WorkflowTaskGetRequest{ProjectID: projectID, ShortID: shortID})
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	return resp.Task, nil
}

func getWorkflowTaskByShortID(ctx context.Context, remote workflowCommandRemote, shortID string) (serverapi.WorkflowTaskDetail, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, workflowCommandTimeout)
	defer cancel()
	resp, err := remote.GetWorkflowTask(rpcCtx, serverapi.WorkflowTaskGetRequest{ShortID: shortID})
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	return resp.Task, nil
}
