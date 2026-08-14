package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata"
	"core/server/runlog"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/tools"
	servicecontract "core/shared/apicontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
	"core/shared/toolspec"
)

type API struct {
	metadataStore            *metadata.Store
	recoveredWarningProvider func() (string, bool, error)
	authority                *Authority
	runtimeClientFactory     runtimewire.RuntimeClientFactory
	managedWorktreeBaseDir   string
}

type APIOptions struct {
	RuntimeClientFactory     runtimewire.RuntimeClientFactory
	RecoveredWarningProvider func() (string, bool, error)
	ManagedWorktreeBaseDir   string
}

func NewAPI(metadataStore *metadata.Store, authority *Authority, options APIOptions) *API {
	return &API{
		metadataStore:            metadataStore,
		recoveredWarningProvider: options.RecoveredWarningProvider,
		authority:                authority,
		runtimeClientFactory:     options.RuntimeClientFactory,
		managedWorktreeBaseDir:   options.ManagedWorktreeBaseDir,
	}
}

func appendRecoveredWarning(store *session.Store, provider func() (string, bool, error)) error {
	if provider == nil {
		return nil
	}
	warning, ok, err := provider()
	if err != nil {
		return err
	}
	if !ok || warning == "" || store == nil {
		return nil
	}
	if store.Meta().GeneratedRecoveredWarningIssued {
		return nil
	}
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		return err
	}
	warningText := warning
	_, err = eventLog.AppendGeneratedRecoveredWarning(session.LocalEntryRecord{
		Visibility: session.EntryVisibilityOngoing,
		Role:       "warning",
		Text:       &warningText,
	})
	return err
}

func (s *API) ActivateSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
	prepared := req
	prepared.OwnerID = strings.TrimSpace(req.OwnerID)
	if err := prepared.Validate(); err != nil {
		return serverapi.SessionRuntimeActivateResponse{}, err
	}
	sessionID, err := runtimeids.ParseSessionID(strings.TrimSpace(prepared.SessionID))
	if err != nil {
		return serverapi.SessionRuntimeActivateResponse{}, err
	}
	if s == nil || s.metadataStore == nil {
		return serverapi.SessionRuntimeActivateResponse{}, errors.New("metadata store is required")
	}
	resolved, err := s.metadataStore.ResolveActiveProjectSession(ctx, sessionID.String())
	if err != nil {
		return serverapi.SessionRuntimeActivateResponse{}, err
	}
	return s.activateSessionRuntime(ctx, prepared, servicecontract.AuthorizedSessionInActiveProject{
		SessionID:       resolved.SessionID,
		ActiveProjectID: resolved.OwningProjectID,
		OwningProjectID: resolved.OwningProjectID,
		ExecutionTarget: resolved.ExecutionTarget,
	})
}

func (s *API) ActivateSessionRuntimeValidated(ctx context.Context, validated servicecontract.Validated[serverapi.SessionRuntimeActivateRequest], authorization servicecontract.AuthorizedSessionInActiveProject) (serverapi.SessionRuntimeActivateResponse, error) {
	return s.activateSessionRuntime(ctx, validated.Value(), authorization)
}

func (s *API) activateSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeActivateRequest, authorization servicecontract.AuthorizedSessionInActiveProject) (serverapi.SessionRuntimeActivateResponse, error) {
	if s == nil || s.authority == nil {
		return serverapi.SessionRuntimeActivateResponse{}, errors.New("session runtime authority is required")
	}
	sessionID := authorization.SessionID
	attachment, err := s.authority.openRuntime(ctx, RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   req.OwnerID,
	}, func(_ context.Context, store *session.Store) (*AgentRuntimePlan, error) {
		persisted := store.Meta().ChatSettings
		if persisted != nil && req.ThinkingOverrideExplicit {
			cloned := *persisted
			cloned.Thinking = nil
			persisted = &cloned
		}
		current := &session.ChatSettingsOverrides{
			Supervisor:     textutil.Value(req.ActiveSettings.Reviewer.Frequency),
			Thinking:       textutil.Value(req.ActiveSettings.ThinkingLevel),
			Fast:           textutil.Value(req.ActiveSettings.PriorityRequestMode),
			Questions:      req.QuestionsEnabled,
			AutoCompaction: req.AutoCompactionEnabled,
		}
		effective, resolveErr := session.ResolveEffectiveChatSettings(
			persisted,
			current,
			session.ChatSettings{
				Supervisor:     req.ActiveSettings.Reviewer.Frequency,
				Thinking:       req.ActiveSettings.ThinkingLevel,
				Fast:           req.ActiveSettings.PriorityRequestMode,
				Questions:      *req.QuestionsEnabled,
				AutoCompaction: *req.AutoCompactionEnabled,
			},
		)
		if resolveErr != nil {
			return nil, resolveErr
		}
		req.ActiveSettings.Reviewer.Frequency = effective.Supervisor
		req.ActiveSettings.ThinkingLevel = effective.Thinking
		req.ActiveSettings.PriorityRequestMode = effective.Fast
		questions := effective.Questions
		autoCompaction := effective.AutoCompaction
		req.QuestionsEnabled = &questions
		req.AutoCompactionEnabled = &autoCompaction
		plan, planErr := s.interactiveRuntimePlan(ctx, req, authorization)
		if planErr != nil {
			return nil, planErr
		}
		return &plan, nil
	})
	if err != nil {
		return serverapi.SessionRuntimeActivateResponse{}, err
	}
	resource := attachment.Resource()
	return serverapi.SessionRuntimeActivateResponse{
		Attachment: serverapi.SessionRuntimeAttachment{
			SessionID:  resource.SessionID().String(),
			Generation: uint64(resource.Generation()),
		},
	}, nil
}

func (s *API) interactiveRuntimePlan(ctx context.Context, req serverapi.SessionRuntimeActivateRequest, authorization servicecontract.AuthorizedSessionInActiveProject) (AgentRuntimePlan, error) {
	if s == nil || s.metadataStore == nil {
		return AgentRuntimePlan{}, errors.New("metadata store is required")
	}
	sessionID := authorization.SessionID.String()
	target := authorization.ExecutionTarget
	if err := context.Cause(ctx); err != nil {
		return AgentRuntimePlan{}, err
	}
	projectWorkspaceBoundary, err := s.metadataStore.ResolveProjectWorkspaceBoundary(ctx, authorization.OwningProjectID)
	if err != nil {
		return AgentRuntimePlan{}, err
	}
	managedWorktreeRoots, err := s.metadataStore.ListManagedWorktreeRoots(ctx)
	if err != nil {
		return AgentRuntimePlan{}, err
	}
	executionRoot := target.WorkspaceRoot
	var currentWorktreeRoot *string
	if target.Worktree != nil {
		executionRoot = target.Worktree.Root
		root := target.Worktree.Root
		currentWorktreeRoot = &root
	}
	filesystemContext, err := runtimewire.NewFilesystemContext(target.EffectiveWorkdir, executionRoot, projectWorkspaceBoundary)
	if err != nil {
		return AgentRuntimePlan{}, err
	}
	enabledTools, err := parseToolIDs(req.EnabledToolIDs)
	if err != nil {
		return AgentRuntimePlan{}, err
	}
	startLogLines := []string{
		fmt.Sprintf(
			"app.interactive.start session_id=%s workspace=%s workdir=%s model=%s",
			sessionID,
			target.WorkspaceRoot,
			target.EffectiveWorkdir,
			req.ActiveSettings.Model,
		),
		fmt.Sprintf(
			"config.settings path=%s created=%t",
			req.Source.SettingsPath,
			req.Source.CreatedDefaultConfig,
		),
	}
	for _, line := range runlog.FormatConfigSourceLines(req.Source.Sources) {
		startLogLines = append(startLogLines, "config.source "+line)
	}
	var managedWorktreePathContext *tools.ManagedWorktreePathContext
	if strings.TrimSpace(s.managedWorktreeBaseDir) != "" {
		managedWorktreePathContext, err = tools.NewManagedWorktreePathContext(s.managedWorktreeBaseDir, currentWorktreeRoot, managedWorktreeRoots)
		if err != nil {
			return AgentRuntimePlan{}, err
		}
	}
	return NewAgentRuntimePlan(AgentRuntimePlanOptions{
		Settings:                 req.ActiveSettings,
		EnabledTools:             enabledTools,
		FilesystemContext:        tools.FilesystemContext{Access: filesystemContext.Access, ManagedWorktree: managedWorktreePathContext},
		Sources:                  req.Source.Sources,
		QuestionsEnabled:         req.QuestionsEnabled,
		AutoCompactionEnabled:    req.AutoCompactionEnabled,
		ClientFactory:            s.runtimeClientFactory,
		StartLogLines:            startLogLines,
		RecoveredWarningProvider: s.recoveredWarningProvider,
	})
}

func (s *API) ReleaseSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
	prepared := req
	prepared.OwnerID = strings.TrimSpace(req.OwnerID)
	if err := prepared.Validate(); err != nil {
		return serverapi.SessionRuntimeReleaseResponse{}, err
	}
	sessionID, err := runtimeids.ParseSessionID(strings.TrimSpace(prepared.Attachment.SessionID))
	if err != nil {
		return serverapi.SessionRuntimeReleaseResponse{}, err
	}
	return s.releaseSessionRuntime(ctx, prepared, servicecontract.AuthorizedSessionInActiveProject{SessionID: sessionID})
}

func (s *API) ReleaseSessionRuntimeValidated(ctx context.Context, validated servicecontract.Validated[serverapi.SessionRuntimeReleaseRequest], authorization servicecontract.AuthorizedSessionInActiveProject) (serverapi.SessionRuntimeReleaseResponse, error) {
	return s.releaseSessionRuntime(ctx, validated.Value(), authorization)
}

func (s *API) releaseSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeReleaseRequest, authorization servicecontract.AuthorizedSessionInActiveProject) (serverapi.SessionRuntimeReleaseResponse, error) {
	if s == nil || s.authority == nil {
		return serverapi.SessionRuntimeReleaseResponse{}, errors.New("session runtime authority is required")
	}
	sessionID := authorization.SessionID
	resource, err := runtimeids.NewSessionResourceRef(sessionID, runtimeids.ResourceGeneration(req.Attachment.Generation))
	if err != nil {
		return serverapi.SessionRuntimeReleaseResponse{}, err
	}
	var policy RuntimeReleasePolicy
	switch req.EffectiveClosePolicy() {
	case "":
		policy = RuntimeReleaseClose
	case serverapi.SessionRuntimeReleaseClosePolicyCloseIfIdle:
		policy = RuntimeReleaseCloseIfIdle
	case serverapi.SessionRuntimeReleaseClosePolicyDetachOnly:
		policy = RuntimeReleaseDetach
	default:
		panic(fmt.Sprintf("validated runtime release has unsupported close policy %q", req.ClosePolicy))
	}
	result, err := s.authority.ReleaseRuntime(ctx, RuntimeReleaseRequest{
		Resource:  resource,
		OwnerID:   req.OwnerID,
		DropOwner: req.DropOwner,
		Policy:    policy,
	})
	if err != nil {
		return serverapi.SessionRuntimeReleaseResponse{}, err
	}
	return serverapi.SessionRuntimeReleaseResponse{
		Released: result.Released,
		Active:   result.Active,
	}, nil
}

var errUnknownToolID = errors.New("unknown tool id")
var ErrRuntimeOwnerIDRequired = serverapi.ErrRuntimeOwnerIDRequired

func parseToolIDs(raw []string) ([]toolspec.ID, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	ids := make([]toolspec.ID, 0, len(raw))
	for _, item := range raw {
		id, ok := toolspec.ParseID(item)
		if !ok {
			return nil, fmt.Errorf("%w %q", errUnknownToolID, item)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func runtimeUnavailableErr(sessionID string) error {
	return errors.Join(serverapi.ErrRuntimeUnavailable, fmt.Errorf("session %q has no active runtime available", strings.TrimSpace(sessionID)))
}

var _ servicecontract.SessionRuntimeService = (*API)(nil)
