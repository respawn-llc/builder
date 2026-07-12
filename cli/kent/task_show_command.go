package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"core/shared/config"
	"core/shared/serverapi"
)

type taskShowOutput struct {
	Summary         serverapi.WorkflowTaskSummary     `json:"summary"`
	Body            string                            `json:"body"`
	SourceURL       string                            `json:"source_url,omitempty"`
	Project         serverapi.ProjectBoardProject     `json:"project"`
	Workflow        serverapi.WorkflowPickerItem      `json:"workflow"`
	SourceWorkspace serverapi.ProjectWorkspaceSummary `json:"source_workspace"`
	ManagedWorktree *serverapi.WorktreeKentFacts      `json:"managed_worktree,omitempty"`
	Status          serverapi.WorkflowTaskStatus      `json:"status"`
	Actions         serverapi.WorkflowTaskActions     `json:"actions"`
	AttentionCount  int                               `json:"attention_count"`
	PlacementCount  int                               `json:"placement_count"`
	RunCount        int                               `json:"run_count"`
	TransitionCount int                               `json:"transition_count"`
	CommentCount    int                               `json:"comment_count"`
}

func taskShowSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" task show", stderr, taskShowUsage)
	projectRef := fs.String("project", ".", "project ID or attached workspace path used to resolve a short ID")
	jsonOut := fs.Bool("json", false, "write the complete task detail as JSON")
	positionals, flagArgs := takeLeadingPositionals(args, 1)
	if ok, exitCode := parseCommandFlags(fs, flagArgs); !ok {
		return exitCode
	}
	positionals = append(positionals, fs.Args()...)
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "task show requires <short-id-or-task-id>")
		return 2
	}
	cfg, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	requestedProjectID, task, err := getWorkflowTaskForShow(context.Background(), cfg, remote, *projectRef, positionals[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if requestedProjectID != "" && task.Summary.ProjectID != "" && task.Summary.ProjectID != requestedProjectID && task.Project.ProjectKey != "" {
		fmt.Fprintf(stderr, "Note: This task belongs to another project %s\n", task.Project.ProjectKey)
	}
	if *jsonOut {
		if _, err := taskStatusText(task.Status); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := json.NewEncoder(stdout).Encode(taskShowOutputFromDetail(task)); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if err := writeTaskDetail(stdout, task); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func taskShowOutputFromDetail(task serverapi.WorkflowTaskDetail) taskShowOutput {
	return taskShowOutput{
		Summary:         task.Summary,
		Body:            task.Body,
		SourceURL:       task.SourceURL,
		Project:         task.Project,
		Workflow:        task.Workflow,
		SourceWorkspace: task.SourceWorkspace,
		ManagedWorktree: task.ManagedWorktree,
		Status:          task.Status,
		Actions:         task.Actions,
		AttentionCount:  len(task.Attention),
		PlacementCount:  len(task.Placements),
		RunCount:        len(task.Runs),
		TransitionCount: len(task.Transitions),
		CommentCount:    len(task.Comments),
	}
}

func getWorkflowTaskForShow(ctx context.Context, cfg config.App, remote workflowCommandRemote, projectRef string, ref string) (string, serverapi.WorkflowTaskDetail, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return "", serverapi.WorkflowTaskDetail{}, errors.New("task id is required")
	}
	requestedProjectID := ""
	if resolved, err := resolveWorkflowProjectID(ctx, cfg, remote, projectRef); err == nil {
		requestedProjectID = resolved
	} else if !strings.HasPrefix(trimmed, "task-") {
		return "", serverapi.WorkflowTaskDetail{}, err
	}
	if strings.HasPrefix(trimmed, "task-") {
		detail, err := getWorkflowTaskByID(ctx, remote, trimmed)
		return requestedProjectID, detail, err
	}
	if requestedProjectID != "" {
		detail, err := getWorkflowTaskByProjectShortID(ctx, remote, requestedProjectID, trimmed)
		if err == nil {
			return requestedProjectID, detail, nil
		}
		if !isWorkflowTaskNotFound(err) {
			return requestedProjectID, serverapi.WorkflowTaskDetail{}, err
		}
	}
	detail, err := getWorkflowTaskByShortID(ctx, remote, trimmed)
	if err == nil {
		return requestedProjectID, detail, nil
	}
	if !isWorkflowTaskNotFound(err) {
		return requestedProjectID, serverapi.WorkflowTaskDetail{}, err
	}
	if requestedProjectID != "" {
		return requestedProjectID, serverapi.WorkflowTaskDetail{}, fmt.Errorf("task %q not found in project %s", trimmed, requestedProjectID)
	}
	return requestedProjectID, serverapi.WorkflowTaskDetail{}, fmt.Errorf("task %q not found", trimmed)
}
func writeTaskDetail(stdout io.Writer, task serverapi.WorkflowTaskDetail) error {
	statusText, err := taskStatusText(task.Status)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s: %s\n", task.Summary.ShortID, task.Summary.Title)
	fmt.Fprintln(stdout, "Body:")
	fmt.Fprintln(stdout, "```md")
	fmt.Fprintln(stdout, task.Body)
	fmt.Fprintln(stdout, "```")
	fmt.Fprintf(stdout, "Status: %s\n", statusText)
	fmt.Fprintf(stdout, "Project: %q (%s)\n", task.Project.DisplayName, task.Project.ProjectID)
	fmt.Fprintf(stdout, "Workflow: %q (%s)\n", task.Workflow.DisplayName, task.Workflow.WorkflowID)
	fmt.Fprintf(stdout, "Created at %s UTC\n", time.UnixMilli(task.Summary.CreatedAtUnixMs).UTC().Format(time.RFC3339))
	if len(task.Runs) > 0 {
		fmt.Fprintf(stdout, "Total agent runs: %d\n", len(task.Runs))
	}
	if strings.TrimSpace(task.SourceWorkspace.RootPath) != "" {
		fmt.Fprintf(stdout, "Main workspace: %s\n", task.SourceWorkspace.RootPath)
	}
	if task.ManagedWorktree != nil && strings.TrimSpace(task.ManagedWorktree.CanonicalRoot) != "" {
		fmt.Fprintf(stdout, "Worktree: %s\n", task.ManagedWorktree.CanonicalRoot)
	}
	if strings.TrimSpace(task.SourceURL) != "" {
		fmt.Fprintf(stdout, "Imported from: %s\n", task.SourceURL)
	}
	writeTaskDetailComments(stdout, task.Summary.ShortID, task.Comments)
	return nil
}

func taskStatusText(status serverapi.WorkflowTaskStatus) (string, error) {
	switch status.Kind {
	case serverapi.WorkflowTaskStatusKindCanceled, serverapi.WorkflowTaskStatusKindDone, serverapi.WorkflowTaskStatusKindWaitingQuestion, serverapi.WorkflowTaskStatusKindWaitingApproval, serverapi.WorkflowTaskStatusKindInterrupted, serverapi.WorkflowTaskStatusKindRunning, serverapi.WorkflowTaskStatusKindQueued, serverapi.WorkflowTaskStatusKindBacklog, serverapi.WorkflowTaskStatusKindActive:
		return string(status.Kind), nil
	default:
		return "", fmt.Errorf("unsupported task status %q", status.Kind)
	}
}
