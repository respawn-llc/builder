package sessionservice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"core/server/metadata"
	"core/server/session"
	sessionruntime "core/server/sessionruntime"
	shelltool "core/server/tools/shell"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
)

type sessionRetargetMetadata interface {
	PlanSessionWorkspaceRetarget(context.Context, metadata.SessionWorkspaceRetargetRequest) (metadata.SessionWorkspaceRetargetPlan, error)
	CommitSessionWorkspaceRetarget(context.Context, metadata.SessionWorkspaceRetargetPlan, time.Time) (metadata.SessionWorkspaceRetargetResult, error)
}

type sessionRetargetRunBlocker interface {
	BlockSessionRuns(sessionIDs []string) func()
	PublishSessionIdentity(sessionID string, target *clientui.SessionExecutionTarget)
}

type sessionMaintenanceRunner interface {
	RunSessionMaintenance(
		ctx context.Context,
		sessionID string,
		fn func(context.Context, *session.Store, *sessionruntime.ActiveRuntimeMaintenance) error,
	) error
}

type sessionProcessSource interface {
	List() []shelltool.Snapshot
}

type SessionWorkspaceRetargeter struct {
	metadata    sessionRetargetMetadata
	runs        sessionRetargetRunBlocker
	maintenance sessionMaintenanceRunner
	processes   sessionProcessSource
}

func NewSessionWorkspaceRetargeter(
	metadataStore sessionRetargetMetadata,
	runs sessionRetargetRunBlocker,
	maintenance sessionMaintenanceRunner,
	processes sessionProcessSource,
) *SessionWorkspaceRetargeter {
	return &SessionWorkspaceRetargeter{
		metadata:    metadataStore,
		runs:        runs,
		maintenance: maintenance,
		processes:   processes,
	}
}

func (s *SessionWorkspaceRetargeter) RetargetWorkspace(ctx context.Context, req metadata.SessionWorkspaceRetargetRequest) (metadata.SessionWorkspaceRetargetResult, error) {
	if s == nil || s.metadata == nil || s.runs == nil || s.maintenance == nil || s.processes == nil {
		return metadata.SessionWorkspaceRetargetResult{}, errors.New("session workspace retarget dependencies are required")
	}
	plan, err := s.metadata.PlanSessionWorkspaceRetarget(ctx, req)
	if err != nil {
		return metadata.SessionWorkspaceRetargetResult{}, err
	}
	releaseRuns := s.runs.BlockSessionRuns([]string{plan.SessionID})
	defer releaseRuns()

	var result metadata.SessionWorkspaceRetargetResult
	err = s.maintenance.RunSessionMaintenance(ctx, plan.SessionID, func(runCtx context.Context, store *session.Store, activeRuntime *sessionruntime.ActiveRuntimeMaintenance) error {
		currentPlan, err := s.metadata.PlanSessionWorkspaceRetarget(runCtx, req)
		if err != nil {
			return err
		}
		ownedProcessActive, err := s.ownedBackgroundProcessActive(currentPlan.SessionID)
		if err != nil {
			return err
		}
		if ownedProcessActive {
			return &serverapi.SessionRetargetError{
				Reason:        serverapi.SessionRetargetBackgroundProcess,
				SessionID:     currentPlan.SessionID,
				SourceProject: currentPlan.SourceProject,
				TargetRoot:    currentPlan.TargetWorkspaceRoot,
			}
		}
		if store == nil {
			return errors.New("session store is required")
		}
		storeDir, err := config.CanonicalWorkspaceRoot(store.Dir())
		if err != nil {
			return fmt.Errorf("canonicalize session store path: %w", err)
		}
		sourceDir, err := config.CanonicalWorkspaceRoot(currentPlan.SourceSessionDir)
		if err != nil {
			return fmt.Errorf("canonicalize source session artifact path: %w", err)
		}
		if storeDir != sourceDir {
			return fmt.Errorf("session store path %q does not match source artifact %q", store.Dir(), currentPlan.SourceSessionDir)
		}
		if err := validateSessionArtifactSource(currentPlan.SourceSessionDir); err != nil {
			return err
		}
		if currentPlan.CrossProject() {
			if err := prepareSessionArtifactTarget(currentPlan.TargetSessionDir); err != nil {
				return err
			}
		}
		updatedAt := time.Now().UTC()
		err = store.RunArtifactRelocation(session.ArtifactRelocationTarget{
			SessionDir:         currentPlan.TargetSessionDir,
			WorkspaceRoot:      currentPlan.TargetWorkspaceRoot,
			WorkspaceContainer: filepath.Base(currentPlan.TargetWorkspaceRoot),
			UpdatedAt:          updatedAt,
		}, func() error {
			if activeRuntime != nil {
				if err := activeRuntime.Validate(); err != nil {
					return err
				}
				if err := activeRuntime.Rebind(currentPlan.TargetWorkspaceRoot); err != nil {
					return err
				}
			}
			runtimeRebound := activeRuntime != nil
			rollbackRuntime := func() error {
				if !runtimeRebound {
					return nil
				}
				runtimeRebound = false
				return activeRuntime.Rebind(activeRuntime.PreviousWorkdir)
			}
			moved := false
			if currentPlan.CrossProject() {
				if err := os.Rename(currentPlan.SourceSessionDir, currentPlan.TargetSessionDir); err != nil {
					return errors.Join(fmt.Errorf("move session artifact: %w", err), rollbackRuntime())
				}
				moved = true
			}
			result, err = s.metadata.CommitSessionWorkspaceRetarget(runCtx, currentPlan, updatedAt)
			if err != nil {
				var rollbackErr error
				if moved {
					if moveErr := os.Rename(currentPlan.TargetSessionDir, currentPlan.SourceSessionDir); moveErr != nil {
						rollbackErr = fmt.Errorf("restore session artifact: %w", moveErr)
					}
				}
				return errors.Join(err, rollbackErr, rollbackRuntime())
			}
			runtimeRebound = false
			return nil
		})
		if err != nil {
			return err
		}
		s.runs.PublishSessionIdentity(currentPlan.SessionID, &clientui.SessionExecutionTarget{
			WorkspaceID:      result.Binding.WorkspaceID,
			WorkspaceName:    result.Binding.WorkspaceName,
			WorkspaceRoot:    result.Binding.CanonicalRoot,
			CwdRelpath:       ".",
			EffectiveWorkdir: result.Binding.CanonicalRoot,
		})
		return nil
	})
	return result, err
}

func (s *SessionWorkspaceRetargeter) ownedBackgroundProcessActive(sessionID string) (bool, error) {
	id := strings.TrimSpace(sessionID)
	for _, process := range s.processes.List() {
		if !process.Running {
			continue
		}
		ownerSessionID := strings.TrimSpace(process.OwnerSessionID)
		if ownerSessionID == "" {
			return false, fmt.Errorf("running background process %q has no owner session id", process.ID)
		}
		if ownerSessionID == id {
			return true, nil
		}
	}
	return false, nil
}

func validateSessionArtifactSource(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat session artifact source %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("session artifact source %q is not a real directory", path)
	}
	return nil
}

func prepareSessionArtifactTarget(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("session artifact target %q already exists", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat session artifact target %q: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create session artifact target parent: %w", err)
	}
	return nil
}
