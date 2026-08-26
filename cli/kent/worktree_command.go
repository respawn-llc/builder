package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"core/shared/apicontract"
	"core/shared/client"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessionenv"
	"core/shared/worktreecontract"
)

const worktreeCommandTimeout = 5 * time.Second
const worktreeMutationTimeout = 30 * time.Second

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
	return withWorktreeCommandRemote(stderr, sessionID, func(remote apicontract.WorktreeService) int {
		ctx, cancel := context.WithTimeout(context.Background(), worktreeCommandTimeout)
		defer cancel()
		status, err := remote.GetWorktreeStatus(ctx, worktreecontract.StatusRequest{SessionID: sessionID})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOut {
			return writeCommandJSON(stdout, stderr, status)
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
		return withWorktreeCommandRemote(stderr, *sessionID, func(remote apicontract.WorktreeService) int {
			ctx, cancel := context.WithTimeout(context.Background(), worktreeCommandTimeout)
			defer cancel()
			response, err := remote.ListWorktrees(ctx, worktreecontract.ListRequest{SessionID: *sessionID})
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			if *jsonOut {
				return writeCommandJSON(stdout, stderr, response)
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
	response, err := remote.ListWorkspaceWorktrees(ctx, worktreecontract.WorkspaceListRequest{
		ProjectID:   binding.ProjectID,
		WorkspaceID: binding.WorkspaceID,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
		return writeCommandJSON(stdout, stderr, response)
	}
	writeWorktreeList(stdout, response.Worktrees, false)
	return 0
}

func writeWorktreeList(stdout io.Writer, worktrees []worktreecontract.ListEntry, showCurrent bool) {
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
	return withWorktreeCommandRemote(stderr, sessionID, func(remote apicontract.WorktreeService) int {
		resolveCtx, resolveCancel := context.WithTimeout(context.Background(), worktreeCommandTimeout)
		resolution, err := remote.ResolveWorktreeCreateTarget(resolveCtx, worktreecontract.CreateTargetResolveRequest{
			SessionID: sessionID,
			Target:    target,
		})
		resolveCancel()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		request := worktreecontract.CreateRequest{
			SetupOperationID: worktreecontract.NewSetupOperationID(),
			SessionID:        sessionID,
			RootPath:         rootPath,
		}
		switch resolution.Resolution.Kind {
		case worktreecontract.CreateTargetResolutionKindNewBranch:
			request.BaseRef = strings.TrimSpace(*baseRef)
			request.CreateBranch = true
			request.BranchName = target
		case worktreecontract.CreateTargetResolutionKindExistingBranch,
			worktreecontract.CreateTargetResolutionKindDetachedRef:
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
			return writeCommandJSON(stdout, stderr, response)
		}
		if response.Worktree.Topology.Variant != worktreecontract.TopologyVariantRegistered || response.Worktree.Topology.Registered == nil {
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
	header, err := newWorktreeCommandTransitionHeader(sessionID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return runScheduledWorktreeCommand(stdout, stderr, sessionID, *jsonOut, "enter", func(ctx context.Context, remote apicontract.WorktreeService) (worktreecontract.ScheduledAcknowledgement, error) {
		return remote.EnterWorktree(ctx, worktreecontract.EnterRequest{
			TransitionHeader: header,
			Selector:         strings.TrimSpace(fs.Args()[0]),
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
	header, err := newWorktreeCommandTransitionHeader(sessionID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return runScheduledWorktreeCommand(stdout, stderr, sessionID, *jsonOut, "leave", func(ctx context.Context, remote apicontract.WorktreeService) (worktreecontract.ScheduledAcknowledgement, error) {
		return remote.LeaveWorktree(ctx, worktreecontract.LeaveRequest{
			TransitionHeader: header,
		})
	})
}

func worktreeDeleteSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" worktree delete", stderr, worktreeDeleteUsage)
	sessionFlag := fs.String("session", "", "session to use; required outside Kent shell commands")
	force := fs.Bool("force", false, "authorize forced Git worktree folder removal when dirty or indeterminate")
	deleteBranch := fs.Bool("delete-branch", false, "safely delete the local branch after removing the worktree")
	forceDeleteBranch := fs.Bool("force-delete-branch", false, "force-delete the local branch; requires --delete-branch")
	jsonOut := fs.Bool("json", false, "write the delete result as JSON")
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 1 {
		fmt.Fprintln(stderr, "worktree delete requires exactly one selector")
		return 2
	}
	_, inAgentShell := sessionenv.LookupSessionID(os.LookupEnv)
	policy, err := worktreeBranchCleanupPolicy(*deleteBranch, *forceDeleteBranch, inAgentShell)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	sessionID, err := resolveWorktreeCommandSession(*sessionFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	header, err := newWorktreeCommandTransitionHeader(sessionID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return withWorktreeCommandRemote(stderr, sessionID, func(remote apicontract.WorktreeService) int {
		ctx, cancel := context.WithTimeout(context.Background(), worktreeMutationTimeout)
		defer cancel()
		result, err := remote.DeleteWorktree(ctx, worktreecontract.DeleteRequest{
			TransitionHeader:    header,
			Selector:            strings.TrimSpace(fs.Args()[0]),
			ForceFolderRemoval:  *force,
			BranchCleanupPolicy: policy,
		})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOut {
			return writeCommandJSON(stdout, stderr, result)
		}
		if result.Kind == worktreecontract.DeleteResultKindScheduled {
			fmt.Fprintf(stdout, "Scheduled deletion: %s\n", result.Scheduled.OperationID.String())
		} else {
			fmt.Fprintln(stdout, "Deleted worktree")
			if result.Completed != nil {
				if result.Completed.Cleanup.Kind == worktreecontract.BranchCleanupOutcomeRetained {
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

func worktreeBranchCleanupPolicy(deleteBranch bool, forceDeleteBranch bool, inAgentShell bool) (worktreecontract.BranchCleanupMode, error) {
	if forceDeleteBranch && !deleteBranch {
		return "", errors.New("--force-delete-branch requires --delete-branch")
	}
	if inAgentShell && deleteBranch {
		return "", errors.New("agent worktree deletion always retains branches; --delete-branch is not allowed inside Kent shell commands")
	}
	if forceDeleteBranch {
		return worktreecontract.BranchCleanupModeDeleteForce, nil
	}
	if deleteBranch {
		return worktreecontract.BranchCleanupModeDeleteSafe, nil
	}
	return worktreecontract.BranchCleanupModeRetain, nil
}

func runScheduledWorktreeCommand(
	stdout io.Writer,
	stderr io.Writer,
	sessionID string,
	jsonOut bool,
	action string,
	call func(context.Context, apicontract.WorktreeService) (worktreecontract.ScheduledAcknowledgement, error),
) int {
	return withWorktreeCommandRemote(stderr, sessionID, func(remote apicontract.WorktreeService) int {
		ctx, cancel := context.WithTimeout(context.Background(), worktreeCommandTimeout)
		defer cancel()
		ack, err := call(ctx, remote)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if jsonOut {
			return writeCommandJSON(stdout, stderr, ack)
		}
		fmt.Fprintf(stdout, "Worktree %s scheduled for the agent's next step. This usually takes a few seconds.\n", action)
		return 0
	})
}

func withWorktreeCommandRemote(stderr io.Writer, sessionID string, fn func(apicontract.WorktreeService) int) int {
	remote, err := openWorktreeCommandRemote(context.Background(), sessionID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	return fn(remote)
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

func worktreeCommandRuntimeOrigin() (*worktreecontract.RuntimeStepOrigin, error) {
	runID, hasRunID := os.LookupEnv(sessionenv.RunIDEnv)
	stepID, hasStepID := os.LookupEnv(sessionenv.StepIDEnv)
	if !hasRunID && !hasStepID {
		return nil, nil
	}
	origin := &worktreecontract.RuntimeStepOrigin{RunID: strings.TrimSpace(runID), StepID: strings.TrimSpace(stepID)}
	return origin, origin.Validate()
}

func newWorktreeCommandTransitionHeader(sessionID string) (worktreecontract.TransitionHeader, error) {
	origin, err := worktreeCommandRuntimeOrigin()
	if err != nil {
		return worktreecontract.TransitionHeader{}, err
	}
	return worktreecontract.TransitionHeader{
		OperationID: worktreecontract.NewOperationID(),
		SessionID:   sessionID,
		Origin:      origin,
	}, nil
}

func openWorktreeCommandRemote(ctx context.Context, sessionID string) (*client.Remote, error) {
	configRoot, err := nearestCommandConfigRoot()
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
	configRoot, err := nearestCommandConfigRoot()
	if err != nil {
		return nil, serverapi.ProjectBinding{}, err
	}
	cfg, discoveryRemote, err := openBindingCommandRemote(ctx, configRoot)
	if err != nil {
		return nil, serverapi.ProjectBinding{}, err
	}
	binding, err := resolveWorkspaceBinding(ctx, discoveryRemote, cfg.WorkspaceRoot)
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

func nearestCommandConfigRoot() (string, error) {
	workspaceRoot, err := config.FindNearestWorkspaceSettingsRoot(".")
	if err != nil {
		return "", err
	}
	if workspaceRoot != nil {
		return *workspaceRoot, nil
	}
	return ".", nil
}
