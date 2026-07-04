package runtimecontrol

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"core/server/metadata"
	"core/server/requestmemo"
	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/runtimeops"
	"core/server/session"
	servicecontract "core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/transcript"
)

type RuntimeResolver interface {
	ResolveRuntime(ctx context.Context, sessionID string) (*runtime.Engine, error)
	WithGuardedRuntime(ctx context.Context, sessionID string, fn func(*runtime.Engine) error) (bool, error)
	BeginCancellableSessionRun(sessionID string) (context.Context, func(), bool)
	BeginSessionRun(sessionID string) (func(), bool)
	SessionRunsBlocked(sessionID string) bool
}

type runtimeRegistrySnapshotProvider interface {
	RuntimeActivityRegistrySnapshot(sessionID string) runtimeactivity.RegistrySnapshot
}

type runtimeReadModelSnapshotProvider interface {
	RuntimeReadModelSnapshot(ctx context.Context, sessionID string, refs []clientui.RuntimeOperationRef) (runtimeactivity.ResponseSnapshot, error)
}

type RuntimeActivityResolver interface {
	Snapshot(ctx context.Context, sessionID string, refs []clientui.RuntimeOperationRef) (runtimeactivity.ResponseSnapshot, error)
}

type sessionIdentityPublisher interface {
	PublishSessionIdentity(sessionID string, target *clientui.SessionExecutionTarget)
}

type defaultRuntimeControlActivityResolver struct {
	runtimes RuntimeResolver
}

func newDefaultRuntimeControlActivityResolver(runtimes RuntimeResolver) RuntimeActivityResolver {
	return defaultRuntimeControlActivityResolver{runtimes: runtimes}
}

func (r defaultRuntimeControlActivityResolver) Snapshot(ctx context.Context, sessionID string, refs []clientui.RuntimeOperationRef) (runtimeactivity.ResponseSnapshot, error) {
	if provider, ok := r.runtimes.(runtimeReadModelSnapshotProvider); ok {
		return provider.RuntimeReadModelSnapshot(ctx, sessionID, refs)
	}
	var engine *runtime.Engine
	var err error
	if r.runtimes != nil {
		engine, err = r.runtimes.ResolveRuntime(ctx, sessionID)
	}
	if err != nil {
		return runtimeactivity.ResponseSnapshot{}, err
	}
	registry := runtimeactivity.RegistrySnapshot{Registered: engine != nil, QueueAccepting: true}
	if provider, ok := r.runtimes.(runtimeRegistrySnapshotProvider); ok {
		registry = provider.RuntimeActivityRegistrySnapshot(sessionID)
	}
	return runtimeactivity.BuildSnapshot(sessionID, func(version clientui.ReadModelVersion) (runtimeactivity.SnapshotInput, error) {
		return runtimeactivity.SnapshotInput{
			Resolver:            runtimeactivity.ResolverSnapshot{Registry: registry, Active: runtimeactivity.ActiveStepFromProvider(engine)},
			InputReconciliation: clientui.NewEmptyRuntimeInputReconciliationSnapshot(version),
		}, nil
	})
}

type runtimeRunStart interface {
	Context() context.Context
	Release()
}

type releaseOnlyRunStart struct {
	ctx     context.Context
	release func()
}

func (s releaseOnlyRunStart) Context() context.Context {
	if s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

func (s releaseOnlyRunStart) Release() {
	if s.release != nil {
		s.release()
	}
}

type PromptHistoryStore interface {
	RecordPromptHistoryEntry(ctx context.Context, entry metadata.PromptHistoryEntry) (metadata.PromptHistoryRecord, bool, error)
}

type WorkflowSessionResolver interface {
	ResolveSessionStore(ctx context.Context, sessionID string) (*session.Store, error)
}

var errWorkflowTaskSessionAutoCompactionDisable = errors.New("auto-compaction cannot be disabled for workflow task sessions")

type Service struct {
	runtimes       RuntimeResolver
	activity       RuntimeActivityResolver
	promptStore    PromptHistoryStore
	workflowStates WorkflowSessionResolver
	operations     *runtimeops.Coordinator
	sessionNames   *requestmemo.Memo[sessionStringMemoRequest, struct{}]
	thinkingLevels *requestmemo.Memo[sessionStringMemoRequest, struct{}]
	fastModes      *requestmemo.Memo[sessionBoolMemoRequest, serverapi.RuntimeSetFastModeEnabledResponse]
	reviewers      *requestmemo.Memo[sessionBoolMemoRequest, serverapi.RuntimeSetReviewerEnabledResponse]
	autoCompacts   *requestmemo.Memo[sessionBoolMemoRequest, serverapi.RuntimeSetAutoCompactionEnabledResponse]
	questions      *requestmemo.Memo[sessionBoolMemoRequest, serverapi.RuntimeSetQuestionsEnabledResponse]

	localEntries   *requestmemo.Memo[localEntryMemoRequest, struct{}]
	queuedDiscards *requestmemo.Memo[queuedUserMessageMemoRequest, serverapi.RuntimeDiscardQueuedUserMessageResponse]
	liveSteers     *requestmemo.Memo[liveSteerMemoRequest, serverapi.RuntimeLiveSteerResponse]
	liveStops      *requestmemo.Memo[liveStopMemoRequest, serverapi.RuntimeLiveStopResponse]
	promptHistory  *requestmemo.Memo[sessionTextMemoRequest, struct{}]
	goals          *requestmemo.Memo[goalSetMemoRequest, serverapi.RuntimeGoalShowResponse]
	goalStatuses   *requestmemo.Memo[goalStatusMemoRequest, serverapi.RuntimeGoalShowResponse]
	goalClears     *requestmemo.Memo[goalClearMemoRequest, serverapi.RuntimeGoalShowResponse]
}

type sessionStringMemoRequest struct {
	SessionID string
	Value     string
}

type sessionBoolMemoRequest struct {
	SessionID string
	Enabled   bool
}

type sessionTextMemoRequest struct {
	SessionID string
	Text      string
}

type runtimeQueuedMessageMemoRequest struct {
	SessionID    string
	Text         string
	OperationRef clientui.RuntimeOperationRef
}

type liveSteerMemoRequest struct {
	SessionID runtimeids.SessionID
	Text      string
}

type liveStopMemoRequest struct {
	SessionID runtimeids.SessionID
}

type turnSubmitMemoRequest struct {
	SessionID string
	Text      string
}

type queuedUserMessageMemoRequest struct {
	SessionID   string
	QueueItemID string
}

type sessionCommandMemoRequest struct {
	SessionID string
	Command   string
}

type sessionOnlyMemoRequest struct {
	SessionID string
}

type runtimeInterruptMemoRequest struct {
	SessionID            string
	TargetOperationRef   *clientui.RuntimeOperationRef
	PendingOperationRefs []clientui.RuntimeOperationRef
}

type localEntryMemoRequest struct {
	SessionID  string
	Role       string
	Text       string
	Visibility transcript.EntryVisibility
	NoticeID   string
}

type goalSetMemoRequest struct {
	SessionID string
	Objective string
	Actor     string
	RunID     string
	StepID    string
}

type goalStatusMemoRequest struct {
	SessionID string
	Status    string
	Actor     string
	RunID     string
	StepID    string
}

type goalClearMemoRequest struct {
	SessionID string
	Actor     string
}

func NewService(runtimes RuntimeResolver) *Service {
	return &Service{
		runtimes:       runtimes,
		activity:       newDefaultRuntimeControlActivityResolver(runtimes),
		operations:     runtimeops.NewCoordinator(),
		sessionNames:   requestmemo.New[sessionStringMemoRequest, struct{}](),
		thinkingLevels: requestmemo.New[sessionStringMemoRequest, struct{}](),
		fastModes:      requestmemo.New[sessionBoolMemoRequest, serverapi.RuntimeSetFastModeEnabledResponse](),
		reviewers:      requestmemo.New[sessionBoolMemoRequest, serverapi.RuntimeSetReviewerEnabledResponse](),
		autoCompacts:   requestmemo.New[sessionBoolMemoRequest, serverapi.RuntimeSetAutoCompactionEnabledResponse](),
		questions:      requestmemo.New[sessionBoolMemoRequest, serverapi.RuntimeSetQuestionsEnabledResponse](),

		localEntries:   requestmemo.New[localEntryMemoRequest, struct{}](),
		queuedDiscards: requestmemo.New[queuedUserMessageMemoRequest, serverapi.RuntimeDiscardQueuedUserMessageResponse](),
		liveSteers:     requestmemo.New[liveSteerMemoRequest, serverapi.RuntimeLiveSteerResponse](),
		liveStops:      requestmemo.New[liveStopMemoRequest, serverapi.RuntimeLiveStopResponse](),
		promptHistory:  requestmemo.New[sessionTextMemoRequest, struct{}](),
		goals:          requestmemo.New[goalSetMemoRequest, serverapi.RuntimeGoalShowResponse](),
		goalStatuses:   requestmemo.New[goalStatusMemoRequest, serverapi.RuntimeGoalShowResponse](),
		goalClears:     requestmemo.New[goalClearMemoRequest, serverapi.RuntimeGoalShowResponse](),
	}
}

func (s *Service) WithRuntimeActivityResolver(resolver RuntimeActivityResolver) *Service {
	if s == nil {
		return nil
	}
	if resolver == nil {
		resolver = newDefaultRuntimeControlActivityResolver(s.runtimes)
	}
	s.activity = resolver
	return s
}

func (s *Service) WithOperationCoordinator(coordinator *runtimeops.Coordinator) *Service {
	if s == nil {
		return nil
	}
	if coordinator == nil {
		coordinator = runtimeops.NewCoordinator()
	}
	s.operations = coordinator
	return s
}

func (s *Service) WithPromptHistoryStore(store PromptHistoryStore) *Service {
	if s == nil {
		return nil
	}
	s.promptStore = store
	return s
}

func (s *Service) WithWorkflowSessionResolver(resolver WorkflowSessionResolver) *Service {
	if s == nil {
		return nil
	}
	s.workflowStates = resolver
	return s
}

func (s *Service) withRuntimeAccess(ctx context.Context, sessionID string, fn func(*runtime.Engine) error) error {
	if s == nil || s.runtimes == nil {
		return fmt.Errorf("runtime resolver is required")
	}
	trimmedSessionID := strings.TrimSpace(sessionID)
	acquired, err := s.runtimes.WithGuardedRuntime(ctx, trimmedSessionID, fn)
	if err != nil {
		return err
	}
	if !acquired {
		return errors.Join(serverapi.ErrRuntimeUnavailable, fmt.Errorf("runtime for session %q is unavailable", trimmedSessionID))
	}
	return nil
}

func (s *Service) beginRunStart(sessionID string) (runtimeRunStart, error) {
	if s == nil || s.runtimes == nil {
		return releaseOnlyRunStart{ctx: context.Background()}, nil
	}
	trimmed := strings.TrimSpace(sessionID)
	startCtx, release, ok := s.runtimes.BeginCancellableSessionRun(trimmed)
	if ok {
		return releaseOnlyRunStart{ctx: startCtx, release: release}, nil
	}
	if s.sessionRunsBlocked(trimmed) {
		return nil, serverapi.ErrSessionWorktreeDeleting
	}
	return nil, serverapi.ErrSessionRunStarting
}

func (s *Service) sessionRunsBlocked(sessionID string) bool {
	if s == nil || s.runtimes == nil {
		return false
	}
	return s.runtimes.SessionRunsBlocked(sessionID)
}

func mergeOperationContexts(contexts ...context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	stop := func() { once.Do(cancel) }
	for _, source := range contexts {
		if source == nil {
			continue
		}
		if err := source.Err(); err != nil {
			stop()
			continue
		}
		done := source.Done()
		if done == nil {
			continue
		}
		go func() {
			select {
			case <-done:
				stop()
			case <-ctx.Done():
			}
		}()
	}
	return ctx, stop
}

func releaseRunStartOnContextDone(start runtimeRunStart, ctx context.Context) func() {
	if start == nil || ctx == nil || ctx.Done() == nil {
		return func() {}
	}
	done := make(chan struct{})
	var once sync.Once
	stop := func() {
		once.Do(func() { close(done) })
	}
	go func() {
		select {
		case <-ctx.Done():
			start.Release()
		case <-done:
		}
	}()
	return stop
}

func (s *Service) operationAttemptCanceled(err error, attempt runtimeops.Attempt) bool {
	return err != nil && attempt.Context().Err() != nil
}

func (s *Service) recordRuntimeAccessFailureOrCancellation(sessionID string, ref clientui.RuntimeOperationRef, err error, attempt runtimeops.Attempt) {
	if s.operationAttemptCanceled(err, attempt) {
		s.operations.RecordCanceledNotCommitted(sessionID, ref)
		return
	}
	s.operations.RecordRuntimeAccessFailure(sessionID, ref)
}

func (s *Service) resolve(ctx context.Context, sessionID string) (*runtime.Engine, error) {
	if s == nil || s.runtimes == nil {
		return nil, fmt.Errorf("runtime resolver is required")
	}
	engine, err := s.runtimes.ResolveRuntime(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return nil, err
	}
	if engine == nil {
		return nil, errors.Join(serverapi.ErrRuntimeUnavailable, fmt.Errorf("runtime for session %q is unavailable", strings.TrimSpace(sessionID)))
	}
	return engine, nil
}

func (s *Service) SetSessionName(ctx context.Context, req serverapi.RuntimeSetSessionNameRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	memoReq := sessionStringMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Value: req.Name}
	_, err := s.sessionNames.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameSessionStringMemoRequest, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, s.withRuntimeAccess(ctx, req.SessionID, func(engine *runtime.Engine) error {
			if err := engine.SetSessionName(req.Name); err != nil {
				return err
			}
			if publisher, ok := s.runtimes.(sessionIdentityPublisher); ok {
				publisher.PublishSessionIdentity(req.SessionID, nil)
			}
			return nil
		})
	})
	return err
}

func (s *Service) SetThinkingLevel(ctx context.Context, req serverapi.RuntimeSetThinkingLevelRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	memoReq := sessionStringMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Value: req.Level}
	_, err := s.thinkingLevels.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameSessionStringMemoRequest, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, s.withRuntimeAccess(ctx, req.SessionID, func(engine *runtime.Engine) error {
			return engine.SetThinkingLevel(req.Level)
		})
	})
	return err
}

func (s *Service) SetFastModeEnabled(ctx context.Context, req serverapi.RuntimeSetFastModeEnabledRequest) (serverapi.RuntimeSetFastModeEnabledResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeSetFastModeEnabledResponse{}, err
	}
	memoReq := sessionBoolMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Enabled: req.Enabled}
	return s.fastModes.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameSessionBoolMemoRequest, func(ctx context.Context) (serverapi.RuntimeSetFastModeEnabledResponse, error) {
		var resp serverapi.RuntimeSetFastModeEnabledResponse
		err := s.withRuntimeAccess(ctx, req.SessionID, func(engine *runtime.Engine) error {
			changed, err := engine.SetFastModeEnabledWithCommittedFeedback(req.Enabled, func(changed bool) string {
				return serverapi.FastModeToggleStatusMessage(req.Enabled, changed)
			})
			resp = serverapi.RuntimeSetFastModeEnabledResponse{Changed: changed}
			return err
		})
		return resp, err
	})
}

func (s *Service) SetReviewerEnabled(ctx context.Context, req serverapi.RuntimeSetReviewerEnabledRequest) (serverapi.RuntimeSetReviewerEnabledResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeSetReviewerEnabledResponse{}, err
	}
	memoReq := sessionBoolMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Enabled: req.Enabled}
	return s.reviewers.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameSessionBoolMemoRequest, func(ctx context.Context) (serverapi.RuntimeSetReviewerEnabledResponse, error) {
		var resp serverapi.RuntimeSetReviewerEnabledResponse
		err := s.withRuntimeAccess(ctx, req.SessionID, func(engine *runtime.Engine) error {
			changed, mode, err := engine.SetReviewerEnabledWithCommittedFeedback(req.Enabled, func(enabled bool, mode string, changed bool) string {
				return serverapi.ReviewerToggleStatusMessage(enabled, mode, changed)
			})
			if err != nil {
				return err
			}
			resp = serverapi.RuntimeSetReviewerEnabledResponse{Changed: changed, Mode: mode}
			return nil
		})
		return resp, err
	})
}

func (s *Service) SetAutoCompactionEnabled(ctx context.Context, req serverapi.RuntimeSetAutoCompactionEnabledRequest) (serverapi.RuntimeSetAutoCompactionEnabledResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeSetAutoCompactionEnabledResponse{}, err
	}
	memoReq := sessionBoolMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Enabled: req.Enabled}
	return s.autoCompacts.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameSessionBoolMemoRequest, func(ctx context.Context) (serverapi.RuntimeSetAutoCompactionEnabledResponse, error) {
		var resp serverapi.RuntimeSetAutoCompactionEnabledResponse
		err := s.withRuntimeAccess(ctx, req.SessionID, func(engine *runtime.Engine) error {
			if !req.Enabled {
				if err := s.rejectWorkflowAutoCompactionDisable(ctx, req.SessionID, engine); err != nil {
					return err
				}
			}
			changed, enabled := engine.SetAutoCompactionEnabled(req.Enabled)
			resp = serverapi.RuntimeSetAutoCompactionEnabledResponse{Changed: changed, Enabled: enabled}
			return nil
		})
		return resp, err
	})
}

func (s *Service) SetQuestionsEnabled(ctx context.Context, req serverapi.RuntimeSetQuestionsEnabledRequest) (serverapi.RuntimeSetQuestionsEnabledResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeSetQuestionsEnabledResponse{}, err
	}
	memoReq := sessionBoolMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Enabled: req.Enabled}
	return s.questions.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameSessionBoolMemoRequest, func(ctx context.Context) (serverapi.RuntimeSetQuestionsEnabledResponse, error) {
		var resp serverapi.RuntimeSetQuestionsEnabledResponse
		err := s.withRuntimeAccess(ctx, req.SessionID, func(engine *runtime.Engine) error {
			changed, enabled, err := engine.SetQuestionsEnabledWithCommittedFeedback(req.Enabled, func(enabled bool, changed bool) string {
				return serverapi.QuestionsToggleStatusMessage(enabled, changed)
			})
			resp = serverapi.RuntimeSetQuestionsEnabledResponse{Changed: changed, Enabled: enabled}
			return err
		})
		return resp, err
	})
}

func (s *Service) AppendCommittedEntry(ctx context.Context, req serverapi.RuntimeAppendCommittedEntryRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	visibility := transcript.NormalizeEntryVisibility(transcript.EntryVisibility(req.Visibility))
	memoReq := localEntryMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Role: strings.TrimSpace(req.Role), Text: req.Text, Visibility: visibility, NoticeID: strings.TrimSpace(req.NoticeID)}
	_, err := s.localEntries.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameLocalEntryMemoRequest, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, s.withRuntimeAccess(ctx, req.SessionID, func(engine *runtime.Engine) error {
			if visibility == transcript.EntryVisibilityAuto && strings.TrimSpace(req.NoticeID) != "" {
				return engine.AppendCommittedEntryWithNoticeID(req.Role, req.Text, req.NoticeID)
			}
			if visibility == transcript.EntryVisibilityAuto {
				return engine.AppendCommittedEntry(req.Role, req.Text)
			}
			return engine.AppendCommittedEntryWithVisibility(req.Role, req.Text, visibility)
		})
	})
	return err
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
	return s.withRuntimeAccess(ctx, trimmedSessionID, func(engine *runtime.Engine) error {
		return engine.AppendCommittedEntry(trimmedRole, trimmedText)
	})
}

func (s *Service) ShouldCompactBeforeUserMessage(ctx context.Context, req serverapi.RuntimeShouldCompactBeforeUserMessageRequest) (serverapi.RuntimeShouldCompactBeforeUserMessageResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeShouldCompactBeforeUserMessageResponse{}, err
	}
	engine, err := s.resolve(ctx, req.SessionID)
	if err != nil {
		return serverapi.RuntimeShouldCompactBeforeUserMessageResponse{}, err
	}
	shouldCompact, err := engine.ShouldCompactBeforeUserMessage(ctx, req.Text)
	if err != nil {
		return serverapi.RuntimeShouldCompactBeforeUserMessageResponse{}, err
	}
	return serverapi.RuntimeShouldCompactBeforeUserMessageResponse{ShouldCompact: shouldCompact}, nil
}

func (s *Service) SubmitUserShellCommand(ctx context.Context, req serverapi.RuntimeSubmitUserShellCommandRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	memoReq := sessionCommandMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Command: req.Command}
	_, err := runtimeops.Do(s.operations, ctx, memoReq.SessionID, req.OperationRef, memoReq, sameSessionCommandMemoRequest, func(ctx context.Context, attempt runtimeops.Attempt) (struct{}, error) {
		start, err := s.beginRunStart(req.SessionID)
		if err != nil {
			s.recordRuntimeAccessFailureOrCancellation(memoReq.SessionID, req.OperationRef, err, attempt)
			return struct{}{}, err
		}
		defer start.Release()
		stopStartCancel := releaseRunStartOnContextDone(start, attempt.Context())
		defer stopStartCancel()
		runCtx, stopRunCtx := mergeOperationContexts(attempt.Context(), start.Context())
		defer stopRunCtx()
		err = s.withRuntimeAccess(runCtx, req.SessionID, func(engine *runtime.Engine) error {
			_, err := engine.SubmitUserShellCommandWithActiveHook(runCtx, memoReq.Command, func() {
				s.operations.MarkOperationActive(memoReq.SessionID, req.OperationRef)
			})
			return err
		})
		if s.operationAttemptCanceled(err, attempt) {
			s.operations.RecordCanceledNotCommitted(memoReq.SessionID, req.OperationRef)
		} else {
			s.operations.RecordShellCompletion(memoReq.SessionID, req.OperationRef, err)
		}
		return struct{}{}, err
	})
	return err
}

func (s *Service) CompactContext(ctx context.Context, req serverapi.RuntimeCompactContextRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	memoReq := sessionStringMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Value: req.Args}
	_, err := runtimeops.Do(s.operations, ctx, memoReq.SessionID, req.OperationRef, memoReq, sameSessionStringMemoRequest, func(ctx context.Context, attempt runtimeops.Attempt) (struct{}, error) {
		start, err := s.beginRunStart(req.SessionID)
		if err != nil {
			s.recordRuntimeAccessFailureOrCancellation(memoReq.SessionID, req.OperationRef, err, attempt)
			return struct{}{}, err
		}
		defer start.Release()
		stopStartCancel := releaseRunStartOnContextDone(start, attempt.Context())
		defer stopStartCancel()
		runCtx, stopRunCtx := mergeOperationContexts(attempt.Context(), start.Context())
		defer stopRunCtx()
		err = s.withRuntimeAccess(runCtx, req.SessionID, func(engine *runtime.Engine) error {
			return engine.CompactContextWithActiveHook(runCtx, req.Args, func() {
				s.operations.MarkOperationActive(memoReq.SessionID, req.OperationRef)
			})
		})
		if s.operationAttemptCanceled(err, attempt) {
			s.operations.RecordCanceledNotCommitted(memoReq.SessionID, req.OperationRef)
		} else {
			s.operations.RecordCompactCompletion(memoReq.SessionID, req.OperationRef, err)
		}
		return struct{}{}, err
	})
	return err
}

func (s *Service) CompactContextForPreSubmit(ctx context.Context, req serverapi.RuntimeCompactContextForPreSubmitRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	memoReq := sessionOnlyMemoRequest{SessionID: strings.TrimSpace(req.SessionID)}
	_, err := runtimeops.Do(s.operations, ctx, memoReq.SessionID, req.OperationRef, memoReq, func(a sessionOnlyMemoRequest, b sessionOnlyMemoRequest) bool { return a.SessionID == b.SessionID }, func(ctx context.Context, attempt runtimeops.Attempt) (struct{}, error) {
		start, err := s.beginRunStart(req.SessionID)
		if err != nil {
			s.recordRuntimeAccessFailureOrCancellation(memoReq.SessionID, req.OperationRef, err, attempt)
			return struct{}{}, err
		}
		defer start.Release()
		stopStartCancel := releaseRunStartOnContextDone(start, attempt.Context())
		defer stopStartCancel()
		runCtx, stopRunCtx := mergeOperationContexts(attempt.Context(), start.Context())
		defer stopRunCtx()
		err = s.withRuntimeAccess(runCtx, req.SessionID, func(engine *runtime.Engine) error {
			return engine.CompactContextForPreSubmitWithActiveHook(runCtx, func() {
				s.operations.MarkOperationActive(memoReq.SessionID, req.OperationRef)
			})
		})
		if s.operationAttemptCanceled(err, attempt) {
			s.operations.RecordCanceledNotCommitted(memoReq.SessionID, req.OperationRef)
		} else {
			s.operations.RecordCompactCompletion(memoReq.SessionID, req.OperationRef, err)
		}
		return struct{}{}, err
	})
	return err
}

func (s *Service) HasQueuedUserWork(ctx context.Context, req serverapi.RuntimeHasQueuedUserWorkRequest) (serverapi.RuntimeHasQueuedUserWorkResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeHasQueuedUserWorkResponse{}, err
	}
	engine, err := s.resolve(ctx, req.SessionID)
	if err != nil {
		return serverapi.RuntimeHasQueuedUserWorkResponse{}, err
	}
	return serverapi.RuntimeHasQueuedUserWorkResponse{HasQueuedUserWork: engine.HasQueuedUserWork()}, nil
}

func (s *Service) SubmitQueuedUserMessages(ctx context.Context, req serverapi.RuntimeSubmitQueuedUserMessagesRequest) (serverapi.RuntimeSubmitQueuedUserMessagesResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeSubmitQueuedUserMessagesResponse{}, err
	}
	memoReq := sessionOnlyMemoRequest{SessionID: strings.TrimSpace(req.SessionID)}
	return runtimeops.Do(s.operations, ctx, memoReq.SessionID, req.OperationRef, memoReq, func(a sessionOnlyMemoRequest, b sessionOnlyMemoRequest) bool { return a.SessionID == b.SessionID }, func(ctx context.Context, attempt runtimeops.Attempt) (serverapi.RuntimeSubmitQueuedUserMessagesResponse, error) {
		start, err := s.beginRunStart(req.SessionID)
		if err != nil {
			s.recordRuntimeAccessFailureOrCancellation(memoReq.SessionID, req.OperationRef, err, attempt)
			return serverapi.RuntimeSubmitQueuedUserMessagesResponse{}, err
		}
		defer start.Release()
		stopStartCancel := releaseRunStartOnContextDone(start, attempt.Context())
		defer stopStartCancel()
		runCtx, stopRunCtx := mergeOperationContexts(attempt.Context(), start.Context())
		defer stopRunCtx()
		var resp serverapi.RuntimeSubmitQueuedUserMessagesResponse
		err = s.withRuntimeAccess(runCtx, req.SessionID, func(engine *runtime.Engine) error {
			msg, err := engine.SubmitQueuedUserMessagesWithActiveHook(runCtx, func() {
				s.operations.MarkOperationActive(memoReq.SessionID, req.OperationRef)
			})
			resp = serverapi.RuntimeSubmitQueuedUserMessagesResponse{Message: msg.Content}
			return err
		})
		if s.operationAttemptCanceled(err, attempt) {
			s.operations.RecordCanceledNotCommitted(memoReq.SessionID, req.OperationRef)
		} else {
			s.operations.RecordQueuedMessageStatus(memoReq.SessionID, req.OperationRef, err == nil)
		}
		return resp, err
	})
}

func (s *Service) Interrupt(ctx context.Context, req serverapi.RuntimeInterruptRequest) (serverapi.RuntimeInterruptResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeInterruptResponse{}, err
	}
	if s == nil || s.runtimes == nil {
		return serverapi.RuntimeInterruptResponse{}, fmt.Errorf("runtime resolver is required")
	}
	sessionID := strings.TrimSpace(req.SessionID)
	reqData := runtimeInterruptMemoRequest{
		SessionID:            sessionID,
		TargetOperationRef:   cloneRuntimeOperationRefPtr(req.TargetOperationRef),
		PendingOperationRefs: append([]clientui.RuntimeOperationRef(nil), req.PendingOperationRefs...),
	}
	return s.interrupt(ctx, reqData)
}

func (s *Service) interrupt(ctx context.Context, req runtimeInterruptMemoRequest) (serverapi.RuntimeInterruptResponse, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	pendingRefs := append([]clientui.RuntimeOperationRef(nil), req.PendingOperationRefs...)
	interruptActive := req.TargetOperationRef == nil
	targetQueuedMessage := false
	var cancelResult runtimeops.CancellationResult
	if req.TargetOperationRef != nil {
		targetQueuedMessage = req.TargetOperationRef.Kind == clientui.RuntimeOperationKindQueuedMessage
		var err error
		cancelResult, err = s.operations.CancelOperationTarget(sessionID, *req.TargetOperationRef)
		if err != nil {
			return serverapi.RuntimeInterruptResponse{}, err
		}
		interruptActive = !targetQueuedMessage && cancelResult.InterruptActive && s.runtimeActivityActiveForControl(ctx, sessionID, pendingRefs)
		if !runtimeOperationRefsContain(pendingRefs, *req.TargetOperationRef) {
			pendingRefs = append([]clientui.RuntimeOperationRef{*req.TargetOperationRef}, pendingRefs...)
		}
	}
	engine, err := s.runtimes.ResolveRuntime(ctx, sessionID)
	if err != nil {
		return serverapi.RuntimeInterruptResponse{}, err
	}
	if engine != nil && interruptActive {
		if err := engine.Interrupt(); err != nil {
			return serverapi.RuntimeInterruptResponse{}, err
		}
	}
	if req.TargetOperationRef != nil {
		cancelResult.CancelOperationAttempt()
	}
	if engine != nil && req.TargetOperationRef != nil && req.TargetOperationRef.Kind == clientui.RuntimeOperationKindQueuedMessage && strings.TrimSpace(req.TargetOperationRef.QueueItemID) != "" {
		engine.DiscardQueuedUserMessage(req.TargetOperationRef.QueueItemID)
	}
	return s.runtimeInterruptResponse(sessionID, engine, pendingRefs), nil
}

func cloneRuntimeOperationRefPtr(ref *clientui.RuntimeOperationRef) *clientui.RuntimeOperationRef {
	if ref == nil {
		return nil
	}
	clone := *ref
	return &clone
}

func (s *Service) runtimeActivityActiveForControl(ctx context.Context, sessionID string, refs []clientui.RuntimeOperationRef) bool {
	if s == nil || s.activity == nil {
		return false
	}
	snapshot, err := s.activity.Snapshot(ctx, sessionID, refs)
	return err == nil && snapshot.Activity.ActiveForControl()
}

func (s *Service) runtimeInterruptResponse(sessionID string, engine *runtime.Engine, refs []clientui.RuntimeOperationRef) serverapi.RuntimeInterruptResponse {
	snapshot, err := s.activity.Snapshot(context.Background(), sessionID, refs)
	if err != nil {
		version := runtimeactivity.NextReadModelVersion(sessionID)
		return serverapi.RuntimeInterruptResponse{
			Version:             version,
			Activity:            clientui.MustRuntimeActivity(clientui.RuntimeActivityUnavailable, clientui.RuntimeActivityOptions{DiagnosticRecovery: true}),
			InputReconciliation: s.operations.Snapshot(sessionID, version, refs),
		}
	}
	return serverapi.RuntimeInterruptResponse{
		Version:             snapshot.Version,
		Activity:            snapshot.Activity,
		InputReconciliation: s.interruptInputReconciliation(sessionID, snapshot, refs),
	}
}

func (s *Service) interruptInputReconciliation(sessionID string, snapshot runtimeactivity.ResponseSnapshot, refs []clientui.RuntimeOperationRef) clientui.RuntimeInputReconciliationSnapshot {
	if len(refs) == 0 || len(snapshot.InputReconciliation.Operations) > 0 {
		return snapshot.InputReconciliation
	}
	return s.operations.Snapshot(sessionID, snapshot.Version, refs)
}

func (s *Service) QueueUserMessage(ctx context.Context, req serverapi.RuntimeQueueUserMessageRequest) (serverapi.RuntimeQueueUserMessageResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeQueueUserMessageResponse{}, err
	}
	memoReq := runtimeQueuedMessageMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Text: req.Text, OperationRef: req.OperationRef}
	return runtimeops.Do(s.operations, ctx, memoReq.SessionID, req.OperationRef, memoReq, sameRuntimeQueuedMessageMemoRequest, func(ctx context.Context, attempt runtimeops.Attempt) (serverapi.RuntimeQueueUserMessageResponse, error) {
		var resp serverapi.RuntimeQueueUserMessageResponse
		runCtx, stopRunCtx := mergeOperationContexts(ctx, attempt.Context())
		defer stopRunCtx()
		err := s.withRuntimeAccess(runCtx, req.SessionID, func(engine *runtime.Engine) error {
			if err := attempt.Context().Err(); err != nil {
				return err
			}
			committed, err := s.operations.TryCommitOperationMutation(memoReq.SessionID, memoReq.OperationRef, func() error {
				text := memoReq.Text
				if s != nil && s.promptStore != nil {
					record, _, err := s.recordPromptHistory(runCtx, memoReq.SessionID, strings.TrimSpace(req.ClientRequestID), memoReq.Text)
					if err != nil {
						return err
					}
					text = record.Text
				}
				item := engine.QueueUserMessageWithClientRequestID(text, strings.TrimSpace(req.ClientRequestID))
				resp = serverapi.RuntimeQueueUserMessageResponse{QueueItemID: item.ID, Text: item.Text, ClientRequestID: item.ClientRequestID}
				return nil
			})
			if err != nil {
				return err
			}
			if !committed {
				return runtimeops.ErrOperationCanceled
			}
			return nil
		})
		if s.operationAttemptCanceled(err, attempt) {
			s.operations.RecordCanceledNotCommitted(memoReq.SessionID, memoReq.OperationRef)
		} else if err != nil {
			s.operations.RecordQueuedMessageFailed(memoReq.SessionID, memoReq.OperationRef)
		} else {
			s.operations.RecordCommitted(memoReq.SessionID, memoReq.OperationRef)
		}
		return resp, err
	})
}

func (s *Service) DiscardQueuedUserMessage(ctx context.Context, req serverapi.RuntimeDiscardQueuedUserMessageRequest) (serverapi.RuntimeDiscardQueuedUserMessageResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeDiscardQueuedUserMessageResponse{}, err
	}
	memoReq := queuedUserMessageMemoRequest{SessionID: strings.TrimSpace(req.SessionID), QueueItemID: strings.TrimSpace(req.QueueItemID)}
	return s.queuedDiscards.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameQueuedUserMessageMemoRequest, func(ctx context.Context) (serverapi.RuntimeDiscardQueuedUserMessageResponse, error) {
		var resp serverapi.RuntimeDiscardQueuedUserMessageResponse
		err := s.withRuntimeAccess(ctx, req.SessionID, func(engine *runtime.Engine) error {
			resp = serverapi.RuntimeDiscardQueuedUserMessageResponse{Discarded: engine.DiscardQueuedUserMessage(memoReq.QueueItemID)}
			return nil
		})
		return resp, err
	})
}

func (s *Service) RecordPromptHistory(ctx context.Context, req serverapi.RuntimeRecordPromptHistoryRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	memoReq := sessionTextMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Text: req.Text}
	_, err := s.promptHistory.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameSessionTextMemoRequest, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, s.withRuntimeAccess(ctx, req.SessionID, func(*runtime.Engine) error {
			_, _, err := s.recordPromptHistory(ctx, memoReq.SessionID, strings.TrimSpace(req.ClientRequestID), memoReq.Text)
			return err
		})
	})
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
	if engine != nil && engine.WorkflowSessionState().RunID != "" {
		return true, nil
	}
	if s != nil && s.workflowStates != nil {
		store, err := s.workflowStates.ResolveSessionStore(ctx, sessionID)
		if err != nil {
			return false, err
		}
		if store != nil && store.Meta().WorkflowSession != nil {
			return true, nil
		}
	}
	return false, nil
}

func sameSessionTextMemoRequest(a sessionTextMemoRequest, b sessionTextMemoRequest) bool {
	return a.SessionID == b.SessionID && a.Text == b.Text
}

func sameRuntimeQueuedMessageMemoRequest(a runtimeQueuedMessageMemoRequest, b runtimeQueuedMessageMemoRequest) bool {
	return a.SessionID == b.SessionID && a.Text == b.Text && a.OperationRef == b.OperationRef
}

func sameLiveSteerMemoRequest(a liveSteerMemoRequest, b liveSteerMemoRequest) bool {
	return a.SessionID == b.SessionID && a.Text == b.Text
}

func sameLiveStopMemoRequest(a liveStopMemoRequest, b liveStopMemoRequest) bool {
	return a.SessionID == b.SessionID
}

func sameTurnSubmitMemoRequest(a turnSubmitMemoRequest, b turnSubmitMemoRequest) bool {
	return a.SessionID == b.SessionID && a.Text == b.Text
}

func sameQueuedUserMessageMemoRequest(a queuedUserMessageMemoRequest, b queuedUserMessageMemoRequest) bool {
	return a.SessionID == b.SessionID && a.QueueItemID == b.QueueItemID
}

func sameSessionStringMemoRequest(a sessionStringMemoRequest, b sessionStringMemoRequest) bool {
	return a.SessionID == b.SessionID && a.Value == b.Value
}

func sameSessionBoolMemoRequest(a sessionBoolMemoRequest, b sessionBoolMemoRequest) bool {
	return a.SessionID == b.SessionID && a.Enabled == b.Enabled
}

func sameSessionCommandMemoRequest(a sessionCommandMemoRequest, b sessionCommandMemoRequest) bool {
	return a.SessionID == b.SessionID && a.Command == b.Command
}

func sameLocalEntryMemoRequest(a localEntryMemoRequest, b localEntryMemoRequest) bool {
	return a.SessionID == b.SessionID && a.Role == b.Role && a.Text == b.Text && a.Visibility == b.Visibility && a.NoticeID == b.NoticeID
}

func sameGoalSetMemoRequest(a goalSetMemoRequest, b goalSetMemoRequest) bool {
	return a.SessionID == b.SessionID && a.Objective == b.Objective && a.Actor == b.Actor && a.RunID == b.RunID && a.StepID == b.StepID
}

func sameGoalStatusMemoRequest(a goalStatusMemoRequest, b goalStatusMemoRequest) bool {
	return a.SessionID == b.SessionID && a.Status == b.Status && a.Actor == b.Actor && a.RunID == b.RunID && a.StepID == b.StepID
}

func sameGoalClearMemoRequest(a goalClearMemoRequest, b goalClearMemoRequest) bool {
	return a.SessionID == b.SessionID && a.Actor == b.Actor
}

func runtimeOperationRefsContain(refs []clientui.RuntimeOperationRef, want clientui.RuntimeOperationRef) bool {
	for _, ref := range refs {
		if ref == want {
			return true
		}
	}
	return false
}

var _ servicecontract.RuntimeControlService = (*Service)(nil)
