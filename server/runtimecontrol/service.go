package runtimecontrol

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"core/server/metadata"
	"core/server/requestmemo"
	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/runtimecommand"
	"core/server/session"
	"core/server/sessionruntime"
	servicecontract "core/shared/apicontract"
	"core/shared/clientui"
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
	askViews             servicecontract.AskViewService
	approvalViews        servicecontract.ApprovalViewService
	attention            servicecontract.AttentionNotificationService
	sessionNames         *requestmemo.Memo[sessionStringMemoRequest, struct{}]
	thinkingLevels       *requestmemo.Memo[sessionStringMemoRequest, struct{}]
	fastModes            *requestmemo.Memo[sessionBoolMemoRequest, committedRuntimeMutationResult[serverapi.RuntimeSetFastModeEnabledResponse]]
	reviewers            *requestmemo.Memo[sessionBoolMemoRequest, committedRuntimeMutationResult[serverapi.RuntimeSetReviewerEnabledResponse]]
	autoCompacts         *requestmemo.Memo[sessionBoolMemoRequest, serverapi.RuntimeSetAutoCompactionEnabledResponse]
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
	Response serverapi.RuntimeGoalShowResponse
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

type runtimeInterruptMemoRequest struct {
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
		thinkingLevels:       requestmemo.New[sessionStringMemoRequest, struct{}](),
		fastModes:            requestmemo.New[sessionBoolMemoRequest, committedRuntimeMutationResult[serverapi.RuntimeSetFastModeEnabledResponse]](),
		reviewers:            requestmemo.New[sessionBoolMemoRequest, committedRuntimeMutationResult[serverapi.RuntimeSetReviewerEnabledResponse]](),
		autoCompacts:         requestmemo.New[sessionBoolMemoRequest, serverapi.RuntimeSetAutoCompactionEnabledResponse](),
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
	if err := req.Validate(); err != nil {
		return err
	}
	memoReq := sessionStringMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Value: req.Name}
	_, err := s.sessionNames.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameSessionStringMemoRequest, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, s.withRuntime(ctx, req.SessionID, func(_ context.Context, engine *runtime.Engine) error {
			if err := engine.SetSessionName(req.Name); err != nil {
				return err
			}
			if publisher, ok := s.activity.(sessionIdentityPublisher); ok {
				return publisher.PublishSessionIdentity(req.SessionID)
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
		return struct{}{}, s.withRuntime(ctx, req.SessionID, func(_ context.Context, engine *runtime.Engine) error {
			if err := engine.SetThinkingLevel(req.Level); err != nil {
				return err
			}
			return s.publishSessionStatus(req.SessionID)
		})
	})
	return err
}

func (s *Service) SetFastModeEnabled(ctx context.Context, req serverapi.RuntimeSetFastModeEnabledRequest) (serverapi.RuntimeSetFastModeEnabledResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeSetFastModeEnabledResponse{}, err
	}
	memoReq := sessionBoolMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Enabled: req.Enabled}
	return memoizedCommittedRuntimeMutation(s, ctx, strings.TrimSpace(req.ClientRequestID), memoReq.SessionID, memoReq, s.fastModes, sameSessionBoolMemoRequest, func(engine *runtime.Engine) (serverapi.RuntimeSetFastModeEnabledResponse, session.CommitReceipt, error) {
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
	memoReq := sessionBoolMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Enabled: req.Enabled}
	return memoizedCommittedRuntimeMutation(s, ctx, strings.TrimSpace(req.ClientRequestID), memoReq.SessionID, memoReq, s.reviewers, sameSessionBoolMemoRequest, func(engine *runtime.Engine) (serverapi.RuntimeSetReviewerEnabledResponse, session.CommitReceipt, error) {
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
	memoReq := sessionBoolMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Enabled: req.Enabled}
	return s.autoCompacts.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameSessionBoolMemoRequest, func(ctx context.Context) (serverapi.RuntimeSetAutoCompactionEnabledResponse, error) {
		var resp serverapi.RuntimeSetAutoCompactionEnabledResponse
		err := s.withRuntime(ctx, req.SessionID, func(_ context.Context, engine *runtime.Engine) error {
			if !req.Enabled {
				if err := s.rejectWorkflowAutoCompactionDisable(ctx, req.SessionID, engine); err != nil {
					return err
				}
			}
			changed, enabled := engine.SetAutoCompactionEnabled(req.Enabled)
			resp = serverapi.RuntimeSetAutoCompactionEnabledResponse{Changed: changed, Enabled: enabled}
			return s.publishSessionStatus(req.SessionID)
		})
		return resp, err
	})
}

func (s *Service) SetQuestionsEnabled(ctx context.Context, req serverapi.RuntimeSetQuestionsEnabledRequest) (serverapi.RuntimeSetQuestionsEnabledResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeSetQuestionsEnabledResponse{}, err
	}
	memoReq := sessionBoolMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Enabled: req.Enabled}
	return memoizedCommittedRuntimeMutation(s, ctx, strings.TrimSpace(req.ClientRequestID), memoReq.SessionID, memoReq, s.questions, sameSessionBoolMemoRequest, func(engine *runtime.Engine) (serverapi.RuntimeSetQuestionsEnabledResponse, session.CommitReceipt, error) {
		changed, enabled, receipt, err := engine.SetQuestionsEnabledWithCommittedFeedback(req.Enabled, func(enabled bool, changed bool) string {
			return serverapi.QuestionsToggleStatusMessage(enabled, changed)
		})
		return serverapi.RuntimeSetQuestionsEnabledResponse{Changed: changed, Enabled: enabled}, receipt, err
	})
}

func memoizedCommittedRuntimeMutation[Req any, Resp any](
	service *Service,
	ctx context.Context,
	requestID string,
	sessionID string,
	req Req,
	memo *requestmemo.Memo[Req, committedRuntimeMutationResult[Resp]],
	same func(Req, Req) bool,
	run func(*runtime.Engine) (Resp, session.CommitReceipt, error),
) (Resp, error) {
	var zero Resp
	result, err := memo.Do(ctx, requestID, req, same, func(ctx context.Context) (committedRuntimeMutationResult[Resp], error) {
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
		return result, err
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
	if err := req.Validate(); err != nil {
		return err
	}
	visibility := transcript.NormalizeEntryVisibility(transcript.EntryVisibility(req.Visibility))
	memoReq := localEntryMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Role: strings.TrimSpace(req.Role), Text: req.Text, Visibility: visibility, NoticeID: strings.TrimSpace(req.NoticeID)}
	_, err := s.localEntries.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameLocalEntryMemoRequest, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, s.withRuntime(ctx, req.SessionID, func(_ context.Context, engine *runtime.Engine) error {
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
	memoReq := sessionCommandMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Command: req.Command}
	_, err := memoizedRuntimeCommand(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, s.userShells, sameSessionCommandMemoRequest, func(ctx context.Context) (struct{}, bool, error) {
		attempt := newRuntimeCommandAttempt(ctx)
		defer attempt.Finish()
		commandErr := s.runAgentExecution(attempt.Context(), req.SessionID, func(runCtx context.Context, engine *runtime.Engine) error {
			_, err := engine.SubmitUserShellCommandWithAcceptance(runCtx, memoReq.Command, attempt.Accept)
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
	memoReq := sessionStringMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Value: req.Args}
	_, err := memoizedRuntimeCommand(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, s.compactions, sameSessionStringMemoRequest, func(ctx context.Context) (struct{}, bool, error) {
		attempt := newRuntimeCommandAttempt(ctx)
		defer attempt.Finish()
		commandErr := s.runAgentExecution(attempt.Context(), req.SessionID, func(runCtx context.Context, engine *runtime.Engine) error {
			_, compactErr := engine.CompactContextWithAcceptance(runCtx, req.Args, attempt.Accept)
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
	return s.interrupt(ctx, runtimeInterruptMemoRequest{SessionID: sessionID})
}

func (s *Service) interrupt(ctx context.Context, req runtimeInterruptMemoRequest) (serverapi.RuntimeInterruptResponse, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	id, err := runtimeids.ParseSessionID(sessionID)
	if err != nil {
		return serverapi.RuntimeInterruptResponse{}, err
	}
	err = s.authority.WithInterruptibleAgentTurn(ctx, id, nil, func(_ context.Context, engine *runtime.Engine) error {
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
	return s.runtimeInterruptResponse(ctx, sessionID)
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
		slog.WarnContext(ctx, "runtime interrupt activity snapshot unavailable", "session_id", sessionID, "error", err)
		version := runtimeactivity.NextReadModelVersion(sessionID)
		return serverapi.RuntimeInterruptResponse{
			Version:  version,
			Activity: clientui.RuntimeActivity{State: clientui.RuntimeActivityUnavailable, DiagnosticRecovery: true},
		}, nil
	}
	return serverapi.RuntimeInterruptResponse{
		Version:  snapshot.Version,
		Activity: snapshot.Activity,
	}, nil
}

func (s *Service) DiscardQueuedUserMessage(ctx context.Context, req serverapi.RuntimeDiscardQueuedUserMessageRequest) (serverapi.RuntimeDiscardQueuedUserMessageResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeDiscardQueuedUserMessageResponse{}, err
	}
	memoReq := queuedUserMessageMemoRequest{SessionID: strings.TrimSpace(req.SessionID), QueueItemID: strings.TrimSpace(req.QueueItemID)}
	return s.queuedDiscards.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameQueuedUserMessageMemoRequest, func(ctx context.Context) (serverapi.RuntimeDiscardQueuedUserMessageResponse, error) {
		var resp serverapi.RuntimeDiscardQueuedUserMessageResponse
		err := s.withRuntime(ctx, req.SessionID, func(_ context.Context, engine *runtime.Engine) error {
			resp.Discarded = engine.DiscardQueuedUserMessage(memoReq.QueueItemID)
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
		return struct{}{}, s.withRuntime(ctx, req.SessionID, func(_ context.Context, _ *runtime.Engine) error {
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
