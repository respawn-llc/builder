package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"core/shared/apicontract"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
)

var bindingCommandRPCTimeout = 5 * time.Second
var bindingCommandRemoteOpener = openBindingCommandRemote
var bindingCommandWorkspaceResolver = resolveWorkspaceBinding
var bindingCommandSessionRetargeter = retargetSessionWorkspace

func projectSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "list":
			return projectListSubcommand(args[1:], stdout, stderr)
		case "create":
			return projectCreateSubcommand(args[1:], stdout, stderr)
		}
	}
	fs := newCommandFlagSet(config.Command+" project", stderr, projectUsage)
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	remaining := fs.Args()
	if len(remaining) > 1 {
		fmt.Fprintln(stderr, "project accepts at most one path argument")
		return 2
	}
	path := "."
	if len(remaining) == 1 {
		path = remaining[0]
	}
	projectID, err := projectIDForPath(context.Background(), path)
	if err != nil {
		fmt.Fprintln(stderr, formatProjectLookupCommandError(path, err))
		return 1
	}
	_, _ = fmt.Fprintln(stdout, projectID)
	return 0
}

func projectListSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" project list", stderr, projectListUsage)
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(stderr, "project list does not accept positional arguments")
		return 2
	}
	projects, err := listProjects(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	for _, project := range projects {
		_, _ = fmt.Fprintf(stdout, "%s\t%s\t%s\n", project.ProjectID, project.DisplayName, project.RootPath)
	}
	return 0
}

func projectCreateSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" project create", stderr, projectCreateUsage)
	name := fs.String("name", "", "project name")
	path := fs.String("path", "", "existing workspace path on the Kent server")
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(stderr, "project create does not accept positional arguments")
		return 2
	}
	binding, err := createProject(context.Background(), *name, *path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, binding.ProjectID)
	return 0
}

func attachSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" attach", stderr, attachUsage)
	projectID := fs.String("project", "", "project ID; defaults to the project attached to the current directory")
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	remaining := fs.Args()
	if len(remaining) > 1 {
		fmt.Fprintln(stderr, "attach accepts at most one path argument; use --project for explicit project ids")
		return 2
	}
	targetPath := "."
	if len(remaining) == 1 {
		targetPath = remaining[0]
	}
	boundProjectID, err := attachWorkspace(context.Background(), *projectID, targetPath)
	if err != nil {
		fmt.Fprintln(stderr, formatAttachWorkspaceCommandError(targetPath, *projectID, err))
		return 1
	}
	_, _ = fmt.Fprintln(stdout, boundProjectID)
	return 0
}

func rebindSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" rebind", stderr, rebindUsage)
	projectID := fs.String("project", "", "target project ID; required to move a session across projects")
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	remaining := fs.Args()
	if len(remaining) != 2 {
		fmt.Fprintln(stderr, "rebind requires <session-id> and <new-path>")
		return 2
	}
	var targetProjectID *string
	fs.Visit(func(parsedFlag *flag.Flag) {
		if parsedFlag.Name != "project" {
			return
		}
		trimmed := strings.TrimSpace(*projectID)
		targetProjectID = &trimmed
	})
	if targetProjectID != nil && *targetProjectID == "" {
		fmt.Fprintln(stderr, "project id must not be blank")
		return 2
	}
	response, err := retargetSessionWorkspaceResponse(context.Background(), remaining[0], remaining[1], targetProjectID)
	if err != nil {
		fmt.Fprintln(stderr, formatSessionRetargetCommandError(remaining[1], err))
		return 1
	}
	_, _ = fmt.Fprintln(stdout, response.Binding.WorkspaceID)
	if response.WorkspaceBindingCreated {
		_, _ = fmt.Fprintf(
			stderr,
			"Attached workspace %q to project %q (%s).\n",
			response.Binding.CanonicalRoot,
			response.Binding.ProjectName,
			response.Binding.ProjectID,
		)
	}
	return 0
}

func projectIDForPath(ctx context.Context, path string) (string, error) {
	targetPath, err := normalizeBindingCommandPath(path)
	if err != nil {
		return "", err
	}
	_, remote, err := bindingCommandRemoteOpener(ctx, targetPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = remote.Close() }()
	binding, err := bindingCommandWorkspaceResolver(ctx, remote, targetPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(binding.ProjectID), nil
}

func attachWorkspace(ctx context.Context, explicitProjectID string, targetPath string) (string, error) {
	sourceCfg, remote, err := bindingCommandRemoteOpener(ctx, ".")
	if err != nil {
		return "", err
	}
	defer func() { _ = remote.Close() }()
	projectID := strings.TrimSpace(explicitProjectID)
	if projectID == "" {
		sourceBinding, err := bindingCommandWorkspaceResolver(ctx, remote, sourceCfg.WorkspaceRoot)
		if err != nil {
			return "", fmt.Errorf("%w: current workspace is not attached to a project; run `"+config.Command+" project` in a workspace that already belongs to the target project or pass --project <project-id>", err)
		}
		projectID = strings.TrimSpace(sourceBinding.ProjectID)
	}
	normalizedTargetPath, err := normalizeBindingCommandPath(targetPath)
	if err != nil {
		return "", err
	}
	resp, err := attachWorkspaceToProject(ctx, remote, projectID, normalizedTargetPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Binding.ProjectID), nil
}

func attachWorkspaceToProject(ctx context.Context, remote apicontract.ProjectViewService, projectID string, workspaceRoot string) (serverapi.ProjectAttachWorkspaceResponse, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, bindingCommandRPCTimeout)
	defer cancel()
	return remote.AttachWorkspaceToProject(rpcCtx, serverapi.ProjectAttachWorkspaceRequest{ProjectID: projectID, WorkspaceRoot: workspaceRoot})
}

func listProjectsWithTimeout(ctx context.Context, remote apicontract.ProjectViewService) (serverapi.ProjectListResponse, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, bindingCommandRPCTimeout)
	defer cancel()
	return remote.ListProjects(rpcCtx, serverapi.ProjectListRequest{})
}

func createProjectWithTimeout(ctx context.Context, remote apicontract.ProjectViewService, displayName string, workspaceRoot string) (serverapi.ProjectCreateResponse, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, bindingCommandRPCTimeout)
	defer cancel()
	return remote.CreateProject(rpcCtx, serverapi.ProjectCreateRequest{DisplayName: displayName, WorkspaceRoot: workspaceRoot})
}

func rebindWorkspaceWithTimeout(ctx context.Context, remote apicontract.ProjectViewService, oldWorkspaceRoot string, newWorkspaceRoot string) (serverapi.ProjectRebindWorkspaceResponse, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, bindingCommandRPCTimeout)
	defer cancel()
	return remote.RebindWorkspace(rpcCtx, serverapi.ProjectRebindWorkspaceRequest{OldWorkspaceRoot: oldWorkspaceRoot, NewWorkspaceRoot: newWorkspaceRoot})
}

func retargetSessionWorkspace(ctx context.Context, remote apicontract.SessionLifecycleService, sessionID string, workspaceRoot string, projectID *string) (serverapi.SessionRetargetWorkspaceResponse, error) {
	return remote.RetargetSessionWorkspace(ctx, serverapi.SessionRetargetWorkspaceRequest{ClientRequestID: uuid.NewString(), SessionID: sessionID, WorkspaceRoot: workspaceRoot, ProjectID: projectID})
}

func listProjects(ctx context.Context) ([]clientui.ProjectSummary, error) {
	_, remote, err := bindingCommandRemoteOpener(ctx, ".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = remote.Close() }()
	resp, err := listProjectsWithTimeout(ctx, remote)
	if err != nil {
		return nil, err
	}
	return resp.Projects, nil
}

func createProject(ctx context.Context, displayName string, workspaceRoot string) (serverapi.ProjectBinding, error) {
	trimmedDisplayName := strings.TrimSpace(displayName)
	if trimmedDisplayName == "" {
		return serverapi.ProjectBinding{}, errors.New("project name is required")
	}
	normalizedWorkspaceRoot, err := normalizeBindingCommandPath(workspaceRoot)
	if err != nil {
		return serverapi.ProjectBinding{}, err
	}
	_, remote, err := bindingCommandRemoteOpener(ctx, ".")
	if err != nil {
		return serverapi.ProjectBinding{}, err
	}
	defer func() { _ = remote.Close() }()
	resp, err := createProjectWithTimeout(ctx, remote, trimmedDisplayName, normalizedWorkspaceRoot)
	if err != nil {
		return serverapi.ProjectBinding{}, err
	}
	return resp.Binding, nil
}

func retargetSessionWorkspaceResponse(ctx context.Context, sessionID string, newPath string, projectID *string) (serverapi.SessionRetargetWorkspaceResponse, error) {
	newCfg, remote, err := bindingCommandRemoteOpener(ctx, newPath)
	if err != nil {
		return serverapi.SessionRetargetWorkspaceResponse{}, err
	}
	defer func() { _ = remote.Close() }()
	resp, err := bindingCommandSessionRetargeter(ctx, remote, sessionID, newCfg.WorkspaceRoot, projectID)
	if err != nil {
		return serverapi.SessionRetargetWorkspaceResponse{}, err
	}
	return resp, nil
}

func openBindingCommandRemote(ctx context.Context, path string) (config.App, *client.Remote, error) {
	cfg, err := loadBindingCommandConfig(path)
	if err != nil {
		return config.App{}, nil, err
	}
	dialCtx, cancel := context.WithTimeout(ctx, bindingCommandRPCTimeout)
	defer cancel()
	remote, err := client.DialConfiguredRemote(dialCtx, cfg)
	if err != nil {
		return config.App{}, nil, err
	}
	// When the operator selected an explicit non-default persistence root, only
	// operate on a server actually serving that root so project/binding commands
	// never display or mutate a different instance reachable on the same TCP
	// endpoint.
	if err := remote.RequireRoot(config.ExplicitPersistenceRootID(cfg)); err != nil {
		_ = remote.Close()
		return config.App{}, nil, err
	}
	return cfg, remote, nil
}

func normalizeBindingCommandPath(path string) (string, error) {
	trimmedPath := strings.TrimSpace(path)
	if trimmedPath == "" {
		return "", errors.New("path is required")
	}
	if filepath.IsAbs(trimmedPath) {
		return filepath.Clean(trimmedPath), nil
	}
	return filepath.Abs(trimmedPath)
}

func resolveWorkspaceBinding(ctx context.Context, projectViews apicontract.ProjectViewService, workspaceRoot string) (serverapi.ProjectBinding, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, bindingCommandRPCTimeout)
	defer cancel()
	resp, err := projectViews.ResolveProjectPath(rpcCtx, serverapi.ProjectResolvePathRequest{Path: workspaceRoot})
	if err != nil {
		return serverapi.ProjectBinding{}, err
	}
	if resp.Binding == nil {
		return serverapi.ProjectBinding{}, errWorkspaceNotRegistered
	}
	return *resp.Binding, nil
}

func loadBindingCommandConfig(path string) (config.App, error) {
	trimmedPath := strings.TrimSpace(path)
	if trimmedPath == "" {
		trimmedPath = "."
	}
	absPath, err := filepath.Abs(trimmedPath)
	if err != nil {
		return config.App{}, err
	}
	if info, statErr := os.Stat(absPath); statErr == nil && !info.IsDir() {
		absPath = filepath.Dir(absPath)
	}
	return config.Load(absPath, config.LoadOptions{})
}

var errWorkspaceNotRegistered = serverapi.ErrWorkspaceNotRegistered
