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
	"strings"
	"sync"
	"time"

	"core/server/auth"
	"core/server/core"
	"core/server/transport"
	"core/shared/config"
	"core/shared/protocol"
)

type ServeServer struct {
	*core.Core
}

var localSocketListener = listenLocalSocket

var (
	testListenReservationsMu sync.Mutex
	testListenReservations   = map[string]net.Listener{}
)

// ReserveTestListenReservation keeps a test-owned listener alive until the
// configured daemon bind path is ready to claim the same address.
func ReserveTestListenReservation(listener net.Listener) {
	if listener == nil {
		return
	}
	addr := strings.TrimSpace(listener.Addr().String())
	if addr == "" {
		_ = listener.Close()
		return
	}
	testListenReservationsMu.Lock()
	if existing := testListenReservations[addr]; existing != nil {
		_ = existing.Close()
	}
	testListenReservations[addr] = listener
	testListenReservationsMu.Unlock()
	go drainTestListenReservation(listener)
}

func drainTestListenReservation(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		_ = conn.Close()
	}
}

func ReleaseTestListenReservation(addr string) {
	trimmed := strings.TrimSpace(addr)
	if trimmed == "" {
		return
	}
	testListenReservationsMu.Lock()
	listener := testListenReservations[trimmed]
	delete(testListenReservations, trimmed)
	testListenReservationsMu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
}

func StartServeServer(ctx context.Context, req Request, authHandler AuthHandler, onboardingHandler OnboardingHandler) (*ServeServer, error) {
	appCore, err := StartCore(ctx, req, authHandler, onboardingHandler)
	if err != nil {
		return nil, err
	}
	return &ServeServer{Core: appCore}, nil
}

func (s *ServeServer) Serve(ctx context.Context) error {
	if ctx == nil {
		return errContextRequired
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.Core == nil {
		return errors.New("server core is required")
	}
	rpc, err := startCoreRPC(s.Core)
	if err != nil {
		return err
	}
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
	listenAddress := net.JoinHostPort(listenCfg.Settings.ServerHost, strconv.Itoa(listenCfg.Settings.ServerPort))
	ReleaseTestListenReservation(listenAddress)
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
	gateway, err := transport.NewGateway(appCore, identity)
	if err != nil {
		for _, cleanup := range cleanupFns {
			cleanup()
		}
		return nil, err
	}
	mux := buildServerMux(appCore, identity, gateway)
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
		Capabilities: protocol.CapabilityFlags{
			JSONRPCWebSocket:        true,
			AuthBootstrap:           true,
			ProjectAttach:           true,
			SessionAttach:           true,
			HealthEndpoint:          true,
			ReadinessEndpoint:       true,
			RunPrompt:               true,
			SessionPlan:             true,
			SessionLifecycle:        true,
			SessionTranscriptPaging: true,
			SessionRuntime:          true,
			RuntimeControl:          true,
			PromptControl:           true,
			PromptActivity:          true,
			SessionActivity:         true,
			ProcessOutput:           true,
			AttentionNotifications:  true,
		},
	}
}

func buildServerMux(appCore *core.Core, identity protocol.ServerIdentity, gateway *transport.Gateway) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc(protocol.HealthPath, func(w http.ResponseWriter, r *http.Request) {
		authReady := serverAuthReady(r.Context(), appCore)
		writeStatusJSON(w, http.StatusOK, map[string]any{
			"status":     protocol.HealthStatusOK,
			"server_id":  identity.ServerID,
			"pid":        identity.PID,
			"auth_ready": authReady,
		})
	})
	mux.HandleFunc(protocol.ReadinessPath, func(w http.ResponseWriter, r *http.Request) {
		authReady := serverAuthReady(r.Context(), appCore)
		// The mux is only reachable once the listeners are accepting, so the
		// transport is always ready here; readiness then tracks auth readiness.
		if authReady {
			writeStatusJSON(w, http.StatusOK, map[string]any{
				"ready":      true,
				"server_id":  identity.ServerID,
				"pid":        identity.PID,
				"auth_ready": true,
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

func serverAuthReady(ctx context.Context, appCore *core.Core) bool {
	if appCore == nil || appCore.AuthManager() == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	state, err := appCore.AuthManager().Load(ctx)
	if err != nil {
		return false
	}
	return auth.EvaluateStartupGate(state).Ready
}
