package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"core/shared/config"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/serverapi"
)

var errRemoteClosed = errors.New("remote client is closed")

// errRequestCanceledByClient identifies a request-canceled error that has been
// normalized to a clear client-facing message (rather than passing through a
// raw transport message such as context.Canceled's text). requestCanceledError
// reports itself as this sentinel via Is when it carries no distinct message.
var errRequestCanceledByClient = errors.New("request canceled by client")

const preferredLocalSocketProbeTimeout = 100 * time.Millisecond

type requestCanceledError struct {
	message string
}

func (e requestCanceledError) normalized() bool {
	message := strings.TrimSpace(e.message)
	return message == "" || message == context.Canceled.Error()
}

func (e requestCanceledError) Error() string {
	if e.normalized() {
		return errRequestCanceledByClient.Error()
	}
	return strings.TrimSpace(e.message)
}

func (e requestCanceledError) Unwrap() error {
	return context.Canceled
}

// Is reports requestCanceledError as errRequestCanceledByClient when it has
// been normalized to the clear client-facing message, letting callers detect
// that state structurally instead of comparing rendered strings.
func (e requestCanceledError) Is(target error) bool {
	return target == errRequestCanceledByClient && e.normalized()
}

type remoteDialPlan struct {
	endpoints []rpcwire.Endpoint
}

type remoteControlConn struct {
	conn      rpcwire.Conn
	pendingMu sync.Mutex
	pending   map[string]chan rpcwire.Frame
	requestID atomic.Uint64
	failOnce  sync.Once
	done      chan struct{}
	errMu     sync.Mutex
	err       error
}

func configuredRemoteDialPlan(cfg config.App) (remoteDialPlan, error) {
	tcpEndpoint, err := rpcwire.ParseWebSocketEndpoint(config.ServerRPCURL(cfg))
	if err != nil {
		return remoteDialPlan{}, err
	}
	endpoints := make([]rpcwire.Endpoint, 0, 2)
	if shouldPreferConfiguredLocalSocket(cfg) {
		if socketPath, ok, err := config.ServerLocalRPCSocketPath(cfg); err != nil {
			return remoteDialPlan{}, err
		} else if ok {
			if _, statErr := os.Stat(socketPath); statErr == nil {
				udsEndpoint, err := rpcwire.NewUnixEndpoint(socketPath, protocol.RPCPath)
				if err != nil {
					return remoteDialPlan{}, err
				}
				endpoints = append(endpoints, udsEndpoint)
			}
		}
	}
	endpoints = append(endpoints, tcpEndpoint)
	return remoteDialPlan{endpoints: endpoints}, nil
}

func shouldPreferConfiguredLocalSocket(cfg config.App) bool {
	// Explicit TCP target overrides must stay authoritative; the derived unix socket
	// is only a default local optimization for the standard local attach target.
	if hasExplicitTCPServerTarget(cfg) {
		return false
	}
	host := strings.TrimSpace(cfg.Settings.ServerHost)
	if host == "" || strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

func hasExplicitTCPServerTarget(cfg config.App) bool {
	sources := cfg.Source.Sources
	if len(sources) == 0 {
		return false
	}
	return sources["server_host"] != "default" || sources["server_port"] != "default"
}

func dialRemoteWithTransport(ctx context.Context, plan remoteDialPlan, transport rpcwire.ClientTransport, projectID string, workspaceID string, workspaceRoot string) (*Remote, error) {
	trimmedProjectID := strings.TrimSpace(projectID)
	trimmedWorkspaceID := strings.TrimSpace(workspaceID)
	trimmedWorkspaceRoot := strings.TrimSpace(workspaceRoot)
	conn, err := plan.dial(ctx, transport)
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = conn.Close() }
	identity, err := handshakeRPC(ctx, conn)
	if err != nil {
		cleanup()
		return nil, err
	}
	if err := attachProjectRPC(ctx, conn, trimmedProjectID, trimmedWorkspaceID, trimmedWorkspaceRoot); err != nil {
		cleanup()
		return nil, err
	}
	control := newRemoteControlConn(conn)
	return &Remote{
		plan:          plan,
		transport:     transport,
		control:       control,
		identity:      identity,
		projectID:     trimmedProjectID,
		workspaceID:   trimmedWorkspaceID,
		workspaceRoot: trimmedWorkspaceRoot,
	}, nil
}

func (p remoteDialPlan) dial(ctx context.Context, transport rpcwire.ClientTransport) (rpcwire.Conn, error) {
	if len(p.endpoints) == 0 {
		return nil, errors.New("remote rpc endpoint is required")
	}
	var dialErr error
	for i, endpoint := range p.endpoints {
		dialCtx, cancel := endpointDialContext(ctx, endpoint, i < len(p.endpoints)-1)
		conn, err := transport.Dial(dialCtx, endpoint)
		cancel()
		if err == nil {
			return conn, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		dialErr = err
	}
	if dialErr == nil {
		dialErr = errors.New("remote rpc endpoint is required")
	}
	return nil, dialErr
}

func endpointDialContext(ctx context.Context, endpoint rpcwire.Endpoint, hasFallback bool) (context.Context, context.CancelFunc) {
	if !hasFallback || endpoint.Transport != rpcwire.TransportUnix {
		return ctx, func() {}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return ctx, func() {}
		}
		probeTimeout := preferredLocalSocketProbeTimeout
		if remaining < preferredLocalSocketProbeTimeout*2 {
			probeTimeout = remaining / 2
		}
		if probeTimeout > 0 && probeTimeout < remaining {
			return context.WithTimeout(ctx, probeTimeout)
		}
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, preferredLocalSocketProbeTimeout)
}

func (c *Remote) openRPCConn(ctx context.Context) (rpcwire.Conn, func(), error) {
	if err := c.ensureOpen(); err != nil {
		return nil, nil, err
	}
	conn, err := c.plan.dial(ctx, c.transport)
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = conn.Close() }
	identity, err := handshakeRPC(ctx, conn)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	if err := validateIdentityRoot(c.rootID(), identity); err != nil {
		cleanup()
		return nil, nil, err
	}
	if err := attachProjectRPC(ctx, conn, c.projectID, c.workspaceID, c.workspaceRoot); err != nil {
		cleanup()
		return nil, nil, err
	}
	return conn, cleanup, nil
}

func (c *Remote) callDedicated(ctx context.Context, requestID string, method string, params any, out any) error {
	conn, cleanup, err := c.openRPCConn(ctx)
	if err != nil {
		return err
	}
	defer cleanup()
	return callRPC(ctx, conn, requestID, method, params, out)
}

func newRemoteControlConn(conn rpcwire.Conn) *remoteControlConn {
	control := &remoteControlConn{
		conn:    conn,
		pending: map[string]chan rpcwire.Frame{},
		done:    make(chan struct{}),
	}
	go control.readLoop()
	return control
}

func (c *remoteControlConn) call(ctx context.Context, method string, params any, out any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(params)
	if err != nil {
		return err
	}
	id := fmt.Sprintf("rpc-%d", c.requestID.Add(1))
	responseCh := make(chan rpcwire.Frame, 1)
	if err := c.registerPending(id, responseCh); err != nil {
		return err
	}
	request := protocol.Request{JSONRPC: protocol.JSONRPCVersion, ID: id, Method: method, Params: data}
	if err := c.conn.Send(ctx, rpcwire.FrameFromRequest(request)); err != nil {
		c.removePending(id)
		return err
	}
	select {
	case frame := <-responseCh:
		return decodeResponseFrame(frame.Response(), out)
	case <-ctx.Done():
		c.removePending(id)
		return ctx.Err()
	case <-c.done:
		c.removePending(id)
		return c.currentErr()
	}
}

func (c *remoteControlConn) Close() error {
	c.fail(errRemoteClosed)
	return c.conn.Close()
}

func (c *remoteControlConn) IsDone() bool {
	if c == nil {
		return true
	}
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

func (c *remoteControlConn) readLoop() {
	for event := range c.conn.Events() {
		if event.Err != nil {
			c.fail(event.Err)
			return
		}
		if strings.TrimSpace(event.Frame.ID) == "" {
			continue
		}
		c.pendingMu.Lock()
		responseCh := c.pending[event.Frame.ID]
		delete(c.pending, event.Frame.ID)
		c.pendingMu.Unlock()
		if responseCh == nil {
			continue
		}
		responseCh <- event.Frame
	}
	c.fail(io.EOF)
}

func (c *remoteControlConn) registerPending(id string, responseCh chan rpcwire.Frame) error {
	select {
	case <-c.done:
		return c.currentErr()
	default:
	}
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	select {
	case <-c.done:
		return c.currentErr()
	default:
	}
	c.pending[id] = responseCh
	return nil
}

func (c *remoteControlConn) removePending(id string) {
	c.pendingMu.Lock()
	delete(c.pending, id)
	c.pendingMu.Unlock()
}

func (c *remoteControlConn) fail(err error) {
	c.failOnce.Do(func() {
		if err == nil {
			err = io.EOF
		}
		c.errMu.Lock()
		c.err = err
		c.errMu.Unlock()
		close(c.done)
		c.pendingMu.Lock()
		c.pending = map[string]chan rpcwire.Frame{}
		c.pendingMu.Unlock()
	})
}

func (c *remoteControlConn) currentErr() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	if c.err == nil {
		return io.EOF
	}
	return c.err
}

// ErrServerRootMismatch reports that an attached (or reconnected) server reports
// a different persistence root than the client pinned via Remote.RequireRoot.
// Callers match it with errors.Is rather than comparing message text.
var ErrServerRootMismatch = errors.New("attached server reports a different persistence root than required")

// validateIdentityRoot enforces the pinned persistence-root id against a server
// identity. An empty expectedRootID disables validation. A server that does not
// report its root (empty id, e.g. an older build) is rejected when an id is
// required, since the whole point is to refuse instances whose root cannot be
// confirmed.
func validateIdentityRoot(expectedRootID string, identity protocol.ServerIdentity) error {
	if strings.TrimSpace(expectedRootID) == "" {
		return nil
	}
	if identity.PersistenceRootID != expectedRootID {
		return ErrServerRootMismatch
	}
	return nil
}

func handshakeRPC(ctx context.Context, conn rpcwire.Conn) (protocol.ServerIdentity, error) {
	var resp protocol.HandshakeResponse
	if err := callRPC(ctx, conn, "handshake", protocol.MethodHandshake, protocol.HandshakeRequest{ProtocolVersion: protocol.Version}, &resp); err != nil {
		return protocol.ServerIdentity{}, err
	}
	return resp.Identity, nil
}

func attachProjectRPC(ctx context.Context, conn rpcwire.Conn, projectID string, workspaceID string, workspaceRoot string) error {
	trimmedProjectID := strings.TrimSpace(projectID)
	if trimmedProjectID == "" {
		return nil
	}
	return callRPC(ctx, conn, "attach-project", protocol.MethodAttachProject, protocol.AttachProjectRequest{ProjectID: trimmedProjectID, WorkspaceID: strings.TrimSpace(workspaceID), WorkspaceRoot: strings.TrimSpace(workspaceRoot)}, nil)
}

func callRPC(ctx context.Context, conn rpcwire.Conn, requestID string, method string, params any, out any) error {
	data, err := json.Marshal(params)
	if err != nil {
		return err
	}
	request := protocol.Request{JSONRPC: protocol.JSONRPCVersion, ID: requestID, Method: method, Params: data}
	if err := conn.Send(ctx, rpcwire.FrameFromRequest(request)); err != nil {
		return err
	}
	response, err := receiveRPCResponse(ctx, conn, requestID)
	if err != nil {
		return err
	}
	return decodeResponseFrame(response, out)
}

func receiveRPCResponse(ctx context.Context, conn rpcwire.Conn, requestID string) (protocol.Response, error) {
	for {
		frame, err := receiveFrame(ctx, conn)
		if err != nil {
			return protocol.Response{}, err
		}
		if frame.ID != requestID {
			continue
		}
		return frame.Response(), nil
	}
}

func receiveFrame(ctx context.Context, conn rpcwire.Conn) (rpcwire.Frame, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return rpcwire.Frame{}, ctx.Err()
	case event, ok := <-conn.Events():
		if !ok {
			return rpcwire.Frame{}, io.EOF
		}
		if event.Err != nil {
			return rpcwire.Frame{}, event.Err
		}
		return event.Frame, nil
	}
}

func decodeResponseFrame(resp protocol.Response, out any) error {
	if resp.Error != nil {
		return protocolError(resp.Error)
	}
	if out == nil || len(resp.Result) == 0 {
		return nil
	}
	return json.Unmarshal(resp.Result, out)
}

func protocolError(resp *protocol.ResponseError) error {
	if resp == nil {
		return nil
	}
	message := strings.TrimSpace(resp.Message)
	if resp.Code == protocol.ErrCodeRequestCanceled {
		return requestCanceledError{message: message}
	}
	if message == "" {
		message = "protocol request failed"
	}
	switch resp.Code {
	case protocol.ErrCodeMethodNotFound:
		return errors.Join(serverapi.ErrMethodNotFound, errors.New(message))
	case protocol.ErrCodeAuthRequired:
		if message == serverapi.ErrServerAuthRequired.Error() {
			return serverapi.ErrServerAuthRequired
		}
		return errors.Join(serverapi.ErrServerAuthRequired, errors.New(message))
	case protocol.ErrCodeStreamGap:
		return errors.Join(serverapi.ErrStreamGap, errors.New(message))
	case protocol.ErrCodeWorkspaceNotRegistered:
		return errors.Join(serverapi.ErrWorkspaceNotRegistered, errors.New(message))
	case protocol.ErrCodeProjectNotFound:
		return errors.Join(serverapi.ErrProjectNotFound, errors.New(message))
	case protocol.ErrCodeProjectUnavailable:
		return errors.Join(serverapi.ErrProjectUnavailable, errors.New(message))
	case protocol.ErrCodeRuntimeUnavailable:
		return errors.Join(serverapi.ErrRuntimeUnavailable, errors.New(message))
	case protocol.ErrCodeStreamUnavailable:
		return errors.Join(serverapi.ErrStreamUnavailable, errors.New(message))
	case protocol.ErrCodeStreamFailed:
		return errors.Join(serverapi.ErrStreamFailed, errors.New(message))
	case protocol.ErrCodePromptNotFound:
		return errors.Join(serverapi.ErrPromptNotFound, errors.New(message))
	case protocol.ErrCodePromptResolved:
		return errors.Join(serverapi.ErrPromptAlreadyResolved, errors.New(message))
	case protocol.ErrCodePromptUnsupported:
		return errors.Join(serverapi.ErrPromptUnsupported, errors.New(message))
	case protocol.ErrCodeWorkflowTaskNotFound:
		return errors.Join(serverapi.ErrWorkflowTaskNotFound, errors.New(message))
	case protocol.ErrCodeWorkflowTaskCompleteNotFound:
		return errors.Join(serverapi.ErrWorkflowTaskCompleteTargetNotFound, errors.New(message))
	case protocol.ErrCodeWorkflowTaskCompleteAmbiguous:
		return errors.Join(serverapi.ErrWorkflowTaskCompleteSelectorAmbiguous, errors.New(message))
	default:
		return errors.New(message)
	}
}
