package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata"
	"core/server/runlog"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	servicecontract "core/shared/apicontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

type API struct {
	metadataStore            *metadata.Store
	fastModeState            *runtime.FastModeState
	recoveredWarningProvider func() (string, bool, error)
	authority                *Authority
	runtimeClientFactory     runtimewire.RuntimeClientFactory
}

type APIOptions struct {
	RuntimeClientFactory     runtimewire.RuntimeClientFactory
	RecoveredWarningProvider func() (string, bool, error)
}

func NewAPI(metadataStore *metadata.Store, fastModeState *runtime.FastModeState, authority *Authority, options APIOptions) *API {
	return &API{
		metadataStore:            metadataStore,
		fastModeState:            fastModeState,
		recoveredWarningProvider: options.RecoveredWarningProvider,
		authority:                authority,
		runtimeClientFactory:     options.RuntimeClientFactory,
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
	if store.Metadata().GeneratedRecoveredWarningIssued {
		return nil
	}
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		return session.MapEventLogMaterializationError(err)
	}
	_, _, err = eventLog.AppendRecord(nil, session.LocalEntryRecord{
		Visibility: session.EntryVisibilityOngoing,
		Role:       "warning",
		Text:       warning,
	})
	if err != nil {
		return err
	}
	return store.MarkGeneratedRecoveredWarningIssued()
}

func (s *API) ActivateSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.SessionRuntimeActivateResponse{}, err
	}
	ownerID := strings.TrimSpace(req.OwnerID)
	if ownerID == "" {
		return serverapi.SessionRuntimeActivateResponse{}, runtimeOwnerIDRequiredError()
	}
	if s == nil || s.authority == nil {
		return serverapi.SessionRuntimeActivateResponse{}, errors.New("session runtime authority is required")
	}
	sessionID, err := runtimeids.ParseSessionID(strings.TrimSpace(req.SessionID))
	if err != nil {
		return serverapi.SessionRuntimeActivateResponse{}, err
	}
	plan, err := s.interactiveRuntimePlan(ctx, req, sessionID.String())
	if err != nil {
		return serverapi.SessionRuntimeActivateResponse{}, err
	}
	attachment, err := s.authority.OpenRuntime(ctx, RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   ownerID,
		Runtime:   &plan,
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

func (s *API) interactiveRuntimePlan(ctx context.Context, req serverapi.SessionRuntimeActivateRequest, sessionID string) (AgentRuntimePlan, error) {
	if s == nil || s.metadataStore == nil {
		return AgentRuntimePlan{}, errors.New("metadata store is required")
	}
	target, err := s.metadataStore.ResolveSessionExecutionTarget(ctx, sessionID)
	if err != nil {
		return AgentRuntimePlan{}, err
	}
	if err := context.Cause(ctx); err != nil {
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
	return NewAgentRuntimePlan(AgentRuntimePlanOptions{
		Settings:                 req.ActiveSettings,
		EnabledTools:             enabledTools,
		Workdir:                  target.EffectiveWorkdir,
		Sources:                  req.Source.Sources,
		FastMode:                 s.fastModeState,
		ClientFactory:            s.runtimeClientFactory,
		StartLogLines:            startLogLines,
		RecoveredWarningProvider: s.recoveredWarningProvider,
	})
}

func (s *API) ReleaseSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.SessionRuntimeReleaseResponse{}, err
	}
	ownerID := strings.TrimSpace(req.OwnerID)
	if ownerID == "" {
		return serverapi.SessionRuntimeReleaseResponse{}, runtimeOwnerIDRequiredError()
	}
	if s == nil || s.authority == nil {
		return serverapi.SessionRuntimeReleaseResponse{}, errors.New("session runtime authority is required")
	}
	sessionID, err := runtimeids.ParseSessionID(strings.TrimSpace(req.Attachment.SessionID))
	if err != nil {
		return serverapi.SessionRuntimeReleaseResponse{}, err
	}
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
		OwnerID:   ownerID,
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
var ErrRuntimeOwnerIDRequired = errors.New("runtime owner id is required")

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

func runtimeOwnerIDRequiredError() error {
	return errors.Join(ErrRuntimeOwnerIDRequired, errors.New("runtime owner_id is required; upgrade the client or connect through the current Kent gateway"))
}

func runtimeUnavailableErr(sessionID string) error {
	return errors.Join(serverapi.ErrRuntimeUnavailable, fmt.Errorf("session %q has no active runtime available", strings.TrimSpace(sessionID)))
}

var _ servicecontract.SessionRuntimeService = (*API)(nil)
