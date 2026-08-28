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

	"core/shared/apicontract"
	"core/shared/client"
	"core/shared/config"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/runtimeids"
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
	workflowRef := fs.String("workflow", "", "workflow selector `<uuid>`; defaults to the project's default workflow")
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
	var selectedWorkflow *runtimeids.WorkflowID
	if flagExplicit(fs, "workflow") {
		selector, parseErr := parseWorkflowSelector(*workflowRef)
		if parseErr != nil {
			fmt.Fprintln(stderr, parseErr)
			return 2
		}
		selectedWorkflow = &selector
	}
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote *client.Remote) int {
		projectID, err := resolveWorkflowProjectID(context.Background(), cfg, remote, *projectRef)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		var workflowID *runtimeids.WorkflowID
		if selectedWorkflow != nil {
			workflowID = selectedWorkflow
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
			var selectedWorkflowID *runtimeids.WorkflowID
			if selectedWorkflow != nil {
				selectedWorkflowID = selectedWorkflow
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
	titleProvided := flagExplicit(fs, "title")
	bodyProvided := flagExplicit(fs, "body")
	bodyFileProvided := flagExplicit(fs, "body-file")
	workspaceProvided := flagExplicit(fs, "source-workspace")
	if !titleProvided && !bodyProvided && !bodyFileProvided && !workspaceProvided {
		fmt.Fprintln(stderr, "task edit requires at least one of --title, --body, --body-file, or --source-workspace")
		return 2
	}
	if bodyProvided && bodyFileProvided {
		fmt.Fprintln(stderr, "--body cannot be combined with --body-file")
		return 2
	}
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote *client.Remote) int {
		taskID, err := resolveWorkflowTaskID(context.Background(), cfg, remote, remote, *projectRef, positionals[0])
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
	executionTargetRaw, branchNameRaw := addInitialBranchExecutionFlags(fs)
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
	invokingSessionID, err := workflowTaskInvokingSessionID()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	executionTarget, branchName, ok := parseInitialBranchExecutionOptions(fs, *executionTargetRaw, *branchNameRaw, stderr)
	if !ok {
		return 2
	}
	var recoveryProject *string
	if flagExplicit(fs, "project") {
		recoveryProject = projectRef
	}
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote *client.Remote) int {
		taskID, err := resolveWorkflowTaskID(context.Background(), cfg, remote, remote, *projectRef, positionals[0])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		resp, terminal, err := runWorkflowMutationWithSetupProgress(context.Background(), remote, stderr, func(ctx context.Context, setupOperationID serverapi.WorkflowSetupOperationID) (serverapi.WorkflowTaskStartResponse, error) {
			return remote.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
				SetupOperationID:           setupOperationID,
				TaskID:                     taskID,
				InvokingSessionID:          invokingSessionID,
				ExecutionTarget:            executionTarget,
				BranchName:                 branchName,
				ProceedDespiteDependencies: *ignoreDependencies,
			})
		}, func(response serverapi.WorkflowTaskStartResponse) bool {
			return response.Outcome == serverapi.WorkflowTaskActionOutcomeApplied
		})
		if err != nil {
			if *jsonOut && resp.Outcome == serverapi.WorkflowTaskActionOutcomeApplied && resp.Validate() == nil {
				_ = writeCommandJSON(stdout, stderr, resp)
			}
			if writeTaskSetupObservationError(taskSetupObservedActionStart, stderr, positionals[0], recoveryProject, err) {
				return 1
			}
			var conflict *serverapi.WorkflowTaskStartConflictError
			if errors.As(err, &conflict) && conflict.Reason == serverapi.WorkflowTaskStartConflictAlreadyStarted {
				renderTaskSetupGuidance(stderr, taskAlreadyStartedGuidance(positionals[0], recoveryProject))
				return 1
			}
			if !writeWorkflowTaskTargetOrBranchError(stderr, err) &&
				!writeWorkflowTaskMutationSelfTargetError(stderr, err) {
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
		if !finishObservedTaskSetup(taskSetupObservedActionStart, stderr, positionals[0], recoveryProject, terminal) {
			if *jsonOut {
				_ = writeCommandJSON(stdout, stderr, resp)
			}
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
	writeWorkflowExecutionTargetSelectionRequiredForCommand(stderr, requirement, "")
}

func writeWorkflowExecutionTargetSelectionRequiredForCommand(
	stderr io.Writer,
	requirement *serverapi.WorkflowExecutionTargetSelectionRequirement,
	command string,
) {
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
	for _, selection := range []string{"none", "head", "default-branch", "ref:<revision>"} {
		prefix := strings.TrimSpace(command)
		if prefix != "" {
			prefix += " "
		}
		fmt.Fprintf(stderr, "  %s--execution-target %s\n", prefix, selection)
	}
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
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote *client.Remote) int {
		taskID, err := resolveWorkflowTaskID(context.Background(), cfg, remote, remote, *projectRef, positionals[0])
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
	executionTargetRaw, branchNameRaw := addInitialBranchExecutionFlags(fs)
	positionals, flagArgs := takeLeadingPositionals(args, 1)
	if ok, exitCode := parseCommandFlags(fs, flagArgs); !ok {
		return exitCode
	}
	positionals = append(positionals, fs.Args()...)
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "task resume requires <short-id-or-task-id>")
		return 2
	}
	invokingSessionID, err := workflowTaskInvokingSessionID()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	executionTarget, branchName, ok := parseInitialBranchExecutionOptions(fs, *executionTargetRaw, *branchNameRaw, stderr)
	if !ok {
		return 2
	}
	var recoveryProject *string
	if flagExplicit(fs, "project") {
		recoveryProject = projectRef
	}
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote *client.Remote) int {
		taskID, err := resolveWorkflowTaskID(context.Background(), cfg, remote, remote, *projectRef, positionals[0])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		resp, terminal, err := runWorkflowMutationWithSetupProgress(context.Background(), remote, stderr, func(ctx context.Context, setupOperationID serverapi.WorkflowSetupOperationID) (serverapi.WorkflowTaskResumeResponse, error) {
			return remote.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{
				TaskID:            taskID,
				InvokingSessionID: invokingSessionID,
				SetupOperationID:  setupOperationID,
				ExecutionTarget:   executionTarget,
				BranchName:        branchName,
			})
		}, func(response serverapi.WorkflowTaskResumeResponse) bool {
			return response.Outcome == serverapi.WorkflowExecutionTargetActionOutcomeApplied
		})
		if err != nil {
			if writeTaskSetupObservationError(taskSetupObservedActionResume, stderr, positionals[0], recoveryProject, err) {
				return 1
			}
			if writeWorkflowTaskTargetOrBranchError(stderr, err) ||
				writeWorkflowTaskMutationSelfTargetError(stderr, err) {
				return 1
			}
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := resp.Validate(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if resp.Outcome == serverapi.WorkflowExecutionTargetActionOutcomeSelectionRequired {
			writeWorkflowExecutionTargetSelectionRequired(stderr, resp.SelectionRequired)
			return 1
		}
		applied, err := requireAppliedExecutionTargetAction(resp.Outcome, resp.Applied)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if !finishObservedTaskSetup(taskSetupObservedActionResume, stderr, positionals[0], recoveryProject, terminal) {
			return 1
		}
		detail, err := getWorkflowTaskByID(context.Background(), remote, taskID)
		if err != nil {
			fmt.Fprintf(stderr, "resumed task %s but failed to load task detail for output: %v\n", taskID, err)
			return 1
		}
		writeTaskResumeResult(stdout, detail, *applied)
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
	if flagExplicit(fs, "session") && strings.TrimSpace(*sessionID) == "" {
		fmt.Fprintln(stderr, "--session requires a non-blank session ID")
		return 2
	}
	invokingSessionID, err := workflowTaskInvokingSessionID()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote *client.Remote) int {
		taskID, err := resolveWorkflowTaskID(context.Background(), cfg, remote, remote, *projectRef, positionals[0])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
		defer cancel()
		if _, err := remote.InterruptWorkflowTask(ctx, serverapi.WorkflowTaskInterruptRequest{
			TaskID:            taskID,
			InvokingSessionID: invokingSessionID,
			SessionID:         strings.TrimSpace(*sessionID),
			Reason:            *reason,
		}); err != nil {
			if writeWorkflowTaskMutationSelfTargetError(stderr, err) {
				return 1
			}
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
	invokingSessionID, err := workflowTaskInvokingSessionID()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return runWorkflowCommandSession(stderr, func(_ config.App, remote *client.Remote) int {
		ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
		defer cancel()
		resp, err := remote.ApproveWorkflowTask(ctx, serverapi.WorkflowTaskApproveRequest{
			ApprovalID:        positionals[0],
			InvokingSessionID: invokingSessionID,
		})
		if err != nil {
			if !writeWorkflowTaskMutationSelfTargetError(stderr, err) {
				fmt.Fprintln(stderr, err)
			}
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
	executionTargetRaw, branchNameRaw := addInitialBranchExecutionFlags(fs)
	ignoreDependencies := fs.Bool("ignore-dependencies", false, "proceed despite current unsatisfied Task Dependencies")
	transition := fs.String("transition", "", "workflow Transition key")
	valuesJSON := fs.String("values-json", "", "nested JSON values keyed by Node key and output name")
	valuesFile := fs.String("values-file", "", "read nested Node/output values from a JSON file")
	jsonOut := fs.Bool("json", false, "write the typed move outcome as JSON")
	positionals, flagArgs := takeLeadingPositionals(args, 2)
	if ok, exitCode := parseCommandFlags(fs, flagArgs); !ok {
		return exitCode
	}
	positionals = append(positionals, fs.Args()...)
	if len(positionals) != 2 {
		fmt.Fprintln(stderr, "task move requires <short-id-or-task-id> <target-node-id>")
		return 2
	}
	invokingSessionID, err := workflowTaskInvokingSessionID()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	executionTarget, branchName, ok := parseInitialBranchExecutionOptions(fs, *executionTargetRaw, *branchNameRaw, stderr)
	if !ok {
		return 2
	}
	values, err := readManualMoveValues(
		*valuesJSON,
		*valuesFile,
		flagExplicit(fs, "values-json"),
		flagExplicit(fs, "values-file"),
	)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote *client.Remote) int {
		taskID, err := resolveWorkflowTaskID(context.Background(), cfg, remote, remote, *projectRef, positionals[0])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		preview, err := remote.PreviewWorkflowTaskMove(context.Background(), serverapi.WorkflowTaskMovePreviewRequest{
			TaskID: taskID, TargetNodeID: positionals[1],
		})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := preview.Validate(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if preview.Outcome == serverapi.WorkflowTaskMovePreviewOutcomeBlocked {
			fmt.Fprintf(stderr, "task move blocked: %s\n", manualMoveBlockerMessage(preview.Blocked.Reason))
			return 1
		}
		if preview.Outcome == serverapi.WorkflowTaskMovePreviewOutcomeNoOp {
			if rejectInitialBranchForMoveNoOp(stderr, branchName) {
				return 2
			}
			if flagExplicit(fs, "transition") || len(values) != 0 {
				fmt.Fprintln(stderr, "task move no-op does not accept --transition or --values-json/--values-file")
				return 2
			}
			if *jsonOut {
				return writeCommandJSON(stdout, stderr, serverapi.WorkflowTaskMoveResponse{
					Outcome: serverapi.WorkflowExecutionTargetActionOutcomeNoOp,
					NoOp: &serverapi.WorkflowTaskMoveNoOp{
						CurrentNodes: preview.NoOp.CurrentNodes,
					},
				})
			}
			detail, detailErr := getWorkflowTaskByID(context.Background(), remote, taskID)
			if detailErr != nil {
				fmt.Fprintf(stderr, "task %s is already at %s but failed to load task detail: %v\n", taskID, positionals[1], detailErr)
				return 1
			}
			writeTaskLifecycleResult(stdout, "No-op move", detail)
			return 0
		}
		if preview.Outcome == serverapi.WorkflowTaskMovePreviewOutcomeDirect {
			if flagExplicit(fs, "transition") || len(values) != 0 {
				fmt.Fprintln(stderr, "direct task move does not accept --transition or --values-json/--values-file")
				return 2
			}
		}
		var transitionKey *string
		if preview.Outcome == serverapi.WorkflowTaskMovePreviewOutcomeTransition {
			transitionKey, err = selectTaskMoveTransition(preview, *transition, flagExplicit(fs, "transition"))
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 2
			}
		}
		var recoveryProject, recoveryCommentary *string
		if flagExplicit(fs, "project") {
			recoveryProject = projectRef
		}
		if flagExplicit(fs, "commentary") {
			recoveryCommentary = commentary
		}
		recoveryArgs, err := taskMoveRecoveryArgs(positionals[0], positionals[1], recoveryProject, recoveryCommentary, transitionKey, values, *ignoreDependencies, *jsonOut)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		resp, err := remote.MoveWorkflowTask(context.Background(), serverapi.WorkflowTaskMoveRequest{
			TaskID:                     taskID,
			InvokingSessionID:          invokingSessionID,
			TargetNodeID:               positionals[1],
			TransitionKey:              transitionKey,
			Values:                     values,
			Commentary:                 *commentary,
			ExecutionTarget:            executionTarget,
			BranchName:                 branchName,
			ProceedDespiteDependencies: *ignoreDependencies,
		})
		if err != nil {
			var setupErr *serverapi.WorkflowSetupRetainedError
			if errors.As(err, &setupErr) {
				guidance, projectionErr := projectMoveSetupGuidance(recoveryArgs, executionTarget, setupErr)
				if projectionErr != nil {
					fmt.Fprintln(stderr, projectionErr)
				} else {
					renderTaskSetupGuidance(stderr, guidance)
				}
				return 1
			}
			if !writeWorkflowTaskTargetOrBranchError(stderr, err) &&
				!writeWorkflowTaskMutationSelfTargetError(stderr, err) {
				fmt.Fprintln(stderr, err)
			}
			return 1
		}
		return writeTaskMoveOutcome(stdout, stderr, remote, taskID, positionals[0], resp, *jsonOut, "")
	})
}

func writeTaskMoveOutcome(
	stdout io.Writer,
	stderr io.Writer,
	remote *client.Remote,
	taskID string,
	taskRef string,
	resp serverapi.WorkflowTaskMoveResponse,
	jsonOut bool,
	recoveryCommand string,
) int {
	if err := resp.Validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if resp.Outcome == serverapi.WorkflowExecutionTargetActionOutcomeSelectionRequired {
		if jsonOut {
			_ = writeCommandJSON(stdout, stderr, resp)
		} else {
			writeWorkflowExecutionTargetSelectionRequiredForCommand(stderr, resp.SelectionRequired, recoveryCommand)
		}
		return 1
	}
	if resp.Outcome == serverapi.WorkflowExecutionTargetActionOutcomeDependencyConfirmationRequired {
		if jsonOut {
			_ = writeCommandJSON(stdout, stderr, resp)
		} else {
			writeTaskDependencyConfirmationRequiredForCommand(stderr, taskRef, resp.UnsatisfiedDependencyCount, recoveryCommand)
		}
		return 1
	}
	if resp.Outcome == serverapi.WorkflowExecutionTargetActionOutcomeNoOp {
		renderWorkflowRetainedWorktreeGuidance(stderr, resp.NoOp.RetainedPreviousWorktree)
		if jsonOut {
			return writeCommandJSON(stdout, stderr, resp)
		}
		detail, err := getWorkflowTaskByID(context.Background(), remote, taskID)
		if err != nil {
			fmt.Fprintf(stderr, "task %s move became a no-op but failed to load task detail: %v\n", taskID, err)
			return 1
		}
		writeTaskLifecycleResult(stdout, "No-op move", detail)
		return 0
	}
	applied, err := requireAppliedExecutionTargetAction(resp.Outcome, resp.Applied)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	renderWorkflowRetainedWorktreeGuidance(stderr, applied.RetainedPreviousWorktree)
	if jsonOut {
		return writeCommandJSON(stdout, stderr, resp)
	}
	detail, err := getWorkflowTaskByID(context.Background(), remote, taskID)
	if err != nil {
		fmt.Fprintf(stderr, "moved task %s but failed to load task detail for output: %v\n", taskID, err)
		return 1
	}
	writeTaskLifecycleResult(stdout, "Moved", detail)
	return 0
}

func selectTaskMoveTransition(
	preview serverapi.WorkflowTaskMovePreviewResponse,
	raw string,
	explicit bool,
) (*string, error) {
	if preview.Outcome != serverapi.WorkflowTaskMovePreviewOutcomeTransition || preview.Transition == nil {
		return nil, errors.New("task move transition selection requires a transition preview")
	}
	key := strings.TrimSpace(raw)
	if key == "" {
		if explicit {
			return nil, errors.New("task move --transition cannot be blank")
		}
		if len(preview.Transition.Choices) != 1 {
			return nil, errors.New("task move requires --transition when multiple incoming Transitions are usable")
		}
		key = preview.Transition.Choices[0].TransitionKey
	}
	for _, choice := range preview.Transition.Choices {
		if choice.TransitionKey == key {
			authoredKey := choice.TransitionKey
			return &authoredKey, nil
		}
	}
	return nil, fmt.Errorf("task move Transition %q is not a usable incoming Transition", key)
}

func manualMoveBlockerMessage(reason serverapi.WorkflowTaskMovePreviewBlocker) string {
	switch reason {
	case serverapi.WorkflowTaskMovePreviewBlockerInvalidWorkflow:
		return "the workflow is invalid; fix the workflow definition and try again"
	case serverapi.WorkflowTaskMovePreviewBlockerNoSourcePosition:
		return "the task has no current workflow position; start the task before moving it"
	case serverapi.WorkflowTaskMovePreviewBlockerUnsupportedDestination:
		return "the destination cannot be entered by Manual Move; choose an executable or terminal node"
	case serverapi.WorkflowTaskMovePreviewBlockerLifecycleConflict:
		return "the task is changing state; wait for the current operation to finish and try again"
	case serverapi.WorkflowTaskMovePreviewBlockerContextSessionUnavailable:
		return "the selected transition needs a retained context session that is unavailable"
	case serverapi.WorkflowTaskMovePreviewBlockerNoUsableTransition:
		return "the destination has no usable incoming transition from the task's current position"
	case serverapi.WorkflowTaskMovePreviewBlockerParallelBranchRequiresFanOut:
		return "the destination is inside a parallel branch; move to the Fan-Out transition or choose another destination"
	default:
		return "the server could not explain why this move is blocked; try again"
	}
}

func readManualMoveValues(inline, file string, inlineProvided, fileProvided bool) (map[string]map[string]string, error) {
	if inlineProvided && fileProvided {
		return nil, errors.New("--values-json and --values-file cannot be combined")
	}
	raw := inline
	if fileProvided {
		if strings.TrimSpace(file) == "" {
			return nil, errors.New("--values-file requires a path")
		}
		content, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read --values-file: %w", err)
		}
		raw = string(content)
	}
	if strings.TrimSpace(raw) == "" {
		if inlineProvided || fileProvided {
			return nil, errors.New("parse manual move values: expected a JSON object")
		}
		return nil, nil
	}
	var values map[string]map[string]string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("parse manual move values: %w", err)
	}
	if values == nil {
		return nil, errors.New("parse manual move values: expected a JSON object")
	}
	return values, nil
}

func writeTaskDependencyConfirmationRequired(stderr io.Writer, taskRef string, count *int) {
	writeTaskDependencyConfirmationRequiredForCommand(stderr, taskRef, count, "")
}

func writeTaskDependencyConfirmationRequiredForCommand(stderr io.Writer, taskRef string, count *int, command string) {
	if count == nil {
		panic("dependency confirmation outcome requires an unsatisfied dependency count")
	}
	fmt.Fprintf(stderr, "Task %s has %d unsatisfied dependencies.\n", taskRef, *count)
	fmt.Fprintf(stderr, "Review them with `%s task show %s`.\n", config.Command, taskRef)
	if command = strings.TrimSpace(command); command != "" {
		fmt.Fprintf(stderr, "Rerun with `%s --ignore-dependencies` to proceed.\n", command)
		return
	}
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
	SubscribeWorktreeSetup(context.Context, *worktreepb.SetupSubscribeRequest) (apicontract.WorktreeSetupSubscription, error)
}

const workflowTaskSetupObservationTimeout = 2 * time.Minute

type worktreeSetupObservation struct {
	cancel context.CancelCauseFunc
	done   <-chan worktreeSetupObservationResult
}

type worktreeSetupObservationResult struct {
	terminal *worktreepb.SetupEvent
	err      error
}

type worktreeSetupObservationError struct{ cause error }

func (e *worktreeSetupObservationError) Error() string { return e.cause.Error() }
func (e *worktreeSetupObservationError) Unwrap() error { return e.cause }

type taskSetupActionKind string

const (
	taskSetupActionRetry         taskSetupActionKind = "retry"
	taskSetupActionChooseNone    taskSetupActionKind = "choose_none"
	taskSetupActionChooseHead    taskSetupActionKind = "choose_head"
	taskSetupActionChooseDefault taskSetupActionKind = "choose_default"
	taskSetupActionChooseRef     taskSetupActionKind = "choose_ref"
	taskSetupActionInspect       taskSetupActionKind = "inspect"
	taskSetupActionListWorktrees taskSetupActionKind = "list_worktrees"
	taskSetupActionMove          taskSetupActionKind = "move"
)

type taskSetupAction struct {
	Kind taskSetupActionKind
	Args []string
}

type taskSetupOutcomeKind string

type taskSetupObservedActionKind string

const (
	taskSetupOutcomeCompleted, taskSetupOutcomeObservedSetupFailure, taskSetupOutcomeObservationFailure      taskSetupOutcomeKind        = "completed", "observed_setup_failure", "observation_failure"
	taskSetupOutcomeStartInterruptedSetupFailure, taskSetupOutcomeStartInterruptedTargetPreparationFailure   taskSetupOutcomeKind        = "start_interrupted_setup_failure", "start_interrupted_target_preparation_failure"
	taskSetupOutcomeResumeInterruptedSetupFailure, taskSetupOutcomeResumeInterruptedTargetPreparationFailure taskSetupOutcomeKind        = "resume_interrupted_setup_failure", "resume_interrupted_target_preparation_failure"
	taskSetupOutcomeAlreadyStartedConflict, taskSetupOutcomeMoveSetupFailure                                 taskSetupOutcomeKind        = "already_started_conflict", "move_setup_failure"
	taskSetupObservedActionStart, taskSetupObservedActionResume                                              taskSetupObservedActionKind = "start", "resume"
)

type taskSetupGuidance struct {
	Outcome              taskSetupOutcomeKind
	Diagnostic           *string
	ScriptPath           *string
	RetainedRoot         *string
	RetainedPreviousRoot *string
	Actions              []taskSetupAction
}

func projectTaskSetupGuidance(action taskSetupObservedActionKind, taskRef string, projectRef *string, terminal *worktreepb.SetupEvent, observationErr error) (taskSetupGuidance, error) {
	if action != taskSetupObservedActionStart && action != taskSetupObservedActionResume {
		return taskSetupGuidance{}, fmt.Errorf("invalid observed Task setup action %q", action)
	}
	base := []string{config.Command, "task", "resume", taskRef}
	inspect := []string{config.Command, "task", "show", taskRef}
	if projectRef != nil {
		base = append(base, "--project", *projectRef)
		inspect = append(inspect, "--project", *projectRef)
	}
	inspection := []taskSetupAction{{Kind: taskSetupActionInspect, Args: inspect}, {Kind: taskSetupActionListWorktrees, Args: []string{config.Command, "worktree", "list"}}}
	if observationErr != nil {
		diagnostic, err := taskSetupDiagnostic(observationErr.Error())
		return taskSetupGuidance{Outcome: taskSetupOutcomeObservationFailure, Diagnostic: diagnostic, Actions: inspection}, err
	}
	if terminal == nil {
		return taskSetupGuidance{}, errors.New("Worktree Setup observation ended without a terminal result")
	}
	var retained *worktreepb.RetainedPreviousWorktree
	switch {
	case terminal.GetCompleted() != nil:
		retained = terminal.GetCompleted().GetRetainedPreviousWorktree()
	case terminal.GetNotRequired() != nil:
		retained = terminal.GetNotRequired().GetRetainedPreviousWorktree()
	case terminal.GetFailed() != nil:
		failed := terminal.GetFailed()
		diagnostic, err := taskSetupDiagnostic(failed.Diagnostic)
		if err != nil {
			return taskSetupGuidance{}, err
		}
		result := taskSetupGuidance{Outcome: taskSetupOutcomeObservedSetupFailure, Diagnostic: diagnostic, ScriptPath: failed.ScriptPath}
		if failed.RetainedWorktree != nil {
			result.RetainedRoot = &failed.RetainedWorktree.Git.CanonicalRoot
		}
		if failed.RetainedPreviousWorktree != nil {
			root := failed.RetainedPreviousWorktree.GetWorktree().GetGit().GetCanonicalRoot()
			result.RetainedPreviousRoot = &root
		}
		if failed.RetryReadiness != worktreepb.SetupRetryReadiness_WORKTREE_SETUP_RETRY_READY {
			result.Actions = inspection
			return result, nil
		}
		if failed.ExecutionTarget == nil {
			return taskSetupGuidance{}, errors.New("retry-ready Task setup failure requires execution target")
		}
		result.Outcome = taskSetupOutcomeStartInterruptedSetupFailure
		if action == taskSetupObservedActionResume {
			result.Outcome = taskSetupOutcomeResumeInterruptedSetupFailure
		}
		if failed.Cause.GetTargetPreparation() != nil {
			result.Outcome = taskSetupOutcomeStartInterruptedTargetPreparationFailure
			if action == taskSetupObservedActionResume {
				result.Outcome = taskSetupOutcomeResumeInterruptedTargetPreparationFailure
			}
		}
		selector, err := taskSetupExecutionTargetSelector(failed.ExecutionTarget)
		if err != nil {
			return taskSetupGuidance{}, err
		}
		result.Actions = taskTargetActions(base, &selector)
		return result, nil
	default:
		return taskSetupGuidance{}, errors.New("Worktree Setup observation returned a non-terminal phase")
	}
	result := taskSetupGuidance{Outcome: taskSetupOutcomeCompleted}
	if retained != nil {
		root := retained.GetWorktree().GetGit().GetCanonicalRoot()
		result.RetainedPreviousRoot = &root
		result.Actions = []taskSetupAction{{Kind: taskSetupActionListWorktrees, Args: []string{config.Command, "worktree", "list"}}}
	}
	return result, nil
}

func taskTargetActions(base []string, current *string) []taskSetupAction {
	choices := []struct {
		kind     taskSetupActionKind
		selector *string
	}{{taskSetupActionRetry, current}, {taskSetupActionChooseNone, taskSetupStringPointer("none")}, {taskSetupActionChooseHead, taskSetupStringPointer("head")}, {taskSetupActionChooseDefault, taskSetupStringPointer("default-branch")}, {taskSetupActionChooseRef, taskSetupStringPointer("ref:<revision>")}}
	actions := make([]taskSetupAction, 0, len(choices))
	for _, choice := range choices {
		args := append([]string(nil), base...)
		if choice.selector != nil {
			args = append(args, "--execution-target", *choice.selector)
		}
		actions = append(actions, taskSetupAction{Kind: choice.kind, Args: args})
	}
	return actions
}

func taskAlreadyStartedGuidance(taskRef string, project *string) taskSetupGuidance {
	resume := []string{config.Command, "task", "resume", taskRef}
	move := []string{config.Command, "task", "move", taskRef, "<target-node-id>"}
	if project != nil {
		resume = append(resume, "--project", *project)
		move = append(move, "--project", *project)
	}
	return taskSetupGuidance{Outcome: taskSetupOutcomeAlreadyStartedConflict, Actions: []taskSetupAction{{Kind: taskSetupActionRetry, Args: resume}, {Kind: taskSetupActionMove, Args: move}}}
}

func taskMoveRecoveryArgs(taskRef, targetNode string, project, commentary, transition *string, values map[string]map[string]string, ignore, jsonOutput bool) ([]string, error) {
	args := []string{config.Command, "task", "move", taskRef, targetNode}
	for _, option := range []struct {
		flag  string
		value *string
	}{{"--project", project}, {"--commentary", commentary}, {"--transition", transition}} {
		if option.value != nil {
			args = append(args, option.flag, *option.value)
		}
	}
	if len(values) != 0 {
		encoded, err := json.Marshal(values)
		if err != nil {
			return nil, err
		}
		args = append(args, "--values-json", string(encoded))
	}
	if ignore {
		args = append(args, "--ignore-dependencies")
	}
	if jsonOutput {
		args = append(args, "--json")
	}
	return args, nil
}

func projectMoveSetupGuidance(base []string, target *serverapi.WorkflowExecutionTargetSelection, setupErr *serverapi.WorkflowSetupRetainedError) (taskSetupGuidance, error) {
	if err := setupErr.Validate(); err != nil {
		return taskSetupGuidance{}, err
	}
	var selector *string
	if target != nil {
		value, err := taskExecutionTargetSelector(*target)
		if err != nil {
			return taskSetupGuidance{}, err
		}
		selector = &value
	}
	script := setupErr.ScriptPath
	diagnostic := setupErr.Diagnostic
	root := setupErr.Worktree.Registered.Git.CanonicalRoot
	var previousRoot *string
	if retained := setupErr.RetainedPreviousWorktree; retained != nil && retained.Worktree.Registered != nil {
		value := retained.Worktree.Registered.Git.CanonicalRoot
		previousRoot = &value
	}
	return taskSetupGuidance{Outcome: taskSetupOutcomeMoveSetupFailure, Diagnostic: &diagnostic, ScriptPath: &script, RetainedRoot: &root, RetainedPreviousRoot: previousRoot, Actions: taskTargetActions(base, selector)}, nil
}

func taskSetupStringPointer(value string) *string { return &value }

func taskSetupDiagnostic(value string) (*string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, errors.New("Task setup diagnostic must not be blank")
	}
	return &value, nil
}

func taskExecutionTargetSelector(target serverapi.WorkflowExecutionTargetSelection) (string, error) {
	switch target.Mode {
	case serverapi.WorkflowExecutionTargetModeNone:
		return "none", nil
	case serverapi.WorkflowExecutionTargetModeHead:
		return "head", nil
	case serverapi.WorkflowExecutionTargetModeDefaultBranch:
		return "default-branch", nil
	case serverapi.WorkflowExecutionTargetModeCustomRef:
		if target.CustomRef != nil {
			return "ref:" + *target.CustomRef, nil
		}
	}
	return "", errors.New("invalid recovery execution target")
}

func taskSetupExecutionTargetSelector(target *worktreepb.SetupExecutionTargetSelection) (string, error) {
	if target == nil {
		return "", errors.New("invalid recovery execution target")
	}
	switch target.Mode {
	case worktreepb.SetupExecutionTargetMode_WORKTREE_SETUP_EXECUTION_TARGET_MODE_NONE:
		return "none", nil
	case worktreepb.SetupExecutionTargetMode_WORKTREE_SETUP_EXECUTION_TARGET_MODE_HEAD:
		return "head", nil
	case worktreepb.SetupExecutionTargetMode_WORKTREE_SETUP_EXECUTION_TARGET_MODE_DEFAULT_BRANCH:
		return "default-branch", nil
	case worktreepb.SetupExecutionTargetMode_WORKTREE_SETUP_EXECUTION_TARGET_MODE_CUSTOM_REF:
		if target.CustomRef != nil {
			return "ref:" + *target.CustomRef, nil
		}
	}
	return "", errors.New("invalid recovery execution target")
}

func runWorkflowMutationWithSetupProgress[T any](
	ctx context.Context,
	remote apicontract.WorkflowService,
	stderr io.Writer,
	mutate func(context.Context, serverapi.WorkflowSetupOperationID) (T, error),
	shouldWait func(T) bool,
) (T, *worktreepb.SetupEvent, error) {
	setupOperationID := serverapi.NewWorkflowSetupOperationID()
	observation, err := subscribeWorktreeSetupProgress(ctx, remote, setupOperationID, stderr)
	if err != nil {
		var zero T
		return zero, nil, &worktreeSetupObservationError{cause: fmt.Errorf("subscribe to Worktree Setup operation: %w", err)}
	}
	defer observation.cancel(context.Canceled)
	resp, mutateErr := mutate(ctx, setupOperationID)
	if mutateErr != nil || !shouldWait(resp) {
		observation.cancel(context.Canceled)
		<-observation.done
		return resp, nil, mutateErr
	}
	timer := time.NewTimer(workflowTaskSetupObservationTimeout)
	defer timer.Stop()
	select {
	case result := <-observation.done:
		if result.err != nil {
			return resp, nil, &worktreeSetupObservationError{cause: result.err}
		}
		return resp, result.terminal, nil
	case <-timer.C:
		observation.cancel(context.DeadlineExceeded)
		<-observation.done
		return resp, nil, &worktreeSetupObservationError{cause: context.DeadlineExceeded}
	}
}

func subscribeWorktreeSetupProgress(ctx context.Context, remote apicontract.WorkflowService, setupOperationID serverapi.WorkflowSetupOperationID, stderr io.Writer) (worktreeSetupObservation, error) {
	subscriber, ok := remote.(worktreeSetupProgressSubscriber)
	if !ok {
		return worktreeSetupObservation{}, errors.New("worktree setup progress subscription is unavailable")
	}
	observationCtx, cancel := context.WithCancelCause(ctx)
	subscription, err := subscriber.SubscribeWorktreeSetup(observationCtx, &worktreepb.SetupSubscribeRequest{SetupOperationId: setupOperationID.Domain().String()})
	if err != nil {
		cancel(context.Canceled)
		return worktreeSetupObservation{}, err
	}
	done := make(chan worktreeSetupObservationResult, 1)
	go func() {
		defer func() { _ = subscription.Close() }()
		for {
			event, err := subscription.Next(observationCtx)
			if err != nil {
				if errors.Is(err, io.EOF) {
					done <- worktreeSetupObservationResult{err: io.ErrUnexpectedEOF}
					return
				}
				if cause := context.Cause(observationCtx); cause != nil {
					done <- worktreeSetupObservationResult{err: cause}
				} else {
					done <- worktreeSetupObservationResult{err: err}
				}
				return
			}
			if event.GetSetupOperationId() != setupOperationID.Domain().String() {
				done <- worktreeSetupObservationResult{err: errors.New("worktree setup event operation ID does not match subscription")}
				return
			}
			writeWorktreeSetupProgress(stderr, event)
			if event.GetCompleted() != nil ||
				event.GetNotRequired() != nil ||
				event.GetFailed() != nil {
				done <- worktreeSetupObservationResult{terminal: event}
				return
			}
		}
	}()
	return worktreeSetupObservation{cancel: cancel, done: done}, nil
}

func finishObservedTaskSetup(action taskSetupObservedActionKind, stderr io.Writer, taskRef string, projectRef *string, terminal *worktreepb.SetupEvent) bool {
	guidance, err := projectTaskSetupGuidance(action, taskRef, projectRef, terminal, nil)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return false
	}
	renderTaskSetupGuidance(stderr, guidance)
	return guidance.Outcome == taskSetupOutcomeCompleted
}

func writeTaskSetupObservationError(action taskSetupObservedActionKind, stderr io.Writer, taskRef string, projectRef *string, err error) bool {
	var observationErr *worktreeSetupObservationError
	if !errors.As(err, &observationErr) {
		return false
	}
	guidance, projectionErr := projectTaskSetupGuidance(action, taskRef, projectRef, nil, observationErr)
	if projectionErr != nil {
		fmt.Fprintln(stderr, projectionErr)
		return true
	}
	renderTaskSetupGuidance(stderr, guidance)
	return true
}

func renderTaskSetupGuidance(stderr io.Writer, guidance taskSetupGuidance) {
	switch guidance.Outcome {
	case taskSetupOutcomeCompleted, taskSetupOutcomeObservationFailure:
	case taskSetupOutcomeStartInterruptedSetupFailure, taskSetupOutcomeStartInterruptedTargetPreparationFailure:
		fmt.Fprintln(stderr, "The Task was started and is now interrupted.")
	case taskSetupOutcomeResumeInterruptedSetupFailure, taskSetupOutcomeResumeInterruptedTargetPreparationFailure:
		fmt.Fprintln(stderr, "The Task was resumed and is now interrupted.")
	case taskSetupOutcomeObservedSetupFailure:
		fmt.Fprintln(stderr, "Worktree setup failed.")
	case taskSetupOutcomeAlreadyStartedConflict:
		fmt.Fprintln(stderr, "The Task is already started. Resume it if interrupted; otherwise move it.")
	case taskSetupOutcomeMoveSetupFailure:
		fmt.Fprintln(stderr, "Worktree setup failed.")
		fmt.Fprintln(stderr, "The move was not applied.")
	default:
		panic(fmt.Sprintf("render Task setup guidance with invalid outcome %q", guidance.Outcome))
	}
	if guidance.ScriptPath != nil {
		fmt.Fprintln(stderr, *guidance.ScriptPath)
	}
	if guidance.Diagnostic != nil {
		fmt.Fprintln(stderr, *guidance.Diagnostic)
	}
	if guidance.RetainedRoot != nil {
		fmt.Fprintf(stderr, "Retained Worktree: %s\n", *guidance.RetainedRoot)
	}
	if guidance.RetainedPreviousRoot != nil {
		renderRetainedWorktreeRootGuidance(stderr, *guidance.RetainedPreviousRoot)
	}
	for _, action := range guidance.Actions {
		fmt.Fprintf(stderr, "  %s\n", commandString(action.Args))
	}
}

func renderRetainedWorktreeGuidance(stderr io.Writer, retained *worktreepb.RetainedPreviousWorktree) {
	if retained == nil {
		return
	}
	renderRetainedWorktreeRootGuidance(stderr, retained.GetWorktree().GetGit().GetCanonicalRoot())
}

func renderRetainedWorktreeRootGuidance(stderr io.Writer, root string) {
	fmt.Fprintf(stderr, "Warning: previous Worktree retained at %s\n  %s\n", root, commandString([]string{config.Command, "worktree", "list"}))
}

func renderWorkflowRetainedWorktreeGuidance(stderr io.Writer, retained *serverapi.WorkflowRetainedPreviousWorktree) {
	if retained == nil || retained.Worktree.Registered == nil {
		return
	}
	root := retained.Worktree.Registered.Git.CanonicalRoot
	fmt.Fprintf(stderr, "Warning: previous Worktree retained at %s\n  %s\n", root, commandString([]string{config.Command, "worktree", "list"}))
}

func writeWorktreeSetupProgress(stderr io.Writer, event *worktreepb.SetupEvent) {
	if event.GetStarted() == nil {
		return
	}
	fmt.Fprintf(stderr, "Waiting for worktree setup script %s in %s.\n", event.GetStarted().GetScriptPath(), event.GetStarted().GetWorktreeRoot())
}

func waitForWorkflowTaskRunSession(ctx context.Context, remote apicontract.WorkflowService, taskID string, _ string, timeout time.Duration, interval time.Duration) (serverapi.WorkflowTaskDetail, error) {
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
		if len(detail.CurrentScripts) > 0 || len(detail.LiveSessions) > 0 {
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
