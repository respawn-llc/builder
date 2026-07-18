package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"core/shared/protocol"
	"core/shared/rpcwire"
)

type configuredPTYRouteEventKind string

const (
	configuredPTYRouteEventUpdateRequestAdmitted             configuredPTYRouteEventKind = "update_request_admitted"
	configuredPTYRouteEventTranscriptSubscriptionEstablished configuredPTYRouteEventKind = "transcript_subscription_established"
	configuredPTYRouteEventInitialInputLoaded                configuredPTYRouteEventKind = "initial_input_loaded"
	configuredPTYRouteEventTranscriptTransportLost           configuredPTYRouteEventKind = "transcript_transport_lost"
	configuredPTYRouteEventMainTranscriptResubscribed        configuredPTYRouteEventKind = "main_transcript_resubscribed"
)

func (k configuredPTYRouteEventKind) validate() error {
	switch k {
	case configuredPTYRouteEventUpdateRequestAdmitted,
		configuredPTYRouteEventTranscriptSubscriptionEstablished,
		configuredPTYRouteEventInitialInputLoaded,
		configuredPTYRouteEventTranscriptTransportLost,
		configuredPTYRouteEventMainTranscriptResubscribed:
		return nil
	default:
		return fmt.Errorf("unknown configured PTY route event %q", k)
	}
}

type configuredPTYRouteControlCommand string

const (
	configuredPTYRouteControlWait             configuredPTYRouteControlCommand = "wait"
	configuredPTYRouteControlFailTranscript   configuredPTYRouteControlCommand = "fail_transcript"
	configuredPTYRouteControlResumeTranscript configuredPTYRouteControlCommand = "resume_transcript"
)

func (c configuredPTYRouteControlCommand) validate() error {
	switch c {
	case configuredPTYRouteControlWait, configuredPTYRouteControlFailTranscript, configuredPTYRouteControlResumeTranscript:
		return nil
	default:
		return fmt.Errorf("unknown configured PTY route command %q", c)
	}
}

type configuredPTYRouteControlRequest struct {
	Command configuredPTYRouteControlCommand `json:"command"`
	Event   configuredPTYRouteEventKind      `json:"event,omitempty"`
}

type configuredPTYRouteControlResponse struct {
	Command configuredPTYRouteControlCommand `json:"command,omitempty"`
	Event   configuredPTYRouteEventKind      `json:"event,omitempty"`
}

type configuredPTYRouteProxy struct {
	server                         *http.Server
	listener                       net.Listener
	upstream                       rpcwire.Endpoint
	endpoint                       string
	ctx                            context.Context
	cancel                         context.CancelFunc
	events                         map[configuredPTYRouteEventKind]chan struct{}
	published                      map[configuredPTYRouteEventKind]bool
	transcriptMu                   sync.Mutex
	transcript                     rpcwire.Conn
	transcriptSubscriptionRequests int
	transcriptResume               chan struct{}
	transcriptResumed              bool
	connectionsMu                  sync.Mutex
	connections                    map[*configuredPTYRouteProxyConnection]struct{}
	handlersMu                     sync.Mutex
	handlersCond                   *sync.Cond
	handlers                       int
	closing                        bool
	serveDone                      chan struct{}
	closeOnce                      sync.Once
	closeErr                       error
}

type configuredPTYRouteProxyConnection struct {
	mu       sync.Mutex
	client   rpcwire.Conn
	upstream rpcwire.Conn
}

type configuredPTYRoutePendingRequest struct {
	method                        string
	transcriptSubscriptionOrdinal int
}

func startConfiguredPTYRouteProxy(t *testing.T, upstreamEndpoint string) *configuredPTYRouteProxy {
	t.Helper()

	upstream, err := rpcwire.ParseWebSocketEndpoint(strings.TrimSpace(upstreamEndpoint))
	if err != nil {
		t.Fatalf("parse configured PTY upstream endpoint: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for configured PTY route proxy: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	proxy := &configuredPTYRouteProxy{
		listener:         listener,
		upstream:         upstream,
		endpoint:         (&url.URL{Scheme: "ws", Host: listener.Addr().String(), Path: protocol.RPCPath}).String(),
		ctx:              ctx,
		cancel:           cancel,
		events:           make(map[configuredPTYRouteEventKind]chan struct{}),
		published:        make(map[configuredPTYRouteEventKind]bool),
		connections:      make(map[*configuredPTYRouteProxyConnection]struct{}),
		serveDone:        make(chan struct{}),
		transcriptResume: make(chan struct{}),
	}
	proxy.handlersCond = sync.NewCond(&proxy.handlersMu)
	for _, event := range []configuredPTYRouteEventKind{
		configuredPTYRouteEventUpdateRequestAdmitted,
		configuredPTYRouteEventTranscriptSubscriptionEstablished,
		configuredPTYRouteEventInitialInputLoaded,
		configuredPTYRouteEventTranscriptTransportLost,
		configuredPTYRouteEventMainTranscriptResubscribed,
	} {
		proxy.events[event] = make(chan struct{})
	}

	mux := http.NewServeMux()
	mux.Handle(protocol.RPCPath, rpcwire.NewWebSocketTransport().Handler(proxy.handleConnection))
	mux.HandleFunc(configuredPTYRouteControlPath, proxy.handleControl)
	proxy.server = &http.Server{Handler: mux}
	go func() {
		defer close(proxy.serveDone)
		if err := proxy.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			proxy.connectionsMu.Lock()
			if proxy.closeErr == nil {
				proxy.closeErr = err
			}
			proxy.connectionsMu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = proxy.Close()
	})
	return proxy
}

const configuredPTYRouteControlPath = "/__configured_pty_route_control"

func (p *configuredPTYRouteProxy) Endpoint() string {
	if p == nil {
		return ""
	}
	return p.endpoint
}

func (p *configuredPTYRouteProxy) Wait(ctx context.Context, event configuredPTYRouteEventKind) error {
	if p == nil {
		return errors.New("configured PTY route proxy is required")
	}
	if err := event.validate(); err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("context is required")
	}
	p.connectionsMu.Lock()
	published := p.published[event]
	wait := p.events[event]
	p.connectionsMu.Unlock()
	if published {
		return nil
	}
	select {
	case <-wait:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *configuredPTYRouteProxy) FailTranscript(ctx context.Context) error {
	if p == nil {
		return errors.New("configured PTY route proxy is required")
	}
	return failConfiguredPTYTranscript(ctx, p.endpoint)
}

func (p *configuredPTYRouteProxy) Close() error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		p.handlersMu.Lock()
		p.closing = true
		p.handlersMu.Unlock()
		p.cancel()
		errs := make([]error, 0, 1)
		p.connectionsMu.Lock()
		transports := make([]rpcwire.Conn, 0, len(p.connections)*2)
		for connection := range p.connections {
			connection.mu.Lock()
			if connection.client != nil {
				transports = append(transports, connection.client)
			}
			if connection.upstream != nil {
				transports = append(transports, connection.upstream)
			}
			connection.mu.Unlock()
		}
		p.connectionsMu.Unlock()
		for _, transport := range transports {
			errs = append(errs, transport.Close())
		}
		errs = append(errs, p.server.Close())
		p.transcriptMu.Lock()
		if p.transcript != nil {
			errs = append(errs, p.transcript.Close())
			p.transcript = nil
		}
		p.transcriptMu.Unlock()
		<-p.serveDone
		p.handlersMu.Lock()
		for p.handlers != 0 {
			p.handlersCond.Wait()
		}
		p.handlersMu.Unlock()
		p.connectionsMu.Lock()
		errs = append(errs, p.closeErr)
		p.connectionsMu.Unlock()
		p.closeErr = errors.Join(errs...)
	})
	return p.closeErr
}

func (p *configuredPTYRouteProxy) handleConnection(_ context.Context, client rpcwire.Conn) {
	p.handlersMu.Lock()
	if p.closing {
		p.handlersMu.Unlock()
		_ = client.Close()
		return
	}
	p.handlers++
	p.handlersMu.Unlock()
	defer func() {
		p.handlersMu.Lock()
		p.handlers--
		p.handlersCond.Broadcast()
		p.handlersMu.Unlock()
	}()

	connection := &configuredPTYRouteProxyConnection{client: client}
	p.connectionsMu.Lock()
	p.connections[connection] = struct{}{}
	p.connectionsMu.Unlock()
	defer func() {
		p.connectionsMu.Lock()
		delete(p.connections, connection)
		p.connectionsMu.Unlock()
		_ = client.Close()
		connection.mu.Lock()
		upstream := connection.upstream
		connection.mu.Unlock()
		if upstream != nil {
			_ = upstream.Close()
		}
	}()

	upstream, err := rpcwire.NewWebSocketTransport().Dial(p.ctx, p.upstream)
	if err != nil {
		return
	}
	connection.mu.Lock()
	connection.upstream = upstream
	connection.mu.Unlock()

	requestMethods := make(map[string]configuredPTYRoutePendingRequest)
	var requestMethodsMu sync.Mutex
	relayCtx, cancel := context.WithCancel(p.ctx)
	defer cancel()
	var relay sync.WaitGroup
	relay.Add(2)
	go func() {
		defer relay.Done()
		for {
			select {
			case <-relayCtx.Done():
				return
			case event, ok := <-client.Events():
				if !ok || event.Err != nil {
					cancel()
					_ = upstream.Close()
					return
				}
				frame := event.Frame
				if frame.Method != "" {
					if err := (protocol.Request{
						JSONRPC: frame.JSONRPC,
						ID:      frame.ID,
						Method:  frame.Method,
						Params:  frame.Params,
					}).Validate(); err != nil {
						cancel()
						_ = upstream.Close()
						return
					}
				}
				pendingRequest := configuredPTYRoutePendingRequest{method: frame.Method}
				if frame.Method == protocol.MethodSessionSubscribeTranscript {
					p.transcriptMu.Lock()
					p.transcriptSubscriptionRequests++
					pendingRequest.transcriptSubscriptionOrdinal = p.transcriptSubscriptionRequests
					resume := p.transcriptResume
					p.transcriptMu.Unlock()
					if pendingRequest.transcriptSubscriptionOrdinal == 2 {
						select {
						case <-resume:
						case <-relayCtx.Done():
							return
						}
					}
				}
				if frame.Method != "" && frame.ID != "" {
					requestMethodsMu.Lock()
					requestMethods[frame.ID] = pendingRequest
					requestMethodsMu.Unlock()
				}
				if err := upstream.Send(relayCtx, frame); err != nil {
					if frame.Method != "" && frame.ID != "" {
						requestMethodsMu.Lock()
						delete(requestMethods, frame.ID)
						requestMethodsMu.Unlock()
					}
					cancel()
					_ = client.Close()
					return
				}
			}
		}
	}()
	go func() {
		defer relay.Done()
		for {
			select {
			case <-relayCtx.Done():
				return
			case event, ok := <-upstream.Events():
				if !ok || event.Err != nil {
					cancel()
					_ = client.Close()
					return
				}
				frame := event.Frame
				if err := client.Send(relayCtx, frame); err != nil {
					cancel()
					_ = upstream.Close()
					return
				}
				if frame.Method == "" && frame.ID != "" {
					requestMethodsMu.Lock()
					pendingRequest := requestMethods[frame.ID]
					delete(requestMethods, frame.ID)
					requestMethodsMu.Unlock()
					if frame.Error == nil {
						switch pendingRequest.method {
						case protocol.MethodSessionSubscribeTranscript:
							if pendingRequest.transcriptSubscriptionOrdinal == 1 {
								p.transcriptMu.Lock()
								if p.transcript == nil {
									p.transcript = upstream
								}
								p.transcriptMu.Unlock()
								p.publish(configuredPTYRouteEventTranscriptSubscriptionEstablished)
							} else if pendingRequest.transcriptSubscriptionOrdinal == 2 {
								p.publish(configuredPTYRouteEventMainTranscriptResubscribed)
							}
						case protocol.MethodSessionGetInitialInput:
							p.publish(configuredPTYRouteEventInitialInputLoaded)
						}
					}
				}
			}
		}
	}()
	relay.Wait()
}

func (p *configuredPTYRouteProxy) publish(event configuredPTYRouteEventKind) {
	p.connectionsMu.Lock()
	defer p.connectionsMu.Unlock()
	if p.published[event] {
		return
	}
	p.published[event] = true
	close(p.events[event])
}

func (p *configuredPTYRouteProxy) handleControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	var request configuredPTYRouteControlRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeConfiguredPTYRouteControlError(w, http.StatusBadRequest, err)
		return
	}
	if err := request.Command.validate(); err != nil {
		writeConfiguredPTYRouteControlError(w, http.StatusBadRequest, err)
		return
	}
	switch request.Command {
	case configuredPTYRouteControlWait:
		if err := request.Event.validate(); err != nil {
			writeConfiguredPTYRouteControlError(w, http.StatusBadRequest, err)
			return
		}
		if err := p.Wait(r.Context(), request.Event); err != nil {
			writeConfiguredPTYRouteControlError(w, http.StatusRequestTimeout, err)
			return
		}
		writeConfiguredPTYRouteControlJSON(w, http.StatusOK, configuredPTYRouteControlResponse{Event: request.Event})
	case configuredPTYRouteControlFailTranscript:
		if err := p.failTranscript(r.Context()); err != nil {
			writeConfiguredPTYRouteControlError(w, http.StatusConflict, err)
			return
		}
		writeConfiguredPTYRouteControlJSON(w, http.StatusOK, configuredPTYRouteControlResponse{Event: configuredPTYRouteEventTranscriptTransportLost})
	case configuredPTYRouteControlResumeTranscript:
		p.resumeTranscript()
		writeConfiguredPTYRouteControlJSON(w, http.StatusOK, configuredPTYRouteControlResponse{Command: configuredPTYRouteControlResumeTranscript})
	}
}

func (p *configuredPTYRouteProxy) resumeTranscript() {
	p.transcriptMu.Lock()
	defer p.transcriptMu.Unlock()
	if p.transcriptResumed {
		return
	}
	p.transcriptResumed = true
	close(p.transcriptResume)
}

func (p *configuredPTYRouteProxy) failTranscript(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	p.transcriptMu.Lock()
	transcript := p.transcript
	p.transcript = nil
	p.transcriptMu.Unlock()
	if transcript == nil {
		return errors.New("transcript transport is not established")
	}
	err := transcript.Close()
	p.publish(configuredPTYRouteEventTranscriptTransportLost)
	return err
}

func waitConfiguredPTYRouteEvent(ctx context.Context, endpoint string, event configuredPTYRouteEventKind) error {
	return configuredPTYRouteControlRequestCall(ctx, endpoint, configuredPTYRouteControlRequest{
		Command: configuredPTYRouteControlWait,
		Event:   event,
	})
}

func failConfiguredPTYTranscript(ctx context.Context, endpoint string) error {
	return configuredPTYRouteControlRequestCall(ctx, endpoint, configuredPTYRouteControlRequest{
		Command: configuredPTYRouteControlFailTranscript,
	})
}

func resumeConfiguredPTYTranscript(ctx context.Context, endpoint string) error {
	return configuredPTYRouteControlRequestCall(ctx, endpoint, configuredPTYRouteControlRequest{
		Command: configuredPTYRouteControlResumeTranscript,
	})
}

func configuredPTYRouteControlRequestCall(ctx context.Context, endpoint string, request configuredPTYRouteControlRequest) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if err := request.Command.validate(); err != nil {
		return err
	}
	if request.Command == configuredPTYRouteControlWait {
		if err := request.Event.validate(); err != nil {
			return err
		}
	} else if request.Event != "" {
		return errors.New("configured PTY route control command must not include an event")
	}
	controlURL, err := configuredPTYRouteControlURL(endpoint)
	if err != nil {
		return err
	}
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, controlURL, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var result configuredPTYRouteControlResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&result); err != nil {
		return err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("configured PTY route control request failed with status %d", response.StatusCode)
	}
	switch request.Command {
	case configuredPTYRouteControlWait:
		if err := result.Event.validate(); err != nil {
			return err
		}
		if result.Event != request.Event {
			return fmt.Errorf("configured PTY route control returned event %q, want %q", result.Event, request.Event)
		}
	case configuredPTYRouteControlFailTranscript:
		if err := result.Event.validate(); err != nil {
			return err
		}
		if result.Event != configuredPTYRouteEventTranscriptTransportLost {
			return fmt.Errorf("configured PTY route control returned event %q, want transcript transport lost", result.Event)
		}
	case configuredPTYRouteControlResumeTranscript:
		if result.Command != configuredPTYRouteControlResumeTranscript {
			return fmt.Errorf("configured PTY route control returned command %q, want resume transcript", result.Command)
		}
	}
	return nil
}

func configuredPTYRouteControlURL(endpoint string) (string, error) {
	trimmedEndpoint := strings.TrimSpace(endpoint)
	if trimmedEndpoint == "" {
		return "", errors.New("configured PTY route endpoint is required")
	}
	parsed, err := url.Parse(trimmedEndpoint)
	if err != nil {
		return "", fmt.Errorf("parse configured PTY route endpoint: %w", err)
	}
	if parsed.Scheme != "ws" && parsed.Scheme != "wss" {
		return "", fmt.Errorf("configured PTY route endpoint must use websocket scheme, got %q", parsed.Scheme)
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return "", errors.New("configured PTY route endpoint host is required")
	}
	if parsed.Path != protocol.RPCPath {
		return "", fmt.Errorf("configured PTY route endpoint path must be %q", protocol.RPCPath)
	}
	scheme := "http"
	if parsed.Scheme == "wss" {
		scheme = "https"
	}
	parsed.Scheme = scheme
	parsed.Path = configuredPTYRouteControlPath
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func writeConfiguredPTYRouteControlJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeConfiguredPTYRouteControlError(w http.ResponseWriter, status int, err error) {
	writeConfiguredPTYRouteControlJSON(w, status, struct {
		Error string `json:"error"`
	}{Error: err.Error()})
}
