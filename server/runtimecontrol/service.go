package runtimecontrol

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata"
	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/session"
	"core/server/sessionruntime"
	servicecontract "core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/transcript"
)

type RuntimeActivityResolver interface {
	RuntimeReadModelSnapshot(ctx context.Context, sessionID string, refs []clientui.RuntimeOperationRef) (runtimeactivity.ResponseSnapshot, error)
}

type sessionIdentityPublisher interface {
	PublishSessionIdentity(sessionID string) error
}

type sessionStatusPublisher interface {
	PublishSessionStatus(sessionID string) error
}

type PromptHistoryStore interface {
	RecordPromptHistoryEntry(ctx context.Context, entry metadata.PromptHistoryEntry) (metadata.PromptHistoryRecord, bool, error)
}

type PromptCommandResolver interface {
	ResolvePromptCommand(ctx context.Context, sessionID, name, arguments string) (string, error)
}

type WorkflowTaskSessionResolver interface {
	SessionHasWorkflowTask(ctx context.Context, sessionID string) (bool, error)
}

var errWorkflowTaskSessionAutoCompactionDisable = errors.New("auto-compaction cannot be disabled for workflow task sessions")

type Service struct {
	authority      *sessionruntime.Authority
	activity       RuntimeActivityResolver
	promptStore    PromptHistoryStore
	promptCommands PromptCommandResolver
	workflowTasks  WorkflowTaskSessionResolver
	persisted      session.PersistedSessionResolver
	askViews       servicecontract.AskViewService
	approvalViews  servicecontract.ApprovalViewService
	attention      servicecontract.AttentionNotificationService
}

type committedRuntimeMutationResult[Resp any] struct {
	Response Resp
	Err      error
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
	if errors.Is(err, sessionruntime.ErrSessionStartsBlocked) {
		return errors.Join(serverapi.ErrSessionWorktreeDeleting, err)
	}
	if errors.Is(err, sessionruntime.ErrSessionRunActive) {
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

func (s *Service) WithWorkflowTaskSessionResolver(resolver WorkflowTaskSessionResolver) *Service {
	if s == nil {
		return nil
	}
	s.workflowTasks = resolver
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

func mergeOperationContexts(contexts ...context.Context) (context.Context, func()) {
	return sessionruntime.MergeContexts(contexts...)
}

func (s *Service) SetSessionName(ctx context.Context, req serverapi.RuntimeSetSessionNameRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	return s.withRuntime(ctx, req.SessionID, func(_ context.Context, engine *runtime.Engine) error {
		if err := engine.SetSessionName(req.Name); err != nil {
			return err
		}
		if publisher, ok := s.activity.(sessionIdentityPublisher); ok {
			return publisher.PublishSessionIdentity(req.SessionID)
		}
		return nil
	})
}

func (s *Service) SetThinkingLevel(ctx context.Context, req serverapi.RuntimeSetThinkingLevelRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	return s.withRuntime(ctx, req.SessionID, func(_ context.Context, engine *runtime.Engine) error {
		if err := engine.SetThinkingLevel(req.Level); err != nil {
			return err
		}
		return s.publishSessionStatus(req.SessionID)
	})
}

func (s *Service) SetFastModeEnabled(ctx context.Context, req serverapi.RuntimeSetFastModeEnabledRequest) (serverapi.RuntimeSetFastModeEnabledResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeSetFastModeEnabledResponse{}, err
	}
	return committedRuntimeMutation(s, ctx, strings.TrimSpace(req.SessionID), func(engine *runtime.Engine) (serverapi.RuntimeSetFastModeEnabledResponse, session.CommitReceipt, error) {
		changed, receipt, err := engine.SetFastModeEnabledWithCommittedFeedback(req.Enabled, func(changed bool) string {
			return serverapi.FastModeToggleStatusMessage(req.Enabled, changed)
		})
		return serverapi.RuntimeSetFastModeEnabledResponse{Changed: changed}, receipt, err
	})
}

func (s *Service) SetReviewerEnabled(ctx context.Context, req serverapi.RuntimeSetReviewerEnabledRequest) (serverapi.RuntimeSetReviewerEnabledResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeSetReviewerEnabledResponse{}, err
	}
	return committedRuntimeMutation(s, ctx, strings.TrimSpace(req.SessionID), func(engine *runtime.Engine) (serverapi.RuntimeSetReviewerEnabledResponse, session.CommitReceipt, error) {
		changed, mode, receipt, err := engine.SetReviewerEnabledWithCommittedFeedback(req.Enabled, func(enabled bool, mode string, changed bool) string {
			return serverapi.ReviewerToggleStatusMessage(enabled, mode, changed)
		})
		return serverapi.RuntimeSetReviewerEnabledResponse{Changed: changed, Mode: mode}, receipt, err
	})
}

func (s *Service) SetAutoCompactionEnabled(ctx context.Context, req serverapi.RuntimeSetAutoCompactionEnabledRequest) (serverapi.RuntimeSetAutoCompactionEnabledResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeSetAutoCompactionEnabledResponse{}, err
	}
	var resp serverapi.RuntimeSetAutoCompactionEnabledResponse
	err := s.withRuntime(ctx, req.SessionID, func(_ context.Context, engine *runtime.Engine) error {
		if !req.Enabled {
			if err := s.rejectWorkflowAutoCompactionDisable(ctx, req.SessionID, engine); err != nil {
				return err
			}
		}
		changed, enabled, err := engine.SetAutoCompactionEnabled(req.Enabled)
		if err != nil {
			return err
		}
		resp = serverapi.RuntimeSetAutoCompactionEnabledResponse{Changed: changed, Enabled: enabled}
		return s.publishSessionStatus(req.SessionID)
	})
	return resp, err
}

func (s *Service) SetQuestionsEnabled(ctx context.Context, req serverapi.RuntimeSetQuestionsEnabledRequest) (serverapi.RuntimeSetQuestionsEnabledResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeSetQuestionsEnabledResponse{}, err
	}
	return committedRuntimeMutation(s, ctx, strings.TrimSpace(req.SessionID), func(engine *runtime.Engine) (serverapi.RuntimeSetQuestionsEnabledResponse, session.CommitReceipt, error) {
		changed, enabled, receipt, err := engine.SetQuestionsEnabledWithCommittedFeedback(req.Enabled, func(enabled bool, changed bool) string {
			return serverapi.QuestionsToggleStatusMessage(enabled, changed)
		})
		return serverapi.RuntimeSetQuestionsEnabledResponse{Changed: changed, Enabled: enabled}, receipt, err
	})
}

func committedRuntimeMutation[Resp any](
	service *Service,
	ctx context.Context,
	sessionID string,
	run func(*runtime.Engine) (Resp, session.CommitReceipt, error),
) (Resp, error) {
	var zero Resp
	var result committedRuntimeMutationResult[Resp]
	err := service.withRuntime(ctx, sessionID, func(_ context.Context, engine *runtime.Engine) error {
		response, receipt, mutationErr := run(engine)
		result.Response = response
		if !receipt.Committed {
			return mutationErr
		}
		result.Err = errors.Join(mutationErr, service.publishSessionStatus(sessionID))
		return nil
	})
	if err != nil {
		return zero, err
	}
	return result.Response, result.Err
}

func (s *Service) publishSessionStatus(sessionID string) error {
	if publisher, ok := s.activity.(sessionStatusPublisher); ok {
		return publisher.PublishSessionStatus(sessionID)
	}
	return nil
}

func (s *Service) AppendCommittedEntry(ctx context.Context, req serverapi.RuntimeAppendCommittedEntryRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	visibility := transcript.NormalizeEntryVisibility(transcript.EntryVisibility(req.Visibility))
	return s.withRuntime(ctx, req.SessionID, func(_ context.Context, engine *runtime.Engine) error {
		if visibility == transcript.EntryVisibilityAuto && strings.TrimSpace(req.NoticeID) != "" {
			return engine.AppendCommittedEntryWithNoticeID(req.Role, req.Text, req.NoticeID)
		}
		if visibility == transcript.EntryVisibilityAuto {
			return engine.AppendCommittedEntry(req.Role, req.Text)
		}
		return engine.AppendCommittedEntryWithVisibility(req.Role, req.Text, visibility)
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
	return s.withRuntime(ctx, trimmedSessionID, func(_ context.Context, engine *runtime.Engine) error {
		return engine.AppendCommittedEntry(trimmedRole, trimmedText)
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
	return s.runAgentExecution(ctx, req.SessionID, func(runCtx context.Context, engine *runtime.Engine) error {
		_, err := engine.SubmitUserShellCommandWithActiveHook(runCtx, req.Command, nil)
		return err
	})
}

func (s *Service) CompactContext(ctx context.Context, req serverapi.RuntimeCompactContextRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	return s.runAgentExecution(ctx, req.SessionID, func(runCtx context.Context, engine *runtime.Engine) error {
		_, err := engine.CompactContextWithActiveHook(runCtx, req.Args, nil)
		return err
	})
}

func (s *Service) CompactContextForPreSubmit(ctx context.Context, req serverapi.RuntimeCompactContextForPreSubmitRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	return s.runAgentExecution(ctx, req.SessionID, func(runCtx context.Context, engine *runtime.Engine) error {
		_, err := engine.CompactContextForPreSubmitWithActiveHook(runCtx, nil)
		return err
	})
}

func (s *Service) HasQueuedUserWork(ctx context.Context, req serverapi.RuntimeHasQueuedUserWorkRequest) (serverapi.RuntimeHasQueuedUserWorkResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeHasQueuedUserWorkResponse{}, err
	}
	var hasQueuedUserWork bool
	err := s.withRuntime(ctx, req.SessionID, func(_ context.Context, engine *runtime.Engine) error {
		hasQueuedUserWork = engine.HasQueuedUserWork()
		return nil
	})
	if err != nil {
		return serverapi.RuntimeHasQueuedUserWorkResponse{}, err
	}
	return serverapi.RuntimeHasQueuedUserWorkResponse{HasQueuedUserWork: hasQueuedUserWork}, nil
}

func (s *Service) SubmitQueuedUserMessages(ctx context.Context, req serverapi.RuntimeSubmitQueuedUserMessagesRequest) (serverapi.RuntimeSubmitQueuedUserMessagesResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeSubmitQueuedUserMessagesResponse{}, err
	}
	var resp serverapi.RuntimeSubmitQueuedUserMessagesResponse
	err := s.runAgentExecution(ctx, req.SessionID, func(runCtx context.Context, engine *runtime.Engine) error {
		msg, _, err := engine.SubmitQueuedUserMessagesWithActiveHook(runCtx, nil)
		if msg.Content != nil {
			resp.Message = *msg.Content
		}
		return err
	})
	return resp, err
}

func (s *Service) Interrupt(ctx context.Context, req serverapi.RuntimeInterruptRequest) (serverapi.RuntimeInterruptResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeInterruptResponse{}, err
	}
	if s == nil || s.authority == nil {
		return serverapi.RuntimeInterruptResponse{}, errors.New("session runtime authority is required")
	}
	sessionID := strings.TrimSpace(req.SessionID)
	err := s.withRuntime(ctx, sessionID, func(_ context.Context, engine *runtime.Engine) error {
		for _, ref := range req.PendingOperationRefs {
			if ref.Kind != clientui.RuntimeOperationKindQueuedMessage || ref.QueueItemID == nil {
				continue
			}
			engine.DiscardQueuedUserMessage(ref.QueueItemID.String())
		}
		return engine.Interrupt()
	})
	if err != nil && !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		return serverapi.RuntimeInterruptResponse{}, err
	}
	return s.runtimeInterruptResponse(sessionID, req.PendingOperationRefs)
}

func (s *Service) runtimeInterruptResponse(sessionID string, refs []clientui.RuntimeOperationRef) (serverapi.RuntimeInterruptResponse, error) {
	var snapshot runtimeactivity.ResponseSnapshot
	var err error
	if s.activity != nil {
		snapshot, err = s.activity.RuntimeReadModelSnapshot(context.Background(), sessionID, refs)
	} else {
		err = errors.New("runtime activity resolver is unavailable")
	}
	if err != nil {
		version := runtimeactivity.NextReadModelVersion(sessionID)
		return serverapi.RuntimeInterruptResponse{
			Version:             version,
			Activity:            clientui.RuntimeActivity{State: clientui.RuntimeActivityUnavailable, DiagnosticRecovery: true},
			InputReconciliation: clientui.RuntimeInputReconciliationSnapshot{},
		}, nil
	}
	return serverapi.RuntimeInterruptResponse{
		Version:             snapshot.Version,
		Activity:            snapshot.Activity,
		InputReconciliation: snapshot.InputReconciliation,
	}, nil
}

func (s *Service) DiscardQueuedUserMessage(ctx context.Context, req serverapi.RuntimeDiscardQueuedUserMessageRequest) (serverapi.RuntimeDiscardQueuedUserMessageResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeDiscardQueuedUserMessageResponse{}, err
	}
	var resp serverapi.RuntimeDiscardQueuedUserMessageResponse
	err := s.withRuntime(ctx, req.SessionID, func(_ context.Context, engine *runtime.Engine) error {
		resp.Discarded = engine.DiscardQueuedUserMessage(strings.TrimSpace(req.QueueItemID))
		return nil
	})
	return resp, err
}

func (s *Service) RecordPromptHistory(ctx context.Context, req serverapi.RuntimeRecordPromptHistoryRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	_, _, err := s.recordPromptHistory(
		ctx,
		strings.TrimSpace(req.SessionID),
		strings.TrimSpace(req.ClientRequestID),
		req.Text,
	)
	return err
}

func (s *Service) recordPromptHistory(ctx context.Context, sessionID string, sourceID string, text string) (metadata.PromptHistoryRecord, bool, error) {
	if s == nil || s.promptStore == nil {
		return metadata.PromptHistoryRecord{}, false, nil
	}
	return s.promptStore.RecordPromptHistoryEntry(ctx, metadata.PromptHistoryEntry{
		SessionID: strings.TrimSpace(sessionID),
		SourceID:  strings.TrimSpace(sourceID),
		Text:      text,
	})
}

func (s *Service) launchPromptHistoryAppend(
	engine *runtime.Engine,
	sessionID string,
	sourceID string,
	text string,
) {
	if s == nil || s.promptStore == nil || engine == nil {
		return
	}
	_ = engine.LaunchPromptHistoryAppend(func(ctx context.Context) error {
		_, _, err := s.recordPromptHistory(
			ctx,
			sessionID,
			sourceID,
			text,
		)
		return err
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
