package serverapi

import (
	"errors"
	"strings"

	"core/shared/clientui"
)

var ErrWorktreeNotFound = errors.New("worktree not found")
var ErrWorktreeBlocked = errors.New("worktree is blocked")

type WorktreeView struct {
	WorktreeID      string `json:"worktree_id"`
	DisplayName     string `json:"display_name"`
	CanonicalRoot   string `json:"canonical_root"`
	Availability    string `json:"availability"`
	BranchRef       string `json:"branch_ref,omitempty"`
	BranchName      string `json:"branch_name,omitempty"`
	Detached        bool   `json:"detached,omitempty"`
	LockedReason    string `json:"locked_reason,omitempty"`
	PrunableReason  string `json:"prunable_reason,omitempty"`
	DirtyFileCount  int    `json:"dirty_file_count,omitempty"`
	IsMain          bool   `json:"is_main,omitempty"`
	IsCurrent       bool   `json:"is_current,omitempty"`
	Managed         bool   `json:"managed,omitempty"`
	CreatedBranch   bool   `json:"created_branch,omitempty"`
	OriginSessionID string `json:"origin_session_id,omitempty"`
}

type WorktreeListRequest struct {
	SessionID         string `json:"session_id"`
	IncludeDirtyCount bool   `json:"include_dirty_count,omitempty"`
}

type WorktreeListResponse struct {
	Target    clientui.SessionExecutionTarget `json:"target"`
	Worktrees []WorktreeView                  `json:"worktrees"`
}

type WorktreeCreateTargetResolutionKind string

const (
	WorktreeCreateTargetResolutionKindNewBranch      WorktreeCreateTargetResolutionKind = "new_branch"
	WorktreeCreateTargetResolutionKindExistingBranch WorktreeCreateTargetResolutionKind = "existing_branch"
	WorktreeCreateTargetResolutionKindDetachedRef    WorktreeCreateTargetResolutionKind = "detached_ref"
)

type WorktreeCreateTargetResolution struct {
	Input       string                             `json:"input"`
	Kind        WorktreeCreateTargetResolutionKind `json:"kind"`
	ResolvedRef string                             `json:"resolved_ref,omitempty"`
}

type WorktreeCreateTargetResolveRequest struct {
	SessionID string `json:"session_id"`
	Target    string `json:"target"`
}

type WorktreeCreateTargetResolveResponse struct {
	Resolution WorktreeCreateTargetResolution `json:"resolution"`
}

type WorktreeCreateRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	BaseRef         string `json:"base_ref,omitempty"`
	CreateBranch    bool   `json:"create_branch,omitempty"`
	BranchName      string `json:"branch_name,omitempty"`
	RootPath        string `json:"root_path,omitempty"`
}

type WorktreeCreateResponse struct {
	Target         clientui.SessionExecutionTarget `json:"target"`
	Worktree       WorktreeView                    `json:"worktree"`
	CreatedBranch  bool                            `json:"created_branch"`
	SetupScheduled bool                            `json:"setup_scheduled"`
}

type WorktreeSwitchRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	WorktreeID      string `json:"worktree_id"`
}

type WorktreeSwitchResponse struct {
	Target   clientui.SessionExecutionTarget `json:"target"`
	Worktree WorktreeView                    `json:"worktree"`
}

type WorktreeDeleteRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	WorktreeID      string `json:"worktree_id"`
	DeleteBranch    bool   `json:"delete_branch,omitempty"`
}

type WorktreeDeleteResponse struct {
	Target               clientui.SessionExecutionTarget `json:"target"`
	Worktree             WorktreeView                    `json:"worktree"`
	BranchDeleted        bool                            `json:"branch_deleted,omitempty"`
	BranchCleanupMessage string                          `json:"branch_cleanup_message,omitempty"`
}

func (r WorktreeListRequest) Validate() error {
	if err := validateRequiredSessionID(r.SessionID); err != nil {
		return err
	}
	return nil
}

func (r WorktreeCreateTargetResolveRequest) Validate() error {
	if err := validateRequiredSessionID(r.SessionID); err != nil {
		return err
	}
	if strings.TrimSpace(r.Target) == "" {
		return errors.New("target is required")
	}
	return nil
}

func (r WorktreeCreateRequest) Validate() error {
	if err := validateClientRequestID(r.ClientRequestID); err != nil {
		return err
	}
	if err := validateRequiredSessionID(r.SessionID); err != nil {
		return err
	}
	if r.CreateBranch {
		if strings.TrimSpace(r.BaseRef) == "" {
			return errors.New("base_ref is required when create_branch=true")
		}
		if strings.TrimSpace(r.BranchName) == "" {
			return errors.New("branch_name is required when create_branch=true")
		}
		return nil
	}
	if strings.TrimSpace(r.BaseRef) == "" {
		return errors.New("base_ref is required when create_branch=false")
	}
	if strings.TrimSpace(r.BranchName) != "" {
		return errors.New("branch_name must be empty when create_branch=false")
	}
	return nil
}

func (r WorktreeSwitchRequest) Validate() error {
	if err := validateClientRequestID(r.ClientRequestID); err != nil {
		return err
	}
	if err := validateRequiredSessionID(r.SessionID); err != nil {
		return err
	}
	if strings.TrimSpace(r.WorktreeID) == "" {
		return errors.New("worktree_id is required")
	}
	return nil
}

func (r WorktreeDeleteRequest) Validate() error {
	if err := validateClientRequestID(r.ClientRequestID); err != nil {
		return err
	}
	if err := validateRequiredSessionID(r.SessionID); err != nil {
		return err
	}
	if strings.TrimSpace(r.WorktreeID) == "" {
		return errors.New("worktree_id is required")
	}
	return nil
}
