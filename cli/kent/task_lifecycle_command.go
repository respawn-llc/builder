package main

import (
	"context"
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
	workflowRef := fs.String("workflow", "", "workflow UUID; defaults to the project's default workflow")
	projectRef := fs.String("project", ".", "project ID or attached workspace path")
	sourceURL := fs.String("source-url", "", "URL of the issue or document that originated the task")
	sourceWorkspace := fs.String("source-workspace", "", "workspace ID or path used as the task's source checkout")
	var labelSelectors repeatedStringFlag
	fs.Var(&labelSelectors, "label", "existing Project label name or canonical UUIDv4; repeat for multiple labels")
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
	var selectedWorkflow *workflowSelector
	if flagWasProvided(fs, "workflow") {
		selector, parseErr := parseWorkflowSelector(*workflowRef)
		if parseErr != nil {
			fmt.Fprintln(stderr, parseErr)
			return 2
		}
		selectedWorkflow = &selector
	}
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote workflowCommandRemote) int {
		projectID, err := resolveWorkflowProjectID(context.Background(), cfg, remote, *projectRef)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		var workflowID *string
		if selectedWorkflow != nil {
			value := selectedWorkflow.PersistedID()
			workflowID = &value
		}
		labelIDs := []string(nil)
		if len(labelSelectors) > 0 {
			_, snapshot, err := loadWorkflowProjectLabelCatalog(context.Background(), remote, projectID)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			labelIDs, err = resolveWorkflowProjectLabelSelectors(snapshot, labelSelectors)
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
		resp, err := remote.CreateWorkflowTask(ctx, serverapi.WorkflowTaskCreateRequest{
			ProjectID:         projectID,
			WorkflowID:        workflowID,
			Title:             *title,
			Body:              taskBody,
			SourceURL:         *sourceURL,
			SourceWorkspaceID: sourceWorkspaceID,
			LabelIDs:          labelIDs,
		})
		if err != nil {
			var selectedWorkflowID *string
			if selectedWorkflow != nil {
				value := selectedWorkflow.String()
				selectedWorkflowID = &value
			}
			writeTaskCreateError(stderr, err, taskCreateCommandContext{
				ProjectRef:         *projectRef,
				ResolvedProjectID:  projectID,
				SelectedWorkflowID: selectedWorkflowID,
				Title:              *title,
				Body:               *body,
				BodyFile:           *bodyFile,
				SourceURL:          *sourceURL,
				SourceWorkspace:    *sourceWorkspace,
				LabelSelectors:     append([]string(nil), labelSelectors...),
				JSON:               *jsonOut,
			})
			return 1
		}
		if strings.TrimSpace(resp.Task.ID) == "" {
			fmt.Fprintln(stderr, "task create response is missing task ID")
			return 1
		}
		if resp.Task.ProjectID != projectID {
			fmt.Fprintf(stderr, "task create response project %q does not match requested project %q\n", resp.Task.ProjectID, projectID)
			return 1
		}
		task, err := getWorkflowTaskByID(context.Background(), remote, resp.Task.ID)
		if err != nil {
			fmt.Fprintf(stderr, "created task %s but failed to load task detail for output: %v\n", resp.Task.ID, err)
			return 1
		}
		if task.Summary.ID != resp.Task.ID {
			fmt.Fprintf(stderr, "created task detail ID %q does not match create response task %q\n", task.Summary.ID, resp.Task.ID)
			return 1
		}
		if task.Summary.ProjectID != projectID {
			fmt.Fprintf(stderr, "created task detail project %q does not match requested project %q\n", task.Summary.ProjectID, projectID)
			return 1
		}
		task, err = workflowTaskDetailForCLI(task)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOut {
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

func writeTaskCreateError(stderr io.Writer, err error, commandContext taskCreateCommandContext) {
	var conflictErr *serverapi.WorkflowTaskCreateConflictError
	if errors.As(err, &conflictErr) {
		switch conflictErr.Reason {
		case serverapi.WorkflowTaskCreateConflictReasonSerialization:
			retryCommand := taskCreateRetryCommandArgs(commandContext, commandContext.SelectedWorkflowID)
			fmt.Fprintln(stderr, "Task creation conflicted with a concurrent update. This failure is retryable; no task was created.")
			fmt.Fprintf(stderr, "  %s\n", commandString(retryCommand))
		default:
			fmt.Fprintln(stderr, err)
		}
		return
	}
	var selectionErr *serverapi.WorkflowTaskCreateSelectionError
	if !errors.As(err, &selectionErr) {
		fmt.Fprintln(stderr, err)
		return
	}
	recovery, projectionErr := taskCreateRecoveryForSelectionError(selectionErr, commandContext)
	if projectionErr != nil {
		fmt.Fprintln(stderr, projectionErr)
		return
	}
	switch recovery.Kind {
	case taskWorkflowRecoveryNoLinkedWorkflows:
		fmt.Fprintln(stderr, "This project doesn't have any linked workflows yet. First, create a workflow or link an existing one, then retry.")
	case taskWorkflowRecoveryWorkflowNotLinked:
		fmt.Fprintln(stderr, "The selected workflow isn't linked to this project.")
	case taskWorkflowRecoveryAmbiguousWithoutDefault:
		fmt.Fprintf(
			stderr,
			"Tasks need both a project and a workflow binding, but your input leaves workflow choice ambiguous. Run `%s`, then retry with `--workflow <uuid>` or set a default with `%s`.\n",
			commandString(recovery.Commands[0].Args),
			commandString(recovery.Commands[2].Args),
		)
	}
	for _, command := range recovery.Commands {
		fmt.Fprintf(stderr, "  %s\n", commandString(command.Args))
	}
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
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote workflowCommandRemote) int {
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
			projected, projectionErr := workflowTaskSummaryForCLI(resp.Task)
			if projectionErr != nil {
				fmt.Fprintln(stderr, projectionErr)
				return 1
			}
			resp.Task = projected
			return writeCommandJSON(stdout, stderr, resp)
		}
		fmt.Fprintf(stdout, "Edited task %s.\n", taskSummaryDisplayID(resp.Task))
		return 0
	})
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
	executionTargetRaw := fs.String("execution-target", "", "task-local execution target: "+executionTargetSelectorHelp)
	ignoreDependencies := fs.Bool("ignore-dependencies", false, "proceed despite current unsatisfied Task Dependencies")
	jsonOut := fs.Bool("json", false, "write the typed start outcome as JSON")
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
	executionTarget, err := parseOptionalTaskExecutionTarget(*executionTargetRaw, flagWasProvided(fs, "execution-target"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote workflowCommandRemote) int {
		taskID, err := resolveWorkflowTaskID(context.Background(), cfg, remote, *projectRef, positionals[0])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		resp, err := runWorkflowMutationWithSetupProgress(context.Background(), remote, stderr, func(ctx context.Context, setupOperationID serverapi.WorktreeSetupOperationID) (serverapi.WorkflowTaskStartResponse, error) {
			return remote.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
				SetupOperationID:           setupOperationID,
				TaskID:                     taskID,
				ExecutionTarget:            executionTarget,
				ProceedDespiteDependencies: *ignoreDependencies,
			})
		})
		if err != nil {
			if !writeWorkflowExecutionTargetError(stderr, err) {
				fmt.Fprintln(stderr, err)
			}
			return 1
		}
		if err := resp.Validate(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if resp.Outcome == serverapi.WorkflowTaskActionOutcomeDependencyConfirmationRequired {
			if *jsonOut {
				_ = writeCommandJSON(stdout, stderr, resp)
			} else {
				writeTaskDependencyConfirmationRequired(stderr, positionals[0], resp.UnsatisfiedDependencyCount)
			}
			return 1
		}
		if resp.Outcome == serverapi.WorkflowTaskActionOutcomeSelectionRequired {
			if *jsonOut {
				_ = writeCommandJSON(stdout, stderr, resp)
			} else {
				writeWorkflowExecutionTargetSelectionRequired(stderr, resp.SelectionRequired)
			}
			return 1
		}
		applied, err := requireAppliedWorkflowAction(resp.Outcome, resp.Applied)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOut {
			return writeCommandJSON(stdout, stderr, resp)
		}
		detail, err := getWorkflowTaskByID(context.Background(), remote, taskID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		detail, err = workflowTaskDetailForCLI(detail)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		writeTaskStartResult(stdout, detail, *applied)
		return 0
	})
}

func writeWorkflowExecutionTargetSelectionRequired(stderr io.Writer, requirement *serverapi.WorkflowExecutionTargetSelectionRequirement) {
	switch {
	case requirement == nil:
		fmt.Fprintln(stderr, "Execution target selection is required.")
	case requirement.Reason == serverapi.WorkflowExecutionTargetSelectionReasonPolicyRequiresSelection:
		fmt.Fprintln(stderr, "Execution target selection is required: workflow policy requires selection.")
	case requirement.Reason == serverapi.WorkflowExecutionTargetSelectionReasonConfiguredTargetUnavailable:
		target := "configured target"
		if requirement.ConfiguredTarget != nil {
			target = workflowConfiguredExecutionTargetSelector(*requirement.ConfiguredTarget)
		}
		fmt.Fprintf(stderr, "Execution target selection is required: configured target %s is unavailable (%s).\n", target, requirement.UnavailableCause)
	default:
		fmt.Fprintf(stderr, "Execution target selection is required: %s.\n", requirement.Reason)
	}
	fmt.Fprintln(stderr, "Rerun with one of:")
	fmt.Fprintln(stderr, "  --execution-target none")
	fmt.Fprintln(stderr, "  --execution-target head")
	fmt.Fprintln(stderr, "  --execution-target default-branch")
	fmt.Fprintln(stderr, "  --execution-target ref:<revision>")
}

func workflowConfiguredExecutionTargetSelector(target serverapi.WorkflowExecutionTargetConfiguredTarget) string {
	if target.Mode == serverapi.WorkflowExecutionTargetModeCustomRef {
		if target.RequestedRef != nil {
			return "ref:" + *target.RequestedRef
		}
		return "ref:<revision>"
	}
	return workflowExecutionTargetPolicySelector(serverapi.WorkflowExecutionTargetConfiguration{Mode: target.Mode})
}

func writeWorkflowExecutionTargetError(stderr io.Writer, err error) bool {
	var resolutionErr *serverapi.WorkflowExecutionTargetResolutionError
	if errors.As(err, &resolutionErr) {
		fmt.Fprintf(stderr, "Execution target revision %q failed: %s.\n", resolutionErr.RequestedRef, resolutionErr.Code)
		return true
	}
	var lockedErr *serverapi.WorkflowLockedExecutionTargetError
	if errors.As(err, &lockedErr) {
		fmt.Fprintf(stderr, "Locked execution target is unavailable: %s.\n", lockedErr.Cause)
		return true
	}
	return false
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
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote workflowCommandRemote) int {
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
	})
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
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote workflowCommandRemote) int {
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
	})
}

func taskInterruptSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" task interrupt", stderr, taskInterruptUsage)
	projectRef := fs.String("project", ".", "project ID or attached workspace path used to resolve a short ID")
	sessionID := fs.String("session", "", "live Agent session ID to interrupt; otherwise interrupts the whole task")
	reason := fs.String("reason", "", "operator-visible reason for the interruption")
	positionals, flagArgs := takeLeadingPositionals(args, 1)
	if ok, exitCode := parseCommandFlags(fs, flagArgs); !ok {
		return exitCode
	}
	positionals = append(positionals, fs.Args()...)
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "task interrupt requires <short-id-or-task-id>")
		return 2
	}
	if flagWasProvided(fs, "session") && strings.TrimSpace(*sessionID) == "" {
		fmt.Fprintln(stderr, "--session requires a non-blank session ID")
		return 2
	}
	if denyAgentHumanOnlyTaskAction(stderr) {
		return 1
	}
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote workflowCommandRemote) int {
		taskID, err := resolveWorkflowTaskID(context.Background(), cfg, remote, *projectRef, positionals[0])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
		defer cancel()
		if _, err := remote.InterruptWorkflowTask(ctx, serverapi.WorkflowTaskInterruptRequest{
			TaskID:    taskID,
			SessionID: strings.TrimSpace(*sessionID),
			Reason:    *reason,
		}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		detail, err := getWorkflowTaskByID(context.Background(), remote, taskID)
		if err != nil {
			fmt.Fprintf(stderr, "interrupted task %s but failed to load task detail for output: %v\n", taskID, err)
			return 1
		}
		writeTaskLifecycleResult(stdout, "Interrupted", detail)
		return 0
	})
}

func taskApproveSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" task approve", stderr, taskApproveUsage)
	positionals, flagArgs := takeLeadingPositionals(args, 1)
	if ok, exitCode := parseCommandFlags(fs, flagArgs); !ok {
		return exitCode
	}
	positionals = append(positionals, fs.Args()...)
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "task approve requires <approval-id>")
		return 2
	}
	if denyAgentHumanOnlyTaskAction(stderr) {
		return 1
	}
	return runWorkflowCommandSession(stderr, func(_ config.App, remote workflowCommandRemote) int {
		ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
		defer cancel()
		resp, err := remote.ApproveWorkflowTask(ctx, serverapi.WorkflowTaskApproveRequest{ApprovalID: positionals[0]})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := resp.Validate(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		applied, err := requireAppliedExecutionTargetAction(resp.Outcome, resp.Applied)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if strings.TrimSpace(applied.TaskID) == "" {
			fmt.Fprintf(stderr, "approved workflow change %s but response did not include task id for output\n", positionals[0])
			return 1
		}
		detail, err := getWorkflowTaskByID(context.Background(), remote, applied.TaskID)
		if err != nil {
			fmt.Fprintf(stderr, "approved workflow change %s but failed to load task detail for output: %v\n", positionals[0], err)
			return 1
		}
		writeTaskLifecycleResult(stdout, "Approved", detail)
		return 0
	})
}

func taskMoveSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" task move", stderr, taskMoveUsage)
	projectRef := fs.String("project", ".", "project ID or attached workspace path used to resolve a short ID")
	commentary := fs.String("commentary", "", "note recorded with the workflow transition")
	executionTargetRaw := fs.String("execution-target", "", "task-local execution target: "+executionTargetSelectorHelp)
	ignoreDependencies := fs.Bool("ignore-dependencies", false, "proceed despite current unsatisfied Task Dependencies")
	jsonOut := fs.Bool("json", false, "write the typed move outcome as JSON")
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
	executionTarget, err := parseOptionalTaskExecutionTarget(*executionTargetRaw, flagWasProvided(fs, "execution-target"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote workflowCommandRemote) int {
		taskID, err := resolveWorkflowTaskID(context.Background(), cfg, remote, *projectRef, positionals[0])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		resp, err := runWorkflowMutationWithSetupProgress(context.Background(), remote, stderr, func(ctx context.Context, setupOperationID serverapi.WorktreeSetupOperationID) (serverapi.WorkflowTaskMoveResponse, error) {
			return remote.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
				TaskID:                     taskID,
				TargetNodeID:               positionals[1],
				OutputValues:               outputs.values,
				Commentary:                 *commentary,
				SetupOperationID:           setupOperationID,
				ExecutionTarget:            executionTarget,
				ProceedDespiteDependencies: *ignoreDependencies,
			})
		})
		if err != nil {
			if !writeWorkflowExecutionTargetError(stderr, err) {
				fmt.Fprintln(stderr, err)
			}
			return 1
		}
		if err := resp.Validate(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if resp.Outcome == serverapi.WorkflowTaskActionOutcomeDependencyConfirmationRequired {
			if *jsonOut {
				_ = writeCommandJSON(stdout, stderr, resp)
			} else {
				writeTaskDependencyConfirmationRequired(stderr, positionals[0], resp.UnsatisfiedDependencyCount)
			}
			return 1
		}
		if resp.Outcome == serverapi.WorkflowTaskActionOutcomeSelectionRequired {
			if *jsonOut {
				_ = writeCommandJSON(stdout, stderr, resp)
			} else {
				writeWorkflowExecutionTargetSelectionRequired(stderr, resp.SelectionRequired)
			}
			return 1
		}
		if _, err := requireAppliedWorkflowAction(resp.Outcome, resp.Applied); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOut {
			return writeCommandJSON(stdout, stderr, resp)
		}
		detail, err := getWorkflowTaskByID(context.Background(), remote, taskID)
		if err != nil {
			fmt.Fprintf(stderr, "moved task %s but failed to load task detail for output: %v\n", taskID, err)
			return 1
		}
		writeTaskLifecycleResult(stdout, "Moved", detail)
		return 0
	})
}

func writeTaskDependencyConfirmationRequired(stderr io.Writer, taskRef string, count *int) {
	if count == nil {
		panic("dependency confirmation outcome requires an unsatisfied dependency count")
	}
	fmt.Fprintf(stderr, "Task %s has %d unsatisfied dependencies.\n", taskRef, *count)
	fmt.Fprintf(stderr, "Review them with `%s task show %s`.\n", config.Command, taskRef)
	fmt.Fprintln(stderr, "Rerun with `--ignore-dependencies` to proceed.")
}

func requireAppliedWorkflowAction[T any](outcome serverapi.WorkflowTaskActionOutcome, applied *T) (*T, error) {
	if outcome != serverapi.WorkflowTaskActionOutcomeApplied || applied == nil {
		return nil, errors.New("workflow action requires execution target selection")
	}
	return applied, nil
}

func requireAppliedExecutionTargetAction[T any](outcome serverapi.WorkflowExecutionTargetActionOutcome, applied *T) (*T, error) {
	if outcome != serverapi.WorkflowExecutionTargetActionOutcomeApplied || applied == nil {
		return nil, errors.New("workflow action requires execution target selection")
	}
	return applied, nil
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

func waitForWorkflowTaskRunSession(ctx context.Context, remote workflowCommandRemote, taskID string, _ string, timeout time.Duration, interval time.Duration) (serverapi.WorkflowTaskDetail, error) {
	if strings.TrimSpace(taskID) == "" {
		return serverapi.WorkflowTaskDetail{}, errors.New("task id is required")
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
				return serverapi.WorkflowTaskDetail{}, fmt.Errorf("started task %s but session id was not assigned within %s", taskID, timeout)
			}
			return serverapi.WorkflowTaskDetail{}, fmt.Errorf("started task %s but failed to load task detail while waiting for session id: %w", taskID, err)
		}
		if len(detail.CurrentScripts) > 0 || len(detail.LiveSessionIDs) > 0 {
			return detail, nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-pollCtx.Done():
			timer.Stop()
			return serverapi.WorkflowTaskDetail{}, fmt.Errorf("started task %s but session id was not assigned within %s", taskDisplayID(detail), timeout)
		case <-timer.C:
		}
	}
}
