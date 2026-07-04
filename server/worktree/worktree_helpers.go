package worktree

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
)

func mapSyncedWorktrees(items []syncedWorktree, target clientui.SessionExecutionTarget) ([]serverapi.WorktreeView, error) {
	out := make([]serverapi.WorktreeView, 0, len(items))
	for _, item := range items {
		view, err := worktreeViewFromSynced(item, target)
		if err != nil {
			return nil, err
		}
		out = append(out, view)
	}
	return out, nil
}

func worktreeViewFromSynced(item syncedWorktree, target clientui.SessionExecutionTarget) (serverapi.WorktreeView, error) {
	if err := validatePresentExecutionTargetWorktreeID(target); err != nil {
		return serverapi.WorktreeView{}, err
	}
	isCurrent := item.git.IsMain && target.Worktree == nil
	if target.Worktree != nil {
		targetWorktreeID := strings.TrimSpace(target.Worktree.ID)
		isCurrent = targetWorktreeID == strings.TrimSpace(item.record.ID)
	}
	return serverapi.WorktreeView{
		WorktreeID:      item.record.ID,
		DisplayName:     item.record.DisplayName,
		CanonicalRoot:   item.record.CanonicalRoot,
		Availability:    item.record.Availability,
		BranchRef:       item.git.BranchRef,
		BranchName:      item.git.BranchName,
		Detached:        item.git.Detached,
		LockedReason:    item.git.LockedReason,
		PrunableReason:  item.git.PrunableReason,
		DirtyFileCount:  item.git.DirtyFileCount,
		IsMain:          item.git.IsMain,
		IsCurrent:       isCurrent,
		Managed:         item.record.Managed,
		CreatedBranch:   item.record.CreatedBranch,
		OriginSessionID: item.record.OriginSessionID,
	}, nil
}

func findSyncedWorktreeByID(items []syncedWorktree, worktreeID string) (syncedWorktree, bool) {
	trimmedID := strings.TrimSpace(worktreeID)
	for _, item := range items {
		if strings.TrimSpace(item.record.ID) == trimmedID {
			return item, true
		}
	}
	return syncedWorktree{}, false
}

func findSyncedWorktreeByRoot(items []syncedWorktree, worktreeRoot string) (syncedWorktree, bool) {
	trimmedRoot := strings.TrimSpace(worktreeRoot)
	for _, item := range items {
		if strings.TrimSpace(item.record.CanonicalRoot) == trimmedRoot {
			return item, true
		}
	}
	return syncedWorktree{}, false
}

func findMainWorktree(items []syncedWorktree) (syncedWorktree, bool) {
	for _, item := range items {
		if item.git.IsMain {
			return item, true
		}
	}
	return syncedWorktree{}, false
}

func currentSyncedWorktree(items []syncedWorktree, target clientui.SessionExecutionTarget) (*syncedWorktree, error) {
	if err := validatePresentExecutionTargetWorktreeID(target); err != nil {
		return nil, err
	}
	if target.Worktree == nil {
		return nil, nil
	}
	trimmedID := strings.TrimSpace(target.Worktree.ID)
	for idx := range items {
		if strings.TrimSpace(items[idx].record.ID) == trimmedID {
			return &items[idx], nil
		}
	}
	return nil, nil
}

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
	body, err := json.Marshal(struct {
		HeadOID        string `json:"head_oid,omitempty"`
		BranchRef      string `json:"branch_ref,omitempty"`
		BranchName     string `json:"branch_name,omitempty"`
		Detached       bool   `json:"detached,omitempty"`
		Bare           bool   `json:"bare,omitempty"`
		LockedReason   string `json:"locked_reason,omitempty"`
		PrunableReason string `json:"prunable_reason,omitempty"`
	}{
		HeadOID:        entry.HeadOID,
		BranchRef:      entry.BranchRef,
		BranchName:     entry.BranchName,
		Detached:       entry.Detached,
		Bare:           entry.Bare,
		LockedReason:   entry.LockedReason,
		PrunableReason: entry.PrunableReason,
	})
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
