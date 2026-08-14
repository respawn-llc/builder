package sessionlaunch

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"core/server/auth"
	"core/server/chatcontext"
	"core/server/launch"
	"core/server/llm"
	"core/server/requestmemo"
	"core/server/runtimeview"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/subagentpolicy"
	servicecontract "core/shared/apicontract"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"
)

type authStateReader interface {
	Load(context.Context) (auth.State, error)
	CurrentState(context.Context) (auth.State, error)
	StoredState(context.Context) (auth.State, error)
}

type promptHistoryReader interface {
	ReadPromptHistory(ctx context.Context, sessionID string) ([]string, error)
}

type Service struct {
	planner                     launch.Planner
	authStates                  authStateReader
	promptHistory               promptHistoryReader
	runtime                     *sessionruntime.Authority
	plans                       *requestmemo.Memo[sessionPlanMemoRequest, PlanResult]
	workspaceID                 string
	draftOwner                  *WorkspaceChatDraftOwner
	materializationStoreOptions []session.StoreOption
}

var ErrExistingSessionAuthorityRequired = errors.New("session runtime authority is required for existing-session planning")

type PlanResult struct {
	Plan     launch.SessionPlan
	Warnings []string
}

type sessionPlanMemoRequest struct {
	Mode            serverapi.SessionLaunchMode
	Intent          serverapi.SessionLaunchIntent
	CallerSessionID serverapi.OptionalStringKey
	Overrides       serverapi.RunPromptOverridesKey
}

func NewService(planner launch.Planner) *Service {
	return &Service{planner: planner, plans: requestmemo.New[sessionPlanMemoRequest, PlanResult]()}
}

func (s *Service) ReadWorkspaceChatContext(ctx context.Context) (serverapi.ChatContext, error) {
	resolution, err := s.ResolveWorkspaceChatDraftAggregate(ctx)
	if err != nil {
		return serverapi.ChatContext{}, err
	}
	selected, ok := resolution.limits[normalizeWorkspaceChatDraftAgent(resolution.Draft.Agent)]
	if !ok {
		return serverapi.ChatContext{}, fmt.Errorf("workspace Chat draft Agent %q has no resolved settings", resolution.Draft.Agent)
	}
	provider, err := llm.ResolveEffectiveProviderCapabilities(ctx, nil, selected.settings, s.authStates)
	if err != nil {
		return serverapi.ChatContext{}, err
	}
	policy := chatcontext.ResolvePolicy(selected.settings, provider.Capabilities, nil)
	return chatcontext.Project(chatcontext.ProjectionInput{
		Policy:                policy,
		AutoCompactionEnabled: resolution.Draft.AutoCompaction,
	}), nil
}
func (s *Service) WithAuthStateReader(reader authStateReader) *Service {
	if s == nil {
		return nil
	}
	s.authStates = reader
	return s
}

func (s *Service) WithPromptHistoryReader(reader promptHistoryReader) *Service {
	if s == nil {
		return nil
	}
	s.promptHistory = reader
	return s
}

func (s *Service) WithRuntimeAuthority(authority *sessionruntime.Authority) *Service {
	if s == nil {
		return nil
	}
	s.runtime = authority
	return s
}

func (s *Service) WithWorkspaceChatDraft(owner *WorkspaceChatDraftOwner, workspaceID string) *Service {
	if s != nil {
		s.workspaceID = strings.TrimSpace(workspaceID)
		s.draftOwner = owner
	}
	return s
}

func (s *Service) WithWorkspaceChatMaterializationStoreOptions(options ...session.StoreOption) *Service {
	if s != nil {
		s.materializationStoreOptions = append([]session.StoreOption(nil), options...)
	}
	return s
}

func (s *Service) materializeWorkspaceChatSession(ctx context.Context) (runtimeids.SessionID, error) {
	owner, workspaceID, err := s.workspaceChatDraftOwner()
	if err != nil {
		return runtimeids.SessionID{}, err
	}
	return owner.MaterializeWorkspaceChat(
		ctx,
		workspaceID,
		s.workspaceChatMaterializationResolverInput,
		s.materializeResolvedWorkspaceChat,
	)
}

func (s *Service) workspaceChatMaterializationResolverInput(ctx context.Context) (WorkspaceChatDraftResolverInput, error) {
	input, err := s.workspaceChatDraftResolverInput(ctx)
	if err != nil {
		return WorkspaceChatDraftResolverInput{}, err
	}
	input.SkipProviderReadinessValidation = true
	return input, nil
}

func (s *Service) MaterializeWorkspaceChat(
	ctx context.Context,
	req serverapi.WorkspaceChatMaterializeRequest,
) (serverapi.WorkspaceChatMaterializeResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkspaceChatMaterializeResponse{}, err
	}
	sessionID, err := s.materializeWorkspaceChatSession(ctx)
	if err != nil {
		return serverapi.WorkspaceChatMaterializeResponse{}, err
	}
	response := serverapi.WorkspaceChatMaterializeResponse{SessionID: sessionID}
	if err := response.Validate(); err != nil {
		return serverapi.WorkspaceChatMaterializeResponse{}, err
	}
	return response, nil
}

func (s *Service) materializeResolvedWorkspaceChat(
	ctx context.Context,
	resolution WorkspaceChatDraftResolution,
) (runtimeids.SessionID, error) {
	if s == nil {
		return runtimeids.SessionID{}, errors.New("Session launch service is required")
	}
	if len(s.materializationStoreOptions) == 0 {
		return runtimeids.SessionID{}, errors.New("workspace Chat materialization persistence is required")
	}
	containerDir := strings.TrimSpace(s.planner.ContainerDir)
	if containerDir == "" {
		return runtimeids.SessionID{}, errors.New("Session container directory is required")
	}
	workspaceRoot := strings.TrimSpace(s.planner.Config.WorkspaceRoot)
	if workspaceRoot == "" {
		return runtimeids.SessionID{}, errors.New("workspace root is required")
	}
	store, err := session.NewLazy(
		containerDir,
		filepath.Base(containerDir),
		workspaceRoot,
		sessioncontract.SessionCategoryMain,
		s.materializationStoreOptions...,
	)
	if err != nil {
		return runtimeids.SessionID{}, err
	}
	draft := resolution.Draft
	if err := session.InitializeChatDraft(store, session.ChatDraftState{
		Message: draft.Message,
		Agent:   draft.Agent,
		Settings: &session.ChatSettingsOverrides{
			Supervisor:     &draft.Supervisor,
			Thinking:       &draft.Thinking,
			Fast:           &draft.Fast,
			Questions:      &draft.Questions,
			AutoCompaction: &draft.AutoCompaction,
		},
	}); err != nil {
		return runtimeids.SessionID{}, err
	}
	if err := ctx.Err(); err != nil {
		return runtimeids.SessionID{}, err
	}
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		return runtimeids.SessionID{}, fmt.Errorf("materialized Session id %q is invalid: %w", store.Meta().SessionID, err)
	}
	if !sessionID.IsCanonicalUUIDv4() {
		return runtimeids.SessionID{}, fmt.Errorf("materialized Session id %q must be a canonical UUIDv4", sessionID.String())
	}
	if err := store.EnsureDurable(); err != nil {
		return runtimeids.SessionID{}, errors.Join(err, store.RemoveDurable())
	}
	return sessionID, nil
}

func (s *Service) workspaceChatDraftResolverInput(ctx context.Context) (WorkspaceChatDraftResolverInput, error) {
	planner := s.planner
	if planner.ReloadConfig != nil {
		snapshot, err := planner.ReloadConfig()
		if err != nil {
			return WorkspaceChatDraftResolverInput{}, err
		}
		planner.Config = snapshot
	}
	authState := auth.EmptyState()
	if s.authStates != nil {
		var err error
		authState, err = s.authStates.StoredState(ctx)
		if err != nil {
			return WorkspaceChatDraftResolverInput{}, err
		}
	}
	return WorkspaceChatDraftResolverInput{Settings: planner.Config.Settings, Source: planner.Config.Source, AuthState: authState}, nil
}

func (s *Service) workspaceChatDraftOwner() (*WorkspaceChatDraftOwner, string, error) {
	if s == nil || s.draftOwner == nil {
		return nil, "", errors.New("workspace Chat draft service is required")
	}
	id := strings.TrimSpace(s.workspaceID)
	if id == "" {
		return nil, "", errors.New("workspace id is required")
	}
	return s.draftOwner, id, nil
}

func (s *Service) ResolveWorkspaceChatDraftAggregate(ctx context.Context) (WorkspaceChatDraftResolution, error) {
	owner, workspaceID, err := s.workspaceChatDraftOwner()
	if err != nil {
		return WorkspaceChatDraftResolution{}, err
	}
	return owner.ResolveWorkspaceChatDraft(ctx, workspaceID, s.workspaceChatDraftResolverInput)
}

func (s *Service) LazyChatSettings(ctx context.Context) (serverapi.ChatSettingsReadResponse, error) {
	resolved, err := s.ResolveWorkspaceChatDraftAggregate(ctx)
	if err != nil {
		return serverapi.ChatSettingsReadResponse{}, err
	}
	draft := resolved.Draft
	settings, err := ProjectChatSettings(ChatSettingsProjectionInput{
		Catalog: resolved.Catalog,
		Agent:   draft.Agent,
		Settings: session.ChatSettings{
			Supervisor:     draft.Supervisor,
			Thinking:       resolved.PersistedThinking,
			Fast:           draft.Fast,
			Questions:      resolved.PersistedQuestionsPolicy,
			AutoCompaction: draft.AutoCompaction,
		},
		CompactionMode: resolved.CompactionMode,
	})
	if err != nil {
		return serverapi.ChatSettingsReadResponse{}, err
	}
	return serverapi.ChatSettingsReadResponse{Settings: settings}, nil
}

func (s *Service) MaterializedChatSettings(
	ctx context.Context,
	sessionID runtimeids.SessionID,
) (serverapi.ChatSettingsReadResponse, error) {
	view, err := session.ResolvePersistedSessionView(ctx, s.planner.PersistedSessions, sessionID.String())
	if err != nil {
		return serverapi.ChatSettingsReadResponse{}, err
	}
	meta := view.Meta()
	planner := s.planner
	if planner.ReloadConfig != nil {
		planner.Config, err = planner.ReloadConfig()
		if err != nil {
			return serverapi.ChatSettingsReadResponse{}, err
		}
	}
	authState := auth.EmptyState()
	if s.authStates != nil {
		authState, err = s.authStates.StoredState(ctx)
		if err != nil {
			return serverapi.ChatSettingsReadResponse{}, err
		}
	}
	catalog, err := launch.PrepareChatAgentCatalog(
		planner.Config,
		authState,
		false,
	)
	if err != nil {
		return serverapi.ChatSettingsReadResponse{}, err
	}
	state, err := session.ChatSettingsStateFromMeta(meta)
	if err != nil {
		return serverapi.ChatSettingsReadResponse{}, err
	}
	baselineEntry, ok := catalog.Lookup(state.Agent)
	if !ok {
		baselineEntry, _ = catalog.Lookup(config.DefaultSubagentRole)
	}
	effective, err := session.ResolveEffectiveChatSettings(
		state.Settings,
		nil,
		baselineEntry.Settings.Baseline,
	)
	if err != nil {
		return serverapi.ChatSettingsReadResponse{}, err
	}
	taskID, err := s.workflowTaskID(ctx, sessionID.String())
	if err != nil {
		return serverapi.ChatSettingsReadResponse{}, err
	}
	workflowLocked := taskID != nil
	settings, err := ProjectChatSettings(ChatSettingsProjectionInput{
		Catalog:        catalog,
		Agent:          state.Agent,
		Settings:       effective,
		WorkflowLocked: workflowLocked,
		CompactionMode: planner.Config.Settings.CompactionMode,
		Locked:         meta.Locked,
	})
	if err != nil {
		return serverapi.ChatSettingsReadResponse{}, err
	}
	facts := &serverapi.ChatSettingsSessionFacts{
		SessionID:         sessionID,
		TaskID:            taskID,
		PreviousSessionID: meta.PreviousSessionID,
	}
	return serverapi.ChatSettingsReadResponse{
		Settings: settings,
		Session:  facts,
	}, nil
}

func (s *Service) workflowTaskID(ctx context.Context, sessionID string) (*string, error) {
	reader, ok := s.planner.PersistedSessions.(interface {
		WorkflowTaskIDForSession(context.Context, string) (*string, error)
	})
	if !ok {
		return nil, errors.New("workflow Task reader is required")
	}
	taskID, err := reader.WorkflowTaskIDForSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if taskID == nil {
		return nil, nil
	}
	validated, err := runtimeids.ParseTaskID(*taskID)
	return &validated, err
}

func (s *Service) TransformWorkspaceChatDraftAggregate(ctx context.Context, transform WorkspaceChatDraftTransform) (WorkspaceChatDraft, error) {
	owner, workspaceID, err := s.workspaceChatDraftOwner()
	if err != nil {
		return WorkspaceChatDraft{}, err
	}
	return owner.TransformWorkspaceChatDraft(ctx, workspaceID, s.workspaceChatDraftResolverInput, transform)
}

func (s *Service) WorkspaceChatDraft(ctx context.Context, req serverapi.WorkspaceChatDraftRequest) (serverapi.WorkspaceChatDraftResponse, error) {
	if err := req.Operation.Validate(); err != nil {
		return serverapi.WorkspaceChatDraftResponse{}, err
	}
	switch req.Operation.Kind {
	case serverapi.WorkspaceChatDraftReadMessage:
		resolved, err := s.ResolveWorkspaceChatDraftAggregate(ctx)
		if err != nil {
			return serverapi.WorkspaceChatDraftResponse{}, err
		}
		return serverapi.WorkspaceChatDraftResponse{Message: resolved.Draft.Message, GoalAvailability: runtimeview.GoalAvailabilityFromSession(resolved.GoalAvailability)}, nil
	case serverapi.WorkspaceChatDraftUpdateMessage:
		message := *req.Operation.Message
		var availability session.GoalAvailability
		resolved, err := s.TransformWorkspaceChatDraftAggregate(ctx, func(current WorkspaceChatDraftResolution) (WorkspaceChatDraft, error) {
			availability = current.GoalAvailability
			next := current.Draft
			next.Message = message
			return next, nil
		})
		if err != nil {
			return serverapi.WorkspaceChatDraftResponse{}, err
		}
		return serverapi.WorkspaceChatDraftResponse{Message: resolved.Message, GoalAvailability: runtimeview.GoalAvailabilityFromSession(availability)}, nil
	case serverapi.WorkspaceChatDraftClear:
		owner, workspaceID, err := s.workspaceChatDraftOwner()
		if err != nil {
			return serverapi.WorkspaceChatDraftResponse{}, err
		}
		if err := owner.ClearWorkspaceChatDraft(ctx, workspaceID); err != nil {
			return serverapi.WorkspaceChatDraftResponse{}, err
		}
		resolved, err := s.ResolveWorkspaceChatDraftAggregate(ctx)
		if err != nil {
			return serverapi.WorkspaceChatDraftResponse{}, err
		}
		return serverapi.WorkspaceChatDraftResponse{GoalAvailability: runtimeview.GoalAvailabilityFromSession(resolved.GoalAvailability)}, nil
	default:
		return serverapi.WorkspaceChatDraftResponse{}, fmt.Errorf("workspace Chat draft operation kind %q is invalid", req.Operation.Kind)
	}
}

func (s *Service) PlanSession(ctx context.Context, req serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
	result, err := s.PlanLaunchSession(ctx, req)
	if err != nil {
		return serverapi.SessionPlanResponse{}, err
	}
	response := sessionPlanResponseFromResult(result)
	if err := response.Plan.Validate(); err != nil {
		return serverapi.SessionPlanResponse{}, err
	}
	return response, nil
}

func (s *Service) PlanLaunchSession(ctx context.Context, req serverapi.SessionPlanRequest) (PlanResult, error) {
	if err := req.Validate(); err != nil {
		return PlanResult{}, err
	}
	var selectedSessionID *runtimeids.SessionID
	var parentAgentSessionID *runtimeids.SessionID
	switch req.Intent.Kind() {
	case serverapi.SessionLaunchIntentOpenExisting:
		sessionID, _ := req.Intent.SessionID()
		selectedSessionID = &sessionID
	case serverapi.SessionLaunchIntentCreateNew:
		origin, _ := req.Intent.CreateOrigin()
		if origin.Kind() == serverapi.SessionCreateOriginParentAgent {
			sourceID, _ := origin.SessionID()
			parentAgentSessionID = &sourceID
		}
	}
	overrides, err := req.Overrides.CanonicalKey()
	if err != nil {
		return PlanResult{}, err
	}
	memoReq := sessionPlanMemoRequest{
		Mode:            req.Mode,
		Intent:          req.Intent,
		CallerSessionID: serverapi.CanonicalOptionalString(req.CallerSessionID),
		Overrides:       overrides,
	}
	return s.plans.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameSessionPlanMemoRequest, func(ctx context.Context) (PlanResult, error) {
		planner := s.planner
		if planner.ReloadConfig != nil {
			snapshot, snapshotErr := planner.ReloadConfig()
			if snapshotErr != nil {
				return PlanResult{}, snapshotErr
			}
			planner.Config = snapshot
			planner.ReloadConfig = nil
		}
		roleOverride, err := req.Overrides.AgentRoleOverride()
		if err != nil {
			return PlanResult{}, err
		}
		var caller *subagentpolicy.Caller
		if req.Mode == serverapi.SessionLaunchModeHeadless {
			if req.CallerSessionID != nil {
				resolved, callerErr := launch.ResolveSessionCaller(planner.Config.PersistenceRoot, *req.CallerSessionID)
				if callerErr != nil {
					return PlanResult{}, &serverapi.SubagentLaunchDeniedError{Kind: serverapi.SubagentLaunchDenialCallerMissing}
				}
				caller = &resolved
				if parentAgentSessionID != nil {
					callerSessionID, parseErr := runtimeids.ParseSessionID(*req.CallerSessionID)
					if parseErr != nil || *parentAgentSessionID != callerSessionID {
						return PlanResult{}, &serverapi.SubagentLaunchDeniedError{Kind: serverapi.SubagentLaunchDenialInvalidTarget}
					}
				}
			}
			if parentAgentSessionID != nil && req.CallerSessionID == nil {
				if _, parentErr := launch.ResolveSessionCaller(planner.Config.PersistenceRoot, parentAgentSessionID.String()); parentErr != nil {
					return PlanResult{}, &serverapi.SubagentLaunchDeniedError{Kind: serverapi.SubagentLaunchDenialParentMissing}
				}
			}
		}
		if selectedSessionID != nil {
			return s.planExistingSession(ctx, planner, req, *selectedSessionID, roleOverride, caller)
		}
		target := subagentpolicy.TargetFromOverride(roleOverride)
		if err := subagentpolicy.Authorize(planner.Config.Settings, caller, target); err != nil {
			return PlanResult{}, err
		}
		authState := auth.EmptyState()
		if req.Overrides.NeedsAuthState() && s.authStates != nil {
			var authErr error
			authState, authErr = s.authStates.CurrentState(ctx)
			if authErr != nil {
				return PlanResult{}, authErr
			}
		}
		preparation := launch.RunPromptPreparationContext{}
		preparedOverrides, err := launch.PrepareRunPromptOverridesWithContext(planner.Config, req.Overrides, authState, preparation)
		if err != nil {
			return PlanResult{}, err
		}
		preparedPromptFacingTarget := preparePromptFacingTarget(req.Mode, roleOverride, &preparedOverrides)
		return s.finalizeLaunchPlan(
			ctx,
			func() (launch.SessionPlan, []string, error) {
				return planner.PlanNewSessionWithPreparedOverrides(ctx, launch.SessionRequest{
					Mode:                                launch.Mode(req.Mode),
					Intent:                              req.Intent,
					SkipContinuationAgentRoleValidation: roleOverride.Default,
					PreparedPromptFacingTarget:          preparedPromptFacingTarget,
				}, req.Overrides, preparedOverrides)
			},
		)
	})
}

func (s *Service) planExistingSession(
	ctx context.Context,
	planner launch.Planner,
	req serverapi.SessionPlanRequest,
	sessionID runtimeids.SessionID,
	roleOverride serverapi.RunPromptAgentRoleOverride,
	caller *subagentpolicy.Caller,
) (result PlanResult, resultErr error) {
	if s.runtime == nil {
		return PlanResult{}, ErrExistingSessionAuthorityRequired
	}
	if !existingSessionRequestRequiresMaintenance(req, roleOverride) {
		descriptor, err := session.NewOpenSessionDescriptor(sessionID)
		if err != nil {
			return PlanResult{}, err
		}
		maintenanceRequired := false
		callback := func(ctx context.Context, store *session.Store) error {
			if existingSessionStoreRequiresMaintenance(planner.Config, store) {
				maintenanceRequired = true
				return nil
			}
			result, resultErr = s.planExistingSessionSnapshot(ctx, planner, req, roleOverride, caller, store)
			return resultErr
		}
		resultErr = s.runtime.WithSessionStore(ctx, descriptor, callback)
		if resultErr != nil || !maintenanceRequired {
			return result, resultErr
		}
	}
	maintenanceCallback := func(ctx context.Context, store *session.Store, maintenance *sessionruntime.ActiveRuntimeMaintenance) error {
		result, resultErr = s.planExistingSessionWithStore(ctx, planner, req, roleOverride, caller, store, maintenance)
		return resultErr
	}
	resultErr = s.runtime.RunSessionMaintenance(ctx, sessionID.String(), maintenanceCallback)
	return result, resultErr
}

func existingSessionRequestRequiresMaintenance(
	req serverapi.SessionPlanRequest,
	roleOverride serverapi.RunPromptAgentRoleOverride,
) bool {
	return roleOverride.Present || req.Overrides.HasAny()
}

func existingSessionStoreRequiresMaintenance(
	app config.App,
	store *session.Store,
) bool {
	meta := store.Meta()
	state, err := session.ChatSettingsStateFromMeta(meta)
	if err != nil {
		return true
	}
	if meta.Locked != nil || state.Agent == config.DefaultSubagentRole {
		return false
	}
	return config.LookupSubagentRole(app.Settings, state.Agent).Status != config.SubagentRoleLookupPresent
}

func (s *Service) planExistingSessionSnapshot(
	ctx context.Context,
	planner launch.Planner,
	req serverapi.SessionPlanRequest,
	roleOverride serverapi.RunPromptAgentRoleOverride,
	caller *subagentpolicy.Caller,
	store *session.Store,
) (PlanResult, error) {
	if err := authorizeExistingSessionRole(planner, req, roleOverride, caller, store); err != nil {
		return PlanResult{}, err
	}
	return s.finalizeLaunchPlan(
		ctx,
		func() (launch.SessionPlan, []string, error) {
			plan, err := planner.PlanSessionWithStore(ctx, launch.SessionRequest{
				Mode:                                launch.Mode(req.Mode),
				Intent:                              req.Intent,
				SkipContinuationAgentRoleValidation: roleOverride.Default,
				AgentSelectionResolved:              true,
				ReadOnlySnapshot:                    true,
			}, store)
			if err != nil {
				return launch.SessionPlan{}, nil, err
			}
			if req.Mode == serverapi.SessionLaunchModeInteractive {
				if _, err := store.PromoteSubagentToMain(); err != nil {
					return launch.SessionPlan{}, nil, err
				}
			}
			return plan, nil, nil
		},
	)
}

func (s *Service) planExistingSessionWithStore(
	ctx context.Context,
	planner launch.Planner,
	req serverapi.SessionPlanRequest,
	roleOverride serverapi.RunPromptAgentRoleOverride,
	caller *subagentpolicy.Caller,
	store *session.Store,
	maintenance *sessionruntime.ActiveRuntimeMaintenance,
) (PlanResult, error) {
	locked, err := planner.SelectedSessionLockedContractWithStore(store)
	if err != nil {
		return PlanResult{}, err
	}
	if locked != nil && roleOverride.Present {
		req.Overrides.AgentRole = nil
		roleOverride = serverapi.RunPromptAgentRoleOverride{}
	}
	if err := authorizeExistingSessionRole(planner, req, roleOverride, caller, store); err != nil {
		return PlanResult{}, err
	}
	authState := auth.EmptyState()
	if req.Overrides.NeedsAuthState() && s.authStates != nil {
		authState, err = s.authStates.CurrentState(ctx)
		if err != nil {
			return PlanResult{}, err
		}
	}
	preparation := launch.RunPromptPreparationContext{ModelLock: locked, ToolLock: locked}
	if !roleOverride.Present {
		target, targetErr := planner.SelectedSessionPromptFacingTargetWithStore(store)
		if targetErr != nil {
			return PlanResult{}, targetErr
		}
		preparation.OmittedTarget = &target
	}
	preparedOverrides, err := launch.PrepareRunPromptOverridesWithContext(planner.Config, req.Overrides, authState, preparation)
	if err != nil {
		return PlanResult{}, err
	}
	agentSelectionResolved, err := applyPreparedAgentChatSettings(planner.Config, authState, roleOverride, preparedOverrides, store, maintenance)
	if err != nil {
		return PlanResult{}, err
	}
	preparedPromptFacingTarget := preparePromptFacingTarget(req.Mode, roleOverride, &preparedOverrides)
	return s.finalizeLaunchPlan(
		ctx,
		func() (launch.SessionPlan, []string, error) {
			plan, err := planner.PlanSessionWithStore(ctx, launch.SessionRequest{
				Mode:                                launch.Mode(req.Mode),
				Intent:                              req.Intent,
				SkipContinuationAgentRoleValidation: roleOverride.Default,
				PreparedPromptFacingTarget:          preparedPromptFacingTarget,
				AgentSelectionResolved:              agentSelectionResolved,
			}, store)
			if err != nil {
				return launch.SessionPlan{}, nil, err
			}
			return planner.ApplyPreparedRunPromptOverridesWithStore(plan, store, req.Overrides, preparedOverrides, launch.RunPromptOverrideOptions{
				AgentSelectionPersisted: agentSelectionResolved && strings.TrimSpace(req.Overrides.OpenAIBaseURL) == "",
			})
		},
	)
}

func authorizeExistingSessionRole(
	planner launch.Planner,
	req serverapi.SessionPlanRequest,
	roleOverride serverapi.RunPromptAgentRoleOverride,
	caller *subagentpolicy.Caller,
	store *session.Store,
) error {
	if roleOverride.Present {
		return subagentpolicy.Authorize(
			planner.Config.Settings,
			caller,
			subagentpolicy.TargetFromOverride(roleOverride),
		)
	}
	return authorizePersistedHeadlessRole(planner, req, roleOverride, caller, store)
}

func authorizePersistedHeadlessRole(
	planner launch.Planner,
	req serverapi.SessionPlanRequest,
	roleOverride serverapi.RunPromptAgentRoleOverride,
	caller *subagentpolicy.Caller,
	store *session.Store,
) error {
	if req.Mode != serverapi.SessionLaunchModeHeadless || roleOverride.Present {
		return nil
	}
	persistedRole, err := planner.SelectedSessionContinuationAgentRoleWithStore(store)
	if err != nil || persistedRole == nil || caller == nil {
		return err
	}
	lookup := config.LookupSubagentRole(planner.Config.Settings, *persistedRole)
	if lookup.Status != config.SubagentRoleLookupPresent {
		return nil
	}
	persistedOverride, err := (serverapi.RunPromptOverrides{AgentRole: persistedRole}).AgentRoleOverride()
	if err != nil {
		return err
	}
	return subagentpolicy.Authorize(
		planner.Config.Settings,
		caller,
		subagentpolicy.TargetFromOverride(persistedOverride),
	)
}

func applyPreparedAgentChatSettings(
	app config.App,
	authState auth.State,
	roleOverride serverapi.RunPromptAgentRoleOverride,
	preparedOverrides launch.PreparedRunPromptOverrides,
	store *session.Store,
	maintenance *sessionruntime.ActiveRuntimeMaintenance,
) (bool, error) {
	state, err := session.ChatSettingsStateFromMeta(store.Meta())
	if err != nil {
		return false, err
	}
	targetAgent := state.Agent
	selectAgent := roleOverride.Present
	if roleOverride.Present {
		targetAgent = config.DefaultSubagentRole
		if !roleOverride.Default {
			targetAgent = roleOverride.Role
		}
	} else if store.Meta().Locked == nil && state.Agent != config.DefaultSubagentRole {
		lookup := config.LookupSubagentRole(app.Settings, state.Agent)
		selectAgent = lookup.Status != config.SubagentRoleLookupPresent
		if selectAgent {
			targetAgent = config.DefaultSubagentRole
		}
	}
	if !selectAgent {
		return false, nil
	}
	var prepared launch.PreparedChatSettings
	if roleOverride.Present {
		target := preparedOverrides.BaseTarget
		if targetAgent != config.DefaultSubagentRole {
			target = nil
			if preparedOverrides.NamedTarget != nil && preparedOverrides.NamedTarget.Selector == targetAgent {
				target = &launch.PreparedBaseTarget{
					Settings:     preparedOverrides.NamedTarget.Settings,
					Source:       preparedOverrides.NamedTarget.Source,
					EnabledTools: preparedOverrides.NamedTarget.EnabledTools,
				}
			}
		}
		if target == nil {
			return false, fmt.Errorf("prepared Chat Agent %q target is required", targetAgent)
		}
		if preparedOverrides.ProviderCapabilities == nil {
			return false, fmt.Errorf("prepared Chat Agent %q provider capabilities are required", targetAgent)
		}
		prepared, err = launch.PrepareChatSettingsForPreparedTarget(
			*target,
			llm.SupportsFastModeProvider(*preparedOverrides.ProviderCapabilities),
		)
	} else {
		prepared, err = launch.PrepareChatSettingsForAgent(app, authState, targetAgent)
	}
	if err != nil {
		return false, err
	}
	result, err := store.MutateChatSettings(session.ChatSettingsMutation{
		Agent: &session.ChatAgentSelection{
			Agent:    targetAgent,
			Baseline: prepared.Baseline,
		},
	})
	if errors.Is(err, session.ErrChatAgentLocked) {
		return false, nil
	}
	if err != nil && !result.Committed {
		return false, err
	}
	if result.Changed && maintenance != nil {
		maintenance.RetireRuntime()
	}
	return result.Changed, err
}

func preparePromptFacingTarget(
	mode serverapi.SessionLaunchMode,
	roleOverride serverapi.RunPromptAgentRoleOverride,
	preparedOverrides *launch.PreparedRunPromptOverrides,
) *launch.PreparedBaseTarget {
	if mode != serverapi.SessionLaunchModeHeadless {
		if !roleOverride.Present {
			preparedOverrides.BaseTarget = nil
		}
		return nil
	}
	if preparedOverrides.BaseTarget != nil {
		target := *preparedOverrides.BaseTarget
		return &target
	}
	if preparedOverrides.NamedTarget == nil {
		return nil
	}
	return &launch.PreparedBaseTarget{
		Settings:     preparedOverrides.NamedTarget.Settings,
		Source:       preparedOverrides.NamedTarget.Source,
		EnabledTools: preparedOverrides.NamedTarget.EnabledTools,
	}
}

func (s *Service) finalizeLaunchPlan(
	ctx context.Context,
	resolvePlan func() (launch.SessionPlan, []string, error),
) (PlanResult, error) {
	plan, warnings, err := resolvePlan()
	if err != nil {
		return PlanResult{}, err
	}
	provider, err := llm.ResolveEffectiveProviderCapabilities(
		ctx,
		plan.Locked,
		plan.ActiveSettings,
		s.authStates,
	)
	if err != nil {
		return PlanResult{}, err
	}
	plan = launch.ApplyContextPolicy(plan, provider.Capabilities)
	if s.promptHistory != nil {
		history, err := s.promptHistory.ReadPromptHistory(ctx, plan.Descriptor.SessionID().String())
		if err != nil {
			return PlanResult{}, err
		}
		plan.PromptHistory = history
	}
	return PlanResult{Plan: plan, Warnings: warnings}, nil
}

func sessionPlanResponseFromResult(result PlanResult) serverapi.SessionPlanResponse {
	enabledToolIDs := make([]string, 0, len(result.Plan.EnabledTools))
	for _, id := range result.Plan.EnabledTools {
		enabledToolIDs = append(enabledToolIDs, string(id))
	}
	return serverapi.SessionPlanResponse{Plan: serverapi.SessionPlan{
		SessionID:                result.Plan.Descriptor.SessionID().String(),
		ActiveSettings:           result.Plan.ActiveSettings,
		EnabledToolIDs:           enabledToolIDs,
		ConfiguredModelName:      result.Plan.ConfiguredModelName,
		SessionName:              textutil.Pointer(result.Plan.SessionName),
		PromptHistory:            append([]string(nil), result.Plan.PromptHistory...),
		ModelContractLocked:      result.Plan.ModelContractLocked,
		QuestionsEnabled:         result.Plan.QuestionsEnabled,
		AutoCompactionEnabled:    result.Plan.AutoCompactionEnabled,
		ThinkingOverrideExplicit: result.Plan.ThinkingOverrideExplicit,
		Source:                   result.Plan.Source,
	}, Warnings: result.Warnings}
}

func sameSessionPlanMemoRequest(a sessionPlanMemoRequest, b sessionPlanMemoRequest) bool {
	return a.Mode == b.Mode &&
		a.Intent.Equal(b.Intent) &&
		a.CallerSessionID == b.CallerSessionID &&
		a.Overrides == b.Overrides
}

var _ servicecontract.SessionLaunchService = (*Service)(nil)
var _ chatcontext.WorkspaceOwner = (*Service)(nil)
