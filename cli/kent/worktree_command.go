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

	"core/shared/client"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessionenv"

	"github.com/google/uuid"
)

const worktreeCommandTimeout = 5 * time.Second
const worktreeMutationTimeout = 30 * time.Second

type worktreeCommandRemote interface {
	GetWorktreeStatus(context.Context, serverapi.WorktreeStatusRequest) (serverapi.WorktreeStatusResponse, error)
	ListWorktrees(context.Context, serverapi.WorktreeListRequest) (serverapi.WorktreeListResponse, error)
	ResolveWorktreeCreateTarget(context.Context, serverapi.WorktreeCreateTargetResolveRequest) (serverapi.WorktreeCreateTargetResolveResponse, error)
	CreateWorktree(context.Context, serverapi.WorktreeCreateRequest) (serverapi.WorktreeCreateResponse, error)
	EnterWorktree(context.Context, serverapi.WorktreeEnterRequest) (serverapi.WorktreeScheduledAcknowledgement, error)
	LeaveWorktree(context.Context, serverapi.WorktreeLeaveRequest) (serverapi.WorktreeScheduledAcknowledgement, error)
	DeleteWorktree(context.Context, serverapi.WorktreeDeleteRequest) (serverapi.WorktreeDeleteResult, error)
	Close() error
}

var worktreeCommandRemoteOpener = openWorktreeCommandRemote

func worktreeSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		return worktreeStatusSubcommand(nil, stdout, stderr)
	}
	switch args[0] {
	case "status":
		return worktreeStatusSubcommand(args[1:], stdout, stderr)
	case "list", "ls":
		return worktreeListSubcommand(args[1:], stdout, stderr)
	case "create":
		return worktreeCreateSubcommand(args[1:], stdout, stderr)
	case "enter":
		return worktreeEnterSubcommand(args[1:], stdout, stderr)
	case "leave":
		return worktreeLeaveSubcommand(args[1:], stdout, stderr)
	case "delete", "remove", "rm":
		return worktreeDeleteSubcommand(args[1:], stdout, stderr)
	case "--help", "-h":
		worktreeUsage.write(newCommandFlagSet(config.Command+" worktree", stderr, worktreeUsage))
		return 0
	default:
		fmt.Fprintf(stderr, "unknown worktree command: %s\n", args[0])
		return 2
	}
}

func worktreeStatusSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" worktree status", stderr, worktreeStatusUsage)
	sessionFlag := fs.String("session", "", "session to inspect; required outside Kent shell commands")
	jsonOut := fs.Bool("json", false, "write the status response as JSON")
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(stderr, "worktree status does not accept positional arguments")
		return 2
	}
	sessionID, err := resolveWorktreeCommandSession(*sessionFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return withWorktreeCommandRemote(stderr, sessionID, func(remote worktreeCommandRemote) int {
		ctx, cancel := context.WithTimeout(context.Background(), worktreeCommandTimeout)
		defer cancel()
		status, err := remote.GetWorktreeStatus(ctx, serverapi.WorktreeStatusRequest{SessionID: sessionID})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOut {
			return encodeWorktreeJSON(stdout, stderr, status)
		}
		fmt.Fprintln(stdout, status.Worktree.RecordedRoot)
		for _, problem := range status.Problems {
			fmt.Fprintln(stdout, problem.Kind)
		}
		return 0
	})
}

func worktreeListSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" worktree list", stderr, worktreeListUsage)
	sessionFlag := fs.String("session", "", "session whose current worktree to mark")
	jsonOut := fs.Bool("json", false, "write the list response as JSON")
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(stderr, "worktree list does not accept positional arguments")
		return 2
	}
	if sessionID := resolveOptionalWorktreeCommandSession(*sessionFlag); sessionID != nil {
		return withWorktreeCommandRemote(stderr, *sessionID, func(remote worktreeCommandRemote) int {
			ctx, cancel := context.WithTimeout(context.Background(), worktreeCommandTimeout)
			defer cancel()
			response, err := remote.ListWorktrees(ctx, serverapi.WorktreeListRequest{SessionID: *sessionID})
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			if *jsonOut {
				return encodeWorktreeJSON(stdout, stderr, response)
			}
			writeWorktreeList(stdout, response.Worktrees, true)
			return 0
		})
	}
	remote, binding, err := openWorktreeWorkspaceListRemote(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), worktreeCommandTimeout)
	defer cancel()
	response, err := remote.ListWorkspaceWorktrees(ctx, serverapi.WorktreeWorkspaceListRequest{
		ProjectID:   binding.ProjectID,
		WorkspaceID: binding.WorkspaceID,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
		return encodeWorktreeJSON(stdout, stderr, response)
	}
	writeWorktreeList(stdout, response.Worktrees, false)
	return 0
}

func writeWorktreeList(stdout io.Writer, worktrees []serverapi.WorktreeListEntry, showCurrent bool) {
	for _, entry := range worktrees {
		if !showCurrent {
			fmt.Fprintf(stdout, "%s\t%s\n", entry.Projection.Selector, entry.Topology.Variant)
			continue
		}
		current := " "
		if entry.Projection.IsCurrent {
			current = "*"
		}
		fmt.Fprintf(stdout, "%s %s\t%s\n", current, entry.Projection.Selector, entry.Topology.Variant)
	}
}

func worktreeCreateSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" worktree create", stderr, worktreeCreateUsage)
	sessionFlag := fs.String("session", "", "session to use; required outside Kent shell commands")
	baseRef := fs.String("base", "HEAD", "base ref for a new branch")
	jsonOut := fs.Bool("json", false, "write the create response as JSON")
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	positionals := fs.Args()
	if len(positionals) < 1 || len(positionals) > 2 {
		fmt.Fprintln(stderr, "worktree create requires <branch-or-ref> and optional [path]")
		return 2
	}
	sessionID, err := resolveWorktreeCommandSession(*sessionFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	target := strings.TrimSpace(positionals[0])
	rootPath := ""
	if len(positionals) == 2 {
		rootPath = strings.TrimSpace(positionals[1])
	}
	return withWorktreeCommandRemote(stderr, sessionID, func(remote worktreeCommandRemote) int {
		resolveCtx, resolveCancel := context.WithTimeout(context.Background(), worktreeCommandTimeout)
		resolution, err := remote.ResolveWorktreeCreateTarget(resolveCtx, serverapi.WorktreeCreateTargetResolveRequest{
			SessionID: sessionID,
			Target:    target,
		})
		resolveCancel()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		request := serverapi.WorktreeCreateRequest{
			ClientRequestID:  uuid.NewString(),
			SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
			SessionID:        sessionID,
			RootPath:         rootPath,
		}
		switch resolution.Resolution.Kind {
		case serverapi.WorktreeCreateTargetResolutionKindNewBranch:
			request.BaseRef = strings.TrimSpace(*baseRef)
			request.CreateBranch = true
			request.BranchName = target
		case serverapi.WorktreeCreateTargetResolutionKindExistingBranch,
			serverapi.WorktreeCreateTargetResolutionKindDetachedRef:
			request.BaseRef = strings.TrimSpace(resolution.Resolution.ResolvedRef)
			if request.BaseRef == "" {
				request.BaseRef = target
			}
		default:
			fmt.Fprintf(stderr, "unsupported worktree target resolution: %s\n", resolution.Resolution.Kind)
			return 1
		}
		response, err := remote.CreateWorktree(context.Background(), request)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOut {
			return encodeWorktreeJSON(stdout, stderr, response)
		}
		if response.Worktree.Topology.Variant != serverapi.WorktreeTopologyVariantRegistered || response.Worktree.Topology.Registered == nil {
			fmt.Fprintln(stderr, "create returned a non-registered worktree")
			return 1
		}
		fmt.Fprintln(stdout, response.Worktree.Topology.Registered.Git.CanonicalRoot)
		fmt.Fprintf(stdout, "Enter with: %s worktree enter %s\n", config.Command, response.Worktree.Projection.Selector)
		return 0
	})
}

func worktreeEnterSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" worktree enter", stderr, worktreeEnterUsage)
	sessionFlag := fs.String("session", "", "session to retarget; required outside Kent shell commands")
	jsonOut := fs.Bool("json", false, "write the scheduled acknowledgement as JSON")
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 1 {
		fmt.Fprintln(stderr, "worktree enter requires exactly one selector")
		return 2
	}
	sessionID, err := resolveWorktreeCommandSession(*sessionFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	origin, err := worktreeCommandRuntimeOrigin()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return runScheduledWorktreeCommand(stdout, stderr, sessionID, *jsonOut, func(ctx context.Context, remote worktreeCommandRemote) (serverapi.WorktreeScheduledAcknowledgement, error) {
		return remote.EnterWorktree(ctx, serverapi.WorktreeEnterRequest{
			OperationID: serverapi.NewWorktreeOperationID(),
			SessionID:   sessionID,
			Selector:    strings.TrimSpace(fs.Args()[0]),
			Origin:      origin,
		})
	})
}

func worktreeLeaveSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" worktree leave", stderr, worktreeLeaveUsage)
	sessionFlag := fs.String("session", "", "session to retarget; required outside Kent shell commands")
	jsonOut := fs.Bool("json", false, "write the scheduled acknowledgement as JSON")
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(stderr, "worktree leave does not accept positional arguments")
		return 2
	}
	sessionID, err := resolveWorktreeCommandSession(*sessionFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	origin, err := worktreeCommandRuntimeOrigin()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return runScheduledWorktreeCommand(stdout, stderr, sessionID, *jsonOut, func(ctx context.Context, remote worktreeCommandRemote) (serverapi.WorktreeScheduledAcknowledgement, error) {
		return remote.LeaveWorktree(ctx, serverapi.WorktreeLeaveRequest{
			OperationID: serverapi.NewWorktreeOperationID(),
			SessionID:   sessionID,
			Origin:      origin,
		})
	})
}

func worktreeDeleteSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" worktree delete", stderr, worktreeDeleteUsage)
	sessionFlag := fs.String("session", "", "session to use; required outside Kent shell commands")
	force := fs.Bool("force", false, "authorize forced Git worktree folder removal when dirty or indeterminate")
	deleteBranch := fs.Bool("delete-branch", false, "safely delete the local branch after removing the worktree")
	jsonOut := fs.Bool("json", false, "write the delete result as JSON")
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 1 {
		fmt.Fprintln(stderr, "worktree delete requires exactly one selector")
		return 2
	}
	sessionID, err := resolveWorktreeCommandSession(*sessionFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	origin, err := worktreeCommandRuntimeOrigin()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if _, inAgentShell := sessionenv.LookupSessionID(os.LookupEnv); inAgentShell && *deleteBranch {
		fmt.Fprintln(stderr, "agent worktree deletion always retains branches; --delete-branch is not allowed inside Kent shell commands")
		return 2
	}
	policy := serverapi.WorktreeBranchCleanupModeRetain
	if *deleteBranch {
		policy = serverapi.WorktreeBranchCleanupModeDeleteSafe
	}
	return withWorktreeCommandRemote(stderr, sessionID, func(remote worktreeCommandRemote) int {
		ctx, cancel := context.WithTimeout(context.Background(), worktreeMutationTimeout)
		defer cancel()
		result, err := remote.DeleteWorktree(ctx, serverapi.WorktreeDeleteRequest{
			OperationID:         serverapi.NewWorktreeOperationID(),
			SessionID:           sessionID,
			Selector:            strings.TrimSpace(fs.Args()[0]),
			ForceFolderRemoval:  *force,
			Origin:              origin,
			BranchCleanupPolicy: policy,
		})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOut {
			return encodeWorktreeJSON(stdout, stderr, result)
		}
		if result.Kind == serverapi.WorktreeDeleteResultKindScheduled {
			fmt.Fprintf(stdout, "Scheduled deletion: %s\n", result.Scheduled.OperationID.String())
		} else {
			fmt.Fprintln(stdout, "Deleted worktree")
			if result.Completed != nil {
				if result.Completed.Cleanup.Kind == serverapi.WorktreeBranchCleanupOutcomeRetained {
					fmt.Fprintf(stdout, "Kept branch %s", *result.Completed.Cleanup.BranchName)
					if result.Completed.Cleanup.Diagnostic != nil {
						fmt.Fprintf(stdout, ": %s", *result.Completed.Cleanup.Diagnostic)
					}
					fmt.Fprintln(stdout)
				}
				if result.Completed.LeftoverRoot != nil {
					fmt.Fprintf(stdout, "Left folder untouched: %s\n", *result.Completed.LeftoverRoot)
				}
			}
		}
		return 0
	})
}

func runScheduledWorktreeCommand(
	stdout io.Writer,
	stderr io.Writer,
	sessionID string,
	jsonOut bool,
	call func(context.Context, worktreeCommandRemote) (serverapi.WorktreeScheduledAcknowledgement, error),
) int {
	return withWorktreeCommandRemote(stderr, sessionID, func(remote worktreeCommandRemote) int {
		ctx, cancel := context.WithTimeout(context.Background(), worktreeCommandTimeout)
		defer cancel()
		ack, err := call(ctx, remote)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if jsonOut {
			return encodeWorktreeJSON(stdout, stderr, ack)
		}
		fmt.Fprintf(stdout, "Scheduled: %s\n", ack.OperationID.String())
		return 0
	})
}

func withWorktreeCommandRemote(stderr io.Writer, sessionID string, fn func(worktreeCommandRemote) int) int {
	remote, err := worktreeCommandRemoteOpener(context.Background(), sessionID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	return fn(remote)
}

func encodeWorktreeJSON(stdout io.Writer, stderr io.Writer, value any) int {
	if err := json.NewEncoder(stdout).Encode(value); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func resolveWorktreeCommandSession(sessionFlag string) (string, error) {
	if sessionID := resolveOptionalWorktreeCommandSession(sessionFlag); sessionID != nil {
		return *sessionID, nil
	}
	return "", errors.New("worktree command requires --session outside Kent shell commands")
}

func resolveOptionalWorktreeCommandSession(sessionFlag string) *string {
	if sessionID, ok := sessionenv.LookupSessionID(os.LookupEnv); ok {
		return &sessionID
	}
	if trimmed := strings.TrimSpace(sessionFlag); trimmed != "" {
		return &trimmed
	}
	return nil
}

func worktreeCommandRuntimeOrigin() (*serverapi.RuntimeStepOrigin, error) {
	runID, stepID := sessionenv.LookupRunStepID(os.LookupEnv)
	if runID == "" && stepID == "" {
		return nil, nil
	}
	if runID == "" || stepID == "" {
		return nil, errors.New("Kent runtime origin requires both KENT_RUN_ID and KENT_STEP_ID")
	}
	parsedRunID, runErr := runtimeids.ParseRunID(runID)
	parsedStepID, stepErr := runtimeids.ParseStepID(stepID)
	if runErr != nil {
		return nil, runErr
	}
	if stepErr != nil {
		return nil, stepErr
	}
	return &serverapi.RuntimeStepOrigin{RunID: parsedRunID, StepID: parsedStepID}, nil
}

func openWorktreeCommandRemote(ctx context.Context, sessionID string) (worktreeCommandRemote, error) {
	configRoot, err := worktreeCommandConfigRoot()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(configRoot, config.LoadOptions{})
	if err != nil {
		return nil, err
	}
	dialCtx, cancel := context.WithTimeout(ctx, worktreeCommandTimeout)
	defer cancel()
	remote, err := client.DialConfiguredRemoteForSession(dialCtx, cfg, sessionID)
	if err != nil {
		return nil, err
	}
	if err := remote.RequireRoot(config.ExplicitPersistenceRootID(cfg)); err != nil {
		_ = remote.Close()
		return nil, err
	}
	return remote, nil
}

func openWorktreeWorkspaceListRemote(ctx context.Context) (*client.Remote, serverapi.ProjectBinding, error) {
	configRoot, err := worktreeCommandConfigRoot()
	if err != nil {
		return nil, serverapi.ProjectBinding{}, err
	}
	cfg, discoveryRemote, err := openBindingCommandRemote(ctx, configRoot)
	if err != nil {
		return nil, serverapi.ProjectBinding{}, err
	}
	binding, err := bindingCommandWorkspaceResolver(ctx, discoveryRemote, cfg.WorkspaceRoot)
	_ = discoveryRemote.Close()
	if err != nil {
		return nil, serverapi.ProjectBinding{}, err
	}
	dialCtx, cancel := context.WithTimeout(ctx, worktreeCommandTimeout)
	defer cancel()
	remote, err := client.DialConfiguredRemoteForProjectWorkspaceID(
		dialCtx,
		cfg,
		strings.TrimSpace(binding.ProjectID),
		strings.TrimSpace(binding.WorkspaceID),
	)
	if err != nil {
		return nil, serverapi.ProjectBinding{}, err
	}
	if err := remote.RequireRoot(config.ExplicitPersistenceRootID(cfg)); err != nil {
		_ = remote.Close()
		return nil, serverapi.ProjectBinding{}, err
	}
	return remote, binding, nil
}

func worktreeCommandConfigRoot() (string, error) {
	workspaceRoot, err := config.FindNearestWorkspaceSettingsRoot(".")
	if err != nil {
		return "", err
	}
	if workspaceRoot != nil {
		return *workspaceRoot, nil
	}
	return ".", nil
}
