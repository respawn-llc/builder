package runtimecontrol

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/prompts"
	"core/server/metadata"
	"core/server/requestmemo"
	"core/server/runtime"
	"core/server/session"
	servicecontract "core/shared/apicontract"
	"core/shared/serverapi"
	"core/shared/transcript"
)

type RuntimeResolver interface {
	ResolveRuntime(ctx context.Context, sessionID string) (*runtime.Engine, error)
	WithGuardedRuntime(ctx context.Context, sessionID string, fn func(*runtime.Engine) error) (bool, error)
	BeginSessionRun(sessionID string) (func(), bool)
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
	promptStore    PromptHistoryStore
	workflowStates WorkflowSessionResolver
	sessionNames   *requestmemo.Memo[sessionStringMemoRequest, struct{}]
	thinkingLevels *requestmemo.Memo[sessionStringMemoRequest, struct{}]
	fastModes      *requestmemo.Memo[sessionBoolMemoRequest, serverapi.RuntimeSetFastModeEnabledResponse]
	reviewers      *requestmemo.Memo[sessionBoolMemoRequest, serverapi.RuntimeSetReviewerEnabledResponse]
	autoCompacts   *requestmemo.Memo[sessionBoolMemoRequest, serverapi.RuntimeSetAutoCompactionEnabledResponse]
	questions      *requestmemo.Memo[sessionBoolMemoRequest, serverapi.RuntimeSetQuestionsEnabledResponse]
	turnSubmits    *requestmemo.Memo[turnSubmitMemoRequest, serverapi.RuntimeSubmitUserTurnResponse]
	queues         *requestmemo.Memo[sessionTextMemoRequest, serverapi.RuntimeQueueUserMessageResponse]
	shells         *requestmemo.Memo[sessionCommandMemoRequest, struct{}]

	localEntries         *requestmemo.Memo[localEntryMemoRequest, struct{}]
	compactions          *requestmemo.Memo[sessionStringMemoRequest, struct{}]
	preSubmitCompactions *requestmemo.Memo[sessionOnlyMemoRequest, struct{}]
	queuedSubmits        *requestmemo.Memo[sessionOnlyMemoRequest, serverapi.RuntimeSubmitQueuedUserMessagesResponse]
	interrupts           *requestmemo.Memo[sessionOnlyMemoRequest, struct{}]
	queuedDiscards       *requestmemo.Memo[queuedUserMessageMemoRequest, serverapi.RuntimeDiscardQueuedUserMessageResponse]
	promptHistory        *requestmemo.Memo[sessionTextMemoRequest, struct{}]
	goals                *requestmemo.Memo[goalSetMemoRequest, serverapi.RuntimeGoalShowResponse]
	goalStatuses         *requestmemo.Memo[goalStatusMemoRequest, serverapi.RuntimeGoalShowResponse]
	goalClears           *requestmemo.Memo[goalClearMemoRequest, serverapi.RuntimeGoalShowResponse]
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

type turnSubmitMemoRequest struct {
	SessionID             string
	Text                  string
	PromptHistoryRecorded bool
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
}

type goalStatusMemoRequest struct {
	SessionID string
	Status    string
	Actor     string
}

type goalClearMemoRequest struct {
	SessionID string
	Actor     string
}

func NewService(runtimes RuntimeResolver) *Service {
	return &Service{
		runtimes:       runtimes,
		sessionNames:   requestmemo.New[sessionStringMemoRequest, struct{}](),
		thinkingLevels: requestmemo.New[sessionStringMemoRequest, struct{}](),
		fastModes:      requestmemo.New[sessionBoolMemoRequest, serverapi.RuntimeSetFastModeEnabledResponse](),
		reviewers:      requestmemo.New[sessionBoolMemoRequest, serverapi.RuntimeSetReviewerEnabledResponse](),
		autoCompacts:   requestmemo.New[sessionBoolMemoRequest, serverapi.RuntimeSetAutoCompactionEnabledResponse](),
		questions:      requestmemo.New[sessionBoolMemoRequest, serverapi.RuntimeSetQuestionsEnabledResponse](),
		turnSubmits:    requestmemo.New[turnSubmitMemoRequest, serverapi.RuntimeSubmitUserTurnResponse](),
		queues:         requestmemo.New[sessionTextMemoRequest, serverapi.RuntimeQueueUserMessageResponse](),
		shells:         requestmemo.New[sessionCommandMemoRequest, struct{}](),

		localEntries:         requestmemo.New[localEntryMemoRequest, struct{}](),
		compactions:          requestmemo.New[sessionStringMemoRequest, struct{}](),
		preSubmitCompactions: requestmemo.New[sessionOnlyMemoRequest, struct{}](),
		queuedSubmits:        requestmemo.New[sessionOnlyMemoRequest, serverapi.RuntimeSubmitQueuedUserMessagesResponse](),
		interrupts:           requestmemo.New[sessionOnlyMemoRequest, struct{}](),
		queuedDiscards:       requestmemo.New[queuedUserMessageMemoRequest, serverapi.RuntimeDiscardQueuedUserMessageResponse](),
		promptHistory:        requestmemo.New[sessionTextMemoRequest, struct{}](),
		goals:                requestmemo.New[goalSetMemoRequest, serverapi.RuntimeGoalShowResponse](),
		goalStatuses:         requestmemo.New[goalStatusMemoRequest, serverapi.RuntimeGoalShowResponse](),
		goalClears:           requestmemo.New[goalClearMemoRequest, serverapi.RuntimeGoalShowResponse](),
	}
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
	if !acquired {
		return errors.Join(serverapi.ErrRuntimeUnavailable, fmt.Errorf("runtime for session %q is unavailable", trimmedSessionID))
	}
	return err
}

func (s *Service) beginRunStart(sessionID string) (func(), error) {
	if s == nil || s.runtimes == nil {
		return func() {}, nil
	}
	release, ok := s.runtimes.BeginSessionRun(strings.TrimSpace(sessionID))
	if !ok {
		return nil, serverapi.ErrSessionWorktreeDeleting
	}
	return release, nil
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
			return engine.SetSessionName(req.Name)
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
		engine, err := s.resolve(ctx, req.SessionID)
		if err != nil {
			return struct{}{}, err
		}
		if visibility == transcript.EntryVisibilityAuto && strings.TrimSpace(req.NoticeID) != "" {
			return struct{}{}, engine.AppendCommittedEntryWithNoticeID(req.Role, req.Text, req.NoticeID)
		}
		if visibility == transcript.EntryVisibilityAuto {
			return struct{}{}, engine.AppendCommittedEntry(req.Role, req.Text)
		}
		return struct{}{}, engine.AppendCommittedEntryWithVisibility(req.Role, req.Text, visibility)
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
	engine, err := s.resolve(ctx, trimmedSessionID)
	if err != nil {
		return err
	}
	return engine.AppendCommittedEntry(trimmedRole, trimmedText)
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
	release, err := s.beginRunStart(req.SessionID)
	if err != nil {
		return err
	}
	defer release()
	memoReq := sessionCommandMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Command: req.Command}
	_, err = s.shells.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameSessionCommandMemoRequest, func(ctx context.Context) (struct{}, error) {
		runCtx := context.Background()
		if ctx != nil {
			runCtx = context.WithoutCancel(ctx)
		}
		return struct{}{}, s.withRuntimeAccess(ctx, req.SessionID, func(engine *runtime.Engine) error {
			_, err := engine.SubmitUserShellCommand(runCtx, memoReq.Command)
			return err
		})
	})
	return err
}

func (s *Service) CompactContext(ctx context.Context, req serverapi.RuntimeCompactContextRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	memoReq := sessionStringMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Value: req.Args}
	_, err := s.compactions.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameSessionStringMemoRequest, func(ctx context.Context) (struct{}, error) {
		runCtx := context.Background()
		if ctx != nil {
			runCtx = context.WithoutCancel(ctx)
		}
		return struct{}{}, s.withRuntimeAccess(ctx, req.SessionID, func(engine *runtime.Engine) error {
			return engine.CompactContext(runCtx, req.Args)
		})
	})
	return err
}

func (s *Service) CompactContextForPreSubmit(ctx context.Context, req serverapi.RuntimeCompactContextForPreSubmitRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	memoReq := sessionOnlyMemoRequest{SessionID: strings.TrimSpace(req.SessionID)}
	_, err := s.preSubmitCompactions.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, func(a sessionOnlyMemoRequest, b sessionOnlyMemoRequest) bool { return a.SessionID == b.SessionID }, func(ctx context.Context) (struct{}, error) {
		runCtx := context.Background()
		if ctx != nil {
			runCtx = context.WithoutCancel(ctx)
		}
		return struct{}{}, s.withRuntimeAccess(ctx, req.SessionID, func(engine *runtime.Engine) error {
			return engine.CompactContextForPreSubmit(runCtx)
		})
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
	release, err := s.beginRunStart(req.SessionID)
	if err != nil {
		return serverapi.RuntimeSubmitQueuedUserMessagesResponse{}, err
	}
	defer release()
	memoReq := sessionOnlyMemoRequest{SessionID: strings.TrimSpace(req.SessionID)}
	return s.queuedSubmits.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, func(a sessionOnlyMemoRequest, b sessionOnlyMemoRequest) bool { return a.SessionID == b.SessionID }, func(ctx context.Context) (serverapi.RuntimeSubmitQueuedUserMessagesResponse, error) {
		runCtx := context.Background()
		if ctx != nil {
			runCtx = context.WithoutCancel(ctx)
		}
		var resp serverapi.RuntimeSubmitQueuedUserMessagesResponse
		err := s.withRuntimeAccess(ctx, req.SessionID, func(engine *runtime.Engine) error {
			msg, err := engine.SubmitQueuedUserMessages(runCtx)
			resp = serverapi.RuntimeSubmitQueuedUserMessagesResponse{Message: msg.Content}
			return err
		})
		return resp, err
	})
}

func (s *Service) Interrupt(ctx context.Context, req serverapi.RuntimeInterruptRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	memoReq := sessionOnlyMemoRequest{SessionID: strings.TrimSpace(req.SessionID)}
	_, err := s.interrupts.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, func(a sessionOnlyMemoRequest, b sessionOnlyMemoRequest) bool { return a.SessionID == b.SessionID }, func(ctx context.Context) (struct{}, error) {
		engine, err := s.resolve(ctx, req.SessionID)
		if err != nil {
			return struct{}{}, err
		}
		return struct{}{}, engine.Interrupt()
	})
	return err
}

func (s *Service) QueueUserMessage(ctx context.Context, req serverapi.RuntimeQueueUserMessageRequest) (serverapi.RuntimeQueueUserMessageResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeQueueUserMessageResponse{}, err
	}
	memoReq := sessionTextMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Text: req.Text}
	return s.queues.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameSessionTextMemoRequest, func(ctx context.Context) (serverapi.RuntimeQueueUserMessageResponse, error) {
		var resp serverapi.RuntimeQueueUserMessageResponse
		err := s.withRuntimeAccess(ctx, req.SessionID, func(engine *runtime.Engine) error {
			text := memoReq.Text
			if s != nil && s.promptStore != nil {
				record, _, err := s.recordPromptHistory(ctx, memoReq.SessionID, strings.TrimSpace(req.ClientRequestID), memoReq.Text)
				if err != nil {
					return err
				}
				text = record.Text
			}
			item := engine.QueueUserMessageWithClientRequestID(text, strings.TrimSpace(req.ClientRequestID))
			resp = serverapi.RuntimeQueueUserMessageResponse{QueueItemID: item.ID, Text: item.Text, ClientRequestID: item.ClientRequestID}
			return nil
		})
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

func (s *Service) ShowGoal(ctx context.Context, req serverapi.RuntimeGoalShowRequest) (serverapi.RuntimeGoalShowResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	engine, err := s.resolve(ctx, req.SessionID)
	if err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	goal := engine.Goal()
	if goal == nil {
		return serverapi.RuntimeGoalShowResponse{}, nil
	}
	return serverapi.RuntimeGoalShowResponse{Goal: &serverapi.RuntimeGoal{
		ID:        strings.TrimSpace(goal.ID),
		Objective: goal.Objective,
		Status:    strings.TrimSpace(string(goal.Status)),
		Suspended: engine.GoalLoopSuspended(),
		CreatedAt: goal.CreatedAt,
		UpdatedAt: goal.UpdatedAt,
	}}, nil
}

func (s *Service) SetGoal(ctx context.Context, req serverapi.RuntimeGoalSetRequest) (serverapi.RuntimeGoalShowResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	trimmedObjective := strings.TrimSpace(req.Objective)
	memoReq := goalSetMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Objective: trimmedObjective, Actor: strings.TrimSpace(req.Actor)}
	return s.goals.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameGoalSetMemoRequest, func(ctx context.Context) (serverapi.RuntimeGoalShowResponse, error) {
		var response serverapi.RuntimeGoalShowResponse
		err := s.withGoalMutationAccess(ctx, req.SessionID, func(engine *runtime.Engine) error {
			if strings.TrimSpace(req.Actor) == string(session.GoalActorAgent) {
				currentGoal := engine.Goal()
				if goalBlocksAgentSet(currentGoal) {
					return goalAgentOverwriteDeniedError{Objective: currentGoal.Objective, Status: string(currentGoal.Status)}
				}
				goal, queued, qErr := engine.QueueAgentShellSetGoal(trimmedObjective, session.GoalActor(req.Actor))
				if qErr != nil {
					return qErr
				}
				if queued {
					response = serverapi.RuntimeGoalShowResponse{Goal: runtimeGoalFromSessionGoal(goal, false)}
					return nil
				}
			}
			if err := engine.RequireGoalLoopStartAllowed(); err != nil {
				return err
			}
			goal, err := engine.SetGoal(trimmedObjective, session.GoalActor(req.Actor))
			if err != nil {
				var blocked session.GoalAgentOverwriteBlockedError
				if errors.As(err, &blocked) {
					return goalAgentOverwriteDeniedError{Objective: blocked.Goal.Objective, Status: string(blocked.Goal.Status)}
				}
				return err
			}
			if err := engine.StartGoalLoop(); err != nil {
				return err
			}
			response = serverapi.RuntimeGoalShowResponse{Goal: runtimeGoalFromSessionGoal(goal, false)}
			return nil
		})
		return response, err
	})
}

func goalBlocksAgentSet(goal *session.GoalState) bool {
	return goal != nil && goal.Status != session.GoalStatusComplete
}

// goalAgentOverwriteDeniedError is returned when an agent attempts to overwrite an
// existing active or paused goal. It carries the existing goal's objective and status
// so callers can react to the specific denial, and renders the agent-facing denial
// prompt for the surfaced error message.
type goalAgentOverwriteDeniedError struct {
	Objective string
	Status    string
}

func (e goalAgentOverwriteDeniedError) Error() string {
	return strings.TrimSpace(prompts.RenderGoalAgentDuplicateSetDeniedPrompt(e.Objective, e.Status))
}

func (s *Service) PauseGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return s.setGoalStatus(ctx, req, session.GoalStatusPaused)
}

func (s *Service) ResumeGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return s.setGoalStatus(ctx, req, session.GoalStatusActive)
}

func (s *Service) CompleteGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return s.setGoalStatus(ctx, req, session.GoalStatusComplete)
}

func (s *Service) setGoalStatus(ctx context.Context, req serverapi.RuntimeGoalStatusRequest, status session.GoalStatus) (serverapi.RuntimeGoalShowResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	memoReq := goalStatusMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Status: strings.TrimSpace(string(status)), Actor: strings.TrimSpace(req.Actor)}
	return s.goalStatuses.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameGoalStatusMemoRequest, func(ctx context.Context) (serverapi.RuntimeGoalShowResponse, error) {
		var response serverapi.RuntimeGoalShowResponse
		err := s.withGoalMutationAccess(ctx, req.SessionID, func(engine *runtime.Engine) error {
			if status == session.GoalStatusComplete {
				current := engine.Goal()
				if current != nil && current.Status == session.GoalStatusComplete {
					response = serverapi.RuntimeGoalShowResponse{Goal: runtimeGoalFromSessionGoal(*current, false)}
					return nil
				}
				if current != nil && strings.TrimSpace(req.Actor) == string(session.GoalActorAgent) {
					goal, queued, qErr := engine.QueueAgentShellCompleteGoal(session.GoalActor(req.Actor))
					if qErr != nil {
						return qErr
					}
					if queued {
						response = serverapi.RuntimeGoalShowResponse{Goal: runtimeGoalFromSessionGoal(goal, false)}
						return nil
					}
				}
			}
			if status == session.GoalStatusActive {
				if err := engine.RequireGoalLoopStartAllowed(); err != nil {
					return err
				}
			}
			goal, err := engine.SetGoalStatus(status, session.GoalActor(req.Actor))
			if err != nil {
				return err
			}
			if status == session.GoalStatusActive {
				if err := engine.StartGoalLoop(); err != nil {
					return err
				}
			}
			response = serverapi.RuntimeGoalShowResponse{Goal: runtimeGoalFromSessionGoal(goal, false)}
			return nil
		})
		return response, err
	})
}

func (s *Service) ClearGoal(ctx context.Context, req serverapi.RuntimeGoalClearRequest) (serverapi.RuntimeGoalShowResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	memoReq := goalClearMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Actor: strings.TrimSpace(req.Actor)}
	return s.goalClears.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameGoalClearMemoRequest, func(ctx context.Context) (serverapi.RuntimeGoalShowResponse, error) {
		err := s.withGoalMutationAccess(ctx, req.SessionID, func(engine *runtime.Engine) error {
			_, err := engine.ClearGoal(session.GoalActor(req.Actor))
			return err
		})
		return serverapi.RuntimeGoalShowResponse{}, err
	})
}

func (s *Service) withGoalMutationAccess(ctx context.Context, sessionID string, fn func(*runtime.Engine) error) error {
	return s.withRuntimeAccess(ctx, sessionID, fn)
}

func runtimeGoalFromSessionGoal(goal session.GoalState, suspended bool) *serverapi.RuntimeGoal {
	return &serverapi.RuntimeGoal{
		ID:        strings.TrimSpace(goal.ID),
		Objective: goal.Objective,
		Status:    strings.TrimSpace(string(goal.Status)),
		Suspended: suspended,
		CreatedAt: goal.CreatedAt,
		UpdatedAt: goal.UpdatedAt,
	}
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

func sameTurnSubmitMemoRequest(a turnSubmitMemoRequest, b turnSubmitMemoRequest) bool {
	return a.SessionID == b.SessionID && a.Text == b.Text && a.PromptHistoryRecorded == b.PromptHistoryRecorded
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
	return a.SessionID == b.SessionID && a.Objective == b.Objective && a.Actor == b.Actor
}

func sameGoalStatusMemoRequest(a goalStatusMemoRequest, b goalStatusMemoRequest) bool {
	return a.SessionID == b.SessionID && a.Status == b.Status && a.Actor == b.Actor
}

func sameGoalClearMemoRequest(a goalClearMemoRequest, b goalClearMemoRequest) bool {
	return a.SessionID == b.SessionID && a.Actor == b.Actor
}

var _ servicecontract.RuntimeControlService = (*Service)(nil)
