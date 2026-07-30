package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"core/shared/apicontract"
	"core/shared/client"
	"core/shared/config"
	"core/shared/serverapi"
)

type bindingMutationArguments struct {
	ProjectID   string
	Workspace   string
	WorkspaceID *string
	JSON        bool
}

func parseBindingMutationArguments(command string, usage commandUsage, args []string, stderr io.Writer) (bindingMutationArguments, bool, int) {
	fs := newCommandFlagSet(config.Command+" "+command, stderr, usage)
	projectID := fs.String("project", "", "project ID; required")
	workspaceID := fs.String("workspace", "", "workspace ID")
	jsonOut := fs.Bool("json", false, "write a stable JSON envelope")
	positionals, ok, exitCode := parseInterspersedPositionals(fs, args)
	if !ok {
		return bindingMutationArguments{}, false, exitCode
	}
	if !flagWasProvided(fs, "project") || strings.TrimSpace(*projectID) == "" {
		fmt.Fprintln(stderr, "--project <project-id> is required")
		return bindingMutationArguments{}, false, 2
	}
	if len(positionals) > 1 {
		fmt.Fprintln(stderr, command+" accepts at most one workspace path")
		return bindingMutationArguments{}, false, 2
	}
	var selectedWorkspaceID *string
	if flagWasProvided(fs, "workspace") && strings.TrimSpace(*workspaceID) == "" {
		fmt.Fprintln(stderr, "--workspace must not be blank")
		return bindingMutationArguments{}, false, 2
	}
	if flagWasProvided(fs, "workspace") {
		trimmedWorkspaceID := strings.TrimSpace(*workspaceID)
		selectedWorkspaceID = &trimmedWorkspaceID
	}
	if flagWasProvided(fs, "workspace") && len(positionals) > 0 {
		fmt.Fprintln(stderr, "workspace path and --workspace are mutually exclusive")
		return bindingMutationArguments{}, false, 2
	}
	path := "."
	if len(positionals) == 1 {
		path = positionals[0]
	}
	return bindingMutationArguments{
		ProjectID:   strings.TrimSpace(*projectID),
		Workspace:   path,
		WorkspaceID: selectedWorkspaceID,
		JSON:        *jsonOut,
	}, true, 0
}

func projectDefaultSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	arguments, ok, exitCode := parseBindingMutationArguments("project default", projectDefaultUsage, args, stderr)
	if !ok {
		return exitCode
	}
	return runBindingMutationCommand(arguments, stdout, stderr, func(ctx context.Context, remote *client.Remote, selector serverapi.ProjectWorkspaceSelector) (bindingMutationResult, error) {
		response, err := remote.SetDefaultWorkspace(ctx, serverapi.ProjectDefaultWorkspaceSetRequest{
			ProjectID:                arguments.ProjectID,
			ProjectWorkspaceSelector: selector,
		})
		if err != nil {
			return bindingMutationResult{}, err
		}
		return bindingMutationResult{Project: &response.Project}, nil
	}, true)
}

func detachSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	arguments, ok, exitCode := parseBindingMutationArguments("detach", detachUsage, args, stderr)
	if !ok {
		return exitCode
	}
	return runBindingMutationCommand(arguments, stdout, stderr, func(ctx context.Context, remote *client.Remote, selector serverapi.ProjectWorkspaceSelector) (bindingMutationResult, error) {
		response, err := remote.UnlinkWorkspaceFromProject(ctx, serverapi.ProjectWorkspaceUnlinkRequest{
			ProjectID:                arguments.ProjectID,
			ProjectWorkspaceSelector: selector,
		})
		if err != nil {
			return bindingMutationResult{}, err
		}
		if len(response.Blockers) > 0 {
			blocked, err := newBindingMutationBlockedError(response.ProjectID, response.WorkspaceID, response.Blockers)
			if err != nil {
				return bindingMutationResult{}, err
			}
			return bindingMutationResult{}, blocked
		}
		if !response.Unlinked {
			return bindingMutationResult{}, &serverapi.WorkspaceMutationError{
				ProjectID:   strings.TrimSpace(response.ProjectID),
				WorkspaceID: strings.TrimSpace(response.WorkspaceID),
				Cause:       errors.New("workspace detach did not complete"),
			}
		}
		projectID := strings.TrimSpace(response.ProjectID)
		workspaceID := strings.TrimSpace(response.WorkspaceID)
		if projectID == "" || workspaceID == "" {
			return bindingMutationResult{}, errors.New("workspace detach returned incomplete identity")
		}
		return bindingMutationResult{ProjectID: &projectID, WorkspaceID: &workspaceID}, nil
	}, false)
}

func runBindingMutationCommand(
	arguments bindingMutationArguments,
	stdout io.Writer,
	stderr io.Writer,
	mutate func(context.Context, *client.Remote, serverapi.ProjectWorkspaceSelector) (bindingMutationResult, error),
	defaultMutation bool,
) int {
	cfg, remote, err := bindingCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		return writeBindingMutationFailure(stdout, stderr, arguments.JSON, err, arguments.ProjectID, defaultMutation)
	}
	defer func() { _ = remote.Close() }()

	selector, err := bindingMutationSelector(cfg, arguments)
	if err != nil {
		return writeBindingMutationFailure(stdout, stderr, arguments.JSON, err, arguments.ProjectID, defaultMutation)
	}
	rpcCtx, cancel := context.WithTimeout(context.Background(), bindingCommandRPCTimeout)
	defer cancel()
	result, err := mutate(rpcCtx, remote, selector)
	if err != nil {
		return writeBindingMutationFailure(stdout, stderr, arguments.JSON, err, arguments.ProjectID, defaultMutation)
	}
	if arguments.JSON {
		return writeBindingMutationEnvelope(stdout, stderr, bindingMutationEnvelope{
			Status: "ok",
			Result: &result,
		})
	}
	if err := result.validate(); err != nil {
		return writeBindingMutationFailure(stdout, stderr, arguments.JSON, err, arguments.ProjectID, defaultMutation)
	}
	if defaultMutation {
		_, _ = fmt.Fprintln(stdout, "done")
	} else {
		_, _ = fmt.Fprintln(stdout, *result.WorkspaceID)
	}
	return 0
}

func bindingMutationSelector(cfg config.App, arguments bindingMutationArguments) (serverapi.ProjectWorkspaceSelector, error) {
	if arguments.WorkspaceID != nil {
		selector, err := serverapi.NewProjectWorkspaceSelectorForID(*arguments.WorkspaceID)
		return selector, err
	}
	path := arguments.Workspace
	if path == "." {
		path = cfg.WorkspaceRoot
	}
	normalizedPath, err := normalizeBindingCommandPath(path)
	if err != nil {
		return serverapi.ProjectWorkspaceSelector{}, err
	}
	selector, err := serverapi.NewProjectWorkspaceSelectorForRoot(normalizedPath)
	return selector, err
}

type bindingMutationResult struct {
	ProjectID   *string                       `json:"project_id,omitempty"`
	WorkspaceID *string                       `json:"workspace_id,omitempty"`
	Project     *serverapi.ProjectHomeSummary `json:"project,omitempty"`
}

type bindingMutationBlocker struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Guidance string `json:"guidance"`
	Count    *int   `json:"count,omitempty"`
}

type bindingMutationRemediation struct {
	Kind string `json:"kind"`
}

type bindingMutationJSONError struct {
	Code        string                      `json:"code"`
	Message     string                      `json:"message"`
	ProjectID   *string                     `json:"project_id,omitempty"`
	WorkspaceID *string                     `json:"workspace_id,omitempty"`
	Retryable   *bool                       `json:"retryable,omitempty"`
	Blockers    []bindingMutationBlocker    `json:"blockers,omitempty"`
	Remediation *bindingMutationRemediation `json:"remediation,omitempty"`
}

type bindingMutationEnvelope struct {
	Status string                    `json:"status"`
	Result *bindingMutationResult    `json:"result,omitempty"`
	Error  *bindingMutationJSONError `json:"error,omitempty"`
}

type bindingMutationBlockedError struct {
	ProjectID   string
	WorkspaceID string
	Blockers    []serverapi.ProjectWorkspaceUnlinkBlocker
}

func newBindingMutationBlockedError(projectID string, workspaceID string, blockers []serverapi.ProjectWorkspaceUnlinkBlocker) (*bindingMutationBlockedError, error) {
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(workspaceID) == "" {
		return nil, errors.New("workspace detach blocker response omitted resolved identity")
	}
	if len(blockers) == 0 {
		return nil, errors.New("workspace detach blocker response was empty")
	}
	return &bindingMutationBlockedError{
		ProjectID:   strings.TrimSpace(projectID),
		WorkspaceID: strings.TrimSpace(workspaceID),
		Blockers:    blockers,
	}, nil
}

func (e *bindingMutationBlockedError) Error() string {
	if e == nil {
		return "workspace detach is blocked"
	}
	return "workspace detach is blocked"
}

type blockerGuidanceActionKind string

const (
	blockerGuidanceDefaultWorkspace       blockerGuidanceActionKind = "default_workspace"
	blockerGuidanceActiveSessions         blockerGuidanceActionKind = "active_sessions"
	blockerGuidanceNonTerminalTasks       blockerGuidanceActionKind = "non_terminal_tasks"
	blockerGuidanceExecutableCurrentNodes blockerGuidanceActionKind = "executable_current_nodes"
	blockerGuidanceManagedOwnedWorktrees  blockerGuidanceActionKind = "managed_owned_worktrees"
	blockerGuidanceMissingHistorySnapshot blockerGuidanceActionKind = "missing_history_snapshot"
	blockerGuidanceUnknown                blockerGuidanceActionKind = "unknown"
)

type commandTokens []string

func newCommandTokens(tokens ...string) (*commandTokens, error) {
	if len(tokens) == 0 {
		return nil, errors.New("command tokens are required")
	}
	normalized := make(commandTokens, len(tokens))
	for index, token := range tokens {
		if strings.TrimSpace(token) == "" {
			return nil, errors.New("command tokens must not be blank")
		}
		normalized[index] = token
	}
	return &normalized, nil
}

type blockerGuidance struct {
	Kind    blockerGuidanceActionKind
	Command *commandTokens
}

func blockerGuidanceFor(code string, projectID string) blockerGuidance {
	kind := blockerGuidanceActionKind(strings.TrimSpace(code))
	switch kind {
	case blockerGuidanceDefaultWorkspace,
		blockerGuidanceActiveSessions,
		blockerGuidanceNonTerminalTasks,
		blockerGuidanceExecutableCurrentNodes,
		blockerGuidanceManagedOwnedWorktrees,
		blockerGuidanceMissingHistorySnapshot:
	default:
		kind = blockerGuidanceUnknown
	}
	guidance := blockerGuidance{Kind: kind}
	if kind == blockerGuidanceDefaultWorkspace {
		command, err := newCommandTokens(config.Command, "project", "default", "--project", strings.TrimSpace(projectID), "<replacement-path>")
		if err != nil {
			panic(err)
		}
		guidance.Command = command
	}
	return guidance
}

func renderBlockerGuidance(guidance blockerGuidance) string {
	command := ""
	if guidance.Command != nil {
		command = strings.Join(*guidance.Command, " ")
	}
	switch guidance.Kind {
	case blockerGuidanceDefaultWorkspace:
		return fmt.Sprintf("Choose another attached workspace with `%s`, then retry detach. If it is not attached, run `%s attach --project <project-id> <replacement-path>` first.", command, config.Command)
	case blockerGuidanceActiveSessions:
		return "Stop active runs using this workspace or rebind those Sessions to another attached workspace, then retry detach."
	case blockerGuidanceNonTerminalTasks:
		return "Move editable Backlog Tasks to another source workspace, or complete, manually move, or delete dependent Tasks, then retry detach."
	case blockerGuidanceExecutableCurrentNodes:
		return "Stop active execution, then move, complete, or delete affected Tasks until no executable Current Node uses this workspace, then retry detach."
	case blockerGuidanceManagedOwnedWorktrees:
		return "Delete the dependent managed worktrees or their owning quiescent Tasks, then retry detach."
	case blockerGuidanceMissingHistorySnapshot:
		return "Re-save an editable Task's source workspace to recreate its retained snapshot. If its history cannot be edited, keep the binding and report the blocker because detach is unsafe."
	default:
		return "Resolve this blocker and retry detach. Update the CLI and server together when the CLI does not recognize this blocker code."
	}
}

func (r bindingMutationResult) validate() error {
	if r.Project != nil {
		if r.ProjectID != nil || r.WorkspaceID != nil {
			return errors.New("binding mutation result mixes project and workspace identity")
		}
		return nil
	}
	if r.ProjectID == nil || r.WorkspaceID == nil || strings.TrimSpace(*r.ProjectID) == "" || strings.TrimSpace(*r.WorkspaceID) == "" {
		return errors.New("binding mutation result requires project and workspace IDs")
	}
	return nil
}

func (e bindingMutationJSONError) validate() error {
	if strings.TrimSpace(e.Code) == "" || strings.TrimSpace(e.Message) == "" {
		return errors.New("binding mutation error requires code and message")
	}
	if (e.ProjectID == nil) != (e.WorkspaceID == nil) {
		return errors.New("binding mutation error requires paired IDs")
	}
	if e.ProjectID != nil && (strings.TrimSpace(*e.ProjectID) == "" || strings.TrimSpace(*e.WorkspaceID) == "") {
		return errors.New("binding mutation error IDs must not be blank")
	}
	if e.Retryable != nil && !*e.Retryable {
		return errors.New("binding mutation retryable must be true when present")
	}
	if e.Blockers != nil && len(e.Blockers) == 0 {
		return errors.New("binding mutation blockers must be absent or non-empty")
	}
	for _, blocker := range e.Blockers {
		if strings.TrimSpace(blocker.Code) == "" || strings.TrimSpace(blocker.Message) == "" || strings.TrimSpace(blocker.Guidance) == "" {
			return errors.New("binding mutation blocker requires code, message, and guidance")
		}
		if blocker.Count != nil && *blocker.Count <= 0 {
			return errors.New("binding mutation blocker count must be positive when present")
		}
	}
	if e.Remediation != nil && strings.TrimSpace(e.Remediation.Kind) == "" {
		return errors.New("binding mutation remediation kind must not be blank")
	}
	return nil
}

func (e bindingMutationEnvelope) validate() error {
	switch e.Status {
	case "ok":
		if e.Result == nil || e.Error != nil {
			return errors.New("successful binding mutation envelope requires result only")
		}
		return e.Result.validate()
	case "error":
		if e.Error == nil || e.Result != nil {
			return errors.New("failed binding mutation envelope requires error only")
		}
		return e.Error.validate()
	default:
		return errors.New("binding mutation status must be ok or error")
	}
}

func writeBindingMutationEnvelope(stdout io.Writer, stderr io.Writer, envelope bindingMutationEnvelope) int {
	if err := envelope.validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeCommandJSON(stdout, stderr, envelope)
}

func writeBindingMutationFailure(stdout io.Writer, stderr io.Writer, jsonOut bool, err error, requestedProjectID string, defaultMutation bool) int {
	if jsonOut {
		projection := projectWorkspaceMutationErrorProjection(err, requestedProjectID, defaultMutation)
		if code := writeBindingMutationEnvelope(stdout, stderr, bindingMutationEnvelope{Status: "error", Error: &projection}); code != 0 {
			return code
		}
		return 1
	}
	fmt.Fprintln(stderr, projectWorkspaceMutationErrorMessage(err, requestedProjectID, defaultMutation))
	return 1
}

func projectWorkspaceMutationErrorProjection(err error, requestedProjectID string, defaultMutation bool) bindingMutationJSONError {
	code := "request_failed"
	message := projectWorkspaceMutationErrorMessage(err, requestedProjectID, defaultMutation)
	projection := bindingMutationJSONError{Code: code, Message: message}
	var blockedErr *bindingMutationBlockedError
	if errors.As(err, &blockedErr) {
		projectID := strings.TrimSpace(blockedErr.ProjectID)
		workspaceID := strings.TrimSpace(blockedErr.WorkspaceID)
		projection.Code = "workspace_detach_blocked"
		projection.ProjectID = &projectID
		projection.WorkspaceID = &workspaceID
		projection.Blockers = make([]bindingMutationBlocker, 0, len(blockedErr.Blockers))
		for _, blocker := range blockedErr.Blockers {
			guidance := renderBlockerGuidance(blockerGuidanceFor(blocker.Code, projectID))
			projected := bindingMutationBlocker{
				Code:     strings.TrimSpace(blocker.Code),
				Message:  strings.TrimSpace(blocker.Message),
				Guidance: guidance,
			}
			if blocker.Count > 0 {
				count := blocker.Count
				projected.Count = &count
			}
			projection.Blockers = append(projection.Blockers, projected)
		}
		return projection
	}
	var mutationErr *serverapi.WorkspaceMutationError
	if errors.As(err, &mutationErr) {
		projectID := strings.TrimSpace(mutationErr.ProjectID)
		workspaceID := strings.TrimSpace(mutationErr.WorkspaceID)
		projection.ProjectID = &projectID
		projection.WorkspaceID = &workspaceID
	}
	var conflictErr *serverapi.WorkspaceDetachConflictError
	if !defaultMutation && errors.As(err, &conflictErr) {
		projectID := strings.TrimSpace(conflictErr.ProjectID)
		workspaceID := strings.TrimSpace(conflictErr.WorkspaceID)
		retryable := true
		projection.Code = "workspace_detach_conflict"
		projection.ProjectID = &projectID
		projection.WorkspaceID = &workspaceID
		projection.Retryable = &retryable
	}
	if mutationErr == nil && conflictErr == nil && errors.Is(err, serverapi.ErrProjectNotFound) {
		projection.Code = "project_not_found"
	} else if mutationErr == nil && conflictErr == nil && errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		projection.Code = "workspace_not_attached"
	}
	if errors.Is(err, serverapi.ErrWorkspacePathIdentity) {
		projection.Remediation = &bindingMutationRemediation{Kind: "use_workspace_id"}
	}
	return projection
}

func projectWorkspaceMutationErrorMessage(err error, projectID string, defaultMutation bool) string {
	var blockedErr *bindingMutationBlockedError
	if errors.As(err, &blockedErr) && len(blockedErr.Blockers) > 0 {
		blocker := blockedErr.Blockers[0]
		message := fmt.Sprintf("[%s]", strings.TrimSpace(blocker.Code))
		if blocker.Count > 0 {
			message += fmt.Sprintf(" (%d)", blocker.Count)
		}
		message += " " + strings.TrimSpace(blocker.Message)
		message += ": " + renderBlockerGuidance(blockerGuidanceFor(blocker.Code, blockedErr.ProjectID))
		return message
	}
	if errors.Is(err, serverapi.ErrWorkspaceNotRegistered) && defaultMutation {
		return fmt.Sprintf("workspace is not attached to project %q; run `kent attach --project %s <path>` before retrying", strings.TrimSpace(projectID), strings.TrimSpace(projectID))
	}
	if errors.Is(err, serverapi.ErrWorkspacePathIdentity) {
		return fmt.Sprintf("%s; retry with --workspace <workspace-id>", err)
	}
	return err.Error()
}

var _ apicontract.ProjectViewService = (*client.Remote)(nil)
