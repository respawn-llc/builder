package runtimecontrol

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"core/server/metadata"
	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflowexecution"
	servicecontract "core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
	"core/shared/transcript"
)

type RuntimeActivityResolver interface {
	RuntimeReadModelFeedSnapshot(ctx context.Context, sessionID string) (clientui.RuntimeReadModelUpdate, error)
}

type sessionSettingPublisher interface {
	PublishSessionSettingFeedback(sessionID string, feedback clientui.TranscriptSessionSettingFeedback) error
}

type PromptHistoryStore interface {
	RecordPromptHistoryEntry(ctx context.Context, entry metadata.PromptHistoryEntry) (metadata.PromptHistoryRecord, error)
}

type PromptCommandResolver interface {
	ResolvePromptCommand(ctx context.Context, sessionID, name, arguments string) (string, error)
}

type WorkflowSessionPreparationReader interface {
	WorkflowSessionPreparing(context.Context, runtimeids.SessionID) (bool, error)
}

var errWorkflowTaskSessionAutoCompactionDisable = errors.New("auto-compaction cannot be disabled for workflow task sessions")

type Service struct {
	authority      *sessionruntime.Authority
	activity       RuntimeActivityResolver
	promptStore    PromptHistoryStore
	promptCommands PromptCommandResolver
	workflowTasks  workflowexecution.WorkflowSessionOwnershipReader
	reactivator    workflowexecution.WorkflowSessionReactivatorWithAcceptance
	preparations   WorkflowSessionPreparationReader
	persisted      session.PersistedSessionResolver
	askViews       servicecontract.AskViewService
	approvalViews  servicecontract.ApprovalViewService
	attention      servicecontract.AttentionNotificationService
}

type sessionUserTurnRequest struct {
	SessionID string
	Kind      serverapi.RuntimeUserTurnInputKind
	Text      string
	Name      string
	Arguments string
}

type goalSetRequest struct {
	SessionID string
	Objective string
	Actor     string
	RunID     string
	StepID    string
}

type goalStatusRequest struct {
	SessionID string
	Status    string
	Actor     string
	RunID     string
	StepID    string
}

type goalClearRequest struct {
	SessionID string
	Actor     string
}

func NewService(authority *sessionruntime.Authority) *Service {
	return &Service{authority: authority}
}

func (s *Service) runAgentExecution(
	ctx context.Context,
	sessionID string,
	run func(context.Context, *runtime.Engine) error,
) error {
	if s == nil || s.authority == nil {
		return errors.New("session runtime authority is required")
	}
	id, err := runtimeids.ParseSessionID(strings.TrimSpace(sessionID))
	if err != nil {
		return err
	}
	descriptor, err := session.NewOpenSessionDescriptor(id)
	if err != nil {
		return err
	}
	err = s.authority.RunCurrentAgentExecution(ctx, descriptor, run)
	if err == nil {
		return nil
	}
	if errors.Is(err, sessionruntime.ErrSessionRunActive) ||
		errors.Is(err, sessionruntime.ErrSessionWorkflowActivationActive) {
		return errors.Join(serverapi.ErrSessionRunStarting, err)
	}
	return err
}

func (s *Service) WithRuntimeActivityResolver(resolver RuntimeActivityResolver) *Service {
	if s == nil {
		return nil
	}
	s.activity = resolver
	return s
}

func (s *Service) WithPromptHistoryStore(store PromptHistoryStore) *Service {
	if s == nil {
		return nil
	}
	s.promptStore = store
	return s
}

func (s *Service) WithPromptCommandResolver(resolver PromptCommandResolver) *Service {
	if s == nil {
		return nil
	}
	s.promptCommands = resolver
	return s
}

func (s *Service) WithWorkflowTaskSessionResolver(resolver workflowexecution.WorkflowSessionOwnershipReader) *Service {
	if s == nil {
		return nil
	}
	s.workflowTasks = resolver
	return s
}

func (s *Service) WithWorkflowSessionReactivator(reactivator workflowexecution.WorkflowSessionReactivatorWithAcceptance) *Service {
	if s == nil {
		return nil
	}
	s.reactivator = reactivator
	return s
}

func (s *Service) WithWorkflowSessionPreparationReader(reader WorkflowSessionPreparationReader) *Service {
	if s == nil {
		return nil
	}
	s.preparations = reader
	return s
}

func (s *Service) WithPersistedSessionResolver(resolver session.PersistedSessionResolver) *Service {
	if s == nil {
		return nil
	}
	s.persisted = resolver
	return s
}

func (s *Service) WithLiveWatchPromptSources(asks servicecontract.AskViewService, approvals servicecontract.ApprovalViewService, attention servicecontract.AttentionNotificationService) *Service {
	if s != nil {
		s.askViews, s.approvalViews, s.attention = asks, approvals, attention
	}
	return s
}

func (s *Service) withRuntime(ctx context.Context, sessionID string, fn func(context.Context, *runtime.Engine) error) error {
	if s == nil || s.authority == nil {
		return errors.New("session runtime authority is required")
	}
	id, err := runtimeids.ParseSessionID(strings.TrimSpace(sessionID))
	if err != nil {
		return err
	}
	return s.authority.WithCurrentRuntime(ctx, id, fn)
}

func runRuntimeCommand[Resp any](
	ctx context.Context,
	run func(context.Context) (Resp, bool, error),
) (Resp, error) {
	var zero Resp
	response, accepted, err := run(ctx)
	if !accepted {
		if errors.Is(err, serverapi.ErrRuntimeCommandNotAccepted) {
			return zero, err
		}
		if err == nil {
			err = errors.New("runtime command completed without accepting a mutation")
		}
		return zero, serverapi.NewRuntimeCommandNotAcceptedError(err)
	}
	return response, err
}

func (s *Service) SetSessionName(ctx context.Context, req serverapi.RuntimeSetSessionNameRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	return s.withRuntime(ctx, req.SessionID, func(callbackCtx context.Context, engine *runtime.Engine) error {
		changed, err := engine.SetSessionName(callbackCtx, req.Name)
		if err != nil {
			return err
		}
		if publisher, ok := s.activity.(sessionSettingPublisher); ok {
			name := strings.TrimSpace(req.Name)
			return publisher.PublishSessionSettingFeedback(req.SessionID, clientui.TranscriptSessionSettingFeedback{
				Kind:        clientui.SessionSettingSessionName,
				Changed:     changed,
				SessionName: &name,
			})
		}
		return nil
	})
}

func (s *Service) AppendCommittedEntry(ctx context.Context, req serverapi.RuntimeAppendCommittedEntryRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	visibility := transcript.NormalizeEntryVisibility(transcript.EntryVisibility(req.Visibility))
	return s.withRuntime(ctx, req.SessionID, func(callbackCtx context.Context, engine *runtime.Engine) error {
		if visibility == transcript.EntryVisibilityAuto && strings.TrimSpace(req.NoticeID) != "" {
			return engine.AppendCommittedEntryWithNoticeID(callbackCtx, req.Role, req.Text, req.NoticeID)
		}
		if visibility == transcript.EntryVisibilityAuto {
			return engine.AppendCommittedEntry(callbackCtx, req.Role, req.Text)
		}
		return engine.AppendCommittedEntryWithVisibility(callbackCtx, req.Role, req.Text, visibility)
	})
}

func (s *Service) AppendSessionEntry(ctx context.Context, sessionID string, role string, text string) error {
	trimmedSessionID := strings.TrimSpace(sessionID)
	trimmedRole := strings.TrimSpace(role)
	trimmedText := strings.TrimSpace(text)
	if trimmedSessionID == "" {
		return fmt.Errorf("session id is required")
	}
	if trimmedRole == "" {
		return fmt.Errorf("role is required")
	}
	if trimmedText == "" {
		return fmt.Errorf("text is required")
	}
	return s.withRuntime(ctx, trimmedSessionID, func(callbackCtx context.Context, engine *runtime.Engine) error {
		return engine.AppendCommittedEntry(callbackCtx, trimmedRole, trimmedText)
	})
}

func (s *Service) ShouldCompactBeforeUserMessage(ctx context.Context, req serverapi.RuntimeShouldCompactBeforeUserMessageRequest) (serverapi.RuntimeShouldCompactBeforeUserMessageResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeShouldCompactBeforeUserMessageResponse{}, err
	}
	var shouldCompact bool
	err := s.withRuntime(ctx, req.SessionID, func(callbackCtx context.Context, engine *runtime.Engine) error {
		var err error
		shouldCompact, err = engine.ShouldCompactBeforeUserMessage(callbackCtx, req.Text)
		return err
	})
	if err != nil {
		return serverapi.RuntimeShouldCompactBeforeUserMessageResponse{}, err
	}
	return serverapi.RuntimeShouldCompactBeforeUserMessageResponse{ShouldCompact: shouldCompact}, nil
}

func (s *Service) SubmitUserShellCommand(ctx context.Context, req serverapi.RuntimeSubmitUserShellCommandRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	_, err := runRuntimeCommand(ctx, func(ctx context.Context) (struct{}, bool, error) {
		attempt := runtime.NewCommandAttempt(ctx)
		defer attempt.Finish()
		commandErr := s.runAgentExecution(attempt.Context(), req.SessionID, func(runCtx context.Context, engine *runtime.Engine) error {
			_, err := engine.SubmitUserShellCommandWithAcceptance(runCtx, req.Command, attempt.Accept)
			return err
		})
		return struct{}{}, attempt.Accepted(), commandErr
	})
	return err
}

func (s *Service) CompactContext(ctx context.Context, req serverapi.RuntimeCompactContextRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	_, err := runRuntimeCommand(ctx, func(ctx context.Context) (struct{}, bool, error) {
		attempt := runtime.NewCommandAttempt(ctx)
		defer attempt.Finish()
		commandErr := s.runAgentExecution(attempt.Context(), req.SessionID, func(runCtx context.Context, engine *runtime.Engine) error {
			_, compactErr := engine.CompactContextAdmissionForRequestWithAcceptance(runCtx, req.RequestID, req.Admission, attempt.Accept)
			return compactErr
		})
		return struct{}{}, attempt.Accepted(), commandErr
	})
	return err
}

func (s *Service) Interrupt(ctx context.Context, req serverapi.RuntimeInterruptRequest) (serverapi.RuntimeInterruptResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeInterruptResponse{}, err
	}
	if s == nil || s.authority == nil {
		return serverapi.RuntimeInterruptResponse{}, errors.New("session runtime authority is required")
	}
	sessionID := strings.TrimSpace(req.SessionID)
	return s.interrupt(ctx, sessionID)
}

func (s *Service) interrupt(ctx context.Context, sessionID string) (serverapi.RuntimeInterruptResponse, error) {
	sessionID = strings.TrimSpace(sessionID)
	id, err := runtimeids.ParseSessionID(sessionID)
	if err != nil {
		return serverapi.RuntimeInterruptResponse{}, err
	}
	interrupted, err := s.authority.InterruptCurrentAgentTurn(ctx, id, nil)
	if err == nil && !interrupted {
		err = serverapi.NewRuntimeCommandNotAcceptedError(errors.New("no active Agent Turn"))
	}
	switch {
	case errors.Is(err, sessionruntime.ErrExecutionNoLongerLive):
		err = serverapi.NewRuntimeCommandNotAcceptedError(errors.New("no active Agent Turn"))
	case errors.Is(err, serverapi.ErrRuntimeUnavailable):
		err = serverapi.NewRuntimeCommandNotAcceptedError(err)
	}
	if err != nil {
		return serverapi.RuntimeInterruptResponse{}, err
	}
	return s.runtimeInterruptResponse(ctx, sessionID)
}

func (s *Service) runtimeInterruptResponse(ctx context.Context, sessionID string) (serverapi.RuntimeInterruptResponse, error) {
	var snapshot clientui.RuntimeReadModelUpdate
	var err error
	if s.activity != nil {
		snapshot, err = s.activity.RuntimeReadModelFeedSnapshot(ctx, sessionID)
	} else {
		err = errors.New("runtime activity resolver is unavailable")
	}
	if err != nil {
		slog.WarnContext(ctx, "runtime interrupt activity snapshot unavailable", "session_id", sessionID, "error", err)
		version := runtimeactivity.NextReadModelVersion(sessionID)
		return serverapi.RuntimeInterruptResponse{
			Version: version,
			Activity: clientui.RuntimeActivity{
				State:              clientui.RuntimeActivityUnavailable,
				Reviewer:           clientui.ReviewerActivityInactive,
				DiagnosticRecovery: true,
			},
		}, nil
	}
	return serverapi.RuntimeInterruptResponse{
		Version:  snapshot.Version,
		Activity: snapshot.Activity,
	}, nil
}

func (s *Service) ListPendingWork(ctx context.Context, req serverapi.RuntimeListPendingWorkRequest) (serverapi.RuntimeListPendingWorkResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeListPendingWorkResponse{}, err
	}
	var response serverapi.RuntimeListPendingWorkResponse
	err := s.withRuntime(ctx, req.SessionID, func(_ context.Context, engine *runtime.Engine) error {
		var snapshotErr error
		response.PendingWork, snapshotErr = engine.PendingWorkSnapshot()
		return snapshotErr
	})
	if errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		return serverapi.RuntimeListPendingWorkResponse{
			PendingWork: serverapi.PendingWork{Items: []serverapi.PendingWorkItem{}},
		}, nil
	}
	return response, err
}

func (s *Service) RemovePendingWork(ctx context.Context, req serverapi.RuntimeRemovePendingWorkRequest) (serverapi.RuntimeRemovePendingWorkResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeRemovePendingWorkResponse{}, err
	}
	var response serverapi.RuntimeRemovePendingWorkResponse
	err := s.withRuntime(ctx, req.SessionID, func(callbackCtx context.Context, engine *runtime.Engine) error {
		var removeErr error
		response.Restoration, removeErr = engine.RemovePendingWork(callbackCtx, req.ItemID)
		var notPending *runtimeinput.PendingWorkRemovalError
		if errors.As(removeErr, &notPending) {
			return &serverapi.PendingWorkNotPendingError{ItemID: notPending.ItemID}
		}
		return removeErr
	})
	return response, err
}

func (s *Service) RecordPromptHistory(ctx context.Context, req serverapi.RuntimeRecordPromptHistoryRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	return s.withRuntime(ctx, req.SessionID, func(_ context.Context, _ *runtime.Engine) error {
		_, err := s.recordPromptHistory(ctx, strings.TrimSpace(req.SessionID), req.Text)
		return err
	})
}

func (s *Service) recordPromptHistory(ctx context.Context, sessionID string, text string) (metadata.PromptHistoryRecord, error) {
	if s == nil || s.promptStore == nil {
		return metadata.PromptHistoryRecord{}, nil
	}
	return s.promptStore.RecordPromptHistoryEntry(ctx, metadata.PromptHistoryEntry{
		SessionID: strings.TrimSpace(sessionID),
		Text:      text,
	})
}

func (s *Service) rejectWorkflowAutoCompactionDisable(ctx context.Context, sessionID string, engine *runtime.Engine) error {
	workflowSession, err := s.workflowTaskSession(ctx, sessionID, engine)
	if err != nil {
		return err
	}
	if workflowSession {
		return errWorkflowTaskSessionAutoCompactionDisable
	}
	return nil
}

func (s *Service) workflowTaskSession(ctx context.Context, sessionID string, engine *runtime.Engine) (bool, error) {
	if engine != nil {
		workflowState, err := engine.WorkflowSessionState()
		if err != nil {
			return false, err
		}
		if workflowState != nil && workflowState.TaskID != "" {
			return true, nil
		}
	}
	if s != nil && s.workflowTasks != nil {
		workflow, err := s.workflowTasks.SessionHasWorkflowTask(ctx, sessionID)
		if err != nil {
			return false, err
		}
		return workflow, nil
	}
	return false, nil
}

var _ servicecontract.RuntimeControlService = (*Service)(nil)
