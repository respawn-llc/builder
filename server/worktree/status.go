package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"core/shared/serverapi"
)

func (s *Service) GetWorktreeStatus(ctx context.Context, req serverapi.WorktreeStatusRequest) (serverapi.WorktreeStatusResponse, error) {
	if s == nil || s.metadata == nil || s.git == nil {
		return serverapi.WorktreeStatusResponse{}, errors.New("worktree service dependencies are required")
	}
	if err := req.Validate(); err != nil {
		return serverapi.WorktreeStatusResponse{}, err
	}
	target, err := s.metadata.ResolveSessionExecutionTarget(ctx, req.SessionID)
	if err != nil {
		return serverapi.WorktreeStatusResponse{}, fmt.Errorf(
			"resolve worktree status target for session %q: %w",
			strings.TrimSpace(req.SessionID),
			err,
		)
	}
	root := strings.TrimSpace(target.EffectiveWorkdir)
	if root == "" {
		root = strings.TrimSpace(target.WorkspaceRoot)
	}
	status := serverapi.WorktreeStatusTarget{RecordedRoot: root}
	if target.Worktree != nil {
		record, err := s.metadata.GetWorktreeRecordByID(ctx, target.Worktree.ID)
		if err == nil {
			displayName := record.DisplayName
			status.DisplayName = &displayName
			if gitMetadata, metadataErr := worktreeGitMetadataFromRecord(record); metadataErr == nil && strings.TrimSpace(gitMetadata.BranchRef) != "" {
				branchRef := gitMetadata.BranchRef
				status.RecordedBranchRef = &branchRef
			}
		}
	}
	response := serverapi.WorktreeStatusResponse{Target: target, Worktree: status}
	if _, err := os.Stat(root); err != nil {
		kind := serverapi.WorktreeStatusProblemRootInaccessible
		if errors.Is(err, os.ErrNotExist) {
			kind = serverapi.WorktreeStatusProblemRootMissing
		}
		problemRoot := root
		response.Problems = append(response.Problems, serverapi.WorktreeStatusProblem{Kind: kind, Root: &problemRoot})
		return response, nil
	}
	observed, observedErr := s.git.InspectTarget(ctx, root)
	workspace, workspaceErr := s.git.InspectTarget(ctx, target.WorkspaceRoot)
	if observedErr != nil || workspaceErr != nil {
		problemRoot := root
		response.Problems = append(response.Problems, serverapi.WorktreeStatusProblem{Kind: serverapi.WorktreeStatusProblemGitBindingMissing, Root: &problemRoot})
		return response, nil
	}
	observedRoot := observed.Root
	response.Worktree.ObservedRoot = &observedRoot
	if observed.Identity.CommonDir != workspace.Identity.CommonDir {
		problemRoot := root
		response.Problems = append(response.Problems, serverapi.WorktreeStatusProblem{Kind: serverapi.WorktreeStatusProblemGitBindingMismatched, Root: &problemRoot})
	}
	if response.Worktree.RecordedBranchRef != nil {
		exists, err := s.git.RefExists(ctx, root, *response.Worktree.RecordedBranchRef)
		if err != nil || !exists {
			problemRef := *response.Worktree.RecordedBranchRef
			response.Problems = append(response.Problems, serverapi.WorktreeStatusProblem{Kind: serverapi.WorktreeStatusProblemRecordedRefMissing, Ref: &problemRef})
		}
	}
	return response, nil
}
