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
	"core/shared/worktreecontract"
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

func contractSessionExecutionTarget(target clientui.SessionExecutionTarget) worktreecontract.SessionExecutionTarget {
	var worktree *worktreecontract.SessionExecutionWorktreeTarget
	if target.Worktree != nil {
		worktree = &worktreecontract.SessionExecutionWorktreeTarget{
			ID:           target.Worktree.ID,
			Name:         target.Worktree.Name,
			Root:         target.Worktree.Root,
			Availability: target.Worktree.Availability,
		}
	}
	return worktreecontract.SessionExecutionTarget{
		WorkspaceID:           target.WorkspaceID,
		WorkspaceName:         target.WorkspaceName,
		WorkspaceRoot:         target.WorkspaceRoot,
		WorkspaceAvailability: worktreecontract.ProjectAvailability(target.WorkspaceAvailability),
		Worktree:              worktree,
		CwdRelpath:            target.CwdRelpath,
		EffectiveWorkdir:      target.EffectiveWorkdir,
	}
}

func kentCreatedBranchForCleanup(record metadata.WorktreeRecord, live *GitWorktree) (string, bool, error) {
	if !record.CreatedBranch {
		return "", false, nil
	}
	persisted, err := worktreeGitMetadataFromRecord(record)
	if err != nil {
		return "", false, err
	}
	if persisted.Branch == nil {
		return "", false, nil
	}
	if live != nil && (live.Detached || live.Branch == nil || live.Branch.Ref() != persisted.Branch.Ref()) {
		return "", false, nil
	}
	return persisted.Branch.Name(), true, nil
}

func worktreeNamedBranch(worktree GitWorktree) (string, bool) {
	if worktree.Branch == nil {
		return "", false
	}
	return worktree.Branch.Name(), true
}

type PathInspection struct {
	Availability worktreecontract.PathAvailability
	Directory    bool
}

func PathAvailability(path string) worktreecontract.PathAvailability {
	return InspectPath(path).Availability
}

func InspectPath(path string) PathInspection {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PathInspection{Availability: worktreecontract.PathAvailabilityMissing}
		}
		return PathInspection{Availability: worktreecontract.PathAvailabilityInaccessible}
	}
	if !info.IsDir() {
		return PathInspection{Availability: worktreecontract.PathAvailabilityInaccessible}
	}
	return PathInspection{
		Availability: worktreecontract.PathAvailabilityAvailable,
		Directory:    true,
	}
}

func marshalGitMetadata(entry GitWorktree) (string, error) {
	if err := entry.validateHead(); err != nil {
		return "", err
	}
	persisted := persistedGitWorktree{
		HeadOID:        entry.HeadOID,
		Detached:       entry.Detached,
		Bare:           entry.Bare,
		LockedReason:   entry.LockedReason,
		PrunableReason: entry.PrunableReason,
	}
	recordedBranch := entry.RecordedBranch
	if recordedBranch == nil {
		recordedBranch = entry.Branch
	}
	if recordedBranch != nil {
		branchRef := recordedBranch.Ref()
		branchName := recordedBranch.Name()
		persisted.BranchRef = &branchRef
		persisted.BranchName = &branchName
	}
	body, err := json.Marshal(persisted)
	if err != nil {
		return "", fmt.Errorf("marshal git worktree metadata: %w", err)
	}
	return string(body), nil
}

type persistedGitWorktree struct {
	HeadOID        string  `json:"head_oid,omitempty"`
	BranchRef      *string `json:"branch_ref,omitempty"`
	BranchName     *string `json:"branch_name,omitempty"`
	Detached       bool    `json:"detached,omitempty"`
	Bare           bool    `json:"bare,omitempty"`
	LockedReason   string  `json:"locked_reason,omitempty"`
	PrunableReason string  `json:"prunable_reason,omitempty"`
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
