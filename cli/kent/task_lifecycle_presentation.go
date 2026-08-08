package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"core/shared/config"
	"core/shared/serverapi"
)

type taskLifecycleOperation string

const (
	taskLifecycleOperationStart  taskLifecycleOperation = "start"
	taskLifecycleOperationResume taskLifecycleOperation = "resume"
	taskLifecycleOperationMove   taskLifecycleOperation = "move"
)

type taskLifecyclePresentationKind string

const (
	taskLifecyclePresentationSetupRecovery       taskLifecyclePresentationKind = "setup_recovery"
	taskLifecyclePresentationSetupFailed         taskLifecyclePresentationKind = "setup_failed"
	taskLifecyclePresentationObservationFailed   taskLifecyclePresentationKind = "observation_failed"
	taskLifecyclePresentationObservationTimedOut taskLifecyclePresentationKind = "observation_timed_out"
	taskLifecyclePresentationRetainedWorktree    taskLifecyclePresentationKind = "retained_worktree"
	taskLifecyclePresentationAlreadyStarted      taskLifecyclePresentationKind = "already_started"
)

type taskLifecycleActionKind string

const (
	taskLifecycleActionRetryCurrentTarget  taskLifecycleActionKind = "retry_current_target"
	taskLifecycleActionChooseNoWorktree    taskLifecycleActionKind = "choose_no_worktree"
	taskLifecycleActionChooseHead          taskLifecycleActionKind = "choose_head"
	taskLifecycleActionChooseDefaultBranch taskLifecycleActionKind = "choose_default_branch"
	taskLifecycleActionChooseCustomRef     taskLifecycleActionKind = "choose_custom_ref"
	taskLifecycleActionInspectTask         taskLifecycleActionKind = "inspect_task"
	taskLifecycleActionListWorktrees       taskLifecycleActionKind = "list_worktrees"
	taskLifecycleActionDeleteWorktree      taskLifecycleActionKind = "delete_worktree"
)

type taskLifecycleGuidanceKind string

const taskLifecycleGuidanceMoveTask taskLifecycleGuidanceKind = "move_task"

type taskLifecycleAction struct {
	Kind taskLifecycleActionKind
	Args []string
}

type taskLifecycleWorktree struct {
	Path             string
	DeletionSelector string
}

type taskLifecycleCommandContext struct {
	TaskRef    string
	ProjectRef *string
	ResumeArgs []string
	Move       *taskMoveRecoveryCommand
}

type taskMoveRecoveryCommand struct {
	TaskRef                    string
	TargetNodeID               string
	ProjectRef                 *string
	Commentary                 *string
	TransitionKey              *string
	ValuesJSON                 *string
	ProceedDespiteDependencies bool
	JSON                       bool
	CurrentExecutionTarget     *string
}

type taskLifecyclePresentation struct {
	Kind                     taskLifecyclePresentationKind
	Operation                taskLifecycleOperation
	TaskRef                  string
	Diagnostic               string
	SetupScriptPath          *string
	RetainedWorktree         *taskLifecycleWorktree
	RetainedPreviousWorktree *taskLifecycleWorktree
	Actions                  []taskLifecycleAction
	Guidance                 []taskLifecycleGuidanceKind
}

type taskLifecycleSetupOutcome struct {
	Success      bool
	Presentation *taskLifecyclePresentation
}

func projectTaskLifecycleSetupOutcome(
	operation taskLifecycleOperation,
	command taskLifecycleCommandContext,
	terminal *worktreeSetupTerminalObservation,
	observationErr error,
) (taskLifecycleSetupOutcome, error) {
	if observationErr != nil {
		presentation := taskLifecycleObservationPresentation(operation, command, observationErr)
		return taskLifecycleSetupOutcome{Presentation: &presentation}, nil
	}
	if terminal == nil {
		return taskLifecycleSetupOutcome{}, errors.New("applied lifecycle action requires a terminal Worktree Setup result")
	}
	if err := terminal.Event.Validate(); err != nil {
		return taskLifecycleSetupOutcome{}, fmt.Errorf("invalid Worktree Setup result: %w", err)
	}
	var retained *serverapi.RetainedPreviousWorktree
	switch terminal.Event.Phase {
	case serverapi.WorktreeSetupPhaseCompleted:
		retained = terminal.Event.Completed.RetainedPreviousWorktree
	case serverapi.WorktreeSetupPhaseNotRequired:
		retained = terminal.Event.NotRequired.RetainedPreviousWorktree
	case serverapi.WorktreeSetupPhaseFailed:
		presentation, err := taskLifecycleSetupPresentation(operation, command, *terminal)
		if err != nil {
			return taskLifecycleSetupOutcome{}, err
		}
		return taskLifecycleSetupOutcome{Presentation: &presentation}, nil
	default:
		return taskLifecycleSetupOutcome{}, errors.New("terminal Worktree Setup result has a non-terminal phase")
	}
	if retained == nil {
		return taskLifecycleSetupOutcome{Success: true}, nil
	}
	presentation, err := taskLifecycleRetainedWorktreePresentation(operation, command.TaskRef, retained)
	if err != nil {
		return taskLifecycleSetupOutcome{}, err
	}
	return taskLifecycleSetupOutcome{Success: true, Presentation: &presentation}, nil
}

func taskLifecycleRetainedWorktreePresentation(
	operation taskLifecycleOperation,
	taskRef string,
	retained *serverapi.RetainedPreviousWorktree,
) (taskLifecyclePresentation, error) {
	if retained == nil {
		return taskLifecyclePresentation{}, errors.New("retained previous worktree is required")
	}
	worktree, err := taskLifecycleWorktreeFromTopology(&retained.Worktree)
	if err != nil {
		return taskLifecyclePresentation{}, err
	}
	return taskLifecyclePresentation{
		Kind:                     taskLifecyclePresentationRetainedWorktree,
		Operation:                operation,
		TaskRef:                  taskRef,
		RetainedPreviousWorktree: worktree,
		Actions:                  retainedWorktreeInspectionActions(worktree),
	}, nil
}

func taskLifecycleSetupPresentation(
	operation taskLifecycleOperation,
	command taskLifecycleCommandContext,
	terminal worktreeSetupTerminalObservation,
) (taskLifecyclePresentation, error) {
	event := terminal.Event
	if err := event.Validate(); err != nil {
		return taskLifecyclePresentation{}, fmt.Errorf("invalid Worktree Setup result: %w", err)
	}
	if event.Phase != serverapi.WorktreeSetupPhaseFailed || event.Failed == nil {
		return taskLifecyclePresentation{}, errors.New("setup recovery presentation requires a failed setup event")
	}
	failed := event.Failed
	presentation := taskLifecyclePresentation{
		Kind:       taskLifecyclePresentationSetupRecovery,
		Operation:  operation,
		TaskRef:    command.TaskRef,
		Diagnostic: failed.Diagnostic,
	}
	if terminal.LastStarted != nil {
		scriptPath := terminal.LastStarted.ScriptPath
		presentation.SetupScriptPath = &scriptPath
	}
	var err error
	presentation.RetainedWorktree, err = taskLifecycleWorktreeFromTopology(failed.RetainedWorktree)
	if err != nil {
		return taskLifecyclePresentation{}, err
	}
	if failed.RetainedPreviousWorktree != nil {
		presentation.RetainedPreviousWorktree, err = taskLifecycleWorktreeFromTopology(&failed.RetainedPreviousWorktree.Worktree)
		if err != nil {
			return taskLifecyclePresentation{}, err
		}
	}
	if failed.RetryReadiness != serverapi.WorktreeSetupRetryReady {
		presentation.Kind = taskLifecyclePresentationSetupFailed
		presentation.Actions = taskLifecycleInspectionActions(command.TaskRef, command.ProjectRef)
		presentation.Actions = append(
			presentation.Actions,
			retainedWorktreeInspectionActions(presentation.RetainedPreviousWorktree)...,
		)
		return presentation, nil
	}
	switch operation {
	case taskLifecycleOperationStart, taskLifecycleOperationResume:
		if failed.ExecutionTarget == nil {
			return taskLifecyclePresentation{}, errors.New("Task setup recovery requires the failed execution target selection")
		}
		if len(command.ResumeArgs) == 0 {
			command.ResumeArgs = []string{config.Command, "task", "resume", command.TaskRef}
		}
		presentation.Actions, err = taskResumeRecoveryActions(command.ResumeArgs, *failed.ExecutionTarget)
		if err != nil {
			return taskLifecyclePresentation{}, err
		}
	case taskLifecycleOperationMove:
		if command.Move == nil {
			return taskLifecyclePresentation{}, errors.New("Move setup recovery requires original command context")
		}
		presentation.Actions = taskMoveRecoveryActions(*command.Move)
	default:
		return taskLifecyclePresentation{}, errors.New("setup recovery operation is invalid")
	}
	presentation.Actions = append(
		presentation.Actions,
		retainedWorktreeInspectionActions(presentation.RetainedPreviousWorktree)...,
	)
	return presentation, nil
}

func taskLifecycleWorktreeFromTopology(topology *serverapi.WorktreeTopologyEntry) (*taskLifecycleWorktree, error) {
	if topology == nil {
		return nil, nil
	}
	if err := topology.Validate(); err != nil {
		return nil, fmt.Errorf("invalid retained worktree: %w", err)
	}
	if topology.Variant != serverapi.WorktreeTopologyVariantRegistered || topology.Registered == nil {
		return nil, errors.New("retained worktree must be registered")
	}
	selector, err := topology.DeletionSelector()
	if err != nil {
		return nil, fmt.Errorf("resolve retained worktree deletion selector: %w", err)
	}
	return &taskLifecycleWorktree{
		Path:             topology.Registered.Git.CanonicalRoot,
		DeletionSelector: selector,
	}, nil
}

func taskResumeRecoveryActions(
	base []string,
	executionTarget serverapi.WorkflowExecutionTargetSelection,
) ([]taskLifecycleAction, error) {
	selector, err := taskExecutionTargetSelector(executionTarget)
	if err != nil {
		return nil, err
	}
	return []taskLifecycleAction{
		{Kind: taskLifecycleActionRetryCurrentTarget, Args: append(append([]string(nil), base...), "--execution-target", selector)},
		{Kind: taskLifecycleActionChooseNoWorktree, Args: append(append([]string(nil), base...), "--execution-target", "none")},
		{Kind: taskLifecycleActionChooseHead, Args: append(append([]string(nil), base...), "--execution-target", "head")},
		{Kind: taskLifecycleActionChooseDefaultBranch, Args: append(append([]string(nil), base...), "--execution-target", "default-branch")},
		{Kind: taskLifecycleActionChooseCustomRef, Args: append(append([]string(nil), base...), "--execution-target", "ref:<revision>")},
	}, nil
}

func taskMoveRecoveryActions(command taskMoveRecoveryCommand) []taskLifecycleAction {
	base := command.argsWithoutExecutionTarget()
	currentArgs := taskMoveArgsWithExecutionTarget(base, command.CurrentExecutionTarget)
	return []taskLifecycleAction{
		{Kind: taskLifecycleActionRetryCurrentTarget, Args: append([]string(nil), currentArgs...)},
		{Kind: taskLifecycleActionChooseNoWorktree, Args: taskMoveArgsWithExecutionTarget(base, lifecycleStringPointer("none"))},
		{Kind: taskLifecycleActionChooseHead, Args: taskMoveArgsWithExecutionTarget(base, lifecycleStringPointer("head"))},
		{Kind: taskLifecycleActionChooseDefaultBranch, Args: taskMoveArgsWithExecutionTarget(base, lifecycleStringPointer("default-branch"))},
		{Kind: taskLifecycleActionChooseCustomRef, Args: taskMoveArgsWithExecutionTarget(base, lifecycleStringPointer("ref:<revision>"))},
	}
}

func (command taskMoveRecoveryCommand) argsWithoutExecutionTarget() []string {
	args := []string{config.Command, "task", "move", command.TaskRef, command.TargetNodeID}
	if command.ProjectRef != nil {
		args = append(args, "--project", *command.ProjectRef)
	}
	if command.Commentary != nil {
		args = append(args, "--commentary", *command.Commentary)
	}
	if command.TransitionKey != nil {
		args = append(args, "--transition", *command.TransitionKey)
	}
	if command.ValuesJSON != nil {
		args = append(args, "--values-json", *command.ValuesJSON)
	}
	if command.ProceedDespiteDependencies {
		args = append(args, "--ignore-dependencies")
	}
	if command.JSON {
		args = append(args, "--json")
	}
	return args
}

func taskMoveArgsWithExecutionTarget(base []string, selector *string) []string {
	args := append([]string(nil), base...)
	if selector != nil {
		args = append(args, "--execution-target", *selector)
	}
	return args
}

func lifecycleStringPointer(value string) *string {
	return &value
}

func taskLifecycleInspectionActions(taskRef string, projectRef *string) []taskLifecycleAction {
	inspectArgs := []string{config.Command, "task", "show", taskRef}
	if projectRef != nil {
		inspectArgs = append(inspectArgs, "--project", *projectRef)
	}
	return []taskLifecycleAction{
		{Kind: taskLifecycleActionInspectTask, Args: inspectArgs},
		{Kind: taskLifecycleActionListWorktrees, Args: []string{config.Command, "worktree", "list"}},
	}
}

func retainedWorktreeInspectionActions(retained *taskLifecycleWorktree) []taskLifecycleAction {
	if retained == nil {
		return nil
	}
	return []taskLifecycleAction{
		{Kind: taskLifecycleActionListWorktrees, Args: []string{config.Command, "worktree", "list"}},
		{
			Kind: taskLifecycleActionDeleteWorktree,
			Args: []string{
				config.Command,
				"worktree",
				"delete",
				"--session",
				"<session-id>",
				retained.DeletionSelector,
			},
		},
	}
}

func taskMoveSetupFailurePresentation(
	command taskLifecycleCommandContext,
	setupErr *serverapi.WorktreeSetupRetainedError,
) (taskLifecyclePresentation, error) {
	if setupErr == nil {
		return taskLifecyclePresentation{}, errors.New("Move retained setup error is required")
	}
	if err := setupErr.Validate(); err != nil {
		return taskLifecyclePresentation{}, fmt.Errorf("invalid Move retained setup error: %w", err)
	}
	if command.Move == nil {
		return taskLifecyclePresentation{}, errors.New("Move setup recovery requires original command context")
	}
	retained, err := taskLifecycleWorktreeFromTopology(&setupErr.Worktree)
	if err != nil {
		return taskLifecyclePresentation{}, err
	}
	presentation := taskLifecyclePresentation{
		Kind:             taskLifecyclePresentationSetupRecovery,
		Operation:        taskLifecycleOperationMove,
		TaskRef:          command.TaskRef,
		Diagnostic:       setupErr.Diagnostic,
		RetainedWorktree: retained,
		Actions:          taskMoveRecoveryActions(*command.Move),
	}
	scriptPath := setupErr.ScriptPath
	presentation.SetupScriptPath = &scriptPath
	if setupErr.RetainedPreviousWorktree != nil {
		presentation.RetainedPreviousWorktree, err = taskLifecycleWorktreeFromTopology(
			&setupErr.RetainedPreviousWorktree.Worktree,
		)
		if err != nil {
			return taskLifecyclePresentation{}, err
		}
	}
	presentation.Actions = append(
		presentation.Actions,
		retainedWorktreeInspectionActions(presentation.RetainedPreviousWorktree)...,
	)
	return presentation, nil
}

func optionalTaskLifecycleString(value string, provided bool) *string {
	if !provided {
		return nil
	}
	result := value
	return &result
}

func newTaskMoveRecoveryCommand(
	taskRef string,
	targetNodeID string,
	projectRef *string,
	commentary *string,
	transitionKey *string,
	values map[string]map[string]string,
	proceedDespiteDependencies bool,
	jsonOutput bool,
	executionTarget *serverapi.WorkflowExecutionTargetSelection,
) (taskMoveRecoveryCommand, error) {
	var valuesJSON *string
	if values != nil {
		encoded, err := json.Marshal(values)
		if err != nil {
			return taskMoveRecoveryCommand{}, fmt.Errorf("encode Move recovery values: %w", err)
		}
		value := string(encoded)
		valuesJSON = &value
	}
	var selector *string
	if executionTarget != nil {
		value, err := taskExecutionTargetSelector(*executionTarget)
		if err != nil {
			return taskMoveRecoveryCommand{}, err
		}
		selector = &value
	}
	return taskMoveRecoveryCommand{
		TaskRef:                    taskRef,
		TargetNodeID:               targetNodeID,
		ProjectRef:                 projectRef,
		Commentary:                 commentary,
		TransitionKey:              transitionKey,
		ValuesJSON:                 valuesJSON,
		ProceedDespiteDependencies: proceedDespiteDependencies,
		JSON:                       jsonOutput,
		CurrentExecutionTarget:     selector,
	}, nil
}

func taskExecutionTargetSelector(selection serverapi.WorkflowExecutionTargetSelection) (string, error) {
	if err := selection.Validate(); err != nil {
		return "", err
	}
	switch selection.Mode {
	case serverapi.WorkflowExecutionTargetModeNone:
		return "none", nil
	case serverapi.WorkflowExecutionTargetModeHead:
		return "head", nil
	case serverapi.WorkflowExecutionTargetModeDefaultBranch:
		return "default-branch", nil
	case serverapi.WorkflowExecutionTargetModeCustomRef:
		return "ref:" + *selection.CustomRef, nil
	default:
		return "", errors.New("Move recovery execution target is invalid")
	}
}

func taskResumeCommandArgs(taskRef string, projectRef *string) []string {
	args := []string{config.Command, "task", "resume", taskRef}
	if projectRef != nil {
		args = append(args, "--project", *projectRef)
	}
	return args
}

func taskLifecycleAlreadyStartedPresentation(taskRef string, projectRef *string) taskLifecyclePresentation {
	resumeArgs := taskResumeCommandArgs(taskRef, projectRef)
	return taskLifecyclePresentation{
		Kind:      taskLifecyclePresentationAlreadyStarted,
		Operation: taskLifecycleOperationStart,
		TaskRef:   taskRef,
		Actions: []taskLifecycleAction{
			{Kind: taskLifecycleActionRetryCurrentTarget, Args: resumeArgs},
		},
		Guidance: []taskLifecycleGuidanceKind{taskLifecycleGuidanceMoveTask},
	}
}

func taskLifecycleObservationPresentation(
	operation taskLifecycleOperation,
	command taskLifecycleCommandContext,
	observationErr error,
) taskLifecyclePresentation {
	kind := taskLifecyclePresentationObservationFailed
	if errors.Is(observationErr, context.DeadlineExceeded) {
		kind = taskLifecyclePresentationObservationTimedOut
	}
	return taskLifecyclePresentation{
		Kind:       kind,
		Operation:  operation,
		TaskRef:    command.TaskRef,
		Diagnostic: observationErr.Error(),
		Actions:    taskLifecycleInspectionActions(command.TaskRef, command.ProjectRef),
	}
}

func renderTaskLifecyclePresentation(stderr io.Writer, presentation taskLifecyclePresentation) {
	switch presentation.Kind {
	case taskLifecyclePresentationSetupRecovery:
		fmt.Fprintf(stderr, "Worktree setup failed after one automatic retry during Task %s.\n", presentation.Operation)
		if presentation.SetupScriptPath != nil {
			fmt.Fprintf(stderr, "Setup script: %s\n", *presentation.SetupScriptPath)
		}
		fmt.Fprintf(stderr, "Final setup diagnostic: %s\n", presentation.Diagnostic)
		if presentation.RetainedWorktree != nil {
			fmt.Fprintf(stderr, "The retained worktree is at %s.\n", presentation.RetainedWorktree.Path)
		}
		switch presentation.Operation {
		case taskLifecycleOperationStart:
			fmt.Fprintln(stderr, "The Task was started and is now interrupted. Resume it with one of:")
		case taskLifecycleOperationResume:
			fmt.Fprintln(stderr, "The Task remains interrupted. Resume it with one of:")
		case taskLifecycleOperationMove:
			fmt.Fprintln(stderr, "The Move was not applied. Retry it with one of:")
		}
	case taskLifecyclePresentationObservationTimedOut:
		fmt.Fprintln(stderr, "Timed out while waiting for the Worktree Setup result. Inspect the Task and worktrees before retrying.")
	case taskLifecyclePresentationObservationFailed:
		fmt.Fprintf(stderr, "Worktree Setup observation failed: %s\n", presentation.Diagnostic)
	case taskLifecyclePresentationSetupFailed:
		fmt.Fprintf(stderr, "Task preparation failed: %s\n", presentation.Diagnostic)
		if presentation.RetainedWorktree != nil {
			fmt.Fprintf(stderr, "The retained worktree is at %s.\n", presentation.RetainedWorktree.Path)
		}
	case taskLifecyclePresentationRetainedWorktree:
		fmt.Fprintf(stderr, "Warning: a previous worktree was retained at %s.\n", presentation.RetainedPreviousWorktree.Path)
	case taskLifecyclePresentationAlreadyStarted:
		fmt.Fprintln(stderr, "The Task was already started. Resume it if interrupted, or move it to another Workflow node.")
	}
	if presentation.RetainedPreviousWorktree != nil &&
		presentation.Kind != taskLifecyclePresentationRetainedWorktree {
		fmt.Fprintf(
			stderr,
			"Warning: a previous worktree was retained at %s.\n",
			presentation.RetainedPreviousWorktree.Path,
		)
	}
	for _, action := range presentation.Actions {
		fmt.Fprintf(stderr, "  %s\n", commandString(action.Args))
	}
}
