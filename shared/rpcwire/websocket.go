package rpcwire

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"core/shared/protocol"
	"github.com/lxzan/gws"
)

type Frame struct {
	Kind    FrameKind
	Payload []byte

	encodingErr error
}

type FrameKind uint8

const (
	FrameText FrameKind = iota + 1
	FrameBinary
)

type Event struct {
	Frame Frame
	Err   error
}

type Conn interface {
	Send(context.Context, Frame) error
	Events() <-chan Event
	Closed() <-chan struct{}
	Close() error
}

type ClientTransport interface {
	Dial(context.Context, Endpoint) (Conn, error)
}

type ServerTransport interface {
	Handler(func(context.Context, Conn)) http.Handler
}

const defaultWebSocketHandshakeTimeout = 5 * time.Second

type WebSocketTransport struct{}

func NewWebSocketTransport() WebSocketTransport {
	return WebSocketTransport{}
}

func (WebSocketTransport) Dial(ctx context.Context, endpoint Endpoint) (Conn, error) {
	rawConn, err := dialWebSocketEndpoint(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	adapter := newWebSocketConn()
	socket, err := dialWebSocketClientContext(ctx, rawConn, endpoint, adapter)
	if err != nil {
		_ = rawConn.Close()
		return nil, err
	}
	adapter.socket = socket
	adapter.startReadLoop(socket)
	return adapter, nil
}

func (WebSocketTransport) Handler(handler func(context.Context, Conn)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		adapter := newWebSocketConn()
		upgrader := gws.NewUpgrader(adapter, &gws.ServerOption{HandshakeTimeout: webSocketHandshakeTimeout(r.Context())})
		socket, err := upgrader.Upgrade(w, r)
		if err != nil {
			return
		}
		adapter.socket = socket
		defer func() { _ = adapter.Close() }()
		adapter.startReadLoop(socket)
		handler(r.Context(), adapter)
	})
}

type webSocketConn struct {
	socket          *gws.Conn
	events          chan Event
	closed          chan struct{}
	closeRequested  atomic.Bool
	closeOnce       sync.Once
	closeEventsOnce sync.Once
	writeMu         sync.Mutex
}

func newWebSocketConn() *webSocketConn {
	return &webSocketConn{
		events: make(chan Event, 16),
		closed: make(chan struct{}),
	}
}

func (c *webSocketConn) Send(ctx context.Context, frame Frame) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.socket == nil {
		return errors.New("websocket connection is required")
	}
	if frame.encodingErr != nil {
		return frame.encodingErr
	}
	var opcode gws.Opcode
	switch frame.Kind {
	case FrameText:
		opcode = gws.OpcodeText
	case FrameBinary:
		opcode = gws.OpcodeBinary
	default:
		return fmt.Errorf("unsupported websocket frame kind %d", frame.Kind)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if deadline, ok := ctx.Deadline(); ok {
		if err := c.socket.SetWriteDeadline(deadline); err != nil {
			return err
		}
		defer func() { _ = c.socket.SetWriteDeadline(time.Time{}) }()
	}
	if err := c.socket.WriteMessage(opcode, frame.Payload); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return err
	}
	return nil
}

func (c *webSocketConn) Events() <-chan Event {
	return c.events
}

func (c *webSocketConn) Closed() <-chan struct{} {
	return c.closed
}

func (c *webSocketConn) Close() error {
	if c == nil {
		return nil
	}
	var err error
	c.closeOnce.Do(func() {
		c.closeRequested.Store(true)
		close(c.closed)
		if c.socket != nil {
			err = c.socket.NetConn().Close()
		} else {
			c.closeEvents()
		}
	})
	return err
}

func (c *webSocketConn) startReadLoop(socket *gws.Conn) {
	go func() {
		defer c.closeEvents()
		socket.ReadLoop()
	}()
}

func (c *webSocketConn) OnOpen(_ *gws.Conn) {}

func (c *webSocketConn) OnClose(_ *gws.Conn, err error) {
	if c.closeRequested.Load() {
		return
	}
	if err == nil {
		err = io.EOF
	}
	c.publishError(err)
	_ = c.Close()
}

func (c *webSocketConn) OnPing(_ *gws.Conn, _ []byte) {}

func (c *webSocketConn) OnPong(_ *gws.Conn, _ []byte) {}

func (c *webSocketConn) OnMessage(_ *gws.Conn, message *gws.Message) {
	defer func() { _ = message.Close() }()
	var kind FrameKind
	switch message.Opcode {
	case gws.OpcodeText:
		kind = FrameText
	case gws.OpcodeBinary:
		kind = FrameBinary
	default:
		c.publishError(fmt.Errorf("unsupported websocket opcode %d", message.Opcode))
		_ = c.Close()
		return
	}
	frame := Frame{Kind: kind, Payload: append([]byte(nil), message.Bytes()...)}
	select {
	case c.events <- Event{Frame: frame}:
	case <-c.closed:
	}
}

func (c *webSocketConn) publishError(err error) {
	if err == nil {
		return
	}
	select {
	case c.events <- Event{Err: err}:
	case <-c.closed:
	}
}

func (c *webSocketConn) closeEvents() {
	c.closeEventsOnce.Do(func() {
		close(c.events)
	})
}

func dialWebSocketEndpoint(ctx context.Context, endpoint Endpoint) (net.Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if endpoint.Transport == TransportUnix {
		return (&net.Dialer{}).DialContext(ctx, "unix", endpoint.Address)
	}
	if endpoint.UseTLS {
		return (&tls.Dialer{NetDialer: &net.Dialer{}, Config: webSocketTLSConfig(endpoint)}).DialContext(ctx, "tcp", endpoint.Address)
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp", endpoint.Address)
}

func webSocketTLSConfig(endpoint Endpoint) *tls.Config {
	config := endpoint.TLSConfig
	if config == nil {
		config = &tls.Config{}
	} else {
		config = config.Clone()
	}
	if config.ServerName == "" {
		if serverURL, err := url.Parse(endpoint.ServerURL); err == nil {
			config.ServerName = serverURL.Hostname()
		}
	}
	return config
}

func dialWebSocketClientContext(ctx context.Context, rawConn net.Conn, endpoint Endpoint, adapter *webSocketConn) (*gws.Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	option := &gws.ClientOption{
		Addr:             endpoint.ServerURL,
		RequestHeader:    http.Header{"Origin": []string{endpoint.OriginURL}},
		HandshakeTimeout: webSocketHandshakeTimeout(ctx),
	}
	type result struct {
		socket *gws.Conn
		resp   *http.Response
		err    error
	}
	resultCh := make(chan result, 1)
	go func() {
		socket, resp, err := gws.NewClientFromConn(adapter, option, rawConn)
		resultCh <- result{socket: socket, resp: resp, err: err}
	}()
	select {
	case <-ctx.Done():
		_ = rawConn.Close()
		result := <-resultCh
		closeHandshakeResponse(result.resp)
		if result.socket != nil {
			_ = result.socket.NetConn().Close()
		}
		return nil, ctx.Err()
	case result := <-resultCh:
		closeHandshakeResponse(result.resp)
		if err := ctx.Err(); err != nil {
			if result.socket != nil {
				_ = result.socket.NetConn().Close()
			}
			return nil, err
		}
		return result.socket, result.err
	}
}

func closeHandshakeResponse(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}

func webSocketHandshakeTimeout(ctx context.Context) time.Duration {
	if ctx == nil {
		return defaultWebSocketHandshakeTimeout
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > 0 {
			return remaining
		}
		return time.Millisecond
	}
	return defaultWebSocketHandshakeTimeout
}

func FrameFromRequest(req protocol.Request) Frame {
	payload, err := json.Marshal(req)
	return Frame{Kind: FrameText, Payload: payload, encodingErr: err}
}

func FrameFromResponse(resp protocol.Response) Frame {
	payload, err := json.Marshal(resp)
	return Frame{Kind: FrameText, Payload: payload, encodingErr: err}
}

func (f Frame) Request() protocol.Request {
	request, err := f.DecodeRequest()
	if err != nil {
		panic(err)
	}
	return request
}

func (f Frame) Response() protocol.Response {
	response, err := f.DecodeResponse()
	if err != nil {
		panic(err)
	}
	return response
}

func (f Frame) DecodeRequest() (protocol.Request, error) {
	if f.Kind != FrameText {
		return protocol.Request{}, fmt.Errorf("JSON request requires a text frame, got kind %d", f.Kind)
	}
	var request protocol.Request
	if err := json.Unmarshal(f.Payload, &request); err != nil {
		return protocol.Request{}, fmt.Errorf("decode JSON request: %w", err)
	}
	return request, nil
}

func (f Frame) DecodeResponse() (protocol.Response, error) {
	if f.Kind != FrameText {
		return protocol.Response{}, fmt.Errorf("JSON response requires a text frame, got kind %d", f.Kind)
	}
	var response protocol.Response
	if err := json.Unmarshal(f.Payload, &response); err != nil {
		return protocol.Response{}, fmt.Errorf("decode JSON response: %w", err)
	}
	return response, nil
}
