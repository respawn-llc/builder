package runtimefeed

import (
	"fmt"
	"path/filepath"
	"strings"

	"core/shared/clientui"
	"core/shared/runtimeids"
)

type TranscriptSessionIdentity struct {
	SessionID             runtimeids.SessionID
	SessionName           *string
	ConversationFreshness clientui.ConversationFreshness
	ExecutionTarget       *clientui.SessionExecutionTarget
}

func (i TranscriptSessionIdentity) Validate() error {
	if i.SessionID.IsZero() {
		return fmt.Errorf("transcript session identity requires session id")
	}
	if err := validateOptionalNonEmptyString("transcript session name", i.SessionName); err != nil {
		return err
	}
	switch i.ConversationFreshness {
	case clientui.ConversationFreshnessFresh, clientui.ConversationFreshnessEstablished:
	default:
		return fmt.Errorf("unknown conversation freshness %d", i.ConversationFreshness)
	}
	if i.ExecutionTarget == nil {
		return nil
	}
	if err := validatePresentSessionExecutionTarget(*i.ExecutionTarget); err != nil {
		return fmt.Errorf("validate transcript session execution target: %w", err)
	}
	return nil
}

func validatePresentSessionExecutionTarget(target clientui.SessionExecutionTarget) error {
	if clientui.SessionExecutionTargetIsZero(target) {
		return fmt.Errorf("present session execution target is empty")
	}
	target = clientui.NormalizeSessionExecutionTarget(target)
	switch target.WorkspaceAvailability {
	case clientui.ProjectAvailabilityAvailable,
		clientui.ProjectAvailabilityMissing,
		clientui.ProjectAvailabilityInaccessible:
		if target.WorkspaceID == "" {
			return fmt.Errorf("linked session execution target requires workspace id")
		}
	case clientui.ProjectAvailabilityUnlinked:
	default:
		return fmt.Errorf("unknown session execution target workspace availability %q", target.WorkspaceAvailability)
	}
	if target.WorkspaceRoot == "" {
		return fmt.Errorf("session execution target requires workspace root")
	}
	baseRoot := target.WorkspaceRoot
	if target.Worktree != nil {
		if err := validateSessionExecutionWorktree(*target.Worktree); err != nil {
			return err
		}
		baseRoot = target.Worktree.Root
	}
	if target.CwdRelpath == "" {
		return fmt.Errorf("session execution target requires cwd relative path")
	}
	cwdRelpath := filepath.Clean(target.CwdRelpath)
	if cwdRelpath != target.CwdRelpath {
		return fmt.Errorf("session execution target cwd relative path %q is not normalized", target.CwdRelpath)
	}
	if !filepath.IsLocal(cwdRelpath) {
		return fmt.Errorf("session execution target cwd relative path %q escapes its root", target.CwdRelpath)
	}
	if target.EffectiveWorkdir == "" {
		return fmt.Errorf("session execution target requires effective workdir")
	}
	effectiveWorkdir := filepath.Clean(target.EffectiveWorkdir)
	if effectiveWorkdir != filepath.Clean(filepath.Join(baseRoot, cwdRelpath)) {
		return fmt.Errorf("session execution target effective workdir %q does not match root %q and cwd %q", target.EffectiveWorkdir, baseRoot, target.CwdRelpath)
	}
	relative, err := filepath.Rel(filepath.Clean(baseRoot), effectiveWorkdir)
	if err != nil {
		return fmt.Errorf("derive session execution target effective workdir: %w", err)
	}
	if !filepath.IsLocal(relative) {
		return fmt.Errorf("session execution target effective workdir %q escapes root %q", target.EffectiveWorkdir, baseRoot)
	}
	return nil
}

func validateSessionExecutionWorktree(worktree clientui.SessionExecutionWorktreeTarget) error {
	worktree.ID = strings.TrimSpace(worktree.ID)
	worktree.Root = strings.TrimSpace(worktree.Root)
	worktree.Availability = strings.TrimSpace(worktree.Availability)
	if worktree.ID == "" {
		return fmt.Errorf("session execution worktree requires id")
	}
	if worktree.Root == "" {
		return fmt.Errorf("session execution worktree requires root")
	}
	switch clientui.ProjectAvailability(worktree.Availability) {
	case clientui.ProjectAvailabilityAvailable,
		clientui.ProjectAvailabilityMissing,
		clientui.ProjectAvailabilityInaccessible:
		return nil
	default:
		return fmt.Errorf("unknown session execution worktree availability %q", worktree.Availability)
	}
}
