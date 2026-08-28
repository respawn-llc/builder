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
	"core/server/session"
	"core/server/subagentpolicy"
	servicecontract "core/shared/apicontract"
	"core/shared/config"
	"core/shared/protoapi"
	runtimepb "core/shared/protoapi/gen/kent/api/runtime"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"

	"google.golang.org/protobuf/types/known/emptypb"
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
	workspaceID                 string
	draftOwner                  *WorkspaceChatDraftOwner
	materializationStoreOptions []session.StoreOption
}

type PlanResult struct {
	Plan     launch.SessionPlan
	Warnings []string
}

type PlanRequest struct {
	Mode            launch.Mode
	Intent          serverapi.SessionLaunchIntent
	CallerSessionID *string
	Overrides       serverapi.RunPromptOverrides
}

func (r PlanRequest) Validate() error {
	switch r.Mode {
	case launch.ModeInteractive, launch.ModeHeadless:
	default:
		return fmt.Errorf("Session launch mode %q is invalid", r.Mode)
	}
	if err := r.Intent.Validate(); err != nil {
		return fmt.Errorf("Session launch intent: %w", err)
	}
	if err := serverapi.ValidateOptionalIdentifier("caller_session_id", r.CallerSessionID); err != nil {
		return err
	}
	return r.Overrides.ValidateAgentRoleOverride()
}

func NewService(planner launch.Planner) *Service {
	return &Service{planner: planner}
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
	req *emptypb.Empty,
) (*sessionlaunchpb.MaterializeWorkspaceChatSuccess, error) {
	if req == nil {
		return nil, errors.New("workspace Chat materialization request is required")
	}
	sessionID, err := s.materializeWorkspaceChatSession(ctx)
	if err != nil {
		return nil, err
	}
	success := &sessionlaunchpb.MaterializeWorkspaceChatSuccess{SessionId: sessionID.String()}
	if err := protoapi.Validate(success); err != nil {
		return nil, err
	}
	return success, nil
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

func (s *Service) MutateWorkspaceChatSettingsAggregate(
	ctx context.Context,
	operation serverapi.ChatSettingsMutationOperation,
) (PreparedChatSettingsOperationResult, bool, error) {
	owner, workspaceID, err := s.workspaceChatDraftOwner()
	if err != nil {
		return PreparedChatSettingsOperationResult{}, false, err
	}
	return owner.MutateWorkspaceChatSettings(ctx, workspaceID, s.workspaceChatDraftResolverInput, operation)
}

func (s *Service) PrepareMaterializedChatSettingsOperation(
	ctx context.Context,
	store *session.Store,
) (PreparedChatSettingsOperationInput, error) {
	if s == nil || s.planner.PersistedSessions == nil {
		return PreparedChatSettingsOperationInput{}, errors.New("Session launch planner is required")
	}
	if store == nil {
		return PreparedChatSettingsOperationInput{}, errors.New("Session store is required")
	}
	input, _, err := s.prepareMaterializedChatSettings(ctx, store.Meta())
	return input, err
}

func (s *Service) prepareMaterializedChatSettings(
	ctx context.Context,
	meta session.Meta,
) (PreparedChatSettingsOperationInput, *string, error) {
	planner := s.planner
	if planner.ReloadConfig != nil {
		var err error
		planner.Config, err = planner.ReloadConfig()
		if err != nil {
			return PreparedChatSettingsOperationInput{}, nil, err
		}
	}
	authState := auth.EmptyState()
	var err error
	if s.authStates != nil {
		authState, err = s.authStates.StoredState(ctx)
		if err != nil {
			return PreparedChatSettingsOperationInput{}, nil, err
		}
	}
	catalog, err := launch.PrepareChatAgentCatalog(planner.Config, authState, false)
	if err != nil {
		return PreparedChatSettingsOperationInput{}, nil, err
	}
	raw, err := session.ChatSettingsStateFromMeta(meta)
	if err != nil {
		return PreparedChatSettingsOperationInput{}, nil, err
	}
	entry, selectedAvailable := catalog.Lookup(raw.Agent)
	if !selectedAvailable {
		var defaultAvailable bool
		entry, defaultAvailable = catalog.Lookup(config.DefaultSubagentRole)
		if !defaultAvailable {
			return PreparedChatSettingsOperationInput{}, nil, errors.New("default Chat Agent baseline is missing")
		}
	}
	effective, err := session.ResolveEffectiveChatSettings(raw.Settings, nil, entry.Settings.Baseline)
	if err != nil {
		return PreparedChatSettingsOperationInput{}, nil, err
	}
	if meta.Locked != nil {
		entry.Settings, err = lockedPreparedChatSettings(*meta.Locked, entry.Settings, effective)
		if err != nil {
			return PreparedChatSettingsOperationInput{}, nil, err
		}
	}
	effective = normalizeProjectedChatSettings(effective, entry.Settings)
	persistedQuestions := effective.Questions
	persistedThinking := effective.Thinking
	if !selectedAvailable {
		persistedQuestions = entry.Settings.Baseline.Questions
		persistedThinking = entry.Settings.Baseline.Thinking
	} else if raw.Settings != nil {
		if raw.Settings.Questions != nil {
			persistedQuestions = *raw.Settings.Questions
		}
		if raw.Settings.Thinking != nil {
			persistedThinking = strings.TrimSpace(*raw.Settings.Thinking)
		}
	}
	taskID, err := s.workflowTaskID(ctx, meta.SessionID)
	if err != nil {
		return PreparedChatSettingsOperationInput{}, nil, err
	}
	return PreparedChatSettingsOperationInput{
		Raw:                raw,
		Effective:          effective,
		PersistedQuestions: persistedQuestions,
		PersistedThinking:  persistedThinking,
		Catalog:            catalog,
		Locked:             meta.Locked,
		WorkflowLocked:     taskID != nil,
		CompactionMode:     planner.Config.Settings.CompactionMode,
	}, taskID, nil
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
	record, err := s.planner.PersistedSessions.ResolvePersistedSession(ctx, sessionID.String())
	if err != nil {
		return serverapi.ChatSettingsReadResponse{}, err
	}
	input, taskID, err := s.prepareMaterializedChatSettings(ctx, *record.Meta)
	if err != nil {
		return serverapi.ChatSettingsReadResponse{}, err
	}
	settings, err := ProjectChatSettings(ChatSettingsProjectionInput{
		Catalog:        input.Catalog,
		Agent:          input.Raw.Agent,
		Settings:       input.Effective,
		WorkflowLocked: input.WorkflowLocked,
		CompactionMode: input.CompactionMode,
		Locked:         input.Locked,
	})
	if err != nil {
		return serverapi.ChatSettingsReadResponse{}, err
	}
	facts := &serverapi.ChatSettingsSessionFacts{
		SessionID:         sessionID,
		TaskID:            taskID,
		PreviousSessionID: record.Meta.PreviousSessionID,
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

func (s *Service) WorkspaceChatDraft(
	ctx context.Context,
	req *sessionlaunchpb.WorkspaceChatDraftRequest,
) (*sessionlaunchpb.WorkspaceChatDraftSuccess, error) {
	if req == nil {
		return nil, errors.New("workspace Chat draft request is required")
	}
	var message string
	var availability session.GoalAvailability
	switch operation := req.Operation.(type) {
	case *sessionlaunchpb.WorkspaceChatDraftRequest_ReadMessage:
		resolved, err := s.ResolveWorkspaceChatDraftAggregate(ctx)
		if err != nil {
			return nil, err
		}
		message = resolved.Draft.Message
		availability = resolved.GoalAvailability
	case *sessionlaunchpb.WorkspaceChatDraftRequest_UpdateMessage:
		resolved, err := s.TransformWorkspaceChatDraftAggregate(ctx, func(current WorkspaceChatDraftResolution) (WorkspaceChatDraft, error) {
			availability = current.GoalAvailability
			next := current.Draft
			next.Message = operation.UpdateMessage
			return next, nil
		})
		if err != nil {
			return nil, err
		}
		message = resolved.Message
	case *sessionlaunchpb.WorkspaceChatDraftRequest_Clear:
		owner, workspaceID, err := s.workspaceChatDraftOwner()
		if err != nil {
			return nil, err
		}
		if err := owner.ClearWorkspaceChatDraft(ctx, workspaceID); err != nil {
			return nil, err
		}
		resolved, err := s.ResolveWorkspaceChatDraftAggregate(ctx)
		if err != nil {
			return nil, err
		}
		availability = resolved.GoalAvailability
	default:
		return nil, fmt.Errorf("workspace Chat draft operation %T is invalid", req.Operation)
	}
	generatedAvailability, err := workspaceChatGoalAvailabilityToGenerated(availability)
	if err != nil {
		return nil, err
	}
	success := &sessionlaunchpb.WorkspaceChatDraftSuccess{
		Message:          message,
		GoalAvailability: generatedAvailability,
	}
	if err := protoapi.Validate(success); err != nil {
		return nil, err
	}
	return success, nil
}

func (s *Service) PlanSession(
	ctx context.Context,
	req *sessionlaunchpb.SessionPlanRequest,
) (*sessionlaunchpb.SessionPlanSuccess, error) {
	internal, err := sessionPlanRequestFromGenerated(req)
	if err != nil {
		return nil, err
	}
	result, err := s.PlanLaunchSession(ctx, internal)
	if err != nil {
		return nil, err
	}
	return sessionPlanSuccessFromResult(result)
}

func (s *Service) PlanLaunchSession(ctx context.Context, req PlanRequest) (PlanResult, error) {
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
	if req.Mode == launch.ModeHeadless {
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
	plan, warnings, err := planner.PlanNewSessionWithPreparedOverrides(ctx, launch.SessionRequest{
		Mode:                                req.Mode,
		Intent:                              req.Intent,
		SkipContinuationAgentRoleValidation: roleOverride.Default,
		PreparedPromptFacingTarget:          preparedPromptFacingTarget,
	}, req.Overrides, preparedOverrides)
	return s.finalizeLaunchPlan(ctx, plan, warnings, err)
}

func (s *Service) planExistingSession(ctx context.Context, planner launch.Planner, req PlanRequest, sessionID runtimeids.SessionID, roleOverride serverapi.RunPromptAgentRoleOverride, caller *subagentpolicy.Caller) (PlanResult, error) {
	record, err := session.ResolvePersistedSessionRecord(ctx, planner.PersistedSessions, sessionID.String())
	if err != nil {
		return PlanResult{}, err
	}
	meta := *record.Meta
	if meta.Locked != nil && roleOverride.Present {
		req.Overrides.AgentRole = nil
		roleOverride = serverapi.RunPromptAgentRoleOverride{}
	}
	if roleOverride.Present {
		if err := subagentpolicy.Authorize(planner.Config.Settings, caller, subagentpolicy.TargetFromOverride(roleOverride)); err != nil {
			return PlanResult{}, err
		}
	} else if err := authorizePersistedHeadlessRole(planner, req, caller, meta); err != nil {
		return PlanResult{}, err
	}
	authState := auth.EmptyState()
	if req.Overrides.NeedsAuthState() && s.authStates != nil {
		authState, err = s.authStates.CurrentState(ctx)
		if err != nil {
			return PlanResult{}, err
		}
	}
	preparation := launch.RunPromptPreparationContext{ModelLock: meta.Locked, ToolLock: meta.Locked}
	if !roleOverride.Present {
		target, err := planner.SelectedSessionPromptFacingTargetFromMeta(meta)
		if err != nil {
			return PlanResult{}, err
		}
		preparation.OmittedTarget = &target
	}
	preparedOverrides, err := launch.PrepareRunPromptOverridesWithContext(planner.Config, req.Overrides, authState, preparation)
	if err != nil {
		return PlanResult{}, err
	}
	meta, agentSelectionResolved, activationAgentSelection, err := applyPreparedAgentChatSettings(
		planner.Config,
		authState,
		roleOverride,
		preparedOverrides,
		meta,
	)
	if err != nil {
		return PlanResult{}, err
	}
	preparedPromptFacingTarget := preparePromptFacingTarget(req.Mode, roleOverride, &preparedOverrides)
	plan, warnings, err := planner.PlanPersistedSessionWithPreparedOverrides(ctx, launch.SessionRequest{
		Mode:                                req.Mode,
		Intent:                              req.Intent,
		SkipContinuationAgentRoleValidation: roleOverride.Default,
		PreparedPromptFacingTarget:          preparedPromptFacingTarget,
	}, meta, req.Overrides, preparedOverrides, launch.RunPromptOverrideOptions{
		AgentSelectionPersisted: agentSelectionResolved && strings.TrimSpace(req.Overrides.OpenAIBaseURL) == "",
	})
	plan.ActivationAgentSelection = activationAgentSelection
	return s.finalizeLaunchPlan(ctx, plan, warnings, err)
}

func authorizePersistedHeadlessRole(
	planner launch.Planner,
	req PlanRequest,
	caller *subagentpolicy.Caller,
	meta session.Meta,
) error {
	if req.Mode != launch.ModeHeadless {
		return nil
	}
	if meta.Continuation == nil || meta.Continuation.AgentRole == nil || caller == nil {
		return nil
	}
	persistedRole := strings.TrimSpace(*meta.Continuation.AgentRole)
	lookup := config.LookupSubagentRole(planner.Config.Settings, persistedRole)
	if lookup.Status != config.SubagentRoleLookupPresent {
		return nil
	}
	persistedOverride, err := (serverapi.RunPromptOverrides{AgentRole: &persistedRole}).AgentRoleOverride()
	if err != nil {
		return err
	}
	return subagentpolicy.Authorize(planner.Config.Settings, caller, subagentpolicy.TargetFromOverride(persistedOverride))
}

func applyPreparedAgentChatSettings(
	app config.App,
	authState auth.State,
	roleOverride serverapi.RunPromptAgentRoleOverride,
	preparedOverrides launch.PreparedRunPromptOverrides,
	meta session.Meta,
) (session.Meta, bool, *session.ChatSettingsState, error) {
	state, err := session.ChatSettingsStateFromMeta(meta)
	if err != nil {
		return session.Meta{}, false, nil, err
	}
	targetAgent := state.Agent
	selectAgent := roleOverride.Present
	if roleOverride.Present {
		targetAgent = config.DefaultSubagentRole
		if !roleOverride.Default {
			targetAgent = roleOverride.Role
		}
	} else if meta.Locked == nil && state.Agent != config.DefaultSubagentRole {
		lookup := config.LookupSubagentRole(app.Settings, state.Agent)
		selectAgent = lookup.Status != config.SubagentRoleLookupPresent
		if selectAgent {
			targetAgent = config.DefaultSubagentRole
		}
	}
	if !selectAgent {
		return meta, false, nil, nil
	}
	if meta.Locked != nil {
		return meta, false, nil, nil
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
			return session.Meta{}, false, nil, fmt.Errorf("prepared Chat Agent %q target is required", targetAgent)
		}
		if preparedOverrides.ProviderCapabilities == nil {
			return session.Meta{}, false, nil, fmt.Errorf("prepared Chat Agent %q provider capabilities are required", targetAgent)
		}
		prepared, err = launch.PrepareChatSettingsForPreparedTarget(
			*target,
			llm.SupportsFastModeProvider(*preparedOverrides.ProviderCapabilities),
		)
	} else {
		prepared, err = launch.PrepareChatSettingsForAgent(app, authState, targetAgent)
	}
	if err != nil {
		return session.Meta{}, false, nil, err
	}
	if targetAgent == state.Agent {
		return meta, true, nil, nil
	}
	target, err := session.ChatSettingsStateFromCompleteSettings(targetAgent, prepared.Baseline)
	if err != nil {
		return session.Meta{}, false, nil, err
	}
	projected, changed, err := session.ProjectChatSettingsState(meta, target)
	if errors.Is(err, session.ErrChatAgentLocked) {
		return meta, false, nil, nil
	}
	if err != nil {
		return session.Meta{}, false, nil, err
	}
	if !changed {
		return projected, true, nil, nil
	}
	return projected, true, &target, nil
}

func preparePromptFacingTarget(
	mode launch.Mode,
	roleOverride serverapi.RunPromptAgentRoleOverride,
	preparedOverrides *launch.PreparedRunPromptOverrides,
) *launch.PreparedBaseTarget {
	if mode != launch.ModeHeadless {
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

func (s *Service) finalizeLaunchPlan(ctx context.Context, plan launch.SessionPlan, warnings []string, err error) (PlanResult, error) {
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

func sessionPlanRequestFromGenerated(request *sessionlaunchpb.SessionPlanRequest) (PlanRequest, error) {
	if request == nil {
		return PlanRequest{}, errors.New("Session plan request is required")
	}
	var mode launch.Mode
	switch request.Mode {
	case sessionlaunchpb.SessionLaunchMode_SESSION_LAUNCH_MODE_INTERACTIVE:
		mode = launch.ModeInteractive
	case sessionlaunchpb.SessionLaunchMode_SESSION_LAUNCH_MODE_HEADLESS:
		mode = launch.ModeHeadless
	default:
		return PlanRequest{}, fmt.Errorf("generated Session launch mode %v is invalid", request.Mode)
	}
	intent, err := protoapi.SessionLaunchIntentFromProto(request.Intent)
	if err != nil {
		return PlanRequest{}, err
	}
	overrides := serverapi.RunPromptOverrides{}
	if request.Overrides != nil {
		overrides, err = protoapi.RunPromptOverridesFromProto(request.Overrides)
		if err != nil {
			return PlanRequest{}, err
		}
	}
	internal := PlanRequest{
		Mode:            mode,
		Intent:          intent,
		CallerSessionID: textutil.Pointer(request.CallerSessionId),
		Overrides:       overrides,
	}
	return internal, internal.Validate()
}

func sessionPlanSuccessFromResult(result PlanResult) (*sessionlaunchpb.SessionPlanSuccess, error) {
	settings, err := protoapi.SessionSettingsToProto(result.Plan.ActiveSettings)
	if err != nil {
		return nil, err
	}
	source, err := protoapi.SessionSourceReportToProto(result.Plan.Source)
	if err != nil {
		return nil, err
	}
	enabledToolIDs := make([]sessionlaunchpb.ToolID, 0, len(result.Plan.EnabledTools))
	for _, id := range result.Plan.EnabledTools {
		generated, err := protoapi.SessionToolIDToProto(id)
		if err != nil {
			return nil, err
		}
		enabledToolIDs = append(enabledToolIDs, generated)
	}
	plan := &sessionlaunchpb.SessionPlan{
		SessionId:                result.Plan.Descriptor.SessionID().String(),
		ActiveSettings:           settings,
		EnabledToolIds:           enabledToolIDs,
		SessionName:              textutil.Pointer(result.Plan.SessionName),
		PromptHistory:            append([]string(nil), result.Plan.PromptHistory...),
		ModelContractLocked:      result.Plan.ModelContractLocked,
		QuestionsEnabled:         result.Plan.QuestionsEnabled,
		AutoCompactionEnabled:    result.Plan.AutoCompactionEnabled,
		ThinkingOverrideExplicit: result.Plan.ThinkingOverrideExplicit,
		Source:                   source,
	}
	if result.Plan.ActivationAgentSelection != nil {
		selection := result.Plan.ActivationAgentSelection
		settings := selection.Settings
		if settings == nil ||
			settings.Supervisor == nil ||
			settings.Thinking == nil ||
			settings.Fast == nil ||
			settings.Questions == nil ||
			settings.AutoCompaction == nil {
			return nil, errors.New("activation Agent selection has incomplete Chat settings")
		}
		plan.ActivationAgentSelection = &sessionlaunchpb.SessionRuntimeAgentSelection{
			Agent: selection.Agent,
			Baseline: &sessionlaunchpb.SessionRuntimeChatSettings{
				Supervisor:     *settings.Supervisor,
				Thinking:       *settings.Thinking,
				Fast:           *settings.Fast,
				Questions:      *settings.Questions,
				AutoCompaction: *settings.AutoCompaction,
			},
		}
	}
	if result.Plan.ConfiguredModelName != "" {
		plan.ConfiguredModelName = &result.Plan.ConfiguredModelName
	}
	success := &sessionlaunchpb.SessionPlanSuccess{
		Plan:     plan,
		Warnings: append([]string(nil), result.Warnings...),
	}
	if err := protoapi.Validate(success); err != nil {
		return nil, err
	}
	return success, nil
}

func workspaceChatGoalAvailabilityToGenerated(
	availability session.GoalAvailability,
) (runtimepb.GoalAvailability, error) {
	switch availability {
	case session.GoalAvailable:
		return runtimepb.GoalAvailability_GOAL_AVAILABILITY_AVAILABLE, nil
	case session.GoalAgentCapabilityMissing:
		return runtimepb.GoalAvailability_GOAL_AVAILABILITY_AGENT_CAPABILITY_MISSING, nil
	default:
		return runtimepb.GoalAvailability_GOAL_AVAILABILITY_UNSPECIFIED, fmt.Errorf(
			"workspace Chat goal availability %d is invalid",
			availability,
		)
	}
}

var _ servicecontract.SessionLaunchService = (*Service)(nil)
var _ chatcontext.WorkspaceOwner = (*Service)(nil)
