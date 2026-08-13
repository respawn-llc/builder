package runtimecontrol

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"core/server/launch"
	"core/server/metadata"
	"core/server/requestmemo"
	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/runtimecommand"
	"core/server/session"
	"core/server/sessionruntime"
	servicecontract "core/shared/apicontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/transcript"
)

type RuntimeActivityResolver interface {
	RuntimeReadModelSnapshot(ctx context.Context, sessionID string) (runtimeactivity.ResponseSnapshot, error)
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

type ChatSettingsPreparationResolver interface {
	PrepareSessionChatSettings(ctx context.Context, store *session.Store, agent string) (launch.PreparedChatSettings, error)
}

var errWorkflowTaskSessionAutoCompactionDisable = errors.New("auto-compaction cannot be disabled for workflow task sessions")

type Service struct {
	authority            *sessionruntime.Authority
	execution            *runtimecommand.ExecutionAdapter
	goalAuthority        *runtimecommand.GoalAuthority
	activity             RuntimeActivityResolver
	promptStore          PromptHistoryStore
	promptCommands       PromptCommandResolver
	workflowTasks        WorkflowTaskSessionResolver
	persisted            session.PersistedSessionResolver
	chatSettings         ChatSettingsPreparationResolver
	askViews             servicecontract.AskViewService
	approvalViews        servicecontract.ApprovalViewService
	attention            servicecontract.AttentionNotificationService
	sessionNames         *requestmemo.Memo[sessionStringMemoRequest, struct{}]
	thinkingLevels       *requestmemo.Memo[sessionStringMemoRequest, committedRuntimeMutationResult[struct{}]]
	fastModes            *requestmemo.Memo[sessionBoolMemoRequest, committedRuntimeMutationResult[serverapi.RuntimeSetFastModeEnabledResponse]]
	reviewers            *requestmemo.Memo[sessionBoolMemoRequest, committedRuntimeMutationResult[serverapi.RuntimeSetReviewerEnabledResponse]]
	autoCompacts         *requestmemo.Memo[sessionBoolMemoRequest, committedRuntimeMutationResult[serverapi.RuntimeSetAutoCompactionEnabledResponse]]
	questions            *requestmemo.Memo[sessionBoolMemoRequest, committedRuntimeMutationResult[serverapi.RuntimeSetQuestionsEnabledResponse]]
	userTurns            *requestmemo.Memo[sessionUserTurnMemoRequest, committedRuntimeMutationResult[serverapi.RuntimeSubmitUserTurnResponse]]
	userShells           *requestmemo.Memo[sessionCommandMemoRequest, committedRuntimeMutationResult[struct{}]]
	compactions          *requestmemo.Memo[sessionStringMemoRequest, committedRuntimeMutationResult[struct{}]]
	preSubmitCompactions *requestmemo.Memo[sessionOnlyMemoRequest, committedRuntimeMutationResult[bool]]

	localEntries   *requestmemo.Memo[localEntryMemoRequest, struct{}]
	queuedDiscards *requestmemo.Memo[queuedUserMessageMemoRequest, serverapi.RuntimeDiscardQueuedUserMessageResponse]
	liveSteers     *requestmemo.Memo[liveSteerMemoRequest, serverapi.RuntimeLiveSteerResponse]
	liveStops      *requestmemo.Memo[liveStopMemoRequest, serverapi.RuntimeLiveStopResponse]
	promptHistory  *requestmemo.Memo[sessionTextMemoRequest, struct{}]
	goals          *requestmemo.Memo[goalSetMemoRequest, committedGoalMutationResult]
	goalStatuses   *requestmemo.Memo[goalStatusMemoRequest, committedGoalMutationResult]
	goalClears     *requestmemo.Memo[goalClearMemoRequest, committedGoalMutationResult]
}

type committedRuntimeMutationResult[Resp any] struct {
	Response Resp
	Err      error
}

type committedGoalMutationResult struct {
	Response serverapi.RuntimeGoalMutationResponse
	Err      error
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

type sessionUserTurnMemoRequest struct {
	SessionID string
	Kind      serverapi.RuntimeUserTurnInputKind
	Text      string
	Name      string
	Arguments string
}

type liveSteerMemoRequest struct {
	SessionID       runtimeids.SessionID
	CallerSessionID serverapi.OptionalStringKey
	Text            string
}

type liveStopMemoRequest struct {
	SessionID runtimeids.SessionID
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

func NewService(authority *sessionruntime.Authority) *Service {
	execution := runtimecommand.NewExecutionAdapter(authority)
	return NewServiceWithGoalCommands(
		authority,
		execution,
		runtimecommand.NewGoalAuthority(authority, execution),
	)
}

func NewServiceWithGoalCommands(
	authority *sessionruntime.Authority,
	execution *runtimecommand.ExecutionAdapter,
	goalAuthority *runtimecommand.GoalAuthority,
) *Service {
	if execution == nil {
		execution = runtimecommand.NewExecutionAdapter(authority)
	}
	if goalAuthority == nil {
		goalAuthority = runtimecommand.NewGoalAuthority(authority, execution)
	}
	return &Service{
		authority:            authority,
		execution:            execution,
		goalAuthority:        goalAuthority,
		sessionNames:         requestmemo.New[sessionStringMemoRequest, struct{}](),
		thinkingLevels:       requestmemo.New[sessionStringMemoRequest, committedRuntimeMutationResult[struct{}]](),
		fastModes:            requestmemo.New[sessionBoolMemoRequest, committedRuntimeMutationResult[serverapi.RuntimeSetFastModeEnabledResponse]](),
		reviewers:            requestmemo.New[sessionBoolMemoRequest, committedRuntimeMutationResult[serverapi.RuntimeSetReviewerEnabledResponse]](),
		autoCompacts:         requestmemo.New[sessionBoolMemoRequest, committedRuntimeMutationResult[serverapi.RuntimeSetAutoCompactionEnabledResponse]](),
		questions:            requestmemo.New[sessionBoolMemoRequest, committedRuntimeMutationResult[serverapi.RuntimeSetQuestionsEnabledResponse]](),
		userTurns:            requestmemo.New[sessionUserTurnMemoRequest, committedRuntimeMutationResult[serverapi.RuntimeSubmitUserTurnResponse]](),
		userShells:           requestmemo.New[sessionCommandMemoRequest, committedRuntimeMutationResult[struct{}]](),
		compactions:          requestmemo.New[sessionStringMemoRequest, committedRuntimeMutationResult[struct{}]](),
		preSubmitCompactions: requestmemo.New[sessionOnlyMemoRequest, committedRuntimeMutationResult[bool]](),

		localEntries:   requestmemo.New[localEntryMemoRequest, struct{}](),
		queuedDiscards: requestmemo.New[queuedUserMessageMemoRequest, serverapi.RuntimeDiscardQueuedUserMessageResponse](),
		liveSteers:     requestmemo.New[liveSteerMemoRequest, serverapi.RuntimeLiveSteerResponse](),
		liveStops:      requestmemo.New[liveStopMemoRequest, serverapi.RuntimeLiveStopResponse](),
		promptHistory:  requestmemo.New[sessionTextMemoRequest, struct{}](),
		goals:          requestmemo.New[goalSetMemoRequest, committedGoalMutationResult](),
		goalStatuses:   requestmemo.New[goalStatusMemoRequest, committedGoalMutationResult](),
		goalClears:     requestmemo.New[goalClearMemoRequest, committedGoalMutationResult](),
	}
}

func (s *Service) runAgentExecution(
	ctx context.Context,
	sessionID string,
	run func(context.Context, *runtime.Engine) error,
) error {
	if s == nil || s.execution == nil {
		return errors.New("session runtime authority is required")
	}
	return s.execution.RunAgentExecution(ctx, sessionID, run)
}

func (s *Service) runAgentExecutionID(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	run func(context.Context, *runtime.Engine) error,
) error {
	if s == nil || s.execution == nil {
		return errors.New("session runtime authority is required")
	}
	return s.execution.RunAgentExecutionID(ctx, sessionID, run)
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

func (s *Service) WithChatSettingsPreparationResolver(resolver ChatSettingsPreparationResolver) *Service {
	if s == nil {
		return nil
	}
	s.chatSettings = resolver
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

type runtimeCommandAttempt struct {
	caller     context.Context
	ctx        context.Context
	cancel     context.CancelCauseFunc
	stopCaller func() bool

	mu       sync.Mutex
	accepted bool
	finished bool
}

func newRuntimeCommandAttempt(caller context.Context) *runtimeCommandAttempt {
	if caller == nil {
		caller = context.Background()
	}
	ctx, cancel := context.WithCancelCause(context.WithoutCancel(caller))
	attempt := &runtimeCommandAttempt{caller: caller, ctx: ctx, cancel: cancel}
	attempt.stopCaller = context.AfterFunc(caller, func() {
		attempt.mu.Lock()
		defer attempt.mu.Unlock()
		if !attempt.accepted && !attempt.finished {
			attempt.cancel(context.Cause(caller))
		}
	})
	if cause := context.Cause(caller); cause != nil {
		attempt.cancel(cause)
	}
	return attempt
}

func (a *runtimeCommandAttempt) Context() context.Context {
	if a == nil {
		return context.Background()
	}
	return a.ctx
}

func (a *runtimeCommandAttempt) Accept(commit func() (bool, error)) (bool, error) {
	if a == nil {
		return false, errors.New("runtime command attempt is required")
	}
	if commit == nil {
		return false, errors.New("runtime command acceptance mutation is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.finished {
		return false, errors.New("runtime command attempt already finished")
	}
	if a.accepted {
		return false, errors.New("runtime command was accepted more than once")
	}
	if cause := context.Cause(a.caller); cause != nil {
		a.cancel(cause)
		return false, cause
	}
	committed, err := commit()
	if committed {
		a.accepted = true
		if a.stopCaller != nil {
			a.stopCaller()
		}
	}
	return committed, err
}

func (a *runtimeCommandAttempt) Accepted() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.accepted
}

func (a *runtimeCommandAttempt) Finish() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.finished = true
	if a.stopCaller != nil {
		a.stopCaller()
	}
	a.cancel(context.Canceled)
}

func (s *Service) SetSessionName(ctx context.Context, req serverapi.RuntimeSetSessionNameRequest) error {
	_, err := servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.RuntimeSetSessionNameRequest]) (struct{}, error) {
		return struct{}{}, s.SetSessionNameValidated(ctx, validated, servicecontract.AuthorizedSessionInActiveProject{})
	})
	return err
}

func (s *Service) SetSessionNameValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeSetSessionNameRequest], authorization servicecontract.AuthorizedSessionInActiveProject) error {
	req := validated.Value()
	sessionID, err := runtimeControlSessionID(req.SessionID, authorization)
	if err != nil {
		return err
	}
	memoReq := sessionStringMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Value: req.Name}
	_, err = s.sessionNames.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameSessionStringMemoRequest, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, s.withRuntimeID(ctx, sessionID, func(_ context.Context, engine *runtime.Engine) error {
			if err := engine.SetSessionName(req.Name); err != nil {
				return err
			}
			if publisher, ok := s.activity.(sessionIdentityPublisher); ok {
				return publisher.PublishSessionIdentity(sessionID.String())
			}
			return nil
		})
	})
	return err
}

func (s *Service) SetThinkingLevel(ctx context.Context, req serverapi.RuntimeSetThinkingLevelRequest) error {
	_, err := servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.RuntimeSetThinkingLevelRequest]) (struct{}, error) {
		return struct{}{}, s.SetThinkingLevelValidated(ctx, validated, servicecontract.AuthorizedSessionInActiveProject{})
	})
	return err
}

func (s *Service) SetThinkingLevelValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeSetThinkingLevelRequest], authorization servicecontract.AuthorizedSessionInActiveProject) error {
	req := validated.Value()
	sessionID, err := runtimeControlSessionID(req.SessionID, authorization)
	if err != nil {
		return err
	}
	level := strings.TrimSpace(req.Level)
	memoReq := sessionStringMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Value: level}
	_, err = memoizedChatSettingsMutation(s, ctx, strings.TrimSpace(req.ClientRequestID), memoReq.SessionID, memoReq, s.thinkingLevels, sameSessionStringMemoRequest, func(ctx context.Context) (struct{}, bool, error) {
		_, accepted, mutationErr := s.mutateChatSettings(
			ctx,
			sessionID,
			func(ctx context.Context, store *session.Store, _ *runtime.Engine) (session.ChatSettingsMutation, error) {
				prepared, err := s.prepareChatSettings(ctx, store)
				if err != nil {
					return session.ChatSettingsMutation{}, err
				}
				if !slices.Contains(prepared.SupportedThinkingValues, level) {
					return session.ChatSettingsMutation{}, fmt.Errorf(
						"thinking level %q is unavailable for the selected Session Agent",
						level,
					)
				}
				return session.ChatSettingsMutation{Thinking: &level}, nil
			},
			func(engine *runtime.Engine, _ session.ChatSettingsMutationResult) error {
				return engine.SetThinkingLevel(level)
			},
		)
		return struct{}{}, accepted, mutationErr
	})
	return err
}

func (s *Service) SetFastModeEnabled(ctx context.Context, req serverapi.RuntimeSetFastModeEnabledRequest) (serverapi.RuntimeSetFastModeEnabledResponse, error) {
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.RuntimeSetFastModeEnabledRequest]) (serverapi.RuntimeSetFastModeEnabledResponse, error) {
		return s.SetFastModeEnabledValidated(ctx, validated, servicecontract.AuthorizedSessionInActiveProject{})
	})
}

func (s *Service) SetFastModeEnabledValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeSetFastModeEnabledRequest], authorization servicecontract.AuthorizedSessionInActiveProject) (serverapi.RuntimeSetFastModeEnabledResponse, error) {
	req := validated.Value()
	sessionID, err := runtimeControlSessionID(req.SessionID, authorization)
	if err != nil {
		return serverapi.RuntimeSetFastModeEnabledResponse{}, err
	}
	memoReq := sessionBoolMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Enabled: req.Enabled}
	return memoizedChatSettingsMutation(s, ctx, strings.TrimSpace(req.ClientRequestID), memoReq.SessionID, memoReq, s.fastModes, sameSessionBoolMemoRequest, func(ctx context.Context) (serverapi.RuntimeSetFastModeEnabledResponse, bool, error) {
		result, accepted, err := s.mutateChatSettings(
			ctx,
			sessionID,
			func(ctx context.Context, store *session.Store, engine *runtime.Engine) (session.ChatSettingsMutation, error) {
				if req.Enabled {
					if engine != nil {
						if !engine.FastModeAvailable() {
							return session.ChatSettingsMutation{}, errors.New("fast mode is only available for OpenAI-based Responses providers")
						}
					} else {
						prepared, prepareErr := s.prepareChatSettings(ctx, store)
						if prepareErr != nil {
							return session.ChatSettingsMutation{}, prepareErr
						}
						if !prepared.FastAvailable {
							return session.ChatSettingsMutation{}, errors.New("fast mode is only available for OpenAI-based Responses providers")
						}
					}
				}
				return session.ChatSettingsMutation{Fast: &req.Enabled}, nil
			},
			func(engine *runtime.Engine, result session.ChatSettingsMutationResult) error {
				_, _, applyErr := engine.SetFastModeEnabledWithCommittedFeedback(req.Enabled, func(bool) string {
					return serverapi.FastModeToggleStatusMessage(req.Enabled, result.Changed)
				})
				if applyErr != nil {
					_, stateErr := engine.SetFastModeEnabled(req.Enabled)
					applyErr = errors.Join(applyErr, stateErr)
				}
				return applyErr
			},
		)
		return serverapi.RuntimeSetFastModeEnabledResponse{Changed: result.Changed}, accepted, err
	})
}

func (s *Service) SetReviewerEnabled(ctx context.Context, req serverapi.RuntimeSetReviewerEnabledRequest) (serverapi.RuntimeSetReviewerEnabledResponse, error) {
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.RuntimeSetReviewerEnabledRequest]) (serverapi.RuntimeSetReviewerEnabledResponse, error) {
		return s.SetReviewerEnabledValidated(ctx, validated, servicecontract.AuthorizedSessionInActiveProject{})
	})
}

func (s *Service) SetReviewerEnabledValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeSetReviewerEnabledRequest], authorization servicecontract.AuthorizedSessionInActiveProject) (serverapi.RuntimeSetReviewerEnabledResponse, error) {
	req := validated.Value()
	sessionID, err := runtimeControlSessionID(req.SessionID, authorization)
	if err != nil {
		return serverapi.RuntimeSetReviewerEnabledResponse{}, err
	}
	memoReq := sessionBoolMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Enabled: req.Enabled}
	return memoizedChatSettingsMutation(s, ctx, strings.TrimSpace(req.ClientRequestID), memoReq.SessionID, memoReq, s.reviewers, sameSessionBoolMemoRequest, func(ctx context.Context) (serverapi.RuntimeSetReviewerEnabledResponse, bool, error) {
		mode := "off"
		result, accepted, err := s.mutateChatSettings(
			ctx,
			sessionID,
			func(ctx context.Context, store *session.Store, engine *runtime.Engine) (session.ChatSettingsMutation, error) {
				if req.Enabled {
					prepared, prepareErr := s.prepareChatSettings(ctx, store)
					if prepareErr != nil {
						return session.ChatSettingsMutation{}, prepareErr
					}
					state, stateErr := session.ChatSettingsStateFromMeta(store.Meta())
					if stateErr != nil {
						return session.ChatSettingsMutation{}, stateErr
					}
					current, resolveErr := session.ResolveEffectiveChatSettings(state.Settings, nil, prepared.Baseline)
					if resolveErr != nil {
						return session.ChatSettingsMutation{}, resolveErr
					}
					mode = current.Supervisor
					if mode == "off" {
						mode = prepared.Baseline.Supervisor
						if mode == "off" {
							mode = "edits"
						}
					}
				}
				if engine != nil {
					if _, prepareErr := engine.PrepareReviewerFrequency(mode); prepareErr != nil {
						return session.ChatSettingsMutation{}, prepareErr
					}
				}
				return session.ChatSettingsMutation{Supervisor: &mode}, nil
			},
			func(engine *runtime.Engine, result session.ChatSettingsMutationResult) error {
				_, _, _, applyErr := engine.SetReviewerFrequencyWithCommittedFeedback(mode, func(enabled bool, mode string, _ bool) string {
					return serverapi.ReviewerToggleStatusMessage(enabled, mode, result.Changed)
				})
				if applyErr != nil {
					engine.SetReviewerFrequency(mode)
				}
				return applyErr
			},
		)
		return serverapi.RuntimeSetReviewerEnabledResponse{Changed: result.Changed, Mode: mode}, accepted, err
	})
}

func (s *Service) SetAutoCompactionEnabled(ctx context.Context, req serverapi.RuntimeSetAutoCompactionEnabledRequest) (serverapi.RuntimeSetAutoCompactionEnabledResponse, error) {
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.RuntimeSetAutoCompactionEnabledRequest]) (serverapi.RuntimeSetAutoCompactionEnabledResponse, error) {
		return s.SetAutoCompactionEnabledValidated(ctx, validated, servicecontract.AuthorizedSessionInActiveProject{})
	})
}

func (s *Service) SetAutoCompactionEnabledValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeSetAutoCompactionEnabledRequest], authorization servicecontract.AuthorizedSessionInActiveProject) (serverapi.RuntimeSetAutoCompactionEnabledResponse, error) {
	req := validated.Value()
	sessionID, err := runtimeControlSessionID(req.SessionID, authorization)
	if err != nil {
		return serverapi.RuntimeSetAutoCompactionEnabledResponse{}, err
	}
	memoReq := sessionBoolMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Enabled: req.Enabled}
	return memoizedChatSettingsMutation(s, ctx, strings.TrimSpace(req.ClientRequestID), memoReq.SessionID, memoReq, s.autoCompacts, sameSessionBoolMemoRequest, func(ctx context.Context) (serverapi.RuntimeSetAutoCompactionEnabledResponse, bool, error) {
		result, accepted, mutationErr := s.mutateChatSettings(
			ctx,
			sessionID,
			func(ctx context.Context, _ *session.Store, engine *runtime.Engine) (session.ChatSettingsMutation, error) {
				if !req.Enabled {
					if err := s.rejectWorkflowAutoCompactionDisable(ctx, sessionID.String(), engine); err != nil {
						return session.ChatSettingsMutation{}, err
					}
				}
				return session.ChatSettingsMutation{AutoCompaction: &req.Enabled}, nil
			},
			func(engine *runtime.Engine, _ session.ChatSettingsMutationResult) error {
				engine.SetAutoCompactionEnabled(req.Enabled)
				return nil
			},
		)
		if !accepted {
			return serverapi.RuntimeSetAutoCompactionEnabledResponse{}, false, mutationErr
		}
		return serverapi.RuntimeSetAutoCompactionEnabledResponse{
			Changed: result.Changed,
			Enabled: req.Enabled,
		}, true, mutationErr
	})
}

func (s *Service) SetQuestionsEnabled(ctx context.Context, req serverapi.RuntimeSetQuestionsEnabledRequest) (serverapi.RuntimeSetQuestionsEnabledResponse, error) {
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.RuntimeSetQuestionsEnabledRequest]) (serverapi.RuntimeSetQuestionsEnabledResponse, error) {
		return s.SetQuestionsEnabledValidated(ctx, validated, servicecontract.AuthorizedSessionInActiveProject{})
	})
}

func (s *Service) SetQuestionsEnabledValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeSetQuestionsEnabledRequest], authorization servicecontract.AuthorizedSessionInActiveProject) (serverapi.RuntimeSetQuestionsEnabledResponse, error) {
	req := validated.Value()
	sessionID, err := runtimeControlSessionID(req.SessionID, authorization)
	if err != nil {
		return serverapi.RuntimeSetQuestionsEnabledResponse{}, err
	}
	memoReq := sessionBoolMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Enabled: req.Enabled}
	return memoizedChatSettingsMutation(s, ctx, strings.TrimSpace(req.ClientRequestID), memoReq.SessionID, memoReq, s.questions, sameSessionBoolMemoRequest, func(ctx context.Context) (serverapi.RuntimeSetQuestionsEnabledResponse, bool, error) {
		result, accepted, err := s.mutateChatSettings(
			ctx,
			sessionID,
			func(context.Context, *session.Store, *runtime.Engine) (session.ChatSettingsMutation, error) {
				return session.ChatSettingsMutation{Questions: &req.Enabled}, nil
			},
			func(engine *runtime.Engine, result session.ChatSettingsMutationResult) error {
				_, _, _, applyErr := engine.SetQuestionsEnabledWithCommittedFeedback(req.Enabled, func(enabled bool, _ bool) string {
					return serverapi.QuestionsToggleStatusMessage(enabled, result.Changed)
				})
				if applyErr != nil {
					engine.SetQuestionsEnabled(req.Enabled)
				}
				return applyErr
			},
		)
		return serverapi.RuntimeSetQuestionsEnabledResponse{
			Changed: result.Changed,
			Enabled: req.Enabled,
		}, accepted, err
	})
}

func (s *Service) prepareChatSettings(ctx context.Context, store *session.Store) (launch.PreparedChatSettings, error) {
	if s == nil || s.chatSettings == nil {
		return launch.PreparedChatSettings{}, errors.New("Session Chat settings preparation is unavailable")
	}
	state, err := session.ChatSettingsStateFromMeta(store.Meta())
	if err != nil {
		return launch.PreparedChatSettings{}, err
	}
	return s.chatSettings.PrepareSessionChatSettings(ctx, store, state.Agent)
}

func (s *Service) mutateChatSettings(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	prepare func(context.Context, *session.Store, *runtime.Engine) (session.ChatSettingsMutation, error),
	apply func(*runtime.Engine, session.ChatSettingsMutationResult) error,
) (result session.ChatSettingsMutationResult, accepted bool, resultErr error) {
	if s == nil || s.authority == nil {
		return result, false, errors.New("session runtime authority is required")
	}
	err := s.authority.WithSessionChatSettingsID(ctx, sessionID, func(
		runCtx context.Context,
		store *session.Store,
		engine *runtime.Engine,
	) error {
		mutation, err := prepare(runCtx, store, engine)
		if err != nil {
			return err
		}
		result, err = store.MutateChatSettings(mutation)
		if err != nil && !result.Committed {
			return err
		}
		accepted = true
		resultErr = err
		if engine != nil && apply != nil {
			resultErr = errors.Join(resultErr, apply(engine, result))
		}
		return nil
	})
	if !accepted {
		return result, false, err
	}
	return result, true, errors.Join(resultErr, err)
}

func memoizedChatSettingsMutation[Req any, Resp any](
	service *Service,
	ctx context.Context,
	requestID string,
	sessionID string,
	req Req,
	memo *requestmemo.Memo[Req, committedRuntimeMutationResult[Resp]],
	same func(Req, Req) bool,
	run func(context.Context) (Resp, bool, error),
) (Resp, error) {
	var zero Resp
	result, err := memo.Do(ctx, requestID, req, same, func(ctx context.Context) (committedRuntimeMutationResult[Resp], error) {
		response, accepted, mutationErr := run(ctx)
		if !accepted {
			return committedRuntimeMutationResult[Resp]{}, mutationErr
		}
		return committedRuntimeMutationResult[Resp]{
			Response: response,
			Err:      errors.Join(mutationErr, service.publishSessionStatus(sessionID)),
		}, nil
	})
	if err != nil {
		return zero, err
	}
	return result.Response, result.Err
}

func memoizedRuntimeCommand[Req any, Resp any](
	ctx context.Context,
	requestID string,
	req Req,
	memo *requestmemo.Memo[Req, committedRuntimeMutationResult[Resp]],
	same func(Req, Req) bool,
	run func(context.Context) (Resp, bool, error),
) (Resp, error) {
	var zero Resp
	result, err := memo.Do(ctx, requestID, req, same, func(ctx context.Context) (committedRuntimeMutationResult[Resp], error) {
		response, accepted, commandErr := run(ctx)
		if !accepted {
			return committedRuntimeMutationResult[Resp]{}, runtimeCommandNotAccepted(commandErr)
		}
		return committedRuntimeMutationResult[Resp]{Response: response, Err: commandErr}, nil
	})
	if err != nil {
		return zero, runtimeCommandNotAccepted(err)
	}
	return result.Response, result.Err
}

func runtimeCommandNotAccepted(cause error) error {
	if errors.Is(cause, serverapi.ErrRuntimeCommandNotAccepted) {
		return cause
	}
	if cause == nil {
		cause = errors.New("runtime command completed without accepting a mutation")
	}
	return serverapi.NewRuntimeCommandNotAcceptedError(cause)
}

func (s *Service) publishSessionStatus(sessionID string) error {
	if publisher, ok := s.activity.(sessionStatusPublisher); ok {
		return publisher.PublishSessionStatus(sessionID)
	}
	return nil
}

func (s *Service) AppendCommittedEntry(ctx context.Context, req serverapi.RuntimeAppendCommittedEntryRequest) error {
	_, err := servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.RuntimeAppendCommittedEntryRequest]) (struct{}, error) {
		return struct{}{}, s.AppendCommittedEntryValidated(ctx, validated, servicecontract.AuthorizedSessionInActiveProject{})
	})
	return err
}

func (s *Service) AppendCommittedEntryValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeAppendCommittedEntryRequest], authorization servicecontract.AuthorizedSessionInActiveProject) error {
	req := validated.Value()
	sessionID, err := runtimeControlSessionID(req.SessionID, authorization)
	if err != nil {
		return err
	}
	visibility := transcript.NormalizeEntryVisibility(transcript.EntryVisibility(req.Visibility))
	memoReq := localEntryMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Role: strings.TrimSpace(req.Role), Text: req.Text, Visibility: visibility, NoticeID: strings.TrimSpace(req.NoticeID)}
	_, err = s.localEntries.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameLocalEntryMemoRequest, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, s.withRuntimeID(ctx, sessionID, func(_ context.Context, engine *runtime.Engine) error {
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
	return s.withRuntime(ctx, trimmedSessionID, func(_ context.Context, engine *runtime.Engine) error {
		return engine.AppendCommittedEntry(trimmedRole, trimmedText)
	})
}

func (s *Service) ShouldCompactBeforeUserMessage(ctx context.Context, req serverapi.RuntimeShouldCompactBeforeUserMessageRequest) (serverapi.RuntimeShouldCompactBeforeUserMessageResponse, error) {
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.RuntimeShouldCompactBeforeUserMessageRequest]) (serverapi.RuntimeShouldCompactBeforeUserMessageResponse, error) {
		return s.ShouldCompactBeforeUserMessageValidated(ctx, validated, servicecontract.AuthorizedSessionInActiveProject{})
	})
}

func (s *Service) ShouldCompactBeforeUserMessageValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeShouldCompactBeforeUserMessageRequest], authorization servicecontract.AuthorizedSessionInActiveProject) (serverapi.RuntimeShouldCompactBeforeUserMessageResponse, error) {
	req := validated.Value()
	sessionID, err := runtimeControlSessionID(req.SessionID, authorization)
	if err != nil {
		return serverapi.RuntimeShouldCompactBeforeUserMessageResponse{}, err
	}
	var shouldCompact bool
	err = s.withRuntimeID(ctx, sessionID, func(callbackCtx context.Context, engine *runtime.Engine) error {
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
	_, err := servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.RuntimeSubmitUserShellCommandRequest]) (struct{}, error) {
		return struct{}{}, s.SubmitUserShellCommandValidated(ctx, validated, servicecontract.AuthorizedSessionInActiveProject{})
	})
	return err
}

func (s *Service) SubmitUserShellCommandValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeSubmitUserShellCommandRequest], authorization servicecontract.AuthorizedSessionInActiveProject) error {
	req := validated.Value()
	sessionID, err := runtimeControlSessionID(req.SessionID, authorization)
	if err != nil {
		return err
	}
	memoReq := sessionCommandMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Command: req.Command}
	_, err = memoizedRuntimeCommand(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, s.userShells, sameSessionCommandMemoRequest, func(ctx context.Context) (struct{}, bool, error) {
		attempt := newRuntimeCommandAttempt(ctx)
		defer attempt.Finish()
		commandErr := s.runAgentExecutionID(attempt.Context(), sessionID, func(runCtx context.Context, engine *runtime.Engine) error {
			_, err := engine.SubmitUserShellCommandWithAcceptance(runCtx, memoReq.Command, attempt.Accept)
			return err
		})
		return struct{}{}, attempt.Accepted(), commandErr
	})
	return err
}

func (s *Service) CompactContext(ctx context.Context, req serverapi.RuntimeCompactContextRequest) error {
	_, err := servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.RuntimeCompactContextRequest]) (struct{}, error) {
		return struct{}{}, s.CompactContextValidated(ctx, validated, servicecontract.AuthorizedSessionInActiveProject{})
	})
	return err
}

func (s *Service) CompactContextValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeCompactContextRequest], authorization servicecontract.AuthorizedSessionInActiveProject) error {
	req := validated.Value()
	sessionID, err := runtimeControlSessionID(req.SessionID, authorization)
	if err != nil {
		return err
	}
	memoReq := sessionStringMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Value: req.Args}
	_, err = memoizedRuntimeCommand(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, s.compactions, sameSessionStringMemoRequest, func(ctx context.Context) (struct{}, bool, error) {
		attempt := newRuntimeCommandAttempt(ctx)
		defer attempt.Finish()
		commandErr := s.withRuntimeID(attempt.Context(), sessionID, func(runCtx context.Context, engine *runtime.Engine) error {
			_, compactErr := engine.CompactContextWithAcceptance(runCtx, req.Args, attempt.Accept)
			return compactErr
		})
		return struct{}{}, attempt.Accepted(), commandErr
	})
	return err
}

func (s *Service) Interrupt(ctx context.Context, req serverapi.RuntimeInterruptRequest) (serverapi.RuntimeInterruptResponse, error) {
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.RuntimeInterruptRequest]) (serverapi.RuntimeInterruptResponse, error) {
		return s.InterruptValidated(ctx, validated, servicecontract.AuthorizedSessionInActiveProject{})
	})
}

func (s *Service) InterruptValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeInterruptRequest], authorization servicecontract.AuthorizedSessionInActiveProject) (serverapi.RuntimeInterruptResponse, error) {
	req := validated.Value()
	if s == nil || s.authority == nil {
		return serverapi.RuntimeInterruptResponse{}, errors.New("session runtime authority is required")
	}
	sessionID, err := runtimeControlSessionID(req.SessionID, authorization)
	if err != nil {
		return serverapi.RuntimeInterruptResponse{}, err
	}
	return s.interrupt(ctx, sessionID)
}

func (s *Service) interrupt(ctx context.Context, sessionID runtimeids.SessionID) (serverapi.RuntimeInterruptResponse, error) {
	err := s.authority.WithInterruptibleAgentTurn(ctx, sessionID, nil, func(_ context.Context, engine *runtime.Engine) error {
		interrupted, err := engine.TryInterruptActiveAgentTurn()
		if err != nil {
			return err
		}
		if !interrupted {
			return serverapi.NewRuntimeCommandNotAcceptedError(errors.New("no active Agent Turn"))
		}
		return nil
	})
	switch {
	case errors.Is(err, sessionruntime.ErrExecutionPromptPending):
		err = serverapi.NewRuntimeCommandNotAcceptedError(err)
	case errors.Is(err, sessionruntime.ErrExecutionNoLongerLive):
		err = serverapi.NewRuntimeCommandNotAcceptedError(errors.New("no active Agent Turn"))
	case errors.Is(err, serverapi.ErrRuntimeUnavailable):
		err = serverapi.NewRuntimeCommandNotAcceptedError(err)
	}
	if err != nil {
		return serverapi.RuntimeInterruptResponse{}, err
	}
	return s.runtimeInterruptResponse(ctx, sessionID.String())
}

func (s *Service) runtimeInterruptResponse(ctx context.Context, sessionID string) (serverapi.RuntimeInterruptResponse, error) {
	var snapshot runtimeactivity.ResponseSnapshot
	var err error
	if s.activity != nil {
		snapshot, err = s.activity.RuntimeReadModelSnapshot(ctx, sessionID)
	} else {
		err = errors.New("runtime activity resolver is unavailable")
	}
	if err != nil {
		return serverapi.RuntimeInterruptResponse{}, fmt.Errorf("project runtime activity after accepted interrupt for session %q: %w", sessionID, err)
	}
	return serverapi.RuntimeInterruptResponse{
		Version:  snapshot.Version,
		Activity: snapshot.Activity,
	}, nil
}

func (s *Service) DiscardQueuedUserMessage(ctx context.Context, req serverapi.RuntimeDiscardQueuedUserMessageRequest) (serverapi.RuntimeDiscardQueuedUserMessageResponse, error) {
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.RuntimeDiscardQueuedUserMessageRequest]) (serverapi.RuntimeDiscardQueuedUserMessageResponse, error) {
		return s.DiscardQueuedUserMessageValidated(ctx, validated, servicecontract.AuthorizedSessionInActiveProject{})
	})
}

func (s *Service) DiscardQueuedUserMessageValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeDiscardQueuedUserMessageRequest], authorization servicecontract.AuthorizedSessionInActiveProject) (serverapi.RuntimeDiscardQueuedUserMessageResponse, error) {
	req := validated.Value()
	sessionID, err := runtimeControlSessionID(req.SessionID, authorization)
	if err != nil {
		return serverapi.RuntimeDiscardQueuedUserMessageResponse{}, err
	}
	memoReq := queuedUserMessageMemoRequest{SessionID: strings.TrimSpace(req.SessionID), QueueItemID: strings.TrimSpace(req.QueueItemID)}
	return s.queuedDiscards.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameQueuedUserMessageMemoRequest, func(ctx context.Context) (serverapi.RuntimeDiscardQueuedUserMessageResponse, error) {
		var resp serverapi.RuntimeDiscardQueuedUserMessageResponse
		err := s.withRuntimeID(ctx, sessionID, func(_ context.Context, engine *runtime.Engine) error {
			resp.Discarded = engine.DiscardQueuedUserMessage(memoReq.QueueItemID)
			return nil
		})
		return resp, err
	})
}

func (s *Service) RecordPromptHistory(ctx context.Context, req serverapi.RuntimeRecordPromptHistoryRequest) error {
	_, err := servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.RuntimeRecordPromptHistoryRequest]) (struct{}, error) {
		return struct{}{}, s.RecordPromptHistoryValidated(ctx, validated, servicecontract.AuthorizedSessionInActiveProject{})
	})
	return err
}

func (s *Service) RecordPromptHistoryValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeRecordPromptHistoryRequest], authorization servicecontract.AuthorizedSessionInActiveProject) error {
	req := validated.Value()
	sessionID, err := runtimeControlSessionID(req.SessionID, authorization)
	if err != nil {
		return err
	}
	memoReq := sessionTextMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Text: req.Text}
	_, err = s.promptHistory.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameSessionTextMemoRequest, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, s.withRuntimeID(ctx, sessionID, func(_ context.Context, _ *runtime.Engine) error {
			_, _, err := s.recordPromptHistory(ctx, memoReq.SessionID, strings.TrimSpace(req.ClientRequestID), memoReq.Text)
			return err
		})
	})
	return err
}

func runtimeControlSessionID(raw string, authorization servicecontract.AuthorizedSessionInActiveProject) (runtimeids.SessionID, error) {
	if !authorization.SessionID.IsZero() {
		return authorization.SessionID, nil
	}
	return runtimeids.ParseSessionID(strings.TrimSpace(raw))
}

func (s *Service) withRuntimeID(ctx context.Context, sessionID runtimeids.SessionID, fn func(context.Context, *runtime.Engine) error) error {
	if s == nil || s.authority == nil {
		return errors.New("session runtime authority is required")
	}
	return s.authority.WithCurrentRuntime(ctx, sessionID, fn)
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

var (
	sameSessionTextMemoRequest       = sameComparable[sessionTextMemoRequest]
	sameLiveSteerMemoRequest         = sameComparable[liveSteerMemoRequest]
	sameLiveStopMemoRequest          = sameComparable[liveStopMemoRequest]
	sameQueuedUserMessageMemoRequest = sameComparable[queuedUserMessageMemoRequest]
	sameSessionStringMemoRequest     = sameComparable[sessionStringMemoRequest]
	sameSessionBoolMemoRequest       = sameComparable[sessionBoolMemoRequest]
	sameSessionCommandMemoRequest    = sameComparable[sessionCommandMemoRequest]
	sameLocalEntryMemoRequest        = sameComparable[localEntryMemoRequest]
	sameGoalSetMemoRequest           = sameComparable[goalSetMemoRequest]
	sameGoalStatusMemoRequest        = sameComparable[goalStatusMemoRequest]
	sameGoalClearMemoRequest         = sameComparable[goalClearMemoRequest]
)

func sameComparable[T comparable](a, b T) bool { return a == b }

var _ servicecontract.RuntimeControlService = (*Service)(nil)
