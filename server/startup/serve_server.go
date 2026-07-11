package startup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"core/server/auth"
	"core/server/authservice"
	serverbootstrap "core/server/bootstrap"
	"core/server/capabilityfacts"
	"core/server/core"
	"core/server/metadata"
	"core/server/onboarding"
	"core/server/serverstatus"
	"core/server/transport"
	rpccontract "core/shared/apicontract"
	"core/shared/client"
	"core/shared/config"
	"core/shared/protocol"
	"core/shared/serverapi"
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
		return s.deps.snapshotConfig()
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
		appCore, err := startCoreWithBootstrap(ctx, bootstrapReq, !req.AllowUnauthenticated, authHandler, onboardingHandler, Options{})
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
			appCore, err := startCoreWithBootstrap(ctx, bootstrapReq, !req.AllowUnauthenticated, authHandler, nil, Options{})
			if err != nil {
				return nil, err
			}
			return &ServeServer{Core: appCore, cfg: appCore.Config()}, nil
		}
	}
	cfg, deps, err := buildStartupControlSurface(ctx, bootstrapReq, !req.AllowUnauthenticated, authHandler, Options{})
	if err != nil {
		if errors.Is(err, errStartupControlSurfaceNotRequired) {
			appCore, coreErr := startCoreWithBootstrap(ctx, bootstrapReq, !req.AllowUnauthenticated, authHandler, onboardingHandler, Options{})
			if coreErr != nil {
				return nil, coreErr
			}
			return &ServeServer{Core: appCore, cfg: appCore.Config()}, nil
		}
		return nil, err
	}
	return &ServeServer{deps: deps, cfg: cfg}, nil
}

func runStartupOnboardingHandler(ctx context.Context, cfg config.App, bootstrapReq serverbootstrap.Request, authHandler startupAuthHandler, onboardingHandler OnboardingHandler) (config.App, bool, error) {
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
		CapabilityFactsClient: client.NewLoopbackCapabilityFactsClient(factsService),
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

func buildStartupControlSurface(ctx context.Context, bootstrapReq serverbootstrap.Request, _ bool, authHandler startupAuthHandler, opts Options) (config.App, *startupGatewayDependencies, error) {
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
	return cfg, newStartupGatewayDependencies(ctx, cfg, bootstrapReq, authSupport, rootLease, finalizer, opts.Core), nil
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

// startCoreRPC binds the configured TCP control endpoint and the derived
// same-machine Unix socket (when available) and serves the Core's health,
// readiness, and JSON-RPC handlers over them in background goroutines. It is
// shared by the standalone serve daemon and the embedded interactive server so
// both expose the same control surface for client attach. The caller owns the
// returned handle and must call shutdown to release the listeners.
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
		Capabilities:      serverCapabilityFlags(rpccontract.Routes()),
	}
}

func serverCapabilityFlags(routes []rpccontract.Route) protocol.CapabilityFlags {
	methods := make(map[string]struct{}, len(routes))
	dependencies := make(map[rpccontract.Dependency]struct{}, len(routes))
	for _, route := range routes {
		methods[route.Method] = struct{}{}
		dependencies[route.Dependency] = struct{}{}
	}
	hasMethod := func(method string) bool {
		_, ok := methods[method]
		return ok
	}
	hasDependency := func(dependency rpccontract.Dependency) bool {
		_, ok := dependencies[dependency]
		return ok
	}
	methodHasDependency := func(method string, dependency rpccontract.Dependency) bool {
		for _, route := range routes {
			if route.Method == method && route.Dependency == dependency {
				return true
			}
		}
		return false
	}
	return protocol.CapabilityFlags{
		JSONRPCWebSocket:       hasMethod(protocol.MethodHandshake),
		AuthBootstrap:          hasDependency(rpccontract.DependencyAuthBootstrap),
		ProjectAttach:          methodHasDependency(protocol.MethodAttachProject, rpccontract.DependencyProtocolAttach),
		SessionAttach:          methodHasDependency(protocol.MethodAttachSession, rpccontract.DependencyProtocolAttach),
		HealthEndpoint:         true,
		ReadinessEndpoint:      true,
		RunPrompt:              hasDependency(rpccontract.DependencyRunPrompt),
		SessionPlan:            hasMethod(protocol.MethodSessionPlan),
		SessionLifecycle:       hasDependency(rpccontract.DependencySessionLifecycle),
		SessionTranscript:      hasDependency(rpccontract.DependencySessionTranscript),
		SessionRuntime:         hasDependency(rpccontract.DependencySessionRuntime),
		RuntimeControl:         hasDependency(rpccontract.DependencyRuntimeControl),
		RuntimeLiveControl:     hasDependency(rpccontract.DependencyRuntimeControl) && transport.RuntimeLiveControlRoutesExecutable(),
		PromptControl:          hasDependency(rpccontract.DependencyPromptControl),
		PromptActivity:         hasDependency(rpccontract.DependencyPromptActivity),
		SessionActivity:        hasDependency(rpccontract.DependencySessionActivity),
		ProcessOutput:          hasDependency(rpccontract.DependencyProcessOutput),
		AttentionNotifications: hasDependency(rpccontract.DependencyAttentionNotification),
		OnboardingFinalize:     hasDependency(rpccontract.DependencyOnboardingFinalize),
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
	finalizer   client.OnboardingFinalizeClient
	coreOptions core.Options
	core        *core.Core
	retryCore   *core.Core
	activation  error
}

func newStartupGatewayDependencies(ctx context.Context, cfg config.App, bootstrapReq serverbootstrap.Request, authSupport serverbootstrap.AuthSupport, rootLease *core.RootLockLease, finalizer *onboarding.Finalizer, coreOptions core.Options) *startupGatewayDependencies {
	if ctx == nil {
		ctx = context.Background()
	}
	deps := &startupGatewayDependencies{cfg: cfg, bootstrap: bootstrapReq, authSupport: authSupport, rootLease: rootLease, coreOptions: coreOptions}
	deps.finalizer = startupFinalizeService{service: finalizer, activate: deps.activate, activationContext: ctx}
	return deps
}

func (d *startupGatewayDependencies) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.core != nil {
		return d.core.Close()
	}
	if d.retryCore != nil {
		err := d.retryCore.Close()
		if err == nil {
			d.retryCore = nil
			d.rootLease = nil
		}
		return err
	}
	if d.rootLease != nil {
		err := d.rootLease.Close()
		d.rootLease = nil
		return err
	}
	return nil
}

func (d *startupGatewayDependencies) activate(ctx context.Context, resp serverapi.OnboardingFinalizeResponse) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.core != nil {
		return nil
	}
	if d.retryCore != nil {
		err := errors.New("previous activation cleanup must complete before activation can retry")
		d.activation = err
		return serverapi.NewServerNotReadyError(serverapi.ServerNotReadyActivationFailed, activationFailureDetails(resp, err), err)
	}
	refreshed, err := serverbootstrap.ResolveConfig(d.bootstrap)
	if err != nil {
		d.activation = err
		return serverapi.NewServerNotReadyError(serverapi.ServerNotReadyActivationFailed, activationFailureDetails(resp, err), err)
	}
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(refreshed.Config)
	if err != nil {
		d.activation = err
		return serverapi.NewServerNotReadyError(serverapi.ServerNotReadyActivationFailed, activationFailureDetails(resp, err), err)
	}
	coreOptions := d.coreOptions
	coreOptions.RootLease = d.rootLease
	appCore, err := core.NewWithContextOptions(ctx, refreshed.Config, d.authSupport, runtimeSupport, coreOptions)
	if err != nil {
		if retained, ok := core.RetainedStartupCleanupCore(err); ok {
			d.retryCore = retained
			d.rootLease = nil
		} else {
			_ = runtimeSupport.Background.Close()
		}
		d.activation = err
		return serverapi.NewServerNotReadyError(serverapi.ServerNotReadyActivationFailed, activationFailureDetails(resp, err), err)
	}
	d.core = appCore
	d.rootLease = nil
	d.cfg = refreshed.Config
	d.activation = nil
	return nil
}

func activationFailureDetails(resp serverapi.OnboardingFinalizeResponse, err error) serverapi.ServerNotReadyDetails {
	settingsPath := resp.SettingsPath
	diagnostic := err.Error()
	return serverapi.ServerNotReadyDetails{OnboardingCompleted: true, SettingsPath: &settingsPath, Diagnostic: &diagnostic}
}

func (d *startupGatewayDependencies) activeCore() *core.Core {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.core
}

func (d *startupGatewayDependencies) RouteDependencyAvailable(dep rpccontract.Dependency) error {
	switch dep {
	case rpccontract.DependencyProtocol, rpccontract.DependencyServerStatus, rpccontract.DependencyAuthBootstrap, rpccontract.DependencyAuthStatus, rpccontract.DependencyCapabilityFacts, rpccontract.DependencyOnboardingFinalize:
		return nil
	default:
		d.mu.RLock()
		defer d.mu.RUnlock()
		if d.core != nil {
			return nil
		}
		if d.activation != nil {
			diagnostic := d.activation.Error()
			return serverapi.NewServerNotReadyError(serverapi.ServerNotReadyActivationFailed, serverapi.ServerNotReadyDetails{Diagnostic: &diagnostic}, d.activation)
		}
		return serverapi.NewServerNotReadyError(serverapi.ServerNotReadyOnboardingRequired, nil, nil)
	}
}

type startupReadinessState struct {
	Ready      bool
	Reason     *serverapi.ServerNotReadyReason
	Diagnostic *string
}

func (d *startupGatewayDependencies) ServerReadinessState() startupReadinessState {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.core != nil {
		return startupReadinessState{Ready: true}
	}
	if d.activation != nil {
		reason := serverapi.ServerNotReadyActivationFailed
		diagnostic := d.activation.Error()
		return startupReadinessState{Reason: &reason, Diagnostic: &diagnostic}
	}
	reason := serverapi.ServerNotReadyOnboardingRequired
	return startupReadinessState{Reason: &reason}
}

func (d *startupGatewayDependencies) snapshotConfig() config.App {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.cfg
}

type startupServerStatusService struct {
	base      client.ServerStatusClient
	readiness interface {
		ServerReadinessState() startupReadinessState
	}
}

func (s startupServerStatusService) GetServerReadiness(ctx context.Context, req serverapi.ServerReadinessRequest) (serverapi.ServerReadinessResponse, error) {
	resp, err := s.base.GetServerReadiness(ctx, req)
	if err != nil {
		return serverapi.ServerReadinessResponse{}, err
	}
	if s.readiness == nil {
		return resp, nil
	}
	if state := s.readiness.ServerReadinessState(); !state.Ready {
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

type startupFinalizeService struct {
	service           client.OnboardingFinalizeClient
	activate          func(context.Context, serverapi.OnboardingFinalizeResponse) error
	activationContext context.Context
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
func (d *startupGatewayDependencies) AuthBootstrapClient() client.AuthBootstrapClient {
	cfg := d.snapshotConfig()
	return client.NewLoopbackAuthBootstrapClient(authservice.NewBootstrapService(d.authSupport.AuthManager, d.authSupport.OAuthOptions, cfg.Settings, rpccontract.AllowedPreAuthMethods()))
}
func (d *startupGatewayDependencies) AuthStatusClient() client.AuthStatusClient {
	cfg := d.snapshotConfig()
	return client.NewLoopbackAuthStatusClient(authservice.NewStatusService(d.authSupport.AuthManager, cfg.Settings))
}
func (d *startupGatewayDependencies) CapabilityFactsClient() client.CapabilityFactsClient {
	cfg := d.snapshotConfig()
	return client.NewLoopbackCapabilityFactsClient(capabilityfacts.NewService(capabilityfacts.Options{Config: cfg, AuthManager: d.authSupport.AuthManager}))
}
func (d *startupGatewayDependencies) ServerStatusClient() client.ServerStatusClient {
	cfg := d.snapshotConfig()
	return client.NewLoopbackServerStatusClient(startupServerStatusService{base: serverstatus.NewServerStatusService(d.authSupport.AuthManager, cfg), readiness: d})
}
func (d *startupGatewayDependencies) ServerAuthRequired() bool {
	cfg := d.snapshotConfig()
	return authservice.StartupAuthRequired(cfg.Settings)
}
func (d *startupGatewayDependencies) OnboardingFinalizeClient() client.OnboardingFinalizeClient {
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
func (d *startupGatewayDependencies) ProjectViewClient() client.ProjectViewClient {
	if c := d.activeCore(); c != nil {
		return c.ProjectViewClient()
	}
	return nil
}
func (d *startupGatewayDependencies) WorkflowClient() client.WorkflowClient {
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
func (d *startupGatewayDependencies) SessionViewClient() client.SessionViewClient {
	if c := d.activeCore(); c != nil {
		return c.SessionViewClient()
	}
	return nil
}
func (d *startupGatewayDependencies) SessionLifecycleClient() client.SessionLifecycleClient {
	if c := d.activeCore(); c != nil {
		return c.SessionLifecycleClient()
	}
	return nil
}
func (d *startupGatewayDependencies) SessionRuntimeClient() client.SessionRuntimeClient {
	if c := d.activeCore(); c != nil {
		return c.SessionRuntimeClient()
	}
	return nil
}
func (d *startupGatewayDependencies) SessionActivityClient() client.SessionActivityClient {
	if c := d.activeCore(); c != nil {
		return c.SessionActivityClient()
	}
	return nil
}
func (d *startupGatewayDependencies) SessionTranscriptClient() client.SessionTranscriptClient {
	if c := d.activeCore(); c != nil {
		return c.SessionTranscriptClient()
	}
	return nil
}
func (d *startupGatewayDependencies) SessionLaunchClientForProjectWorkspace(ctx context.Context, projectID string, workspaceRoot string) (client.SessionLaunchClient, error) {
	if c := d.activeCore(); c != nil {
		return c.SessionLaunchClientForProjectWorkspace(ctx, projectID, workspaceRoot)
	}
	return nil, serverapi.NewServerNotReadyError(serverapi.ServerNotReadyOnboardingRequired, nil, nil)
}
func (d *startupGatewayDependencies) SessionLaunchClientForProjectWorkspaceID(ctx context.Context, projectID string, workspaceID string) (client.SessionLaunchClient, error) {
	if c := d.activeCore(); c != nil {
		return c.SessionLaunchClientForProjectWorkspaceID(ctx, projectID, workspaceID)
	}
	return nil, serverapi.NewServerNotReadyError(serverapi.ServerNotReadyOnboardingRequired, nil, nil)
}
func (d *startupGatewayDependencies) RunPromptClientForProjectWorkspace(ctx context.Context, projectID string, workspaceRoot string) (client.RunPromptClient, error) {
	if c := d.activeCore(); c != nil {
		return c.RunPromptClientForProjectWorkspace(ctx, projectID, workspaceRoot)
	}
	return nil, serverapi.NewServerNotReadyError(serverapi.ServerNotReadyOnboardingRequired, nil, nil)
}
func (d *startupGatewayDependencies) RunPromptClientForProjectWorkspaceID(ctx context.Context, projectID string, workspaceID string) (client.RunPromptClient, error) {
	if c := d.activeCore(); c != nil {
		return c.RunPromptClientForProjectWorkspaceID(ctx, projectID, workspaceID)
	}
	return nil, serverapi.NewServerNotReadyError(serverapi.ServerNotReadyOnboardingRequired, nil, nil)
}
func (d *startupGatewayDependencies) RuntimeControlClient() client.RuntimeControlClient {
	if c := d.activeCore(); c != nil {
		return c.RuntimeControlClient()
	}
	return nil
}
func (d *startupGatewayDependencies) RuntimeLiveControlClient() client.RuntimeLiveControlClient {
	if c := d.activeCore(); c != nil {
		return c.RuntimeLiveControlClient()
	}
	return nil
}
func (d *startupGatewayDependencies) AskViewClient() client.AskViewClient {
	if c := d.activeCore(); c != nil {
		return c.AskViewClient()
	}
	return nil
}
func (d *startupGatewayDependencies) ApprovalViewClient() client.ApprovalViewClient {
	if c := d.activeCore(); c != nil {
		return c.ApprovalViewClient()
	}
	return nil
}
func (d *startupGatewayDependencies) PromptControlClient() client.PromptControlClient {
	if c := d.activeCore(); c != nil {
		return c.PromptControlClient()
	}
	return nil
}
func (d *startupGatewayDependencies) PromptActivityClient() client.PromptActivityClient {
	if c := d.activeCore(); c != nil {
		return c.PromptActivityClient()
	}
	return nil
}
func (d *startupGatewayDependencies) AttentionNotificationClient() client.AttentionNotificationClient {
	if c := d.activeCore(); c != nil {
		return c.AttentionNotificationClient()
	}
	return nil
}
func (d *startupGatewayDependencies) ProcessViewClient() client.ProcessViewClient {
	if c := d.activeCore(); c != nil {
		return c.ProcessViewClient()
	}
	return nil
}
func (d *startupGatewayDependencies) ProcessControlClient() client.ProcessControlClient {
	if c := d.activeCore(); c != nil {
		return c.ProcessControlClient()
	}
	return nil
}
func (d *startupGatewayDependencies) ProcessOutputClient() client.ProcessOutputClient {
	if c := d.activeCore(); c != nil {
		return c.ProcessOutputClient()
	}
	return nil
}
func (d *startupGatewayDependencies) WorktreeClient() client.WorktreeClient {
	if c := d.activeCore(); c != nil {
		return c.WorktreeClient()
	}
	return nil
}
