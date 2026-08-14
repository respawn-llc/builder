package startup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"core/server/auth"
	"core/server/authservice"
	serverbootstrap "core/server/bootstrap"
	"core/server/capabilityfacts"
	"core/server/chatcontext"
	"core/server/core"
	"core/server/metadata"
	"core/server/onboarding"
	"core/server/serverstatus"
	"core/server/transport"
	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/protocol"
	"core/shared/serverapi"
	"core/shared/textutil"
)

type ServeServer struct {
	*core.Core
	deps *startupGatewayDependencies
	cfg  config.App
}

func (s *ServeServer) Config() config.App {
	if s == nil {
		return config.App{}
	}
	if s.Core != nil {
		return s.Core.Config()
	}
	if s.deps != nil {
		return cloneStartupConfig(s.deps.loadSnapshot().cfg)
	}
	return s.cfg
}

var localSocketListener = listenLocalSocket
var errStartupControlSurfaceNotRequired = errors.New("startup control surface is not required")

func StartServeServer(ctx context.Context, req Request, authHandler AuthHandler, onboardingHandler OnboardingHandler) (*ServeServer, error) {
	if authHandler == nil {
		return nil, errors.New("auth handler is required")
	}
	bootstrapReq := buildRequest(req, authHandler)
	resolved, err := serverbootstrap.ResolveConfig(bootstrapReq)
	if err != nil {
		return nil, err
	}
	cfg := resolved.Config
	if cfg.Source.SettingsFileExists {
		appCore, err := startCoreWithBootstrap(ctx, bootstrapReq, !req.AllowUnauthenticated, authHandler, onboardingHandler)
		if err != nil {
			return nil, err
		}
		return &ServeServer{Core: appCore, cfg: appCore.Config()}, nil
	}
	if onboardingHandler != nil {
		onboardingCfg, completed, err := runStartupOnboardingHandler(ctx, cfg, bootstrapReq, authHandler, onboardingHandler)
		if err != nil {
			return nil, err
		}
		if completed && onboardingCfg.Source.SettingsFileExists {
			appCore, err := startCoreWithBootstrap(ctx, bootstrapReq, !req.AllowUnauthenticated, authHandler, nil)
			if err != nil {
				return nil, err
			}
			return &ServeServer{Core: appCore, cfg: appCore.Config()}, nil
		}
	}
	cfg, deps, err := buildStartupControlSurface(ctx, bootstrapReq, authHandler)
	if err != nil {
		if errors.Is(err, errStartupControlSurfaceNotRequired) {
			appCore, coreErr := startCoreWithBootstrap(ctx, bootstrapReq, !req.AllowUnauthenticated, authHandler, onboardingHandler)
			if coreErr != nil {
				return nil, coreErr
			}
			return &ServeServer{Core: appCore, cfg: appCore.Config()}, nil
		}
		return nil, err
	}
	return &ServeServer{deps: deps, cfg: cfg}, nil
}

func runStartupOnboardingHandler(ctx context.Context, cfg config.App, bootstrapReq serverbootstrap.Request, authHandler AuthHandler, onboardingHandler OnboardingHandler) (config.App, bool, error) {
	store := authHandler.WrapStore(auth.NewFileStore(config.GlobalAuthConfigPath(cfg)))
	authSupport, err := serverbootstrap.BuildAuthSupport(store, bootstrapReq.LookupEnv, bootstrapReq.Now)
	if err != nil {
		return config.App{}, false, err
	}
	factsService := capabilityfacts.NewService(capabilityfacts.Options{Config: cfg, AuthManager: authSupport.AuthManager})
	reloadConfig := func() (config.App, error) {
		refreshed, err := serverbootstrap.ResolveConfig(bootstrapReq)
		if err != nil {
			return config.App{}, err
		}
		return refreshed.Config, nil
	}
	onboardingCfg, err := onboardingHandler(ctx, OnboardingRequest{
		Config:                cfg,
		AuthManager:           authSupport.AuthManager,
		CapabilityFactsClient: factsService,
		ReloadConfig:          reloadConfig,
	})
	if errors.Is(err, ErrOnboardingRequired) {
		return cfg, false, nil
	}
	if err != nil {
		return config.App{}, false, err
	}
	return onboardingCfg, onboardingCfg.Source.SettingsFileExists, nil
}

func buildStartupControlSurface(ctx context.Context, bootstrapReq serverbootstrap.Request, authHandler AuthHandler) (config.App, *startupGatewayDependencies, error) {
	resolved, err := serverbootstrap.ResolveConfig(bootstrapReq)
	if err != nil {
		return config.App{}, nil, err
	}
	cfg := resolved.Config
	rootLease, err := core.AcquireRootLock(cfg.PersistenceRoot)
	if err != nil {
		return config.App{}, nil, err
	}
	refreshed, err := serverbootstrap.ResolveConfig(bootstrapReq)
	if err != nil {
		_ = rootLease.Close()
		return config.App{}, nil, err
	}
	cfg = refreshed.Config
	if cfg.Source.SettingsFileExists {
		_ = rootLease.Close()
		return config.App{}, nil, errStartupControlSurfaceNotRequired
	}
	store := authHandler.WrapStore(auth.NewFileStore(config.GlobalAuthConfigPath(cfg)))
	authSupport, err := serverbootstrap.BuildAuthSupport(store, bootstrapReq.LookupEnv, bootstrapReq.Now)
	if err != nil {
		_ = rootLease.Close()
		return config.App{}, nil, err
	}
	finalizer, err := onboarding.NewFinalizer(onboarding.Options{
		PersistenceRoot: cfg.PersistenceRoot,
		WorkspaceRoot:   cfg.WorkspaceRoot,
		SettingsPath:    cfg.Source.HomeSettingsPath,
	})
	if err != nil {
		_ = rootLease.Close()
		return config.App{}, nil, err
	}
	return cfg, newStartupGatewayDependencies(ctx, cfg, bootstrapReq, authSupport, rootLease, finalizer), nil
}

func (s *ServeServer) Serve(ctx context.Context) error {
	if ctx == nil {
		return errContextRequired
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil {
		return errors.New("server is required")
	}
	rpc, err := s.startRPC()
	if err != nil {
		return err
	}
	defer s.close()
	select {
	case <-ctx.Done():
		rpc.shutdown()
		rpc.wait()
		return ctx.Err()
	case serveErr := <-rpc.errCh:
		rpc.shutdown()
		rpc.waitRemaining()
		return serveErr
	}
}

func (s *ServeServer) startRPC() (*runningRPC, error) {
	if s.Core != nil {
		return startCoreRPC(s.Core)
	}
	if s.deps == nil {
		return nil, errors.New("startup dependencies are required")
	}
	return startGatewayRPC(s.deps, s.cfg)
}

func (s *ServeServer) Close() error {
	if s == nil {
		return nil
	}
	if s.Core != nil {
		return s.Core.Close()
	}
	if s.deps != nil {
		return s.deps.Close()
	}
	return nil
}

func (s *ServeServer) close() {
	if s == nil || s.deps == nil {
		return
	}
	_ = s.deps.Close()
}

// runningRPC tracks the HTTP servers exposing a Core's control endpoints over
// the bound loopback listeners so they can be shut down together.
type runningRPC struct {
	httpServers []*http.Server
	errCh       chan error
	count       int
}

func (r *runningRPC) shutdown() {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, httpServer := range r.httpServers {
		_ = httpServer.Shutdown(shutdownCtx)
	}
}

func (r *runningRPC) wait() {
	for i := 0; i < r.count; i++ {
		<-r.errCh
	}
}

func (r *runningRPC) waitRemaining() {
	for i := 0; i < r.count-1; i++ {
		<-r.errCh
	}
}

// startCoreRPC binds the configured TCP control endpoint and derived
// same-machine Unix socket and serves the Core control surface. The caller owns
// the returned handle and must call shutdown to release the listeners.
func startCoreRPC(appCore *core.Core) (*runningRPC, error) {
	if appCore == nil {
		return nil, errors.New("server core is required")
	}
	listenCfg := appCore.Config()
	return startGatewayRPC(appCore, listenCfg)
}

func startGatewayRPC(deps transport.GatewayDependencies, listenCfg config.App) (*runningRPC, error) {
	listenAddress := net.JoinHostPort(listenCfg.Settings.ServerHost, strconv.Itoa(listenCfg.Settings.ServerPort))
	tcpListener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return nil, fmt.Errorf("listen local control endpoint: %w", err)
	}
	listeners := []net.Listener{tcpListener}
	cleanupFns := []func(){func() { _ = tcpListener.Close() }}
	if localListener, localCleanup, ok, localErr := localSocketListener(listenCfg); localErr != nil {
		// Derived same-machine UDS is additive only. Configured TCP stays authoritative.
	} else if ok {
		listeners = append(listeners, localListener)
		cleanupFns = append(cleanupFns, localCleanup)
	}
	identity := newServerIdentity(listenCfg)
	gateway, err := transport.NewGateway(deps, identity)
	if err != nil {
		for _, cleanup := range cleanupFns {
			cleanup()
		}
		return nil, err
	}
	mux := buildServerMux(deps, identity, gateway)
	httpServers := make([]*http.Server, 0, len(listeners))
	errCh := make(chan error, len(listeners))
	for _, listener := range listeners {
		httpServer := &http.Server{Handler: mux}
		httpServers = append(httpServers, httpServer)
		go func(server *http.Server, frontend net.Listener) {
			serveErr := server.Serve(frontend)
			if serveErr == nil || errors.Is(serveErr, http.ErrServerClosed) {
				errCh <- nil
				return
			}
			errCh <- serveErr
		}(httpServer, listener)
	}
	return &runningRPC{httpServers: httpServers, errCh: errCh, count: len(listeners)}, nil
}

// newServerIdentity builds the protocol identity stamped on every handshake. The
// PersistenceRootID lets clients confirm an attached server actually serves the
// requested root (see config.PersistenceRootHash and the root-aware attach path).
func newServerIdentity(cfg config.App) protocol.ServerIdentity {
	return protocol.ServerIdentity{
		ProtocolVersion:   protocol.Version,
		ServerID:          fmt.Sprintf(config.Command+":%d", os.Getpid()),
		PID:               os.Getpid(),
		PersistenceRootID: config.PersistenceRootHash(cfg.PersistenceRoot),
	}
}

func buildServerMux(deps transport.GatewayDependencies, identity protocol.ServerIdentity, gateway *transport.Gateway) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc(protocol.HealthPath, func(w http.ResponseWriter, r *http.Request) {
		authReady := serverAuthReady(r.Context(), deps)
		writeStatusJSON(w, http.StatusOK, map[string]any{
			"status":     protocol.HealthStatusOK,
			"server_id":  identity.ServerID,
			"pid":        identity.PID,
			"auth_ready": authReady,
		})
	})
	mux.HandleFunc(protocol.ReadinessPath, func(w http.ResponseWriter, r *http.Request) {
		if readiness, ok := deps.(interface {
			ServerReadinessState() startupReadinessState
		}); ok {
			if state := readiness.ServerReadinessState(); !state.Ready {
				body := map[string]any{
					"ready":           false,
					"transport_ready": true,
					"server_id":       identity.ServerID,
					"pid":             identity.PID,
				}
				if state.Reason != nil {
					body["reason"] = *state.Reason
				}
				if state.Diagnostic != nil {
					body["diagnostic"] = *state.Diagnostic
				}
				writeStatusJSON(w, http.StatusServiceUnavailable, body)
				return
			}
		}
		authReady := serverAuthReady(r.Context(), deps)
		// The mux is only reachable once the listeners are accepting, so the
		// transport is always ready here. Auth gates readiness only when this
		// provider configuration requires startup authentication.
		if !deps.ServerAuthRequired() || authReady {
			writeStatusJSON(w, http.StatusOK, map[string]any{
				"ready":      true,
				"server_id":  identity.ServerID,
				"pid":        identity.PID,
				"auth_ready": authReady,
			})
			return
		}
		writeStatusJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ready":           false,
			"auth_ready":      false,
			"transport_ready": true,
			"server_id":       identity.ServerID,
			"pid":             identity.PID,
		})
	})
	mux.Handle(protocol.RPCPath, gateway.Handler())
	return mux
}

func writeStatusJSON(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func serverAuthReady(ctx context.Context, deps transport.GatewayDependencies) bool {
	if deps == nil || deps.AuthManager() == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	state, err := deps.AuthManager().Load(ctx)
	if err != nil {
		return false
	}
	return auth.EvaluateStartupGate(state).Ready
}

type startupGatewayDependencies struct {
	mu          sync.RWMutex
	cfg         config.App
	bootstrap   serverbootstrap.Request
	authSupport serverbootstrap.AuthSupport
	rootLease   *core.RootLockLease
	finalizer   apicontract.OnboardingFinalizeService
	core        *core.Core
	buildCore   startupCoreBuilder
	snapshot    atomic.Pointer[startupDependencySnapshot]
}

type startupCoreBuilder func(context.Context, config.App, serverbootstrap.AuthSupport, serverbootstrap.RuntimeSupport, core.Options) (*core.Core, error)

type startupDependencySnapshot struct {
	cfg       config.App
	readiness startupReadinessState
	core      *core.Core
}

func newStartupGatewayDependencies(ctx context.Context, cfg config.App, bootstrapReq serverbootstrap.Request, authSupport serverbootstrap.AuthSupport, rootLease *core.RootLockLease, finalizer *onboarding.Finalizer) *startupGatewayDependencies {
	if ctx == nil {
		ctx = context.Background()
	}
	deps := &startupGatewayDependencies{cfg: cfg, bootstrap: bootstrapReq, authSupport: authSupport, rootLease: rootLease, buildCore: core.NewWithContextOptions}
	reason := serverapi.ServerNotReadyOnboardingRequired
	deps.publishSnapshotLocked(startupReadinessState{Reason: &reason}, nil)
	deps.finalizer = startupFinalizeService{service: finalizer, activate: deps.activate, activationContext: ctx}
	return deps
}

func (d *startupGatewayDependencies) Close() error {
	d.mu.Lock()
	if d.core != nil {
		appCore := d.core
		d.core = nil
		reason := serverapi.ServerNotReadyOnboardingRequired
		d.publishSnapshotLocked(startupReadinessState{Reason: &reason}, nil)
		d.mu.Unlock()
		return appCore.Close()
	}
	if d.rootLease != nil {
		err := d.rootLease.Close()
		d.rootLease = nil
		d.mu.Unlock()
		return err
	}
	d.mu.Unlock()
	return nil
}

func (d *startupGatewayDependencies) activate(ctx context.Context, resp serverapi.OnboardingFinalizeResponse) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.core != nil {
		return nil
	}
	refreshed, err := serverbootstrap.ResolveConfig(d.bootstrap)
	if err != nil {
		return d.activationError(resp, err)
	}
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(refreshed.Config)
	if err != nil {
		return d.activationError(resp, err)
	}
	buildCore := d.buildCore
	if buildCore == nil {
		buildCore = core.NewWithContextOptions
	}
	appCore, err := buildCore(
		ctx,
		refreshed.Config,
		d.authSupport,
		runtimeSupport,
		coreOptionsForBootstrap(d.bootstrap, d.rootLease),
	)
	if err != nil {
		_ = runtimeSupport.Background.Close()
		return d.activationError(resp, err)
	}
	d.core = appCore
	d.rootLease = nil
	d.cfg = refreshed.Config
	d.publishSnapshotLocked(startupReadinessState{Ready: true}, appCore)
	return nil
}

func (d *startupGatewayDependencies) activationError(resp serverapi.OnboardingFinalizeResponse, err error) error {
	reason := serverapi.ServerNotReadyActivationFailed
	diagnostic := err.Error()
	d.publishSnapshotLocked(startupReadinessState{Reason: &reason, Diagnostic: &diagnostic}, nil)
	settingsPath := resp.SettingsPath
	return serverapi.NewServerNotReadyError(serverapi.ServerNotReadyActivationFailed, serverapi.ServerNotReadyDetails{
		OnboardingCompleted: true,
		SettingsPath:        &settingsPath,
		Diagnostic:          &diagnostic,
	}, err)
}

func (d *startupGatewayDependencies) activeCore() *core.Core {
	return d.loadSnapshot().core
}

func (d *startupGatewayDependencies) RequireCoreActive() error {
	snapshot := d.loadSnapshot()
	if snapshot.core != nil {
		return nil
	}
	reason := serverapi.ServerNotReadyOnboardingRequired
	if snapshot.readiness.Reason != nil {
		reason = *snapshot.readiness.Reason
	}
	var details any
	if snapshot.readiness.Diagnostic != nil {
		details = serverapi.ServerNotReadyDetails{Diagnostic: snapshot.readiness.Diagnostic}
	}
	return serverapi.NewServerNotReadyError(reason, details, nil)
}

type startupReadinessState struct {
	Ready      bool
	Reason     *serverapi.ServerNotReadyReason
	Diagnostic *string
}

func (d *startupGatewayDependencies) ServerReadinessState() startupReadinessState {
	return cloneStartupReadiness(d.loadSnapshot().readiness)
}

func (d *startupGatewayDependencies) loadSnapshot() startupDependencySnapshot {
	if d == nil {
		return startupDependencySnapshot{}
	}
	snapshot := d.snapshot.Load()
	if snapshot == nil {
		return startupDependencySnapshot{}
	}
	return *snapshot
}

func (d *startupGatewayDependencies) DebugEnabled() bool {
	return d.loadSnapshot().cfg.Settings.Debug
}

type startupServerStatusService struct {
	base       apicontract.ServerStatusService
	readiness  startupReadinessState
	activeCore *core.Core
}

func (s startupServerStatusService) GetServerReadiness(ctx context.Context, req serverapi.ServerReadinessRequest) (serverapi.ServerReadinessResponse, error) {
	resp, err := s.base.GetServerReadiness(ctx, req)
	if err != nil {
		return serverapi.ServerReadinessResponse{}, err
	}
	if state := s.readiness; !state.Ready {
		resp.Ready = false
		diagnosticID := ""
		if state.Reason != nil {
			diagnosticID = string(*state.Reason)
		}
		if state.Diagnostic != nil {
			diagnosticID += ":" + *state.Diagnostic
		}
		code := "server_not_ready"
		if state.Reason != nil {
			code = string(*state.Reason)
		}
		cause := serverapi.ServerReadinessCause{
			Code:     code,
			Severity: "error",
		}
		if len(resp.Causes) > 0 {
			cause = resp.Causes[0]
			cause.Code = code
			cause.Severity = "error"
			cause.Summary = nil
			cause.NextAction = nil
		}
		cause.DiagnosticID = diagnosticID
		resp.Causes = []serverapi.ServerReadinessCause{cause}
	}
	return resp, nil
}

func (s startupServerStatusService) GetUpdateStatus(ctx context.Context, req serverapi.UpdateStatusRequest) (serverapi.UpdateStatusResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.UpdateStatusResponse{}, err
	}
	if state := s.readiness; !state.Ready {
		reason := serverapi.ServerNotReadyOnboardingRequired
		if state.Reason != nil {
			reason = *state.Reason
		}
		var details any
		if state.Diagnostic != nil {
			details = serverapi.ServerNotReadyDetails{Diagnostic: state.Diagnostic}
		}
		return serverapi.UpdateStatusResponse{}, serverapi.NewServerNotReadyError(reason, details, nil)
	}
	if s.activeCore == nil {
		return serverapi.UpdateStatusResponse{}, serverapi.NewServerNotReadyError(serverapi.ServerNotReadyOnboardingRequired, nil, nil)
	}
	return s.activeCore.ServerStatusClient().GetUpdateStatus(ctx, req)
}

type startupFinalizeService struct {
	service           apicontract.OnboardingFinalizeService
	activate          func(context.Context, serverapi.OnboardingFinalizeResponse) error
	activationContext context.Context
}

type startupAuthBootstrapService struct {
	status *authservice.BootstrapService
	deps   *startupGatewayDependencies
}

func (s startupAuthBootstrapService) GetAuthBootstrapStatus(ctx context.Context, req serverapi.AuthGetBootstrapStatusRequest) (serverapi.AuthGetBootstrapStatusResponse, error) {
	return s.status.GetAuthBootstrapStatus(ctx, req)
}

func (s startupAuthBootstrapService) CompleteAuthBootstrap(ctx context.Context, req serverapi.AuthCompleteBootstrapRequest) (serverapi.AuthCompleteBootstrapResponse, error) {
	s.deps.mu.Lock()
	defer s.deps.mu.Unlock()
	return s.deps.authBootstrapMutationService().CompleteAuthBootstrap(ctx, req)
}

func (s startupAuthBootstrapService) AcknowledgeNoAuth(ctx context.Context, req serverapi.AuthAcknowledgeNoAuthRequest) (serverapi.AuthAcknowledgeNoAuthResponse, error) {
	s.deps.mu.Lock()
	defer s.deps.mu.Unlock()
	return s.deps.authBootstrapMutationService().AcknowledgeNoAuth(ctx, req)
}

func (d *startupGatewayDependencies) authBootstrapMutationService() *authservice.BootstrapService {
	settings := d.loadSnapshot().cfg.Settings
	return authservice.NewBootstrapService(d.authSupport.AuthManager, d.authSupport.OAuthOptions, settings, apicontract.AllowedPreAuthMethods())
}

func (d *startupGatewayDependencies) publishSnapshotLocked(readiness startupReadinessState, appCore *core.Core) {
	snapshot := &startupDependencySnapshot{
		cfg:       cloneStartupConfig(d.cfg),
		readiness: cloneStartupReadiness(readiness),
		core:      appCore,
	}
	d.snapshot.Store(snapshot)
}

func cloneStartupReadiness(readiness startupReadinessState) startupReadinessState {
	readiness.Reason = textutil.Pointer(readiness.Reason)
	readiness.Diagnostic = textutil.Pointer(readiness.Diagnostic)
	return readiness
}

func cloneStartupConfig(cfg config.App) config.App {
	cfg.Settings = cloneStartupSettings(cfg.Settings)
	cfg.Source.Sources = maps.Clone(cfg.Source.Sources)
	return cfg
}

func cloneStartupSettings(settings config.Settings) config.Settings {
	cloned := settings
	cloned.SystemPromptFiles = append([]config.SystemPromptFile(nil), settings.SystemPromptFiles...)
	cloned.EnabledTools = maps.Clone(settings.EnabledTools)
	cloned.SkillToggles = maps.Clone(settings.SkillToggles)
	cloned.Shell.PostprocessHook = textutil.Pointer(settings.Shell.PostprocessHook)
	cloned.Workflow.PreCompactionTokens = textutil.Pointer(settings.Workflow.PreCompactionTokens)
	if settings.Subagents != nil {
		cloned.Subagents = make(map[string]config.SubagentRole, len(settings.Subagents))
		for name, role := range settings.Subagents {
			copied := role
			copied.Settings = cloneStartupSettings(role.Settings)
			copied.Sources = maps.Clone(role.Sources)
			cloned.Subagents[name] = copied
		}
	}
	return cloned
}

func (s startupFinalizeService) FinalizeOnboarding(ctx context.Context, req serverapi.OnboardingFinalizeRequest) (serverapi.OnboardingFinalizeResponse, error) {
	resp, err := s.service.FinalizeOnboarding(ctx, req)
	if err != nil {
		return serverapi.OnboardingFinalizeResponse{}, err
	}
	if s.activate != nil {
		activationCtx := s.activationContext
		if activationCtx == nil {
			activationCtx = context.Background()
		}
		if err := s.activate(activationCtx, resp); err != nil {
			return serverapi.OnboardingFinalizeResponse{}, err
		}
	}
	return resp, nil
}

func (d *startupGatewayDependencies) AuthManager() *auth.Manager { return d.authSupport.AuthManager }
func (d *startupGatewayDependencies) AuthBootstrapClient() apicontract.AuthBootstrapService {
	snapshot := d.loadSnapshot()
	return startupAuthBootstrapService{
		status: authservice.NewBootstrapService(d.authSupport.AuthManager, d.authSupport.OAuthOptions, snapshot.cfg.Settings, apicontract.AllowedPreAuthMethods()),
		deps:   d,
	}
}
func (d *startupGatewayDependencies) AuthStatusClient() apicontract.AuthStatusService {
	return authservice.NewStatusService(d.authSupport.AuthManager, d.loadSnapshot().cfg.Settings)
}
func (d *startupGatewayDependencies) CapabilityFactsClient() apicontract.CapabilityFactsService {
	return capabilityfacts.NewService(capabilityfacts.Options{Config: d.loadSnapshot().cfg, AuthManager: d.authSupport.AuthManager})
}
func (d *startupGatewayDependencies) ServerStatusClient() apicontract.ServerStatusService {
	snapshot := d.loadSnapshot()
	return startupServerStatusService{
		base:       serverstatus.NewServerStatusService(d.authSupport.AuthManager, snapshot.cfg, nil),
		readiness:  snapshot.readiness,
		activeCore: snapshot.core,
	}
}
func (d *startupGatewayDependencies) ServerAuthRequired() bool {
	return authservice.StartupAuthRequired(d.loadSnapshot().cfg.Settings)
}
func (d *startupGatewayDependencies) OnboardingFinalizeClient() apicontract.OnboardingFinalizeService {
	return d.finalizer
}

func (d *startupGatewayDependencies) MetadataStore() *metadata.Store {
	if c := d.activeCore(); c != nil {
		return c.MetadataStore()
	}
	return nil
}
func (d *startupGatewayDependencies) ProjectID() string {
	if c := d.activeCore(); c != nil {
		return c.ProjectID()
	}
	return ""
}
func (d *startupGatewayDependencies) ProjectExists(ctx context.Context, projectID string) error {
	if c := d.activeCore(); c != nil {
		return c.ProjectExists(ctx, projectID)
	}
	return serverapi.NewServerNotReadyError(serverapi.ServerNotReadyOnboardingRequired, nil, nil)
}
func (d *startupGatewayDependencies) ProjectViewClient() apicontract.ProjectViewService {
	if c := d.activeCore(); c != nil {
		return c.ProjectViewClient()
	}
	return nil
}
func (d *startupGatewayDependencies) WorkflowClient() apicontract.WorkflowService {
	if c := d.activeCore(); c != nil {
		return c.WorkflowClient()
	}
	return nil
}
func (d *startupGatewayDependencies) SessionBelongsToProject(ctx context.Context, sessionID string, projectID string) error {
	if c := d.activeCore(); c != nil {
		return c.SessionBelongsToProject(ctx, sessionID, projectID)
	}
	return serverapi.NewServerNotReadyError(serverapi.ServerNotReadyOnboardingRequired, nil, nil)
}
func (d *startupGatewayDependencies) SessionViewClient() apicontract.SessionViewService {
	if c := d.activeCore(); c != nil {
		return c.SessionViewClient()
	}
	return nil
}
func (d *startupGatewayDependencies) ChatSettingsClient() apicontract.ChatSettingsService {
	if c := d.activeCore(); c != nil {
		return c.ChatSettingsClient()
	}
	return nil
}
func (d *startupGatewayDependencies) SessionLifecycleClient() apicontract.SessionLifecycleService {
	if c := d.activeCore(); c != nil {
		return c.SessionLifecycleClient()
	}
	return nil
}
func (d *startupGatewayDependencies) SessionRuntimeClient() apicontract.SessionRuntimeService {
	if c := d.activeCore(); c != nil {
		return c.SessionRuntimeClient()
	}
	return nil
}
func (d *startupGatewayDependencies) SessionTranscriptClient() apicontract.SessionTranscriptService {
	if c := d.activeCore(); c != nil {
		return c.SessionTranscriptClient()
	}
	return nil
}
func (d *startupGatewayDependencies) SessionLaunchClientForProjectWorkspace(ctx context.Context, projectID string, workspaceRoot string) (apicontract.SessionLaunchService, error) {
	if c := d.activeCore(); c != nil {
		return c.SessionLaunchClientForProjectWorkspace(ctx, projectID, workspaceRoot)
	}
	return nil, serverapi.NewServerNotReadyError(serverapi.ServerNotReadyOnboardingRequired, nil, nil)
}
func (d *startupGatewayDependencies) SessionLaunchClientForProjectWorkspaceID(ctx context.Context, projectID string, workspaceID string) (apicontract.SessionLaunchService, error) {
	if c := d.activeCore(); c != nil {
		return c.SessionLaunchClientForProjectWorkspaceID(ctx, projectID, workspaceID)
	}
	return nil, serverapi.NewServerNotReadyError(serverapi.ServerNotReadyOnboardingRequired, nil, nil)
}
func (d *startupGatewayDependencies) WorkspaceChatContextOwnerForProjectWorkspace(ctx context.Context, projectID string, workspaceRoot string) (chatcontext.WorkspaceOwner, error) {
	if c := d.activeCore(); c != nil {
		return c.WorkspaceChatContextOwnerForProjectWorkspace(ctx, projectID, workspaceRoot)
	}
	return nil, serverapi.NewServerNotReadyError(serverapi.ServerNotReadyOnboardingRequired, nil, nil)
}
func (d *startupGatewayDependencies) WorkspaceChatContextOwnerForProjectWorkspaceID(ctx context.Context, projectID string, workspaceID string) (chatcontext.WorkspaceOwner, error) {
	if c := d.activeCore(); c != nil {
		return c.WorkspaceChatContextOwnerForProjectWorkspaceID(ctx, projectID, workspaceID)
	}
	return nil, serverapi.NewServerNotReadyError(serverapi.ServerNotReadyOnboardingRequired, nil, nil)
}
func (d *startupGatewayDependencies) SessionChatContextOwner() chatcontext.SessionOwner {
	if c := d.activeCore(); c != nil {
		return c.SessionChatContextOwner()
	}
	return nil
}
func (d *startupGatewayDependencies) RunPromptClientForProjectWorkspace(ctx context.Context, projectID string, workspaceRoot string) (apicontract.RunPromptService, error) {
	if c := d.activeCore(); c != nil {
		return c.RunPromptClientForProjectWorkspace(ctx, projectID, workspaceRoot)
	}
	return nil, serverapi.NewServerNotReadyError(serverapi.ServerNotReadyOnboardingRequired, nil, nil)
}
func (d *startupGatewayDependencies) RunPromptClientForProjectWorkspaceID(ctx context.Context, projectID string, workspaceID string) (apicontract.RunPromptService, error) {
	if c := d.activeCore(); c != nil {
		return c.RunPromptClientForProjectWorkspaceID(ctx, projectID, workspaceID)
	}
	return nil, serverapi.NewServerNotReadyError(serverapi.ServerNotReadyOnboardingRequired, nil, nil)
}
func (d *startupGatewayDependencies) PromptCommandCatalogClientForProjectWorkspace(ctx context.Context, projectID string, workspaceRoot string) (apicontract.PromptCommandCatalogService, error) {
	if c := d.activeCore(); c != nil {
		return c.PromptCommandCatalogClientForProjectWorkspace(ctx, projectID, workspaceRoot)
	}
	return nil, serverapi.NewServerNotReadyError(serverapi.ServerNotReadyOnboardingRequired, nil, nil)
}
func (d *startupGatewayDependencies) RuntimeControlClient() apicontract.RuntimeControlService {
	if c := d.activeCore(); c != nil {
		return c.RuntimeControlClient()
	}
	return nil
}
func (d *startupGatewayDependencies) RuntimeLiveControlClient() apicontract.RuntimeLiveControlService {
	if c := d.activeCore(); c != nil {
		return c.RuntimeLiveControlClient()
	}
	return nil
}
func (d *startupGatewayDependencies) AskViewClient() apicontract.AskViewService {
	if c := d.activeCore(); c != nil {
		return c.AskViewClient()
	}
	return nil
}
func (d *startupGatewayDependencies) ApprovalViewClient() apicontract.ApprovalViewService {
	if c := d.activeCore(); c != nil {
		return c.ApprovalViewClient()
	}
	return nil
}
func (d *startupGatewayDependencies) PromptControlClient() apicontract.PromptControlService {
	if c := d.activeCore(); c != nil {
		return c.PromptControlClient()
	}
	return nil
}
func (d *startupGatewayDependencies) AttentionNotificationClient() apicontract.AttentionNotificationService {
	if c := d.activeCore(); c != nil {
		return c.AttentionNotificationClient()
	}
	return nil
}
func (d *startupGatewayDependencies) ProcessViewClient() apicontract.ProcessViewService {
	if c := d.activeCore(); c != nil {
		return c.ProcessViewClient()
	}
	return nil
}
func (d *startupGatewayDependencies) ProcessControlClient() apicontract.ProcessControlService {
	if c := d.activeCore(); c != nil {
		return c.ProcessControlClient()
	}
	return nil
}
func (d *startupGatewayDependencies) WorktreeClient() apicontract.WorktreeService {
	if c := d.activeCore(); c != nil {
		return c.WorktreeClient()
	}
	return nil
}
