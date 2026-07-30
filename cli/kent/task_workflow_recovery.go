package main

import (
	"errors"
	"fmt"
	"strings"

	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type taskListCommandContext struct {
	ProjectRef             string
	ResolvedProjectID      string
	SelectedWorkflowID     *string
	ColumnKeys             []string
	StatusKinds            []serverapi.WorkflowTaskStatusKind
	AttentionKinds         []serverapi.WorkflowTaskAttentionKind
	Sort                   []serverapi.WorkflowTaskListSort
	LabelSelectors         []string
	ExcludedLabelSelectors []string
	LabelMatch             *serverapi.WorkflowTaskNamedLabelFilterMode
	Unlabeled              bool
	Offset                 int
	Limit                  int
	JSON                   bool
}

type taskCreateCommandContext struct {
	ProjectRef         string
	ResolvedProjectID  string
	SelectedWorkflowID *string
	Title              string
	Body               string
	BodyFile           string
	SourceURL          string
	SourceWorkspace    string
	LabelSelectors     []string
	JSON               bool
}

type taskWorkflowRecoveryKind uint8

const (
	taskWorkflowRecoveryNoLinkedWorkflows taskWorkflowRecoveryKind = iota + 1
	taskWorkflowRecoveryWorkflowNotLinked
	taskWorkflowRecoveryWorkflowRequiredColumns
	taskWorkflowRecoveryAmbiguousWithoutDefault
)

type taskWorkflowRecoveryCommandKind string

const (
	taskWorkflowRecoveryCommandCreateWorkflow       taskWorkflowRecoveryCommandKind = "create_workflow"
	taskWorkflowRecoveryCommandLinkCreatedWorkflow  taskWorkflowRecoveryCommandKind = "link_created_workflow"
	taskWorkflowRecoveryCommandListWorkflows        taskWorkflowRecoveryCommandKind = "list_workflows"
	taskWorkflowRecoveryCommandLinkExistingWorkflow taskWorkflowRecoveryCommandKind = "link_existing_workflow"
	taskWorkflowRecoveryCommandListProjectWorkflows taskWorkflowRecoveryCommandKind = "list_project_workflows"
	taskWorkflowRecoveryCommandRetryTaskList        taskWorkflowRecoveryCommandKind = "retry_task_list"
	taskWorkflowRecoveryCommandRetryTaskCreate      taskWorkflowRecoveryCommandKind = "retry_task_create"
	taskWorkflowRecoveryCommandLinkSelectedWorkflow taskWorkflowRecoveryCommandKind = "link_selected_workflow"
	taskWorkflowRecoveryCommandSetDefaultWorkflow   taskWorkflowRecoveryCommandKind = "set_default_workflow"
)

type taskWorkflowRecoveryCommand struct {
	Kind taskWorkflowRecoveryCommandKind
	Args []string
}

type taskWorkflowRecovery struct {
	Kind               taskWorkflowRecoveryKind
	ProjectRef         string
	SelectedWorkflowID *string
	Commands           []taskWorkflowRecoveryCommand
}

type taskWorkflowRecoveryFailure struct {
	kind       taskWorkflowRecoveryKind
	projectID  string
	workflowID *runtimeids.WorkflowID
}

type taskWorkflowRecoveryContext struct {
	projectRef         string
	resolvedProjectID  string
	selectedWorkflowID *string
	list               *taskListCommandContext
	create             *taskCreateCommandContext
}

func taskListRecoveryForScopeError(scopeErr *serverapi.WorkflowTaskListScopeError, commandContext taskListCommandContext) (taskWorkflowRecovery, error) {
	if scopeErr == nil || scopeErr.ProjectID == nil {
		return taskWorkflowRecovery{}, errors.New("task list scope error with project context is required")
	}
	kind, err := taskListRecoveryKind(scopeErr.Reason)
	if err != nil {
		return taskWorkflowRecovery{}, err
	}
	failure := taskWorkflowRecoveryFailure{
		kind:       kind,
		projectID:  *scopeErr.ProjectID,
		workflowID: scopeErr.WorkflowID,
	}
	return taskWorkflowRecoveryForFailure(failure, taskWorkflowRecoveryContext{
		projectRef:         commandContext.ProjectRef,
		resolvedProjectID:  commandContext.ResolvedProjectID,
		selectedWorkflowID: commandContext.SelectedWorkflowID,
		list:               &commandContext,
	})
}

func taskCreateRecoveryForSelectionError(createErr *serverapi.WorkflowTaskCreateSelectionError, commandContext taskCreateCommandContext) (taskWorkflowRecovery, error) {
	if createErr == nil {
		return taskWorkflowRecovery{}, errors.New("task create selection error is required")
	}
	kind, err := taskCreateRecoveryKind(createErr.Reason)
	if err != nil {
		return taskWorkflowRecovery{}, err
	}
	failure := taskWorkflowRecoveryFailure{
		kind:       kind,
		projectID:  createErr.ProjectID,
		workflowID: createErr.WorkflowID,
	}
	return taskWorkflowRecoveryForFailure(failure, taskWorkflowRecoveryContext{
		projectRef:         commandContext.ProjectRef,
		resolvedProjectID:  commandContext.ResolvedProjectID,
		selectedWorkflowID: commandContext.SelectedWorkflowID,
		create:             &commandContext,
	})
}

func taskListRecoveryKind(reason serverapi.WorkflowTaskListScopeErrorReason) (taskWorkflowRecoveryKind, error) {
	switch reason {
	case serverapi.WorkflowTaskListScopeReasonNoLinkedWorkflows:
		return taskWorkflowRecoveryNoLinkedWorkflows, nil
	case serverapi.WorkflowTaskListScopeReasonWorkflowNotLinked:
		return taskWorkflowRecoveryWorkflowNotLinked, nil
	case serverapi.WorkflowTaskListScopeReasonWorkflowRequiredColumns:
		return taskWorkflowRecoveryWorkflowRequiredColumns, nil
	default:
		return 0, fmt.Errorf("unsupported task-list workflow recovery reason %q", reason)
	}
}

func taskCreateRecoveryKind(reason serverapi.WorkflowTaskCreateSelectionReason) (taskWorkflowRecoveryKind, error) {
	switch reason {
	case serverapi.WorkflowTaskCreateSelectionReasonNoLinkedWorkflows:
		return taskWorkflowRecoveryNoLinkedWorkflows, nil
	case serverapi.WorkflowTaskCreateSelectionReasonWorkflowNotLinked:
		return taskWorkflowRecoveryWorkflowNotLinked, nil
	case serverapi.WorkflowTaskCreateSelectionReasonAmbiguousWithoutDefault:
		return taskWorkflowRecoveryAmbiguousWithoutDefault, nil
	default:
		return 0, fmt.Errorf("unsupported task-create workflow recovery reason %q", reason)
	}
}

func taskWorkflowRecoveryForFailure(failure taskWorkflowRecoveryFailure, commandContext taskWorkflowRecoveryContext) (taskWorkflowRecovery, error) {
	if strings.TrimSpace(commandContext.projectRef) == "" || strings.TrimSpace(commandContext.resolvedProjectID) == "" {
		return taskWorkflowRecovery{}, errors.New("task workflow recovery requires project context")
	}
	if (commandContext.list == nil) == (commandContext.create == nil) {
		return taskWorkflowRecovery{}, errors.New("task workflow recovery requires exactly one command context")
	}
	if failure.projectID != commandContext.resolvedProjectID {
		return taskWorkflowRecovery{}, fmt.Errorf("task workflow recovery project %q does not match resolved project %q", failure.projectID, commandContext.resolvedProjectID)
	}
	var selectedWorkflowID *string
	if commandContext.selectedWorkflowID != nil {
		value := *commandContext.selectedWorkflowID
		selectedWorkflowID = &value
	}
	recovery := taskWorkflowRecovery{
		Kind:               failure.kind,
		ProjectRef:         commandContext.projectRef,
		SelectedWorkflowID: selectedWorkflowID,
	}
	listProjectWorkflows := taskWorkflowListProjectCommand(commandContext.projectRef)
	switch failure.kind {
	case taskWorkflowRecoveryNoLinkedWorkflows:
		if failure.workflowID != nil || selectedWorkflowID != nil {
			return taskWorkflowRecovery{}, errors.New("no-linked-workflows recovery cannot select a workflow")
		}
		recovery.Commands = append(
			taskWorkflowNoLinksPreparationCommands(commandContext.projectRef),
			commandContext.retryCommand(nil),
		)
	case taskWorkflowRecoveryWorkflowNotLinked:
		if selectedWorkflowID == nil || failure.workflowID == nil {
			return taskWorkflowRecovery{}, errors.New("workflow-not-linked recovery requires a selected workflow")
		}
		selected, err := workflowIDForCLI(*failure.workflowID)
		if err != nil {
			return taskWorkflowRecovery{}, err
		}
		if selected != *selectedWorkflowID {
			return taskWorkflowRecovery{}, fmt.Errorf("task workflow recovery workflow %q does not match selected workflow %q", selected, *selectedWorkflowID)
		}
		workflowPlaceholder := "<uuid>"
		recovery.Commands = []taskWorkflowRecoveryCommand{
			listProjectWorkflows,
			commandContext.retryCommand(&workflowPlaceholder),
			{Kind: taskWorkflowRecoveryCommandLinkSelectedWorkflow, Args: []string{config.Command, "workflow", "link", commandContext.projectRef, selected}},
		}
	case taskWorkflowRecoveryWorkflowRequiredColumns:
		if commandContext.list == nil || failure.workflowID != nil || selectedWorkflowID != nil {
			return taskWorkflowRecovery{}, errors.New("workflow-required-for-columns recovery requires project-wide task list context")
		}
		workflowPlaceholder := "<uuid>"
		recovery.Commands = []taskWorkflowRecoveryCommand{
			listProjectWorkflows,
			commandContext.retryCommand(&workflowPlaceholder),
		}
	case taskWorkflowRecoveryAmbiguousWithoutDefault:
		if commandContext.create == nil || failure.workflowID != nil || selectedWorkflowID != nil {
			return taskWorkflowRecovery{}, errors.New("ambiguous recovery requires task create context without a selected workflow")
		}
		workflowPlaceholder := "<uuid>"
		recovery.Commands = []taskWorkflowRecoveryCommand{
			listProjectWorkflows,
			commandContext.retryCommand(&workflowPlaceholder),
			{Kind: taskWorkflowRecoveryCommandSetDefaultWorkflow, Args: []string{config.Command, "workflow", "default", commandContext.projectRef, "<uuid>"}},
		}
	default:
		return taskWorkflowRecovery{}, fmt.Errorf("unsupported task workflow recovery kind %d", failure.kind)
	}
	return recovery, nil
}

func (c taskWorkflowRecoveryContext) retryCommand(workflowID *string) taskWorkflowRecoveryCommand {
	if c.list != nil {
		return taskWorkflowRecoveryCommand{
			Kind: taskWorkflowRecoveryCommandRetryTaskList,
			Args: taskListRetryCommandArgs(*c.list, workflowID),
		}
	}
	return taskWorkflowRecoveryCommand{
		Kind: taskWorkflowRecoveryCommandRetryTaskCreate,
		Args: taskCreateRetryCommandArgs(*c.create, workflowID),
	}
}

func taskWorkflowNoLinksPreparationCommands(projectRef string) []taskWorkflowRecoveryCommand {
	return []taskWorkflowRecoveryCommand{
		{Kind: taskWorkflowRecoveryCommandCreateWorkflow, Args: []string{config.Command, "workflow", "create", "<name>"}},
		{Kind: taskWorkflowRecoveryCommandLinkCreatedWorkflow, Args: []string{config.Command, "workflow", "link", projectRef, "<created-uuid>"}},
		{Kind: taskWorkflowRecoveryCommandListWorkflows, Args: []string{config.Command, "workflow", "list"}},
		{Kind: taskWorkflowRecoveryCommandLinkExistingWorkflow, Args: []string{config.Command, "workflow", "link", projectRef, "<uuid>"}},
	}
}

func taskWorkflowListProjectCommand(projectRef string) taskWorkflowRecoveryCommand {
	return taskWorkflowRecoveryCommand{
		Kind: taskWorkflowRecoveryCommandListProjectWorkflows,
		Args: []string{config.Command, "workflow", "list", "--project", projectRef},
	}
}

func taskCreateRetryCommandArgs(commandContext taskCreateCommandContext, workflowID *string) []string {
	args := []string{config.Command, "task", "create", "--project", commandContext.ProjectRef}
	if workflowID != nil {
		args = append(args, "--workflow", *workflowID)
	}
	args = append(args, "--title", commandContext.Title)
	if strings.TrimSpace(commandContext.BodyFile) != "" {
		args = append(args, "--body-file", commandContext.BodyFile)
	} else {
		args = append(args, "--body", commandContext.Body)
	}
	if strings.TrimSpace(commandContext.SourceURL) != "" {
		args = append(args, "--source-url", commandContext.SourceURL)
	}
	if strings.TrimSpace(commandContext.SourceWorkspace) != "" {
		args = append(args, "--source-workspace", commandContext.SourceWorkspace)
	}
	for _, selector := range commandContext.LabelSelectors {
		args = append(args, "--label", selector)
	}
	if commandContext.JSON {
		args = append(args, "--json")
	}
	return args
}

func taskListRetryCommandArgs(commandContext taskListCommandContext, workflowID *string) []string {
	args := []string{config.Command, "task", "list", "--project", commandContext.ProjectRef}
	if workflowID != nil {
		args = append(args, "--workflow", *workflowID)
	}
	for _, status := range commandContext.StatusKinds {
		args = append(args, "--status", string(status))
	}
	for _, attention := range commandContext.AttentionKinds {
		args = append(args, "--attention", string(attention))
	}
	for _, columnKey := range commandContext.ColumnKeys {
		args = append(args, "--column", columnKey)
	}
	for _, sortSelector := range commandContext.Sort {
		args = append(args, "--sort", string(sortSelector.Field)+":"+string(sortSelector.Direction))
	}
	for _, selector := range commandContext.LabelSelectors {
		args = append(args, "--label", selector)
	}
	for _, selector := range commandContext.ExcludedLabelSelectors {
		args = append(args, "--not-label", selector)
	}
	if commandContext.LabelMatch != nil {
		args = append(args, "--label-match", string(*commandContext.LabelMatch))
	}
	if commandContext.Unlabeled {
		args = append(args, "--unlabeled")
	}
	args = append(args, "--limit", fmt.Sprintf("%d", commandContext.Limit))
	if commandContext.JSON {
		args = append(args, "--json")
	}
	return args
}
