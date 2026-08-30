package sessionservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"core/server/metadata"
	"core/server/session"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	"core/shared/runtimeids"
)

const detachedArchiveGracePeriod = 5 * time.Minute

var ErrDetachedArchiveGraceExpired = errors.New("detached Session archive grace period expired")

type sessionRemovalMetadata interface {
	session.PersistedSessionResolver
	DeleteSession(context.Context, string) error
}

type SessionRemovalFailureState interface {
	sessionRemovalFailureState()
}

type SessionRemovalMetadataNotRemoved struct{}

func (SessionRemovalMetadataNotRemoved) sessionRemovalFailureState() {}

type SessionRemovalMetadataRemovedCleanupFailed struct {
	RemainingPath string
}

func (SessionRemovalMetadataRemovedCleanupFailed) sessionRemovalFailureState() {}

type SessionRemovalFailureError struct {
	State SessionRemovalFailureState
	Cause error
}

func (e *SessionRemovalFailureError) Error() string {
	switch state := e.State.(type) {
	case SessionRemovalMetadataNotRemoved:
		return fmt.Sprintf("Session metadata was not removed: %v", e.Cause)
	case SessionRemovalMetadataRemovedCleanupFailed:
		return fmt.Sprintf(
			"Session metadata was removed but artifact cleanup failed at %s: %v",
			state.RemainingPath,
			e.Cause,
		)
	default:
		return fmt.Sprintf("Session removal failed: %v", e.Cause)
	}
}

func (e *SessionRemovalFailureError) Unwrap() error {
	return e.Cause
}

type ArchiveDetachExpiryDiagnostic struct {
	SessionID  string
	OutputPath string
	Cause      error
}

func logArchiveDetachExpiry(diagnostic ArchiveDetachExpiryDiagnostic) {
	slog.Error(
		"accepted Session archive exceeded its detached lifetime",
		"session_id", diagnostic.SessionID,
		"output_path", diagnostic.OutputPath,
		"error", diagnostic.Cause,
	)
}

func (s *SessionLifecycleService) WithDebugMode(debug bool) *SessionLifecycleService {
	if s != nil {
		s.debug = debug
	}
	return s
}

func (s *SessionLifecycleService) Archive(
	invocationCtx context.Context,
	sessionID string,
	outputPath string,
) error {
	if s == nil || s.authority == nil {
		return errors.New("session runtime authority is required")
	}
	if s.removal == nil {
		return errors.New("Session removal metadata is required")
	}
	id, err := runtimeids.ParseSessionID(sessionID)
	if err != nil {
		return err
	}
	if invocationCtx == nil {
		invocationCtx = context.Background()
	}
	if err := session.PreflightSessionArchiveDestination(outputPath); err != nil {
		return err
	}
	result, err := s.authority.AcceptLifecycleTask(func(lifecycleCtx context.Context) error {
		return s.runAcceptedArchive(lifecycleCtx, invocationCtx, id, outputPath)
	})
	if err != nil {
		return err
	}
	return waitForAcceptedSessionOperation(invocationCtx, result)
}

func (s *SessionLifecycleService) Delete(invocationCtx context.Context, sessionID string) error {
	if s == nil || s.authority == nil {
		return errors.New("session runtime authority is required")
	}
	if s.removal == nil {
		return errors.New("Session removal metadata is required")
	}
	id, err := runtimeids.ParseSessionID(sessionID)
	if err != nil {
		return err
	}
	if invocationCtx == nil {
		invocationCtx = context.Background()
	}
	result, err := s.authority.AcceptLifecycleTask(func(lifecycleCtx context.Context) error {
		return s.authority.WithDestructiveSessionAdmission(
			lifecycleCtx,
			id,
			func(runCtx context.Context) error {
				return s.removeSessionUnderAdmission(runCtx, id)
			},
		)
	})
	if err != nil {
		return err
	}
	return waitForAcceptedSessionOperation(invocationCtx, result)
}

func (s *SessionLifecycleService) ArchiveSession(
	ctx context.Context,
	request *sessionlaunchpb.SessionArchiveRequest,
) (*sessionlaunchpb.SessionArchiveSuccess, error) {
	if request == nil {
		return nil, errors.New("Session archive request is required")
	}
	if err := s.Archive(ctx, request.SessionId, request.OutputPath); err != nil {
		return nil, err
	}
	return &sessionlaunchpb.SessionArchiveSuccess{
		SessionId:  request.SessionId,
		OutputPath: request.OutputPath,
	}, nil
}

func (s *SessionLifecycleService) DeleteSession(
	ctx context.Context,
	request *sessionlaunchpb.SessionDeleteRequest,
) (*sessionlaunchpb.SessionDeleteSuccess, error) {
	if request == nil {
		return nil, errors.New("Session delete request is required")
	}
	if err := s.Delete(ctx, request.SessionId); err != nil {
		return nil, err
	}
	return &sessionlaunchpb.SessionDeleteSuccess{SessionId: request.SessionId}, nil
}

func waitForAcceptedSessionOperation(invocationCtx context.Context, result <-chan error) error {
	select {
	case err := <-result:
		return err
	case <-invocationCtx.Done():
		return context.Cause(invocationCtx)
	}
}

func (s *SessionLifecycleService) runAcceptedArchive(
	lifecycleCtx context.Context,
	invocationCtx context.Context,
	sessionID runtimeids.SessionID,
	outputPath string,
) error {
	operationCtx, cancelOperation := context.WithCancel(lifecycleCtx)
	defer cancelOperation()
	operationDone := make(chan error, 1)
	go func() {
		operationDone <- s.authority.WithDestructiveSessionAdmission(
			operationCtx,
			sessionID,
			func(runCtx context.Context) error {
				record, err := session.ResolvePersistedSessionRecord(
					runCtx,
					s.removal,
					sessionID.String(),
				)
				if err != nil {
					return err
				}
				if err := session.ArchiveSessionDirectory(
					runCtx,
					sessionID,
					record.SessionDir,
					outputPath,
				); err != nil {
					return err
				}
				if err := s.removeSessionUnderAdmission(runCtx, sessionID); err != nil {
					var removalErr *SessionRemovalFailureError
					if errors.As(err, &removalErr) {
						if _, removed := removalErr.State.(SessionRemovalMetadataRemovedCleanupFailed); removed {
							return err
						}
					}
					return &SessionRemovalFailureError{
						State: SessionRemovalMetadataNotRemoved{},
						Cause: err,
					}
				}
				return nil
			},
		)
	}()

	select {
	case err := <-operationDone:
		return err
	case <-lifecycleCtx.Done():
		cancelOperation()
		return errors.Join(context.Cause(lifecycleCtx), <-operationDone)
	case <-invocationCtx.Done():
	}

	graceCtx, cancelGrace := context.WithTimeout(context.Background(), detachedArchiveGracePeriod)
	defer cancelGrace()
	return s.waitForDetachedArchive(
		lifecycleCtx,
		graceCtx,
		operationDone,
		cancelOperation,
		sessionID,
		outputPath,
	)
}

func (s *SessionLifecycleService) waitForDetachedArchive(
	lifecycleCtx context.Context,
	graceCtx context.Context,
	operationDone <-chan error,
	cancelOperation context.CancelFunc,
	sessionID runtimeids.SessionID,
	outputPath string,
) error {
	select {
	case err := <-operationDone:
		return err
	case <-lifecycleCtx.Done():
		cancelOperation()
		return errors.Join(context.Cause(lifecycleCtx), <-operationDone)
	case <-graceCtx.Done():
		cancelOperation()
		operationErr := <-operationDone
		cause := errors.Join(ErrDetachedArchiveGraceExpired, operationErr)
		diagnostic := ArchiveDetachExpiryDiagnostic{
			SessionID:  sessionID.String(),
			OutputPath: outputPath,
			Cause:      cause,
		}
		logArchiveDetachExpiry(diagnostic)
		if s.debug {
			panic(diagnostic)
		}
		return cause
	}
}

func (s *SessionLifecycleService) removeSessionUnderAdmission(
	ctx context.Context,
	sessionID runtimeids.SessionID,
) error {
	record, err := session.ResolvePersistedSessionRecord(ctx, s.removal, sessionID.String())
	if err != nil {
		return err
	}
	schedule, err := session.PreflightSessionArtifactRemoval(record.SessionDir)
	if err != nil {
		return err
	}
	if err := s.removal.DeleteSession(ctx, sessionID.String()); err != nil {
		return err
	}
	if err := session.RemovePreflightedSessionArtifacts(schedule); err != nil {
		var removalErr *session.SessionArtifactRemovalError
		if errors.As(err, &removalErr) {
			return &SessionRemovalFailureError{
				State: SessionRemovalMetadataRemovedCleanupFailed{
					RemainingPath: removalErr.RemainingPath,
				},
				Cause: err,
			}
		}
		return err
	}
	return nil
}

var _ sessionRemovalMetadata = (*metadata.Store)(nil)
