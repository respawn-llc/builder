package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"core/shared/client"
	"core/shared/config"
	"core/shared/serverapi"
)

type projectDeleteOperations interface {
	ListWorkflowTasks(context.Context, serverapi.WorkflowTaskListRequest) (serverapi.WorkflowTaskListResponse, error)
	DeleteProject(context.Context, serverapi.ProjectDeleteRequest) (serverapi.ProjectDeleteResponse, error)
}

type projectDeleteError struct {
	Code      string
	Message   string
	ProjectID string
	Blockers  []projectDeleteBlocker
}

type projectDeleteBlocker struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Count   *int   `json:"count,omitempty"`
}

type projectDeleteOutcome struct {
	Result *projectDeleteResult
	Error  *projectDeleteError
}

type projectDeleteResult struct {
	ProjectID string `json:"project_id"`
}

type projectDeleteJSONError struct {
	Code      string                 `json:"code"`
	Message   string                 `json:"message"`
	ProjectID string                 `json:"project_id"`
	Blockers  []projectDeleteBlocker `json:"blockers,omitempty"`
}

type projectDeleteJSONEnvelope struct {
	Status string                  `json:"status"`
	Result *projectDeleteResult    `json:"result,omitempty"`
	Error  *projectDeleteJSONError `json:"error,omitempty"`
}

func projectDeleteSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" project delete", stderr, projectDeleteUsage)
	confirm := fs.Bool("confirm", false, "confirm project deletion")
	jsonOut := fs.Bool("json", false, "write a stable JSON envelope")
	positionals, ok, exitCode := parseInterspersedPositionals(fs, args)
	if !ok {
		return exitCode
	}
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "project delete requires <project-id>")
		return 2
	}
	projectID := strings.TrimSpace(positionals[0])
	if projectID == "" {
		fmt.Fprintln(stderr, "project id must not be blank")
		return 2
	}

	_, remote, err := bindingCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		return writeProjectDeleteOutcome(stdout, stderr, projectDeleteOutcome{
			Error: &projectDeleteError{Code: "request_failed", Message: err.Error(), ProjectID: projectID},
		}, *jsonOut)
	}
	ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
	defer cancel()
	outcome := runProjectDeleteUseCase(ctx, remote, projectID, *confirm, isAgentShell())
	exitCode = writeProjectDeleteOutcome(stdout, stderr, outcome, *jsonOut)
	if closeErr := closeCommandRemote(remote, "project deletion", nil); closeErr != nil {
		fmt.Fprintln(stderr, closeErr)
	}
	return exitCode
}

func runProjectDeleteUseCase(
	ctx context.Context,
	operations projectDeleteOperations,
	projectID string,
	confirmed bool,
	agent bool,
) projectDeleteOutcome {
	projectID = strings.TrimSpace(projectID)
	if unfinished, err := projectDeleteHasUnfinishedWork(ctx, operations, projectID); err != nil {
		return projectDeleteFailure(projectID, projectDeleteErrorCode(err), err)
	} else if agent && unfinished {
		return projectDeleteFailure(
			projectID,
			"human_only_unfinished_work",
			fmt.Errorf("Project deletion is human-only because project %s contains unfinished work.", projectID),
		)
	}
	if !confirmed {
		return projectDeleteFailure(
			projectID,
			"confirmation_required",
			fmt.Errorf("Project deletion was not confirmed. Rerun with --confirm to delete project %s.", projectID),
		)
	}
	response, err := operations.DeleteProject(ctx, serverapi.ProjectDeleteRequest{ProjectID: projectID})
	if err != nil {
		return projectDeleteFailure(projectID, projectDeleteErrorCode(err), err)
	}
	if response.ProjectID != projectID {
		return projectDeleteFailure(projectID, "request_failed", errors.New("project deletion returned a mismatched project identity"))
	}
	if response.Deleted {
		if len(response.Blockers) != 0 {
			return projectDeleteFailure(projectID, "request_failed", errors.New("project deletion returned blockers with deleted=true"))
		}
		return projectDeleteOutcome{Result: &projectDeleteResult{ProjectID: projectID}}
	}
	if len(response.Blockers) == 0 {
		return projectDeleteFailure(projectID, "request_failed", errors.New("project deletion returned deleted=false without blockers"))
	}
	blockers, err := projectDeleteBlockersForCLI(response.Blockers)
	if err != nil {
		return projectDeleteFailure(projectID, "request_failed", err)
	}
	return projectDeleteOutcome{
		Error: &projectDeleteError{
			Code:      "project_delete_blocked",
			Message:   "project deletion was blocked",
			ProjectID: projectID,
			Blockers:  blockers,
		},
	}
}

func projectDeleteHasUnfinishedWork(ctx context.Context, operations projectDeleteOperations, projectID string) (bool, error) {
	projectID = strings.TrimSpace(projectID)
	limit := 1
	requestProjectID := projectID
	response, err := operations.ListWorkflowTasks(ctx, serverapi.WorkflowTaskListRequest{
		ProjectID:   &requestProjectID,
		StatusKinds: projectDeleteUnfinishedTaskStatuses(),
		LabelFilter: serverapi.WorkflowTaskLabelFilterNone(),
		Limit:       &limit,
	})
	if err != nil {
		var scopeErr *serverapi.WorkflowTaskListScopeError
		if errors.As(err, &scopeErr) &&
			scopeErr.Reason == serverapi.WorkflowTaskListScopeReasonNoLinkedWorkflows &&
			scopeErr.ProjectID != nil &&
			*scopeErr.ProjectID == projectID &&
			scopeErr.WorkflowID == nil {
			return false, nil
		}
		return false, err
	}
	if response.Scope.ProjectID != projectID || response.Scope.WorkflowID != nil {
		return false, errors.New("project deletion task preflight returned an unexpected scope")
	}
	if len(response.Tasks) > 1 {
		return false, errors.New("project deletion task preflight returned too many tasks")
	}
	if len(response.Tasks) == 0 {
		return false, nil
	}
	task := response.Tasks[0]
	expectedNativeState, valid := task.Status.Kind.NativeState()
	if !valid || task.Status.Kind == serverapi.WorkflowTaskStatusKindDone ||
		task.Status.NativeState == serverapi.WorkflowTaskNativeStateTerminal ||
		task.Status.NativeState != expectedNativeState {
		return false, errors.New("project deletion task preflight returned an invalid task status")
	}
	return true, nil
}

func projectDeleteUnfinishedTaskStatuses() []serverapi.WorkflowTaskStatusKind {
	return []serverapi.WorkflowTaskStatusKind{
		serverapi.WorkflowTaskStatusKindWaitingQuestion,
		serverapi.WorkflowTaskStatusKindWaitingApproval,
		serverapi.WorkflowTaskStatusKindInterrupted,
		serverapi.WorkflowTaskStatusKindRunning,
		serverapi.WorkflowTaskStatusKindQueued,
		serverapi.WorkflowTaskStatusKindBacklog,
		serverapi.WorkflowTaskStatusKindActive,
	}
}

func projectDeleteFailure(projectID string, code string, err error) projectDeleteOutcome {
	return projectDeleteOutcome{
		Error: &projectDeleteError{
			Code:      code,
			Message:   err.Error(),
			ProjectID: projectID,
		},
	}
}

func projectDeleteErrorCode(err error) string {
	if errors.Is(err, serverapi.ErrProjectNotFound) {
		return "project_not_found"
	}
	return "request_failed"
}

func projectDeleteBlockersForCLI(blockers []serverapi.ProjectDeleteBlocker) ([]projectDeleteBlocker, error) {
	output := make([]projectDeleteBlocker, 0, len(blockers))
	for _, blocker := range blockers {
		if strings.TrimSpace(blocker.Code) == "" || strings.TrimSpace(blocker.Message) == "" {
			return nil, errors.New("project deletion returned an incomplete blocker")
		}
		if blocker.Count < 0 {
			return nil, errors.New("project deletion returned a negative blocker count")
		}
		var count *int
		if blocker.Count > 0 {
			value := blocker.Count
			count = &value
		}
		output = append(output, projectDeleteBlocker{
			Code:    blocker.Code,
			Message: blocker.Message,
			Count:   count,
		})
	}
	return output, nil
}

func writeProjectDeleteOutcome(stdout io.Writer, stderr io.Writer, outcome projectDeleteOutcome, jsonOut bool) int {
	if err := outcome.validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if outcome.Error != nil {
		if jsonOut {
			envelope := projectDeleteJSONEnvelope{
				Status: "error",
				Error: &projectDeleteJSONError{
					Code:      outcome.Error.Code,
					Message:   outcome.Error.Message,
					ProjectID: outcome.Error.ProjectID,
					Blockers:  outcome.Error.Blockers,
				},
			}
			if err := envelope.validate(); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			if exitCode := writeCommandJSON(stdout, stderr, envelope); exitCode != 0 {
				return exitCode
			}
			return 1
		}
		for _, blocker := range outcome.Error.Blockers {
			writeWorkflowBlockerLine(stderr, blocker.Code, blocker.Message, projectDeleteBlockerCount(blocker.Count))
		}
		fmt.Fprintln(stderr, outcome.Error.Message)
		return 1
	}
	if jsonOut {
		envelope := projectDeleteJSONEnvelope{
			Status: "ok",
			Result: outcome.Result,
		}
		if err := envelope.validate(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return writeCommandJSON(stdout, stderr, envelope)
	}
	fmt.Fprintf(stdout, "Deleted project %s. Workspace files were not deleted.\n", outcome.Result.ProjectID)
	return 0
}

func projectDeleteBlockerCount(count *int) *int64 {
	if count == nil {
		return nil
	}
	value := int64(*count)
	return &value
}

func (outcome projectDeleteOutcome) validate() error {
	if (outcome.Result == nil) == (outcome.Error == nil) {
		return errors.New("project deletion outcome must contain exactly one result or error")
	}
	if outcome.Result != nil && strings.TrimSpace(outcome.Result.ProjectID) == "" {
		return errors.New("project deletion result requires a project id")
	}
	if outcome.Error != nil && strings.TrimSpace(outcome.Error.ProjectID) == "" {
		return errors.New("project deletion error requires a project id")
	}
	return nil
}

func (envelope projectDeleteJSONEnvelope) validate() error {
	switch envelope.Status {
	case "ok":
		if envelope.Result == nil || envelope.Error != nil || strings.TrimSpace(envelope.Result.ProjectID) == "" {
			return errors.New("project deletion success envelope is invalid")
		}
	case "error":
		if envelope.Error == nil || envelope.Result != nil ||
			strings.TrimSpace(envelope.Error.Code) == "" ||
			strings.TrimSpace(envelope.Error.Message) == "" ||
			strings.TrimSpace(envelope.Error.ProjectID) == "" {
			return errors.New("project deletion error envelope is invalid")
		}
		if envelope.Error.Code == "project_delete_blocked" && len(envelope.Error.Blockers) == 0 {
			return errors.New("project deletion blocked envelope requires blockers")
		}
		if envelope.Error.Code != "project_delete_blocked" && len(envelope.Error.Blockers) != 0 {
			return errors.New("project deletion non-blocked envelope must omit blockers")
		}
		for _, blocker := range envelope.Error.Blockers {
			if strings.TrimSpace(blocker.Code) == "" || strings.TrimSpace(blocker.Message) == "" {
				return errors.New("project deletion error envelope blocker is invalid")
			}
			if blocker.Count != nil && *blocker.Count <= 0 {
				return errors.New("project deletion error envelope blocker count is invalid")
			}
		}
	default:
		return errors.New("project deletion envelope status is invalid")
	}
	return nil
}

var _ projectDeleteOperations = (*client.Remote)(nil)
