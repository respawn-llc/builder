package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"core/shared/config"
	"core/shared/serverapi"
)

type taskShowOutput struct {
	Summary              serverapi.WorkflowTaskSummary         `json:"summary"`
	Body                 string                                `json:"body"`
	SourceURL            string                                `json:"source_url,omitempty"`
	Project              serverapi.ProjectBoardProject         `json:"project"`
	Workflow             serverapi.WorkflowTaskWorkflowSummary `json:"workflow"`
	SourceWorkspace      serverapi.ProjectWorkspaceSummary     `json:"source_workspace"`
	ExecutionTarget      *serverapi.WorkflowExecutionTarget    `json:"execution_target,omitempty"`
	WorktreePath         *string                               `json:"worktree_path"`
	CurrentNodes         []serverapi.WorkflowTaskCurrentNode   `json:"current_nodes"`
	LiveSessions         []serverapi.WorkflowTaskLiveSession   `json:"live_sessions"`
	CurrentScripts       []serverapi.WorkflowTaskCurrentScript `json:"current_scripts"`
	RetainedSessionCount int                                   `json:"retained_session_count"`
	Status               serverapi.WorkflowTaskStatus          `json:"status"`
	Actions              serverapi.WorkflowTaskActions         `json:"actions"`
	LabelIDs             []string                              `json:"label_ids"`
	AttentionCount       int                                   `json:"attention_count"`
	Dependencies         *taskShowDependencySummary            `json:"dependencies,omitempty"`
}

type taskShowDependencySummary struct {
	BlockerCount            int `json:"blocker_count"`
	UnsatisfiedBlockerCount int `json:"unsatisfied_blocker_count"`
	BlockedTaskCount        int `json:"blocked_task_count"`
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
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote workflowCommandRemote) int {
		requestedProjectID, task, err := getWorkflowTaskForShow(context.Background(), cfg, remote, *projectRef, positionals[0])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if requestedProjectID != "" && task.Summary.ProjectID != "" && task.Summary.ProjectID != requestedProjectID && task.Project.ProjectKey != "" {
			fmt.Fprintf(stderr, "Note: This task belongs to another project %s\n", task.Project.ProjectKey)
		}
		task, err = workflowTaskDetailForCLI(task)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOut {
			if _, err := taskStatusText(task.Status); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			return writeCommandJSON(stdout, stderr, taskShowOutputFromDetail(task))
		}
		labelNames, err := taskLabelNamesForHumanOutput(context.Background(), remote, task.Summary.ProjectID, task.LabelIDs)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := writeTaskDetailWithLabelNames(stdout, task, labelNames); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	})
}

func taskShowOutputFromDetail(task serverapi.WorkflowTaskDetail) taskShowOutput {
	output := taskShowOutput{
		Summary:              task.Summary,
		Body:                 task.Body,
		SourceURL:            task.SourceURL,
		Project:              task.Project,
		Workflow:             task.Workflow,
		SourceWorkspace:      task.SourceWorkspace,
		ExecutionTarget:      task.ExecutionTarget,
		WorktreePath:         task.WorktreePath,
		CurrentNodes:         task.CurrentNodes,
		LiveSessions:         task.LiveSessions,
		CurrentScripts:       task.CurrentScripts,
		RetainedSessionCount: task.RetainedSessionCount,
		Status:               task.Status,
		Actions:              task.Actions,
		LabelIDs:             normalizedLabelIDs(task.LabelIDs),
		AttentionCount:       task.AttentionCount,
	}
	if task.Dependencies.BlockerCount != 0 ||
		task.Dependencies.UnsatisfiedBlockerCount != 0 ||
		task.Dependencies.DirectlyBlockedTaskCount != 0 {
		output.Dependencies = &taskShowDependencySummary{
			BlockerCount:            task.Dependencies.BlockerCount,
			UnsatisfiedBlockerCount: task.Dependencies.UnsatisfiedBlockerCount,
			BlockedTaskCount:        task.Dependencies.DirectlyBlockedTaskCount,
		}
	}
	return output
}

func normalizedLabelIDs(ids []string) []string {
	normalized := make([]string, 0, len(ids))
	return append(normalized, ids...)
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
	return writeTaskDetailWithLabelNames(stdout, task, nil)
}

func writeTaskDetailWithLabelNames(stdout io.Writer, task serverapi.WorkflowTaskDetail, labelNames []string) error {
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
	fmt.Fprintf(stdout, "Project: %q (%s)\n", task.Project.DisplayName, task.Summary.ProjectID)
	fmt.Fprintf(stdout, "Workflow: %q (%s)\n", task.Workflow.DisplayName, task.Workflow.WorkflowID)
	fmt.Fprintf(stdout, "Created at %s UTC\n", time.UnixMilli(task.Summary.CreatedAtUnixMs).UTC().Format(time.RFC3339))
	if strings.TrimSpace(task.SourceWorkspace.RootPath) != "" {
		fmt.Fprintf(stdout, "Main workspace: %s\n", task.SourceWorkspace.RootPath)
	}
	if task.ExecutionTarget != nil {
		writeTaskExecutionTarget(stdout, *task.ExecutionTarget)
	}
	if task.WorktreePath != nil {
		fmt.Fprintf(stdout, "Worktree: %s\n", *task.WorktreePath)
	}
	for _, session := range task.LiveSessions {
		fmt.Fprintf(stdout, "Current session: %s\n", session.SessionID)
	}
	fmt.Fprintf(stdout, "Retained sessions: %d\n", task.RetainedSessionCount)
	for _, script := range task.CurrentScripts {
		fmt.Fprintf(stdout, "Current script: %s (%s)\n", script.Path, script.CurrentNode.NodeID)
	}
	for _, node := range task.CurrentNodes {
		if node.EffectiveAssignee != nil {
			fmt.Fprintf(stdout, "Current node %s effective assignee: %s\n", node.NodeID, *node.EffectiveAssignee)
		}
		if node.EffectiveThinking != nil {
			fmt.Fprintf(stdout, "Current node %s effective thinking: %s\n", node.NodeID, *node.EffectiveThinking)
		}
	}
	if strings.TrimSpace(task.SourceURL) != "" {
		fmt.Fprintf(stdout, "Imported from: %s\n", task.SourceURL)
	}
	if len(labelNames) > 0 {
		fmt.Fprintf(stdout, "Labels:")
		for _, name := range labelNames {
			fmt.Fprintf(stdout, " %q", name)
		}
		fmt.Fprintln(stdout)
	}
	return writeTaskDependencyDirections(stdout, task.Dependencies.Directions)
}

func taskLabelNamesForHumanOutput(ctx context.Context, remote workflowCommandRemote, projectID string, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	_, snapshot, err := loadWorkflowProjectLabelCatalog(ctx, remote, projectID)
	if err != nil {
		return nil, err
	}
	return workflowProjectLabelNames(snapshot, ids)
}

func writeTaskExecutionTarget(stdout io.Writer, target serverapi.WorkflowExecutionTarget) {
	fmt.Fprintf(stdout, "Execution target: %s\n", target.Mode)
	if target.RequestedRef != nil {
		fmt.Fprintf(stdout, "Requested revision: %s\n", *target.RequestedRef)
	}
	if target.ResolvedRef != nil {
		fmt.Fprintf(stdout, "Resolved revision: %s\n", *target.ResolvedRef)
	}
	if target.CommitOID != nil {
		label := "Resolved commit"
		if target.Provenance == serverapi.WorkflowExecutionTargetProvenanceLegacyObserved {
			label = "Observed commit (legacy)"
		}
		fmt.Fprintf(stdout, "%s: %s\n", label, shortCommitOID(*target.CommitOID))
	}
}

func shortCommitOID(commitOID string) string {
	const displayLength = 12
	trimmed := strings.TrimSpace(commitOID)
	if len(trimmed) <= displayLength {
		return trimmed
	}
	return trimmed[:displayLength]
}

func taskStatusText(status serverapi.WorkflowTaskStatus) (string, error) {
	switch status.Kind {
	case serverapi.WorkflowTaskStatusKindDone, serverapi.WorkflowTaskStatusKindWaitingQuestion, serverapi.WorkflowTaskStatusKindWaitingApproval, serverapi.WorkflowTaskStatusKindInterrupted, serverapi.WorkflowTaskStatusKindRunning, serverapi.WorkflowTaskStatusKindQueued, serverapi.WorkflowTaskStatusKindBacklog, serverapi.WorkflowTaskStatusKindActive:
		return string(status.Kind), nil
	default:
		return "", fmt.Errorf("unsupported task status %q", status.Kind)
	}
}
