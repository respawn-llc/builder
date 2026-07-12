package app

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"

	"core/cli/app/internal/projectbinding"
	"core/cli/app/internal/status"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"

	"github.com/google/uuid"
)

type launchMode string

const (
	launchModeInteractive launchMode = "interactive"
	launchModeHeadless    launchMode = "headless"
)

type sessionLaunchRequest struct {
	Mode              launchMode
	SelectedSessionID string
	ForceNewSession   bool
	ParentSessionID   string
	Overrides         serverapi.RunPromptOverrides
}

type sessionLaunchPlan struct {
	Mode                                 launchMode
	SessionID                            string
	SelectedViaPicker                    bool
	SelectedSessionWorkspaceRoot         string
	SelectedSessionWorkspaceLookupFailed bool
	ActiveSettings                       config.Settings
	EnabledTools                         []toolspec.ID
	ConfiguredModelName                  string
	SessionName                          string
	PromptHistory                        []string
	ModelContractLocked                  bool
	StatusConfig                         uiStatusConfig
	WorkspaceRoot                        string
	Source                               config.SourceReport
}

type resolvedSessionPlanRequest struct {
	request             serverapi.SessionPlanRequest
	selectedViaPicker   bool
	sessionSummaries    []clientui.SessionSummary
	hasSessionSummaries bool
}

type runtimeLaunchPlan struct {
	Logger      *runLogger
	Wiring      *runtimeWiring
	close       func() error
	detachClose func() error
}

func (p *runtimeLaunchPlan) Close() error {
	if p == nil || p.close == nil {
		return nil
	}
	return p.close()
}

func (p *runtimeLaunchPlan) DetachOnlyClose() error {
	if p == nil {
		return nil
	}
	if p.detachClose != nil {
		return p.detachClose()
	}
	return p.Close()
}

type sessionPickerRunner func([]clientui.SessionSummary, string, sessionPickerHeaderInfo) (sessionPickerResult, error)

type sessionViewReader interface {
	GetSessionMainView(ctx context.Context, req serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error)
}

type launchPlannerServer interface {
	OwnsServer() bool
	Config() config.App
	PresentationTheme() string
	ProjectID() string
	AuthStatusClient() client.AuthStatusClient
	ProjectViewClient() client.ProjectViewClient
	SessionLaunchClient() client.SessionLaunchClient
	SessionViewClient() client.SessionViewClient
}

type launchPlannerAuthStateProvider interface {
	AuthStateResolver() status.AuthStateResolver
	AuthStatePath() string
}

type launchPlannerAuthStateMetadata struct {
	Resolver status.AuthStateResolver
	Path     string
}

type launchPlannerRuntimePreparer interface {
	PrepareRuntime(ctx context.Context, plan sessionLaunchPlan, diagnosticWriter io.Writer, startLogLine string) (*runtimeLaunchPlan, error)
}

type launchPlanner struct {
	server      launchPlannerServer
	pickSession sessionPickerRunner
}

func newSessionLaunchPlanner(server launchPlannerServer) *launchPlanner {
	return &launchPlanner{
		server: server,
		pickSession: func(summaries []clientui.SessionSummary, theme string, header sessionPickerHeaderInfo) (sessionPickerResult, error) {
			return runSessionPickerFlow(summaries, theme, header)
		},
	}
}

func (p *launchPlanner) PlanSession(ctx context.Context, req sessionLaunchRequest) (sessionLaunchPlan, error) {
	if p == nil || p.server == nil || p.server.SessionLaunchClient() == nil {
		return sessionLaunchPlan{}, errors.New("launch planner bootstrap is required")
	}
	resolved, err := p.resolvePlanRequest(ctx, req)
	if err != nil {
		return sessionLaunchPlan{}, err
	}
	resp, err := p.server.SessionLaunchClient().PlanSession(ctx, resolved.request)
	if err != nil {
		return sessionLaunchPlan{}, err
	}
	enabledTools := make([]toolspec.ID, 0, len(resp.Plan.EnabledToolIDs))
	for _, raw := range resp.Plan.EnabledToolIDs {
		if id, ok := toolspec.ParseID(raw); ok {
			enabledTools = append(enabledTools, id)
		}
	}
	cfg := p.server.Config()
	authState := launchPlannerAuthState(p.server)
	selectedSessionWorkspaceRoot := ""
	selectedSessionWorkspaceLookupFailed := false
	if resolved.selectedViaPicker {
		selectedSessionWorkspaceRoot, err = loadSelectedSessionWorkspaceRoot(ctx, p.server.SessionViewClient(), resp.Plan.SessionID)
		if err != nil {
			selectedSessionWorkspaceLookupFailed = true
		}
	}
	return sessionLaunchPlan{
		Mode:                                 req.Mode,
		SessionID:                            resp.Plan.SessionID,
		SelectedViaPicker:                    resolved.selectedViaPicker,
		SelectedSessionWorkspaceRoot:         selectedSessionWorkspaceRoot,
		SelectedSessionWorkspaceLookupFailed: selectedSessionWorkspaceLookupFailed,
		ActiveSettings:                       resp.Plan.ActiveSettings,
		EnabledTools:                         enabledTools,
		ConfiguredModelName:                  resp.Plan.ConfiguredModelName,
		SessionName:                          resp.Plan.SessionName,
		PromptHistory:                        append([]string(nil), resp.Plan.PromptHistory...),
		ModelContractLocked:                  resp.Plan.ModelContractLocked,
		StatusConfig: uiStatusConfig{
			WorkspaceRoot:   resp.Plan.WorkspaceRoot,
			PersistenceRoot: cfg.PersistenceRoot,
			SessionViews:    p.server.SessionViewClient(),
			Settings:        resp.Plan.ActiveSettings,
			Source:          resp.Plan.Source,
			AuthManager:     status.NormalizeAuthStateResolver(authState.Resolver),
			AuthStatus:      p.server.AuthStatusClient(),
			AuthStatePath:   authState.Path,
			OwnsServer:      p.server.OwnsServer(),
		},
		WorkspaceRoot: resp.Plan.WorkspaceRoot,
		Source:        resp.Plan.Source,
	}, nil
}

func launchPlannerAuthState(server launchPlannerServer) launchPlannerAuthStateMetadata {
	authProvider, ok := server.(launchPlannerAuthStateProvider)
	if !ok {
		return launchPlannerAuthStateMetadata{}
	}
	return launchPlannerAuthStateMetadata{
		Resolver: authProvider.AuthStateResolver(),
		Path:     strings.TrimSpace(authProvider.AuthStatePath()),
	}
}

func loadSelectedSessionWorkspaceRoot(ctx context.Context, sessionViews sessionViewReader, sessionID string) (string, error) {
	if sessionViews == nil {
		return "", errors.New("session view client is required")
	}
	resp, err := sessionViews.GetSessionMainView(ctx, serverapi.SessionMainViewRequest{SessionID: strings.TrimSpace(sessionID)})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.MainView.Session.ExecutionTarget.WorkspaceRoot), nil
}

func (p *launchPlanner) PrepareRuntime(ctx context.Context, plan sessionLaunchPlan, diagnosticWriter io.Writer, startLogLine string) (*runtimeLaunchPlan, error) {
	if p == nil || p.server == nil {
		return nil, io.ErrClosedPipe
	}
	if preparer, ok := p.server.(launchPlannerRuntimePreparer); ok {
		return preparer.PrepareRuntime(ctx, plan, diagnosticWriter, startLogLine)
	}
	runtimeServer, ok := p.server.(runtimeAttachmentSource)
	if !ok {
		return nil, errors.New("runtime attachment server is required")
	}
	return prepareSharedRuntime(ctx, runtimeServer, plan, diagnosticWriter, startLogLine)
}

func (p *launchPlanner) resolvePlanRequest(ctx context.Context, req sessionLaunchRequest) (resolvedSessionPlanRequest, error) {
	overrides := sessionPlanOverridesFromConfig(p.server.Config())
	overrides = mergeSessionPlanOverrides(overrides, req.Overrides)
	resolved := resolvedSessionPlanRequest{request: serverapi.SessionPlanRequest{
		ClientRequestID:   uuid.NewString(),
		Mode:              serverapi.SessionLaunchMode(req.Mode),
		SelectedSessionID: strings.TrimSpace(req.SelectedSessionID),
		ForceNewSession:   req.ForceNewSession,
		ParentSessionID:   strings.TrimSpace(req.ParentSessionID),
		Overrides:         overrides,
	}}
	if resolved.request.Mode == serverapi.SessionLaunchModeHeadless && resolved.request.SelectedSessionID == "" {
		resolved.request.ForceNewSession = true
		return resolved, nil
	}
	if resolved.request.SelectedSessionID != "" || resolved.request.ForceNewSession {
		return resolved, nil
	}
	summaries, err := p.listSessionSummaries(ctx)
	if err != nil {
		return resolvedSessionPlanRequest{}, err
	}
	resolved.sessionSummaries = append([]clientui.SessionSummary(nil), summaries...)
	resolved.hasSessionSummaries = true
	if len(summaries) == 0 {
		resolved.request.ForceNewSession = true
		return resolved, nil
	}
	if p.pickSession == nil {
		return resolvedSessionPlanRequest{}, errors.New("session picker is required")
	}
	cfg := p.server.Config()
	picked, err := p.pickSession(summaries, p.server.PresentationTheme(), p.sessionPickerHeaderInfo(cfg))
	if err != nil {
		return resolvedSessionPlanRequest{}, err
	}
	if picked.Canceled {
		return resolvedSessionPlanRequest{}, projectbinding.ErrStartupCanceledByUser
	}
	if picked.CreateNew {
		resolved.request.ForceNewSession = true
		return resolved, nil
	}
	if picked.Session == nil {
		return resolvedSessionPlanRequest{}, errors.New("no session selected")
	}
	resolved.request.SelectedSessionID = picked.Session.SessionID
	resolved.selectedViaPicker = true
	return resolved, nil
}

func (p *launchPlanner) sessionPickerHeaderInfo(cfg config.App) sessionPickerHeaderInfo {
	workspaceRoot := strings.TrimSpace(cfg.WorkspaceRoot)
	authState := launchPlannerAuthState(p.server)
	settings := cfg.Settings
	modelName := strings.TrimSpace(settings.Model)
	thinkingLevel := strings.TrimSpace(settings.ThinkingLevel)
	if !p.server.OwnsServer() {
		// A configured server's persisted runtime settings are not client config
		// state. The picker has no session plan yet, so omit those values until a
		// server read model supplies them.
		settings.Model = ""
		settings.ThinkingLevel = ""
		settings.ModelVerbosity = ""
		settings.EnabledTools = nil
		modelName = ""
		thinkingLevel = ""
	}
	statusReq := populateStatusRequestCacheKeys(uiStatusRequest{
		WorkspaceRoot:     workspaceRoot,
		PersistenceRoot:   strings.TrimSpace(cfg.PersistenceRoot),
		Settings:          settings,
		Source:            cfg.Source,
		AuthCacheIdentity: status.AuthCacheIdentity(authState.Resolver),
		AuthStatus:        p.server.AuthStatusClient(),
		AuthStatePath:     strings.TrimSpace(authState.Path),
		ModelName:         modelName,
		ThinkingLevel:     thinkingLevel,
		OwnsServer:        p.server.OwnsServer(),
	})
	return sessionPickerHeaderInfo{
		Version:       config.Version,
		StatusRequest: statusReq,
		AuthManager:   status.NormalizeAuthStateResolver(authState.Resolver),
		OwnsServer:    p != nil && p.server != nil && p.server.OwnsServer(),
		ServerAddress: net.JoinHostPort(cfg.Settings.ServerHost, strconv.Itoa(cfg.Settings.ServerPort)),
	}
}

func (p *launchPlanner) listSessionSummaries(ctx context.Context) ([]clientui.SessionSummary, error) {
	if p == nil || p.server == nil {
		return nil, errors.New("launch planner bootstrap is required")
	}
	if p.server.ProjectViewClient() == nil {
		return nil, errors.New("project view client is required")
	}
	projectID := strings.TrimSpace(p.server.ProjectID())
	if projectID == "" {
		return nil, errors.New("project id is required")
	}
	resp, err := p.server.ProjectViewClient().GetProjectOverview(ctx, serverapi.ProjectGetOverviewRequest{ProjectID: projectID})
	if err != nil {
		return nil, err
	}
	return append([]clientui.SessionSummary(nil), resp.Overview.Sessions...), nil
}

func sessionPlanOverridesFromConfig(cfg config.App) serverapi.RunPromptOverrides {
	sources := cfg.Source.Sources
	overrides := serverapi.RunPromptOverrides{}
	if sourceIsCLI(sources, "model") {
		overrides.Model = cfg.Settings.Model
	}
	if sourceIsCLI(sources, "provider_override") {
		overrides.ProviderOverride = cfg.Settings.ProviderOverride
	}
	if sourceIsCLI(sources, "thinking_level") {
		overrides.ThinkingLevel = cfg.Settings.ThinkingLevel
	}
	if sourceIsCLI(sources, "theme") {
		overrides.Theme = cfg.Settings.Theme
	}
	if sourceIsCLI(sources, "timeouts.model_request_seconds") {
		overrides.ModelTimeoutSeconds = cfg.Settings.Timeouts.ModelRequestSeconds
	}
	if sourceIsCLI(sources, "openai_base_url") {
		overrides.OpenAIBaseURL = cfg.Settings.OpenAIBaseURL
	}
	if hasCLIToolOverride(cfg.Source) {
		overrides.Tools = enabledToolsCSV(cfg.Settings.EnabledTools)
	}
	return overrides
}

func mergeSessionPlanOverrides(base serverapi.RunPromptOverrides, override serverapi.RunPromptOverrides) serverapi.RunPromptOverrides {
	merged := base
	if value := strings.TrimSpace(override.AgentRole); value != "" {
		merged.AgentRole = value
	}
	if value := strings.TrimSpace(override.Model); value != "" {
		merged.Model = value
	}
	if value := strings.TrimSpace(override.ProviderOverride); value != "" {
		merged.ProviderOverride = value
	}
	if value := strings.TrimSpace(override.ThinkingLevel); value != "" {
		merged.ThinkingLevel = value
	}
	if value := strings.TrimSpace(override.Theme); value != "" {
		merged.Theme = value
	}
	if override.ModelTimeoutSeconds > 0 {
		merged.ModelTimeoutSeconds = override.ModelTimeoutSeconds
	}
	if value := strings.TrimSpace(override.Tools); value != "" {
		merged.Tools = value
	}
	if value := strings.TrimSpace(override.OpenAIBaseURL); value != "" {
		merged.OpenAIBaseURL = value
	}
	return merged
}

func sourceIsCLI(sources map[string]string, key string) bool {
	return strings.TrimSpace(sources[key]) == "cli"
}

func hasCLIToolOverride(source config.SourceReport) bool {
	for _, id := range toolspec.CatalogIDs() {
		if sourceIsCLI(source.Sources, "tools."+toolspec.ConfigName(id)) {
			return true
		}
	}
	return false
}

func enabledToolsCSV(enabled map[toolspec.ID]bool) string {
	names := []string{}
	for _, id := range toolspec.CatalogIDs() {
		if enabled[id] {
			names = append(names, toolspec.ConfigName(id))
		}
	}
	return strings.Join(names, ",")
}
