package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"core/server/workflow"
	"core/shared/config"
)

// ErrBaseRefRequired is returned when a create spec omits a required base ref.
// Callers match it via errors.Is; the create_branch context is added with %w.
var ErrBaseRefRequired = errors.New("base ref is required")

type ExecutionTargetResolutionFailureKind string

const (
	ExecutionTargetResolutionFailureNonGit      ExecutionTargetResolutionFailureKind = "non_git"
	ExecutionTargetResolutionFailureUnavailable ExecutionTargetResolutionFailureKind = "unavailable"
	ExecutionTargetResolutionFailureInvalidRef  ExecutionTargetResolutionFailureKind = "invalid_ref"
)

type ExecutionTargetResolutionError struct {
	Kind         ExecutionTargetResolutionFailureKind
	Policy       workflow.ExecutionPolicyMode
	RequestedRef string
	err          error
}

func (e *ExecutionTargetResolutionError) Error() string {
	switch e.Kind {
	case ExecutionTargetResolutionFailureNonGit:
		return fmt.Sprintf("cannot resolve %q execution target because the source workspace is not a Git worktree", e.Policy)
	case ExecutionTargetResolutionFailureInvalidRef:
		return fmt.Sprintf("cannot resolve custom execution target ref %q", e.RequestedRef)
	default:
		return fmt.Sprintf("cannot resolve %q execution target from the source workspace", e.Policy)
	}
}

func (e *ExecutionTargetResolutionError) Unwrap() error {
	return e.err
}

type ExecutionTargetResolution struct {
	Source workflow.ExecutionTargetResolvedSource
}

// InvalidCreateTargetError reports that a requested create target is neither a
// valid branch name nor a resolvable ref. It exposes the offending target so
// callers can inspect it via errors.As instead of parsing message wording.
type InvalidCreateTargetError struct {
	Target string
}

func (e *InvalidCreateTargetError) Error() string {
	return fmt.Sprintf("target %q is not a valid branch name or resolvable ref", e.Target)
}

type GitWorktree struct {
	Root           string
	HeadOID        string
	BranchRef      string
	BranchName     string
	Detached       bool
	Bare           bool
	LockedReason   string
	PrunableReason string
	DirtyFileCount int
	IsMain         bool
}

type CreateSpec struct {
	BaseRef      string
	CreateBranch bool
	BranchName   string
}

type CreateTargetResolutionKind string

const (
	CreateTargetResolutionKindNewBranch      CreateTargetResolutionKind = "new_branch"
	CreateTargetResolutionKindExistingBranch CreateTargetResolutionKind = "existing_branch"
	CreateTargetResolutionKindDetachedRef    CreateTargetResolutionKind = "detached_ref"
)

type CreateTargetResolution struct {
	Input       string
	Kind        CreateTargetResolutionKind
	ResolvedRef string
}

type gitCommandRunner interface {
	Output(ctx context.Context, dir string, args ...string) ([]byte, error)
	Run(ctx context.Context, dir string, args ...string) ([]byte, int, error)
}

type GitInspector struct {
	runner gitCommandRunner
}

func NewGitInspector(runner gitCommandRunner) *GitInspector {
	if runner == nil {
		runner = execGitCommandRunner{}
	}
	return &GitInspector{runner: runner}
}

func (i *GitInspector) List(ctx context.Context, workspaceRoot string) ([]GitWorktree, error) {
	if i == nil {
		return nil, fmt.Errorf("git inspector is required")
	}
	canonicalRoot, err := config.CanonicalWorkspaceRoot(workspaceRoot)
	if err != nil {
		return nil, err
	}
	output, err := i.runner.Output(ctx, canonicalRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return parseGitWorktreeListPorcelain(string(output), canonicalRoot)
}

func (i *GitInspector) BranchExists(ctx context.Context, workspaceRoot string, branchName string) (bool, error) {
	if i == nil {
		return false, fmt.Errorf("git inspector is required")
	}
	canonicalRoot, err := config.CanonicalWorkspaceRoot(workspaceRoot)
	if err != nil {
		return false, err
	}
	trimmedBranch := strings.TrimSpace(branchName)
	if trimmedBranch == "" {
		return false, fmt.Errorf("branch name is required")
	}
	_, exitCode, err := i.runner.Run(ctx, canonicalRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+trimmedBranch)
	if err == nil {
		return true, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, ctxErr
	}
	if exitCode == 1 {
		return false, nil
	}
	return false, err
}

func (i *GitInspector) CommonDirectory(ctx context.Context, workspaceRoot string) (string, error) {
	if i == nil {
		return "", fmt.Errorf("git inspector is required")
	}
	canonicalRoot, err := config.CanonicalWorkspaceRoot(workspaceRoot)
	if err != nil {
		return "", err
	}
	output, exitCode, err := i.runner.Run(ctx, canonicalRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", formatGitRunError(exitCode, err, output, "rev-parse", "--path-format=absolute", "--git-common-dir")
	}
	commonDir := strings.TrimSpace(string(output))
	if commonDir == "" {
		return "", errors.New("Git common directory is required")
	}
	return config.CanonicalWorkspaceRoot(commonDir)
}

func (i *GitInspector) ResolveExecutionTarget(ctx context.Context, workspaceRoot string, policy workflow.ExecutionPolicyMode, customRef *string) (ExecutionTargetResolution, error) {
	if i == nil {
		return ExecutionTargetResolution{}, fmt.Errorf("git inspector is required")
	}
	canonicalRoot, err := config.CanonicalWorkspaceRoot(workspaceRoot)
	if err != nil {
		return ExecutionTargetResolution{}, err
	}
	if err := i.requireGitWorktree(ctx, canonicalRoot, policy); err != nil {
		return ExecutionTargetResolution{}, err
	}
	switch policy {
	case workflow.ExecutionPolicyHead:
		return i.resolveHeadExecutionTarget(ctx, canonicalRoot, policy)
	case workflow.ExecutionPolicyDefaultBranch:
		return i.resolveDefaultBranchExecutionTarget(ctx, canonicalRoot)
	case workflow.ExecutionPolicyCustomRef:
		if customRef == nil || strings.TrimSpace(*customRef) == "" {
			return ExecutionTargetResolution{}, executionTargetResolutionFailure(ExecutionTargetResolutionFailureInvalidRef, policy, "", nil)
		}
		return i.resolveCustomRefExecutionTarget(ctx, canonicalRoot, strings.TrimSpace(*customRef))
	default:
		return ExecutionTargetResolution{}, fmt.Errorf("Git execution target resolution does not support policy %q", policy)
	}
}

func (i *GitInspector) requireGitWorktree(ctx context.Context, workspaceRoot string, policy workflow.ExecutionPolicyMode) error {
	output, exitCode, err := i.runner.Run(ctx, workspaceRoot, "rev-parse", "--is-inside-work-tree")
	if err == nil {
		if strings.TrimSpace(string(output)) == "true" {
			return nil
		}
		return executionTargetResolutionFailure(ExecutionTargetResolutionFailureNonGit, policy, "", nil)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if exitCode == 128 {
		return executionTargetResolutionFailure(ExecutionTargetResolutionFailureNonGit, policy, "", nil)
	}
	return executionTargetResolutionFailure(ExecutionTargetResolutionFailureUnavailable, policy, "", formatGitRunError(exitCode, err, output, "rev-parse", "--is-inside-work-tree"))
}

func (i *GitInspector) resolveHeadExecutionTarget(ctx context.Context, workspaceRoot string, policy workflow.ExecutionPolicyMode) (ExecutionTargetResolution, error) {
	namedRef, detached, err := i.resolveSymbolicHead(ctx, workspaceRoot, policy)
	if err != nil {
		return ExecutionTargetResolution{}, err
	}
	commit, err := i.resolveCommit(ctx, workspaceRoot, "HEAD", policy, ExecutionTargetResolutionFailureUnavailable, "")
	if err != nil {
		return ExecutionTargetResolution{}, err
	}
	if detached {
		return ExecutionTargetResolution{Source: workflow.ExecutionTargetResolvedSource{
			Kind:   workflow.ExecutionTargetSourceDetachedCommit,
			Commit: commit,
		}}, nil
	}
	return ExecutionTargetResolution{Source: workflow.ExecutionTargetResolvedSource{
		Kind:     workflow.ExecutionTargetSourceNamedRef,
		NamedRef: namedRef,
		Commit:   commit,
	}}, nil
}

func (i *GitInspector) resolveDefaultBranchExecutionTarget(ctx context.Context, workspaceRoot string) (ExecutionTargetResolution, error) {
	headRef, detached, err := i.resolveSymbolicHead(ctx, workspaceRoot, workflow.ExecutionPolicyDefaultBranch)
	if err != nil {
		return ExecutionTargetResolution{}, err
	}
	remote := "origin"
	if !detached && headRef != nil {
		branch := strings.TrimPrefix(*headRef, "refs/heads/")
		output, exitCode, err := i.runner.Run(ctx, workspaceRoot, "for-each-ref", "--format=%(upstream:remotename)", "refs/heads/"+branch)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ExecutionTargetResolution{}, ctxErr
			}
			return ExecutionTargetResolution{}, executionTargetResolutionFailure(ExecutionTargetResolutionFailureUnavailable, workflow.ExecutionPolicyDefaultBranch, "", formatGitRunError(exitCode, err, output, "for-each-ref", "--format=%(upstream:remotename)", "refs/heads/"+branch))
		}
		if upstreamRemote := strings.TrimSpace(string(output)); upstreamRemote != "" {
			remote = upstreamRemote
		}
	}
	defaultPointer := "refs/remotes/" + remote + "/HEAD"
	output, exitCode, err := i.runner.Run(ctx, workspaceRoot, "symbolic-ref", "--quiet", defaultPointer)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ExecutionTargetResolution{}, ctxErr
		}
		if exitCode == 1 {
			return ExecutionTargetResolution{}, executionTargetResolutionFailure(ExecutionTargetResolutionFailureUnavailable, workflow.ExecutionPolicyDefaultBranch, "", nil)
		}
		return ExecutionTargetResolution{}, executionTargetResolutionFailure(ExecutionTargetResolutionFailureUnavailable, workflow.ExecutionPolicyDefaultBranch, "", formatGitRunError(exitCode, err, output, "symbolic-ref", "--quiet", defaultPointer))
	}
	namedRef := strings.TrimSpace(string(output))
	if !strings.HasPrefix(namedRef, "refs/") {
		return ExecutionTargetResolution{}, executionTargetResolutionFailure(ExecutionTargetResolutionFailureUnavailable, workflow.ExecutionPolicyDefaultBranch, "", nil)
	}
	commit, err := i.resolveCommit(ctx, workspaceRoot, namedRef, workflow.ExecutionPolicyDefaultBranch, ExecutionTargetResolutionFailureUnavailable, "")
	if err != nil {
		return ExecutionTargetResolution{}, err
	}
	return ExecutionTargetResolution{Source: workflow.ExecutionTargetResolvedSource{
		Kind:     workflow.ExecutionTargetSourceNamedRef,
		NamedRef: &namedRef,
		Commit:   commit,
	}}, nil
}

func (i *GitInspector) resolveCustomRefExecutionTarget(ctx context.Context, workspaceRoot string, requestedRef string) (ExecutionTargetResolution, error) {
	commit, err := i.resolveCommit(ctx, workspaceRoot, requestedRef, workflow.ExecutionPolicyCustomRef, ExecutionTargetResolutionFailureInvalidRef, requestedRef)
	if err != nil {
		return ExecutionTargetResolution{}, err
	}
	namedRef, err := i.resolveCanonicalNamedRef(ctx, workspaceRoot, requestedRef)
	if err != nil {
		return ExecutionTargetResolution{}, err
	}
	if namedRef == nil {
		return ExecutionTargetResolution{Source: workflow.ExecutionTargetResolvedSource{
			Kind:   workflow.ExecutionTargetSourceDetachedCommit,
			Commit: commit,
		}}, nil
	}
	return ExecutionTargetResolution{Source: workflow.ExecutionTargetResolvedSource{
		Kind:     workflow.ExecutionTargetSourceNamedRef,
		NamedRef: namedRef,
		Commit:   commit,
	}}, nil
}

func (i *GitInspector) resolveSymbolicHead(ctx context.Context, workspaceRoot string, policy workflow.ExecutionPolicyMode) (*string, bool, error) {
	output, exitCode, err := i.runner.Run(ctx, workspaceRoot, "symbolic-ref", "--quiet", "HEAD")
	if err == nil {
		namedRef := strings.TrimSpace(string(output))
		if !strings.HasPrefix(namedRef, "refs/") {
			return nil, false, executionTargetResolutionFailure(ExecutionTargetResolutionFailureUnavailable, policy, "", nil)
		}
		return &namedRef, false, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, false, ctxErr
	}
	if exitCode == 1 {
		return nil, true, nil
	}
	return nil, false, executionTargetResolutionFailure(ExecutionTargetResolutionFailureUnavailable, policy, "", formatGitRunError(exitCode, err, output, "symbolic-ref", "--quiet", "HEAD"))
}

func (i *GitInspector) resolveCommit(ctx context.Context, workspaceRoot string, ref string, policy workflow.ExecutionPolicyMode, failureKind ExecutionTargetResolutionFailureKind, requestedRef string) (string, error) {
	output, exitCode, err := i.runner.Run(ctx, workspaceRoot, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		if exitCode == 1 {
			return "", executionTargetResolutionFailure(failureKind, policy, requestedRef, nil)
		}
		return "", executionTargetResolutionFailure(ExecutionTargetResolutionFailureUnavailable, policy, requestedRef, formatGitRunError(exitCode, err, output, "rev-parse", "--verify", "--quiet", ref+"^{commit}"))
	}
	commit := strings.TrimSpace(string(output))
	if commit == "" {
		return "", executionTargetResolutionFailure(failureKind, policy, requestedRef, nil)
	}
	return commit, nil
}

func (i *GitInspector) resolveCanonicalNamedRef(ctx context.Context, workspaceRoot string, requestedRef string) (*string, error) {
	output, exitCode, err := i.runner.Run(ctx, workspaceRoot, "rev-parse", "--symbolic-full-name", "--verify", requestedRef)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if exitCode == 1 {
			return nil, nil
		}
		return nil, executionTargetResolutionFailure(ExecutionTargetResolutionFailureUnavailable, workflow.ExecutionPolicyCustomRef, requestedRef, formatGitRunError(exitCode, err, output, "rev-parse", "--symbolic-full-name", "--verify", requestedRef))
	}
	namedRef := strings.TrimSpace(string(output))
	if strings.HasPrefix(namedRef, "refs/") {
		return &namedRef, nil
	}
	if namedRef != "HEAD" {
		return nil, nil
	}
	headRef, detached, err := i.resolveSymbolicHead(ctx, workspaceRoot, workflow.ExecutionPolicyCustomRef)
	if err != nil || detached {
		return nil, err
	}
	return headRef, nil
}

func executionTargetResolutionFailure(kind ExecutionTargetResolutionFailureKind, policy workflow.ExecutionPolicyMode, requestedRef string, err error) *ExecutionTargetResolutionError {
	return &ExecutionTargetResolutionError{
		Kind:         kind,
		Policy:       policy,
		RequestedRef: requestedRef,
		err:          err,
	}
}

func (i *GitInspector) ResolveCreateTarget(ctx context.Context, workspaceRoot string, rawTarget string) (CreateTargetResolution, error) {
	if i == nil {
		return CreateTargetResolution{}, fmt.Errorf("git inspector is required")
	}
	canonicalRoot, err := config.CanonicalWorkspaceRoot(workspaceRoot)
	if err != nil {
		return CreateTargetResolution{}, err
	}
	trimmedTarget := strings.TrimSpace(rawTarget)
	if trimmedTarget == "" {
		return CreateTargetResolution{}, fmt.Errorf("target is required")
	}
	validBranchName, err := i.isValidBranchName(ctx, canonicalRoot, trimmedTarget)
	if err != nil {
		return CreateTargetResolution{}, err
	}
	if validBranchName {
		branchOutput, branchExit, err := i.runner.Run(ctx, canonicalRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+trimmedTarget+"^{object}")
		if err == nil {
			return CreateTargetResolution{Input: trimmedTarget, Kind: CreateTargetResolutionKindExistingBranch, ResolvedRef: trimmedTarget}, nil
		}
		if branchExit != 1 {
			return CreateTargetResolution{}, formatGitRunError(branchExit, err, branchOutput, "rev-parse", "--verify", "--quiet", "refs/heads/"+trimmedTarget+"^{object}")
		}
	}
	refOutput, refExit, err := i.runner.Run(ctx, canonicalRoot, "rev-parse", "--verify", "--quiet", trimmedTarget+"^{object}")
	if err != nil {
		if refExit == 1 {
			if !validBranchName {
				return CreateTargetResolution{}, &InvalidCreateTargetError{Target: trimmedTarget}
			}
			return CreateTargetResolution{Input: trimmedTarget, Kind: CreateTargetResolutionKindNewBranch}, nil
		}
		return CreateTargetResolution{}, formatGitRunError(refExit, err, refOutput, "rev-parse", "--verify", "--quiet", trimmedTarget+"^{object}")
	}
	return CreateTargetResolution{Input: trimmedTarget, Kind: CreateTargetResolutionKindDetachedRef, ResolvedRef: strings.TrimSpace(string(refOutput))}, nil
}

func (i *GitInspector) isValidBranchName(ctx context.Context, workspaceRoot string, branchName string) (bool, error) {
	output, exitCode, err := i.runner.Run(ctx, workspaceRoot, "check-ref-format", "--branch", branchName)
	if err == nil {
		return true, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if exitCode > 0 {
		return false, nil
	}
	return false, formatGitRunError(exitCode, err, output, "check-ref-format", "--branch", branchName)
}

func (i *GitInspector) Add(ctx context.Context, workspaceRoot string, worktreeRoot string, spec CreateSpec) (bool, error) {
	if i == nil {
		return false, fmt.Errorf("git inspector is required")
	}
	canonicalWorkspaceRoot, err := config.CanonicalWorkspaceRoot(workspaceRoot)
	if err != nil {
		return false, err
	}
	canonicalWorktreeRoot, err := config.CanonicalWorkspaceRoot(worktreeRoot)
	if err != nil {
		return false, err
	}
	normalized, err := normalizeCreateSpec(spec)
	if err != nil {
		return false, err
	}
	args := []string{"worktree", "add"}
	if normalized.CreateBranch {
		args = append(args, "-b", normalized.BranchName, canonicalWorktreeRoot)
		if normalized.BaseRef != "" {
			args = append(args, normalized.BaseRef)
		}
	} else {
		args = append(args, canonicalWorktreeRoot, normalized.BaseRef)
	}
	if _, err := i.runner.Output(ctx, canonicalWorkspaceRoot, args...); err != nil {
		return false, err
	}
	return normalized.CreateBranch, nil
}

func (i *GitInspector) DirtyFileCount(ctx context.Context, worktreeRoot string) (int, error) {
	if i == nil {
		return 0, fmt.Errorf("git inspector is required")
	}
	canonicalWorktreeRoot, err := config.CanonicalWorkspaceRoot(worktreeRoot)
	if err != nil {
		return 0, err
	}
	output, err := i.runner.Output(ctx, canonicalWorktreeRoot, "status", "--porcelain=v1", "-z")
	if err != nil {
		return 0, err
	}
	return countPorcelainStatusEntries(output), nil
}

func (i *GitInspector) Remove(ctx context.Context, workspaceRoot string, worktreeRoot string, force bool) error {
	if i == nil {
		return fmt.Errorf("git inspector is required")
	}
	canonicalWorkspaceRoot, err := config.CanonicalWorkspaceRoot(workspaceRoot)
	if err != nil {
		return err
	}
	canonicalWorktreeRoot, err := config.CanonicalWorkspaceRoot(worktreeRoot)
	if err != nil {
		return err
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, canonicalWorktreeRoot)
	_, err = i.runner.Output(ctx, canonicalWorkspaceRoot, args...)
	return err
}

func countPorcelainStatusEntries(output []byte) int {
	fields := strings.Split(string(output), "\x00")
	count := 0
	for idx := 0; idx < len(fields); idx++ {
		entry := fields[idx]
		if strings.TrimSpace(entry) == "" {
			continue
		}
		count++
		if len(entry) >= 2 && (entry[0] == 'R' || entry[1] == 'R' || entry[0] == 'C' || entry[1] == 'C') {
			idx++
		}
	}
	return count
}

func (i *GitInspector) Prune(ctx context.Context, workspaceRoot string) error {
	if i == nil {
		return fmt.Errorf("git inspector is required")
	}
	canonicalWorkspaceRoot, err := config.CanonicalWorkspaceRoot(workspaceRoot)
	if err != nil {
		return err
	}
	_, err = i.runner.Output(ctx, canonicalWorkspaceRoot, "worktree", "prune")
	return err
}

func (i *GitInspector) deleteBranch(ctx context.Context, workspaceRoot string, branchName string, force bool) error {
	if i == nil {
		return fmt.Errorf("git inspector is required")
	}
	canonicalWorkspaceRoot, err := config.CanonicalWorkspaceRoot(workspaceRoot)
	if err != nil {
		return err
	}
	trimmedBranch := strings.TrimSpace(branchName)
	if trimmedBranch == "" {
		return fmt.Errorf("branch name is required")
	}
	deleteArg := "-d"
	if force {
		deleteArg = "-D"
	}
	_, err = i.runner.Output(ctx, canonicalWorkspaceRoot, "branch", deleteArg, trimmedBranch)
	return err
}

func defaultWorktreeRoot(baseDir string, workspaceID string, pathSeed string) (string, error) {
	trimmedBaseDir := strings.TrimSpace(baseDir)
	if trimmedBaseDir == "" {
		return "", fmt.Errorf("worktree base dir is required")
	}
	trimmedWorkspaceID := strings.TrimSpace(workspaceID)
	if trimmedWorkspaceID == "" {
		return "", fmt.Errorf("workspace id is required")
	}
	trimmedSeed := strings.TrimSpace(pathSeed)
	if trimmedSeed == "" {
		return "", fmt.Errorf("worktree path seed is required")
	}
	relativeBranchPath := filepath.Clean(filepath.FromSlash(trimmedSeed))
	if relativeBranchPath == "." || filepath.IsAbs(relativeBranchPath) || relativeBranchPath == ".." || strings.HasPrefix(relativeBranchPath, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("worktree path seed %q cannot be mapped to worktree path", trimmedSeed)
	}
	return config.CanonicalWorkspaceRoot(filepath.Join(trimmedBaseDir, trimmedWorkspaceID, relativeBranchPath))
}

func normalizeCreateSpec(spec CreateSpec) (CreateSpec, error) {
	baseRef := strings.TrimSpace(spec.BaseRef)
	branchName := strings.TrimSpace(spec.BranchName)
	if spec.CreateBranch {
		if branchName == "" {
			return CreateSpec{}, fmt.Errorf("branch name is required when create_branch=true")
		}
		if baseRef == "" {
			return CreateSpec{}, fmt.Errorf("%w when create_branch=true", ErrBaseRefRequired)
		}
		return CreateSpec{BaseRef: baseRef, CreateBranch: true, BranchName: branchName}, nil
	}
	if baseRef == "" {
		return CreateSpec{}, fmt.Errorf("%w when create_branch=false", ErrBaseRefRequired)
	}
	if branchName != "" {
		return CreateSpec{}, fmt.Errorf("branch name must be empty when create_branch=false")
	}
	return CreateSpec{BaseRef: baseRef, CreateBranch: false}, nil
}

type execGitCommandRunner struct{}

func (execGitCommandRunner) Output(ctx context.Context, dir string, args ...string) ([]byte, error) {
	output, exitCode, err := execGitCommandRunner{}.Run(ctx, dir, args...)
	if err != nil {
		return nil, formatGitRunError(exitCode, err, output, args...)
	}
	return output, nil
}

func (execGitCommandRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, int, error) {
	argv := append([]string(nil), args...)
	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.Dir = strings.TrimSpace(dir)
	cmd.Env = sanitizedGitCommandEnv(os.Environ())
	output, err := cmd.CombinedOutput()
	if err == nil {
		return output, 0, nil
	}
	exitCode := -1
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode = exitErr.ExitCode()
	}
	return output, exitCode, err
}

func sanitizedGitCommandEnv(base []string) []string {
	filtered := make([]string, 0, len(base))
	for _, entry := range base {
		key := entry
		if idx := strings.IndexByte(entry, '='); idx >= 0 {
			key = entry[:idx]
		}
		switch key {
		case "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_CONFIG", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS", "GIT_DIR", "GIT_GLOB_PATHSPECS", "GIT_GRAFT_FILE", "GIT_ICASE_PATHSPECS", "GIT_IMPLICIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_INTERNAL_SUPER_PREFIX", "GIT_LITERAL_PATHSPECS", "GIT_NAMESPACE", "GIT_NOGLOB_PATHSPECS", "GIT_NO_REPLACE_OBJECTS", "GIT_OBJECT_DIRECTORY", "GIT_PREFIX", "GIT_REPLACE_REF_BASE", "GIT_SHALLOW_FILE", "GIT_WORK_TREE":
			continue
		}
		if strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func formatGitRunError(exitCode int, err error, output []byte, args ...string) error {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	if exitCode < 0 {
		return fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), trimmed, err)
	}
	return fmt.Errorf("git %s: %s", strings.Join(args, " "), trimmed)
}

func parseGitWorktreeListPorcelain(body string, workspaceRoot string) ([]GitWorktree, error) {
	canonicalWorkspaceRoot, err := config.CanonicalWorkspaceRoot(workspaceRoot)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	entries := make([]GitWorktree, 0, 4)
	current := GitWorktree{}
	haveCurrent := false
	flush := func() error {
		if !haveCurrent {
			return nil
		}
		if strings.TrimSpace(current.Root) == "" {
			return fmt.Errorf("git worktree entry missing root")
		}
		canonicalRoot, err := config.CanonicalWorkspaceRoot(current.Root)
		if err != nil {
			return err
		}
		current.Root = canonicalRoot
		current.IsMain = canonicalRoot == canonicalWorkspaceRoot
		entries = append(entries, current)
		current = GitWorktree{}
		haveCurrent = false
		return nil
	}
	for _, rawLine := range lines {
		line := strings.TrimRight(rawLine, "\r")
		if strings.TrimSpace(line) == "" {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		key, value, hasValue := strings.Cut(line, " ")
		if !hasValue {
			value = ""
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "worktree":
			if err := flush(); err != nil {
				return nil, err
			}
			current = GitWorktree{Root: value}
			haveCurrent = true
		case "HEAD":
			if !haveCurrent {
				return nil, fmt.Errorf("git worktree HEAD entry without worktree root")
			}
			current.HeadOID = value
		case "branch":
			if !haveCurrent {
				return nil, fmt.Errorf("git worktree branch entry without worktree root")
			}
			current.BranchRef = value
			current.BranchName = shortBranchName(value)
		case "detached":
			if !haveCurrent {
				return nil, fmt.Errorf("git worktree detached entry without worktree root")
			}
			current.Detached = true
		case "bare":
			if !haveCurrent {
				return nil, fmt.Errorf("git worktree bare entry without worktree root")
			}
			current.Bare = true
		case "locked":
			if !haveCurrent {
				return nil, fmt.Errorf("git worktree locked entry without worktree root")
			}
			current.LockedReason = value
		case "prunable":
			if !haveCurrent {
				return nil, fmt.Errorf("git worktree prunable entry without worktree root")
			}
			current.PrunableReason = value
		default:
			return nil, fmt.Errorf("unsupported git worktree porcelain key %q", strings.TrimSpace(key))
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return entries, nil
}

func shortBranchName(ref string) string {
	trimmed := strings.TrimSpace(ref)
	if strings.HasPrefix(trimmed, "refs/heads/") {
		return strings.TrimPrefix(trimmed, "refs/heads/")
	}
	return trimmed
}
