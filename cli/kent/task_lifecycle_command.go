package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"core/shared/config"
	"core/shared/serverapi"
)

var (
	taskStartSessionPollTimeout  = 7 * time.Second
	taskStartSessionPollInterval = 200 * time.Millisecond
)

func taskCreateSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" task create", stderr, taskCreateUsage)
	title := fs.String("title", "", "task title")
	body := fs.String("body", "", "task body")
	bodyFile := fs.String("body-file", "", "read the task body from this file")
	workflowRef := fs.String("workflow", "", "workflow ID or exact name; defaults to the project's default workflow")
	projectRef := fs.String("project", ".", "project ID or attached workspace path")
	sourceURL := fs.String("source-url", "", "URL of the issue or document that originated the task")
	sourceWorkspace := fs.String("source-workspace", "", "workspace ID or path used as the task's source checkout")
	jsonOut := fs.Bool("json", false, "write the created task detail as JSON")
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(stderr, "task create does not accept positional arguments")
		return 2
	}
	taskBody, err := readTaskBodyFlag(*body, *bodyFile)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	cfg, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	projectID, err := resolveWorkflowProjectID(context.Background(), cfg, remote, *projectRef)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	workflowID := ""
	if strings.TrimSpace(*workflowRef) != "" {
		workflowID, err = resolveWorkflowID(context.Background(), remote, *workflowRef)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	sourceWorkspaceID := ""
	if strings.TrimSpace(*sourceWorkspace) != "" {
		sourceWorkspaceID, err = resolveWorkflowSourceWorkspaceID(context.Background(), cfg, remote, *sourceWorkspace)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
	defer cancel()
	resp, err := remote.CreateWorkflowTask(ctx, serverapi.WorkflowTaskCreateRequest{ProjectID: projectID, WorkflowID: workflowID, Title: *title, Body: taskBody, SourceURL: *sourceURL, SourceWorkspaceID: sourceWorkspaceID})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	task, err := getWorkflowTaskByID(context.Background(), remote, resp.Task.ID)
	if err != nil {
		fmt.Fprintf(stderr, "created task %s but failed to load task detail for output: %v\n", resp.Task.ID, err)
		return 1
	}
	if *jsonOut {
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

func taskEditSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" task edit", stderr, taskEditUsage)
	title := fs.String("title", "", "replace the task title")
	body := fs.String("body", "", "replace the task body; pass an empty value to clear")
	bodyFile := fs.String("body-file", "", "replace the task body with this file; an empty file clears it")
	sourceWorkspace := fs.String("source-workspace", "", "replace the source workspace with this workspace ID or path")
	projectRef := fs.String("project", ".", "project ID or attached workspace path used to resolve a short ID")
	jsonOut := fs.Bool("json", false, "write the updated task as JSON")
	positionals, flagArgs := takeLeadingPositionals(args, 1)
	if ok, exitCode := parseCommandFlags(fs, flagArgs); !ok {
		return exitCode
	}
	positionals = append(positionals, fs.Args()...)
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "task edit requires <short-id-or-task-id>")
		return 2
	}
	titleProvided := flagWasProvided(fs, "title")
	bodyProvided := flagWasProvided(fs, "body")
	bodyFileProvided := flagWasProvided(fs, "body-file")
	workspaceProvided := flagWasProvided(fs, "source-workspace")
	if !titleProvided && !bodyProvided && !bodyFileProvided && !workspaceProvided {
		fmt.Fprintln(stderr, "task edit requires at least one of --title, --body, --body-file, or --source-workspace")
		return 2
	}
	if bodyProvided && bodyFileProvided {
		fmt.Fprintln(stderr, "--body cannot be combined with --body-file")
		return 2
	}
	cfg, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	taskID, err := resolveWorkflowTaskID(context.Background(), cfg, remote, *projectRef, positionals[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// Send title only when provided; omitting it leaves the persisted title unchanged.
	req := serverapi.WorkflowTaskUpdateRequest{TaskID: taskID}
	if titleProvided {
		req.Title = title
	}
	if bodyProvided || bodyFileProvided {
		newBody, err := readTaskEditBody(*body, *bodyFile, bodyFileProvided)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		req.Body = &newBody
	}
	if workspaceProvided {
		workspaceID, err := resolveWorkflowSourceWorkspaceID(context.Background(), cfg, remote, *sourceWorkspace)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		req.SourceWorkspaceID = workspaceID
	}
	ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
	defer cancel()
	resp, err := remote.UpdateWorkflowTask(ctx, req)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
		if err := json.NewEncoder(stdout).Encode(resp); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "Edited task %s.\n", taskSummaryDisplayID(resp.Task))
	return 0
}

// readTaskEditBody reads the replacement body for task edit. Unlike task create,
// an empty value is allowed (it clears the body) since the caller opted into a
// body change by passing the flag.
func readTaskEditBody(body string, bodyFile string, bodyFileProvided bool) (string, error) {
	if bodyFileProvided {
		content, err := os.ReadFile(bodyFile)
		if err != nil {
			return "", fmt.Errorf("read --body-file: %w", err)
		}
		return string(content), nil
	}
	return body, nil
}

func taskStartSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" task start", stderr, taskStartUsage)
	projectRef := fs.String("project", ".", "project ID or attached workspace path used to resolve a short ID")
	positionals, flagArgs := takeLeadingPositionals(args, 1)
	if ok, exitCode := parseCommandFlags(fs, flagArgs); !ok {
		return exitCode
	}
	positionals = append(positionals, fs.Args()...)
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "task start requires <short-id-or-task-id>")
		return 2
	}
	if denyAgentHumanOnlyTaskAction(stderr) {
		return 1
	}
	cfg, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	taskID, err := resolveWorkflowTaskID(context.Background(), cfg, remote, *projectRef, positionals[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	resp, err := runWorkflowMutationWithSetupProgress(context.Background(), remote, stderr, func(ctx context.Context, setupOperationID serverapi.WorktreeSetupOperationID) (serverapi.WorkflowTaskStartResponse, error) {
		return remote.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{SetupOperationID: setupOperationID, TaskID: taskID})
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	detail, err := waitForWorkflowTaskRunSession(context.Background(), remote, taskID, resp.RunID, taskStartSessionPollTimeout, taskStartSessionPollInterval)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	writeTaskStartResult(stdout, detail, resp)
	return 0
}

func taskCancelSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" task cancel", stderr, taskCancelUsage)
	projectRef := fs.String("project", ".", "project ID or attached workspace path used to resolve a short ID")
	reason := fs.String("reason", "", "reason recorded with the cancellation")
	positionals, flagArgs := takeLeadingPositionals(args, 1)
	if ok, exitCode := parseCommandFlags(fs, flagArgs); !ok {
		return exitCode
	}
	positionals = append(positionals, fs.Args()...)
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "task cancel requires <short-id-or-task-id>")
		return 2
	}
	if denyAgentHumanOnlyTaskAction(stderr) {
		return 1
	}
	cfg, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	taskID, err := resolveWorkflowTaskID(context.Background(), cfg, remote, *projectRef, positionals[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
	defer cancel()
	if err := remote.CancelWorkflowTask(ctx, serverapi.WorkflowTaskCancelRequest{TaskID: taskID, Reason: *reason}); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	detail, err := getWorkflowTaskByID(context.Background(), remote, taskID)
	if err != nil {
		fmt.Fprintf(stderr, "canceled task %s but failed to load task detail for output: %v\n", taskID, err)
		return 1
	}
	fmt.Fprintf(stdout, "Canceled task %s.\n", taskDisplayID(detail))
	return 0
}

func taskDeleteSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" task delete", stderr, taskDeleteUsage)
	projectRef := fs.String("project", ".", "project ID or attached workspace path used to resolve a short ID")
	positionals, flagArgs := takeLeadingPositionals(args, 1)
	if ok, exitCode := parseCommandFlags(fs, flagArgs); !ok {
		return exitCode
	}
	positionals = append(positionals, fs.Args()...)
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "task delete requires <short-id-or-task-id>")
		return 2
	}
	if denyAgentHumanOnlyTaskAction(stderr) {
		return 1
	}
	cfg, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	taskID, err := resolveWorkflowTaskID(context.Background(), cfg, remote, *projectRef, positionals[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// Load the task detail before deletion so we can report a stable display id;
	// the task no longer exists once the delete RPC succeeds.
	displayID := taskID
	if detail, err := getWorkflowTaskByID(context.Background(), remote, taskID); err == nil {
		displayID = taskDisplayID(detail)
	}
	ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
	defer cancel()
	if err := remote.DeleteWorkflowTask(ctx, serverapi.WorkflowTaskDeleteRequest{TaskID: taskID}); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Deleted task %s.\n", displayID)
	return 0
}

func taskResumeSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" task resume", stderr, taskResumeUsage)
	projectRef := fs.String("project", ".", "project ID or attached workspace path used to resolve a short ID")
	positionals, flagArgs := takeLeadingPositionals(args, 1)
	if ok, exitCode := parseCommandFlags(fs, flagArgs); !ok {
		return exitCode
	}
	positionals = append(positionals, fs.Args()...)
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "task resume requires <short-id-or-task-id>")
		return 2
	}
	if denyAgentHumanOnlyTaskAction(stderr) {
		return 1
	}
	cfg, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	taskID, err := resolveWorkflowTaskID(context.Background(), cfg, remote, *projectRef, positionals[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
	defer cancel()
	resp, err := remote.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{TaskID: taskID})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	detail, err := getWorkflowTaskByID(context.Background(), remote, taskID)
	if err != nil {
		fmt.Fprintf(stderr, "resumed task %s but failed to load task detail for output: %v\n", taskID, err)
		return 1
	}
	writeTaskResumeResult(stdout, detail, resp)
	return 0
}

func taskApproveSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" task approve", stderr, taskApproveUsage)
	positionals, flagArgs := takeLeadingPositionals(args, 1)
	if ok, exitCode := parseCommandFlags(fs, flagArgs); !ok {
		return exitCode
	}
	positionals = append(positionals, fs.Args()...)
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "task approve requires <transition-id>")
		return 2
	}
	if denyAgentHumanOnlyTaskAction(stderr) {
		return 1
	}
	_, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	resp, err := runWorkflowMutationWithSetupProgress(context.Background(), remote, stderr, func(ctx context.Context, setupOperationID serverapi.WorktreeSetupOperationID) (serverapi.WorkflowTaskApproveResponse, error) {
		return remote.ApproveWorkflowTask(ctx, serverapi.WorkflowTaskApproveRequest{SetupOperationID: setupOperationID, TransitionID: positionals[0]})
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if strings.TrimSpace(resp.TaskID) == "" {
		fmt.Fprintf(stderr, "approved transition %s but response did not include task id for output\n", resp.TransitionID)
		return 1
	}
	detail, err := getWorkflowTaskByID(context.Background(), remote, resp.TaskID)
	if err != nil {
		fmt.Fprintf(stderr, "approved transition %s but failed to load task detail for output: %v\n", resp.TransitionID, err)
		return 1
	}
	writeTaskTransitionResult(stdout, "Approved transition of", detail, resp.TransitionID, resp.RunIDs)
	return 0
}

func taskMoveSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" task move", stderr, taskMoveUsage)
	projectRef := fs.String("project", ".", "project ID or attached workspace path used to resolve a short ID")
	commentary := fs.String("commentary", "", "note recorded with the workflow transition")
	outputs := stringMapFlag{}
	fs.Var(&outputs, "output", "transition value as name=value; repeatable")
	positionals, flagArgs := takeLeadingPositionals(args, 2)
	if ok, exitCode := parseCommandFlags(fs, flagArgs); !ok {
		return exitCode
	}
	positionals = append(positionals, fs.Args()...)
	if len(positionals) != 2 {
		fmt.Fprintln(stderr, "task move requires <short-id-or-task-id> <target-node-id>")
		return 2
	}
	if denyAgentHumanOnlyTaskAction(stderr) {
		return 1
	}
	cfg, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	taskID, err := resolveWorkflowTaskID(context.Background(), cfg, remote, *projectRef, positionals[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	resp, err := runWorkflowMutationWithSetupProgress(context.Background(), remote, stderr, func(ctx context.Context, setupOperationID serverapi.WorktreeSetupOperationID) (serverapi.WorkflowTaskMoveResponse, error) {
		return remote.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{SetupOperationID: setupOperationID, TaskID: taskID, TargetNodeID: positionals[1], OutputValues: outputs.values, Commentary: *commentary})
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	detail, err := getWorkflowTaskByID(context.Background(), remote, taskID)
	if err != nil {
		fmt.Fprintf(stderr, "moved task %s but failed to load task detail for output: %v\n", taskID, err)
		return 1
	}
	writeTaskTransitionResult(stdout, "Moved task", detail, resp.TransitionID, resp.RunIDs)
	return 0
}

type worktreeSetupProgressSubscriber interface {
	SubscribeWorktreeSetup(context.Context, serverapi.WorktreeSetupSubscribeRequest) (serverapi.WorktreeSetupSubscription, error)
}

func runWorkflowMutationWithSetupProgress[T any](ctx context.Context, remote workflowCommandRemote, stderr io.Writer, mutate func(context.Context, serverapi.WorktreeSetupOperationID) (T, error)) (T, error) {
	setupOperationID := serverapi.NewWorktreeSetupOperationID()
	stopSetupProgress, err := subscribeWorktreeSetupProgress(ctx, remote, setupOperationID, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "warning: worktree setup progress subscription unavailable: %v\n", err)
		stopSetupProgress = func() error { return nil }
	}
	resp, mutateErr := mutate(ctx, setupOperationID)
	if setupProgressErr := stopSetupProgress(); setupProgressErr != nil {
		fmt.Fprintf(stderr, "warning: worktree setup progress stream ended unexpectedly: %v\n", setupProgressErr)
	}
	return resp, mutateErr
}

func subscribeWorktreeSetupProgress(ctx context.Context, remote workflowCommandRemote, setupOperationID serverapi.WorktreeSetupOperationID, stderr io.Writer) (func() error, error) {
	subscriber, ok := remote.(worktreeSetupProgressSubscriber)
	if !ok {
		return nil, errors.New("worktree setup progress subscription is unavailable")
	}
	subscription, err := subscriber.SubscribeWorktreeSetup(ctx, serverapi.WorktreeSetupSubscribeRequest{SetupOperationID: setupOperationID})
	if err != nil {
		return nil, err
	}
	progressCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		defer func() { _ = subscription.Close() }()
		for {
			event, err := subscription.Next(progressCtx)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(progressCtx.Err(), context.Canceled) || errors.Is(err, io.EOF) {
					done <- nil
					return
				}
				done <- err
				return
			}
			writeWorktreeSetupProgress(stderr, event)
			if event.Phase == serverapi.WorktreeSetupPhaseCompleted || event.Phase == serverapi.WorktreeSetupPhaseFailed {
				done <- nil
				return
			}
		}
	}()
	return func() error {
		cancel()
		return <-done
	}, nil
}

func writeWorktreeSetupProgress(stderr io.Writer, event serverapi.WorktreeSetupEvent) {
	if event.Phase != serverapi.WorktreeSetupPhaseStarted {
		return
	}
	fmt.Fprintf(stderr, "Waiting for worktree setup script %s in %s.\n", event.ScriptPath, event.WorktreeRoot)
}

type stringMapFlag struct {
	values map[string]string
}

func (f *stringMapFlag) String() string {
	if f == nil || len(f.values) == 0 {
		return ""
	}
	return fmt.Sprintf("%v", f.values)
}

func (f *stringMapFlag) Set(raw string) error {
	name, value, ok := strings.Cut(raw, "=")
	name = strings.TrimSpace(name)
	if !ok || name == "" {
		return fmt.Errorf("output must be name=value")
	}
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[name] = value
	return nil
}

func waitForWorkflowTaskRunSession(ctx context.Context, remote workflowCommandRemote, taskID string, runID string, timeout time.Duration, interval time.Duration) (serverapi.WorkflowTaskDetail, error) {
	if strings.TrimSpace(taskID) == "" {
		return serverapi.WorkflowTaskDetail{}, errors.New("task id is required")
	}
	trimmedRunID := strings.TrimSpace(runID)
	if trimmedRunID == "" {
		return serverapi.WorkflowTaskDetail{}, errors.New("run id is required")
	}
	if interval <= 0 {
		interval = taskStartSessionPollInterval
	}
	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		detail, err := getWorkflowTaskByID(pollCtx, remote, taskID)
		if err != nil {
			if pollCtx.Err() != nil {
				return serverapi.WorkflowTaskDetail{}, fmt.Errorf("started task %s with run %s but session id was not assigned within %s", taskID, trimmedRunID, timeout)
			}
			return serverapi.WorkflowTaskDetail{}, fmt.Errorf("started task %s with run %s but failed to load task detail while waiting for session id: %w", taskID, trimmedRunID, err)
		}
		if run, ok := workflowTaskRunByID(detail, trimmedRunID); ok {
			if workflowTaskRunDoesNotRequireSession(run) || strings.TrimSpace(run.SessionID) != "" {
				return detail, nil
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-pollCtx.Done():
			timer.Stop()
			return serverapi.WorkflowTaskDetail{}, fmt.Errorf("started task %s with run %s but session id was not assigned within %s", taskDisplayID(detail), trimmedRunID, timeout)
		case <-timer.C:
		}
	}
}

func workflowTaskRunDoesNotRequireSession(run serverapi.WorkflowRun) bool {
	return serverapi.WorkflowNodeKind(strings.TrimSpace(run.NodeKind)) == serverapi.WorkflowNodeKindScript
}
