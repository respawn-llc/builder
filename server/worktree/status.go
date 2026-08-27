package worktree

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
)

func (s *Service) GetWorktreeStatus(ctx context.Context, req *worktreepb.StatusRequest) (*worktreepb.StatusSuccess, error) {
	if s == nil || s.metadata == nil || s.git == nil {
		return nil, errors.New("worktree service dependencies are required")
	}
	target, err := s.metadata.ResolveSessionExecutionTarget(ctx, req.SessionId)
	if err != nil {
		return nil, fmt.Errorf(
			"resolve worktree status target for session %q: %w",
			strings.TrimSpace(req.SessionId),
			err,
		)
	}
	root := strings.TrimSpace(target.WorkspaceRoot)
	if target.Worktree != nil {
		root = strings.TrimSpace(target.Worktree.Root)
	}
	status := &worktreepb.StatusTarget{RecordedRoot: root}
	if target.Worktree != nil {
		record, err := s.metadata.GetWorktreeRecordByID(ctx, target.Worktree.ID)
		switch {
		case err == nil:
			displayName := record.DisplayName
			status.DisplayName = &displayName
			gitMetadata, metadataErr := worktreeGitMetadataFromRecord(record)
			if metadataErr != nil {
				return nil, fmt.Errorf(
					"decode recorded worktree metadata for %q: %w",
					strings.TrimSpace(target.Worktree.ID),
					metadataErr,
				)
			}
			if gitMetadata.RecordedBranch != nil {
				branchRef := gitMetadata.RecordedBranch.Ref()
				status.RecordedBranchRef = &branchRef
			}
		case errors.Is(err, sql.ErrNoRows):
		default:
			return nil, fmt.Errorf(
				"resolve recorded worktree metadata for %q: %w",
				strings.TrimSpace(target.Worktree.ID),
				err,
			)
		}
	}
	projectedTarget, err := contractSessionExecutionTarget(target)
	if err != nil {
		return nil, err
	}
	response := &worktreepb.StatusSuccess{
		Target:   projectedTarget,
		Worktree: status,
		Problems: []*worktreepb.StatusProblem{},
	}
	if _, err := os.Stat(root); err != nil {
		kind := worktreepb.StatusProblemKind_WORKTREE_STATUS_PROBLEM_ROOT_INACCESSIBLE
		if errors.Is(err, os.ErrNotExist) {
			kind = worktreepb.StatusProblemKind_WORKTREE_STATUS_PROBLEM_ROOT_MISSING
		}
		problemRoot := root
		response.Problems = append(response.Problems, &worktreepb.StatusProblem{Kind: kind, Root: &problemRoot})
		return response, nil
	}
	observed, err := s.git.InspectTarget(ctx, root)
	if errors.Is(err, errGitTargetNotFound) {
		problemRoot := root
		response.Problems = append(response.Problems, &worktreepb.StatusProblem{
			Kind: worktreepb.StatusProblemKind_WORKTREE_STATUS_PROBLEM_GIT_BINDING_MISSING,
			Root: &problemRoot,
		})
		return response, nil
	}
	if err != nil {
		return response, fmt.Errorf("inspect recorded worktree root %q: %w", root, err)
	}
	workspace, err := s.git.InspectTarget(ctx, target.WorkspaceRoot)
	if errors.Is(err, errGitTargetNotFound) {
		problemRoot := target.WorkspaceRoot
		response.Problems = append(response.Problems, &worktreepb.StatusProblem{
			Kind: worktreepb.StatusProblemKind_WORKTREE_STATUS_PROBLEM_GIT_BINDING_MISSING,
			Root: &problemRoot,
		})
		return response, nil
	}
	if err != nil {
		return response, fmt.Errorf("inspect workspace root %q: %w", target.WorkspaceRoot, err)
	}
	observedRoot := observed.Root
	response.Worktree.ObservedRoot = &observedRoot
	if observed.Identity.CommonDir != workspace.Identity.CommonDir {
		problemRoot := root
		response.Problems = append(response.Problems, &worktreepb.StatusProblem{
			Kind: worktreepb.StatusProblemKind_WORKTREE_STATUS_PROBLEM_GIT_BINDING_MISMATCHED,
			Root: &problemRoot,
		})
	}
	if response.Worktree.RecordedBranchRef != nil {
		exists, err := s.git.RefExists(ctx, root, *response.Worktree.RecordedBranchRef)
		if err != nil {
			return response, fmt.Errorf(
				"inspect recorded worktree ref %q at %q: %w",
				*response.Worktree.RecordedBranchRef,
				root,
				err,
			)
		}
		if !exists {
			problemRef := *response.Worktree.RecordedBranchRef
			response.Problems = append(response.Problems, &worktreepb.StatusProblem{
				Kind: worktreepb.StatusProblemKind_WORKTREE_STATUS_PROBLEM_RECORDED_REF_MISSING,
				Ref:  &problemRef,
			})
		}
	}
	return response, nil
}
