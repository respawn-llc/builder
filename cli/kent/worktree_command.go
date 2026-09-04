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
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/serverapi"
	"core/shared/sessionenv"
	"core/shared/worktreecontract"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
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
		status, err := remote.GetWorktreeStatus(ctx, &worktreepb.StatusRequest{SessionId: sessionID})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOut {
			return writeWorktreeProtoJSON(stdout, stderr, status)
		}
		fmt.Fprintln(stdout, status.GetWorktree().GetRecordedRoot())
		for _, problem := range status.Problems {
			kind, err := worktreeStatusProblemKindJSON(problem.Kind)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			fmt.Fprintln(stdout, kind)
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
			response, err := remote.ListWorktrees(ctx, &worktreepb.ListRequest{SessionId: *sessionID})
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			if *jsonOut {
				return writeWorktreeProtoJSON(stdout, stderr, response)
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
	response, err := remote.ListWorkspaceWorktrees(ctx, &worktreepb.WorkspaceListRequest{
		ProjectId:   binding.ProjectID,
		WorkspaceId: binding.WorkspaceID,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
		return writeWorktreeProtoJSON(stdout, stderr, response)
	}
	writeWorktreeList(stdout, response.Worktrees, false)
	return 0
}

func writeWorktreeList(stdout io.Writer, worktrees []*worktreepb.ListEntry, showCurrent bool) {
	for _, entry := range worktrees {
		variant, err := worktreeTopologyVariantJSON(entry.GetTopology())
		if err != nil {
			fmt.Fprintln(stdout, err)
			continue
		}
		if !showCurrent {
			fmt.Fprintf(stdout, "%s\t%s\n", entry.GetProjection().GetSelector(), variant)
			continue
		}
		current := " "
		if entry.GetProjection().GetIsCurrent() {
			current = "*"
		}
		fmt.Fprintf(stdout, "%s %s\t%s\n", current, entry.GetProjection().GetSelector(), variant)
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
		resolution, err := remote.ResolveWorktreeCreateTarget(resolveCtx, &worktreepb.CreateTargetResolveRequest{
			SessionId: sessionID,
			Target:    target,
		})
		resolveCancel()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		request := &worktreepb.CreateRequest{
			SetupOperationId: worktreecontract.NewSetupOperationID().String(),
			SessionId:        sessionID,
			Spec:             &worktreepb.CreateSpec{},
		}
		if rootPath != "" {
			request.RootPath = &rootPath
		}
		switch resolution.GetResolution().GetKind() {
		case worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_NEW_BRANCH:
			base := strings.TrimSpace(*baseRef)
			request.Spec.BaseRef = &base
			request.Spec.CreateBranch = true
			request.Spec.BranchName = &target
		case worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_EXISTING_BRANCH,
			worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_DETACHED_REF:
			base := strings.TrimSpace(resolution.GetResolution().GetResolvedRef())
			if base == "" {
				base = target
			}
			request.Spec.BaseRef = &base
		default:
			fmt.Fprintf(stderr, "unsupported worktree target resolution: %s\n", resolution.GetResolution().GetKind())
			return 1
		}
		response, err := remote.CreateWorktree(context.Background(), request)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOut {
			return writeWorktreeProtoJSON(stdout, stderr, response)
		}
		registered := response.GetWorktree().GetTopology().GetRegistered()
		if registered == nil {
			fmt.Fprintln(stderr, "create returned a non-registered worktree")
			return 1
		}
		fmt.Fprintln(stdout, registered.GetGit().GetCanonicalRoot())
		fmt.Fprintf(stdout, "Enter with: %s worktree enter %s\n", config.Command, response.GetWorktree().GetProjection().GetSelector())
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
	operationID := worktreecontract.NewOperationID()
	return runScheduledWorktreeCommand(stdout, stderr, sessionID, *jsonOut, "enter", func(ctx context.Context, remote apicontract.WorktreeService) (*worktreepb.ScheduledAcknowledgement, error) {
		return remote.EnterWorktree(ctx, &worktreepb.EnterRequest{
			OperationId: operationID.String(),
			SessionId:   sessionID,
			Selector:    strings.TrimSpace(fs.Args()[0]),
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
	operationID := worktreecontract.NewOperationID()
	return runScheduledWorktreeCommand(stdout, stderr, sessionID, *jsonOut, "leave", func(ctx context.Context, remote apicontract.WorktreeService) (*worktreepb.ScheduledAcknowledgement, error) {
		return remote.LeaveWorktree(ctx, &worktreepb.LeaveRequest{
			OperationId: operationID.String(),
			SessionId:   sessionID,
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
	return withWorktreeCommandRemote(stderr, sessionID, func(remote apicontract.WorktreeService) int {
		ctx, cancel := context.WithTimeout(context.Background(), worktreeMutationTimeout)
		defer cancel()
		result, err := remote.DeleteWorktree(ctx, &worktreepb.DeleteRequest{
			SessionId:           sessionID,
			Selector:            strings.TrimSpace(fs.Args()[0]),
			ForceFolderRemoval:  *force,
			BranchCleanupPolicy: policy,
		})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOut {
			return writeWorktreeProtoJSON(stdout, stderr, result)
		}
		fmt.Fprintln(stdout, "Deleted worktree")
		if result.GetCleanup().GetKind() == worktreepb.BranchCleanupOutcomeKind_WORKTREE_BRANCH_CLEANUP_OUTCOME_RETAINED {
			fmt.Fprintf(stdout, "Kept branch %s", result.GetCleanup().GetBranchName())
			if result.GetCleanup().Diagnostic != nil {
				fmt.Fprintf(stdout, ": %s", result.GetCleanup().GetDiagnostic())
			}
			fmt.Fprintln(stdout)
		}
		if result.LeftoverRoot != nil {
			fmt.Fprintf(stdout, "Left folder untouched: %s\n", result.GetLeftoverRoot())
		}
		return 0
	})
}

func worktreeBranchCleanupPolicy(deleteBranch bool, forceDeleteBranch bool, inAgentShell bool) (worktreepb.BranchCleanupMode, error) {
	if forceDeleteBranch && !deleteBranch {
		return worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_UNSPECIFIED, errors.New("--force-delete-branch requires --delete-branch")
	}
	if inAgentShell && deleteBranch {
		return worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_UNSPECIFIED, errors.New("agent worktree deletion always retains branches; --delete-branch is not allowed inside Kent shell commands")
	}
	if forceDeleteBranch {
		return worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_DELETE_FORCE, nil
	}
	if deleteBranch {
		return worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_DELETE_SAFE, nil
	}
	return worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_RETAIN, nil
}

func runScheduledWorktreeCommand(
	stdout io.Writer,
	stderr io.Writer,
	sessionID string,
	jsonOut bool,
	action string,
	call func(context.Context, apicontract.WorktreeService) (*worktreepb.ScheduledAcknowledgement, error),
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
			return writeWorktreeProtoJSON(stdout, stderr, ack)
		}
		fmt.Fprintf(stdout, "Worktree %s scheduled for the agent's next step. This usually takes a few seconds.\n", action)
		return 0
	})
}

func writeWorktreeProtoJSON(stdout io.Writer, stderr io.Writer, message proto.Message) int {
	data, err := protojson.Marshal(message)
	if err == nil {
		_, err = fmt.Fprintln(stdout, string(data))
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func worktreeTopologyVariantJSON(topology *worktreepb.TopologyEntry) (string, error) {
	switch topology.GetTopology().(type) {
	case *worktreepb.TopologyEntry_MainWorkspace:
		return "main_workspace", nil
	case *worktreepb.TopologyEntry_Registered:
		return "registered", nil
	case *worktreepb.TopologyEntry_External:
		return "external", nil
	case *worktreepb.TopologyEntry_Missing:
		return "missing", nil
	default:
		return "", errors.New("worktree topology is missing")
	}
}

func worktreeStatusProblemKindJSON(kind worktreepb.StatusProblemKind) (string, error) {
	switch kind {
	case worktreepb.StatusProblemKind_WORKTREE_STATUS_PROBLEM_ROOT_MISSING:
		return "root_missing", nil
	case worktreepb.StatusProblemKind_WORKTREE_STATUS_PROBLEM_ROOT_INACCESSIBLE:
		return "root_inaccessible", nil
	case worktreepb.StatusProblemKind_WORKTREE_STATUS_PROBLEM_GIT_BINDING_MISSING:
		return "git_binding_missing", nil
	case worktreepb.StatusProblemKind_WORKTREE_STATUS_PROBLEM_GIT_BINDING_MISMATCHED:
		return "git_binding_mismatched", nil
	case worktreepb.StatusProblemKind_WORKTREE_STATUS_PROBLEM_RECORDED_REF_MISSING:
		return "recorded_ref_missing", nil
	default:
		return "", fmt.Errorf("unsupported worktree status problem %s", kind)
	}
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
