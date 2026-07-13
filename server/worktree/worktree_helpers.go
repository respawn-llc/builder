package worktree

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"core/server/metadata"
	"core/shared/clientui"
	"core/shared/config"
)

func validatePresentExecutionTargetWorktreeID(target clientui.SessionExecutionTarget) error {
	if target.Worktree == nil {
		return nil
	}
	if strings.TrimSpace(target.Worktree.ID) == "" {
		return errors.New("session execution target worktree id is required")
	}
	return nil
}

func (s *Service) shouldAttemptBranchCleanup(target syncedWorktree, explicitDeleteBranch bool) bool {
	if strings.TrimSpace(target.git.BranchName) == "" {
		return false
	}
	if explicitDeleteBranch {
		return true
	}
	return target.record.Managed && target.record.CreatedBranch
}

func (s *Service) branchCleanupSkippedMessage(target syncedWorktree, explicitDeleteBranch bool) string {
	branchName := strings.TrimSpace(target.git.BranchName)
	if branchName == "" {
		return ""
	}
	if explicitDeleteBranch || (target.record.Managed && target.record.CreatedBranch) {
		return ""
	}
	return fmt.Sprintf("Kept branch %s: Kent cannot prove this worktree created it", branchName)
}

func kentCreatedBranchForCleanup(record metadata.WorktreeRecord, live *GitWorktree) (string, bool, error) {
	if !record.CreatedBranch {
		return "", false, nil
	}
	persisted, err := worktreeGitMetadataFromRecord(record)
	if err != nil {
		return "", false, err
	}
	persistedRef := strings.TrimSpace(persisted.BranchRef)
	if persistedRef == "" {
		return "", false, nil
	}
	branchName := worktreeNamedBranch(persisted)
	if branchName == "" {
		return "", false, nil
	}
	if live != nil && (live.Detached || strings.TrimSpace(live.BranchRef) != persistedRef) {
		return "", false, nil
	}
	return branchName, true, nil
}

func worktreeNamedBranch(worktree GitWorktree) string {
	if branchName := strings.TrimSpace(worktree.BranchName); branchName != "" {
		return branchName
	}
	return shortBranchName(strings.TrimSpace(worktree.BranchRef))
}

func pathAvailability(path string) string {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "missing"
		}
		return "inaccessible"
	}
	return "available"
}

func marshalGitMetadata(entry GitWorktree) (string, error) {
	body, err := json.Marshal(entry)
	if err != nil {
		return "", fmt.Errorf("marshal git worktree metadata: %w", err)
	}
	return string(body), nil
}

func clampCwdRelpath(cwdRelpath string, nextBaseRoot string) string {
	trimmedRelpath := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(cwdRelpath))))
	if trimmedRelpath == "" || trimmedRelpath == "." || trimmedRelpath == "/" {
		return "."
	}
	if filepath.IsAbs(filepath.FromSlash(trimmedRelpath)) || trimmedRelpath == ".." || strings.HasPrefix(trimmedRelpath, "../") {
		return "."
	}
	candidate := filepath.Join(strings.TrimSpace(nextBaseRoot), filepath.FromSlash(trimmedRelpath))
	info, err := os.Stat(candidate)
	if err != nil || !info.IsDir() {
		return "."
	}
	return trimmedRelpath
}

func sameOrDescendantPath(root string, candidate string) bool {
	trimmedRoot := strings.TrimSpace(root)
	trimmedCandidate := strings.TrimSpace(candidate)
	if trimmedRoot == "" || trimmedCandidate == "" {
		return false
	}
	if trimmedRoot == trimmedCandidate {
		return true
	}
	rel, err := filepath.Rel(trimmedRoot, trimmedCandidate)
	if err != nil {
		return false
	}
	cleaned := filepath.Clean(rel)
	return cleaned != ".." && !strings.HasPrefix(cleaned, ".."+string(filepath.Separator))
}

func resolveSetupScriptPath(workspaceRoot string, configuredPath string) (string, error) {
	expanded, err := expandTildePath(configuredPath)
	if err != nil {
		return "", err
	}
	path := expanded
	if !filepath.IsAbs(path) {
		path = filepath.Join(strings.TrimSpace(workspaceRoot), path)
	}
	canonical, err := config.CanonicalWorkspaceRoot(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("setup script %q is a directory", canonical)
	}
	return canonical, nil
}

func expandTildePath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || !strings.HasPrefix(trimmed, "~") {
		return trimmed, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	if trimmed == "~" {
		return home, nil
	}
	if strings.HasPrefix(trimmed, "~/") {
		return filepath.Join(home, strings.TrimPrefix(trimmed, "~/")), nil
	}
	if strings.HasPrefix(trimmed, "~\\") {
		return filepath.Join(home, strings.TrimPrefix(trimmed, "~\\")), nil
	}
	return trimmed, nil
}
