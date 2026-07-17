package main

import (
	"errors"
	"fmt"
	"strings"

	"core/shared/config"
	"core/shared/serverapi"
)

type taskListCommandContext struct {
	ProjectRef         string
	ResolvedProjectID  string
	SelectedWorkflowID *string
	ColumnKeys         []string
	StatusKinds        []serverapi.WorkflowTaskStatusKind
	AttentionKinds     []serverapi.WorkflowTaskAttentionKind
	Sort               []serverapi.WorkflowTaskListSort
	PageSize           int
	PageToken          string
	JSON               bool
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
	JSON               bool
}

type taskWorkflowRecoveryKind string

const (
	taskWorkflowRecoveryNoLinkedWorkflows       taskWorkflowRecoveryKind = taskWorkflowRecoveryKind(serverapi.WorkflowTaskListScopeReasonNoLinkedWorkflows)
	taskWorkflowRecoveryWorkflowNotLinked       taskWorkflowRecoveryKind = taskWorkflowRecoveryKind(serverapi.WorkflowTaskListScopeReasonWorkflowNotLinked)
	taskWorkflowRecoveryWorkflowRequiredColumns taskWorkflowRecoveryKind = taskWorkflowRecoveryKind(serverapi.WorkflowTaskListScopeReasonWorkflowRequiredColumns)
	taskWorkflowRecoveryAmbiguousWithoutDefault taskWorkflowRecoveryKind = taskWorkflowRecoveryKind(serverapi.WorkflowTaskCreateSelectionReasonAmbiguousWithoutDefault)
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
	workflowID *string
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
	failure := taskWorkflowRecoveryFailure{
		kind:       taskWorkflowRecoveryKind(scopeErr.Reason),
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
	failure := taskWorkflowRecoveryFailure{
		kind:       taskWorkflowRecoveryKind(createErr.Reason),
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
			commandContext.retryCommand(nil, true),
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
			commandContext.retryCommand(&workflowPlaceholder, false),
			{Kind: taskWorkflowRecoveryCommandLinkSelectedWorkflow, Args: []string{config.Command, "workflow", "link", commandContext.projectRef, selected}},
		}
	case taskWorkflowRecoveryWorkflowRequiredColumns:
		if commandContext.list == nil || failure.workflowID != nil || selectedWorkflowID != nil {
			return taskWorkflowRecovery{}, errors.New("workflow-required-for-columns recovery requires project-wide task list context")
		}
		workflowPlaceholder := "<uuid>"
		recovery.Commands = []taskWorkflowRecoveryCommand{
			listProjectWorkflows,
			commandContext.retryCommand(&workflowPlaceholder, false),
		}
	case taskWorkflowRecoveryAmbiguousWithoutDefault:
		if commandContext.create == nil || failure.workflowID != nil || selectedWorkflowID != nil {
			return taskWorkflowRecovery{}, errors.New("ambiguous recovery requires task create context without a selected workflow")
		}
		workflowPlaceholder := "<uuid>"
		recovery.Commands = []taskWorkflowRecoveryCommand{
			listProjectWorkflows,
			commandContext.retryCommand(&workflowPlaceholder, false),
			{Kind: taskWorkflowRecoveryCommandSetDefaultWorkflow, Args: []string{config.Command, "workflow", "default", commandContext.projectRef, "<uuid>"}},
		}
	default:
		return taskWorkflowRecovery{}, fmt.Errorf("unsupported task workflow recovery reason %q", failure.kind)
	}
	return recovery, nil
}

func (c taskWorkflowRecoveryContext) retryCommand(workflowID *string, preservePageToken bool) taskWorkflowRecoveryCommand {
	if c.list != nil {
		return taskWorkflowRecoveryCommand{
			Kind: taskWorkflowRecoveryCommandRetryTaskList,
			Args: taskListRetryCommandArgs(*c.list, workflowID, preservePageToken),
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
	if commandContext.JSON {
		args = append(args, "--json")
	}
	return args
}

func taskListRetryCommandArgs(commandContext taskListCommandContext, workflowID *string, preservePageToken bool) []string {
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
	args = append(args, "--page-size", fmt.Sprintf("%d", commandContext.PageSize))
	if preservePageToken && strings.TrimSpace(commandContext.PageToken) != "" {
		args = append(args, "--page-token", commandContext.PageToken)
	}
	if commandContext.JSON {
		args = append(args, "--json")
	}
	return args
}
