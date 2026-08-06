package transport

import (
	"context"
	"errors"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/shared/apicontract"
	"core/shared/protocol"
	"core/shared/serverapi"

	"golang.org/x/net/websocket"
)

type gatewayConcurrencyWorkflowService struct {
	apicontract.WorkflowService
	getWorkflowTask func(context.Context, serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error)
}

func (s *gatewayConcurrencyWorkflowService) GetWorkflowTask(
	ctx context.Context,
	req serverapi.WorkflowTaskGetRequest,
) (serverapi.WorkflowTaskGetResponse, error) {
	return s.getWorkflowTask(ctx, req)
}

type gatewayConcurrencyDependencies struct {
	GatewayDependencies
	workflow apicontract.WorkflowService
	debug    bool
}

func (d *gatewayConcurrencyDependencies) WorkflowClient() apicontract.WorkflowService {
	return d.workflow
}

func (d *gatewayConcurrencyDependencies) DebugEnabled() bool {
	return d.debug
}

type gatewayCloseTracker struct {
	active             atomic.Int32
	entered            chan struct{}
	canceled           chan string
	exited             chan string
	allowActivationEnd chan struct{}
}

type gatewayCloseWorkflowService struct {
	apicontract.WorkflowService
	tracker *gatewayCloseTracker
}

func (s *gatewayCloseWorkflowService) GetWorkflowTask(ctx context.Context, _ serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	s.tracker.active.Add(1)
	s.tracker.entered <- struct{}{}
	defer func() {
		s.tracker.active.Add(-1)
		s.tracker.exited <- "workflow"
	}()
	<-ctx.Done()
	s.tracker.canceled <- "workflow"
	return serverapi.WorkflowTaskGetResponse{}, ctx.Err()
}

type gatewayCloseRuntimeService struct {
	apicontract.SessionRuntimeService
	tracker        *gatewayCloseTracker
	releaseStarted chan gatewayCloseRelease
}

type gatewayCloseRelease struct {
	request serverapi.SessionRuntimeReleaseRequest
	active  int32
}

func (s *gatewayCloseRuntimeService) ActivateSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
	s.tracker.active.Add(1)
	s.tracker.entered <- struct{}{}
	defer func() {
		s.tracker.active.Add(-1)
		s.tracker.exited <- "runtime"
	}()
	<-ctx.Done()
	s.tracker.canceled <- "runtime"
	<-s.tracker.allowActivationEnd
	return serverapi.SessionRuntimeActivateResponse{
		Attachment: serverapi.SessionRuntimeAttachment{SessionID: req.SessionID, Generation: 1},
	}, nil
}

func (s *gatewayCloseRuntimeService) ReleaseSessionRuntime(_ context.Context, req serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
	s.releaseStarted <- gatewayCloseRelease{request: req, active: s.tracker.active.Load()}
	return serverapi.SessionRuntimeReleaseResponse{Released: true}, nil
}

type gatewayCloseDependencies struct {
	GatewayDependencies
	workflow apicontract.WorkflowService
	runtime  apicontract.SessionRuntimeService
}

func (d *gatewayCloseDependencies) WorkflowClient() apicontract.WorkflowService {
	return d.workflow
}

func (d *gatewayCloseDependencies) SessionRuntimeClient() apicontract.SessionRuntimeService {
	return d.runtime
}

func TestGatewayConcurrentUnaryResponsesAreCorrelated(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	defer func() { _ = appCore.Close() }()

	blockedEntered := make(chan struct{})
	releaseBlocked := make(chan struct{})
	var releaseBlockedOnce sync.Once
	release := func() {
		releaseBlockedOnce.Do(func() { close(releaseBlocked) })
	}
	workflow := &gatewayConcurrencyWorkflowService{
		WorkflowService: appCore.WorkflowClient(),
		getWorkflowTask: func(ctx context.Context, req serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
			if req.TaskID == "task-1" {
				close(blockedEntered)
				select {
				case <-releaseBlocked:
				case <-ctx.Done():
					return serverapi.WorkflowTaskGetResponse{}, ctx.Err()
				}
			}
			return serverapi.WorkflowTaskGetResponse{}, errors.New("blocked workflow task lookup")
		},
	}
	deps := &gatewayConcurrencyDependencies{
		GatewayDependencies: appCore,
		workflow:            workflow,
	}
	gateway, err := NewGateway(deps, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptest.NewServer(gateway.Handler())
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	t.Cleanup(release)

	sendGatewayRequest(t, conn, "blocked", protocol.MethodWorkflowTaskGet, serverapi.WorkflowTaskGetRequest{TaskID: "task-1"})
	select {
	case <-blockedEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for blocked workflow task lookup")
	}
	sendGatewayRequest(t, conn, "fast", protocol.MethodWorkflowTaskGet, serverapi.WorkflowTaskGetRequest{TaskID: "task-2"})

	if err := conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	var response protocol.Response
	if err := websocket.JSON.Receive(conn, &response); err != nil {
		t.Fatalf("receive fast response: %v", err)
	}
	if response.ID != "fast" {
		t.Fatalf("first response id = %q, want fast", response.ID)
	}
	if response.Error == nil {
		t.Fatal("fast response unexpectedly succeeded")
	}

	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("clear read deadline: %v", err)
	}
	release()
	if err := websocket.JSON.Receive(conn, &response); err != nil {
		t.Fatalf("receive blocked response: %v", err)
	}
	if response.ID != "blocked" {
		t.Fatalf("second response id = %q, want blocked", response.ID)
	}
}

func TestGatewayOrdinaryHandlerPanicClosesOnlyItsConnection(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	defer func() { _ = appCore.Close() }()

	panicEntered := make(chan struct{})
	workflow := &gatewayConcurrencyWorkflowService{
		WorkflowService: appCore.WorkflowClient(),
		getWorkflowTask: func(_ context.Context, req serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
			if req.TaskID == "panic" {
				close(panicEntered)
				panic("gateway test panic")
			}
			return serverapi.WorkflowTaskGetResponse{}, errors.New("unexpected workflow task lookup")
		},
	}
	deps := &gatewayConcurrencyDependencies{
		GatewayDependencies: appCore,
		workflow:            workflow,
	}
	gateway, err := NewGateway(deps, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptest.NewServer(gateway.Handler())
	defer server.Close()

	conn := dialGateway(t, server)
	handshakeGateway(t, conn)
	defer func() { _ = conn.Close() }()
	sendGatewayRequest(t, conn, "panic", protocol.MethodWorkflowTaskGet, serverapi.WorkflowTaskGetRequest{TaskID: "panic"})
	select {
	case <-panicEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for panic route")
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	var response protocol.Response
	if err := websocket.JSON.Receive(conn, &response); err == nil {
		t.Fatal("panic request unexpectedly returned a response")
	}

	next := dialGateway(t, server)
	defer func() { _ = next.Close() }()
	handshakeGateway(t, next)
}

func TestGatewayOrdinaryHandlerPanicFailsFastInDebug(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	defer func() { _ = appCore.Close() }()

	panicCause := errors.New("gateway debug test panic")
	workflow := &gatewayConcurrencyWorkflowService{
		WorkflowService: appCore.WorkflowClient(),
		getWorkflowTask: func(_ context.Context, _ serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
			panic(panicCause)
		},
	}
	deps := &gatewayConcurrencyDependencies{
		GatewayDependencies: appCore,
		workflow:            workflow,
		debug:               true,
	}
	gateway, err := NewGateway(deps, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	req := protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      "panic",
		Method:  protocol.MethodWorkflowTaskGet,
		Params:  mustJSON(t, serverapi.WorkflowTaskGetRequest{TaskID: "panic"}),
	}
	state := &connectionState{handshakeDone: true}
	stopped := false
	defer func() {
		recovered := recover()
		diagnostic, ok := recovered.(gatewayRequestPanicDiagnostic)
		if !ok {
			t.Fatalf("recovered panic = %#v, want gatewayRequestPanicDiagnostic", recovered)
		}
		if diagnostic.Operation != gatewayOrdinaryRequestOperation {
			t.Fatalf("diagnostic operation = %q, want %q", diagnostic.Operation, gatewayOrdinaryRequestOperation)
		}
		if diagnostic.Method != protocol.MethodWorkflowTaskGet {
			t.Fatalf("diagnostic method = %q, want %q", diagnostic.Method, protocol.MethodWorkflowTaskGet)
		}
		if diagnostic.RequestID != "panic" {
			t.Fatalf("diagnostic request id = %q, want panic", diagnostic.RequestID)
		}
		cause, ok := diagnostic.Cause.(error)
		if !ok || !errors.Is(cause, panicCause) {
			t.Fatalf("diagnostic cause = %#v, want original panic cause", diagnostic.Cause)
		}
		if diagnostic.Stack == "" {
			t.Fatal("diagnostic stack is empty")
		}
		if !stopped {
			t.Fatal("debug panic did not close the connection")
		}
	}()
	gateway.serveOrdinaryGatewayRequest(nil, context.Background(), state, req, gatewayRequestSchedule{
		kind: gatewayRequestScheduleOrdinary,
	}, func() {
		stopped = true
	})
}

func TestGatewayCloseCancelsAndDrainsHandlersBeforeRuntimeCleanup(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	defer func() { _ = appCore.Close() }()
	store := createGatewayAuthoritativeSession(t, appCore)

	tracker := &gatewayCloseTracker{
		entered:            make(chan struct{}, 2),
		canceled:           make(chan string, 2),
		exited:             make(chan string, 2),
		allowActivationEnd: make(chan struct{}),
	}
	var allowActivationEndOnce sync.Once
	allowActivationEnd := func() {
		allowActivationEndOnce.Do(func() { close(tracker.allowActivationEnd) })
	}
	runtime := &gatewayCloseRuntimeService{
		SessionRuntimeService: appCore.SessionRuntimeClient(),
		tracker:               tracker,
		releaseStarted:        make(chan gatewayCloseRelease, 1),
	}
	workflow := &gatewayCloseWorkflowService{
		WorkflowService: appCore.WorkflowClient(),
		tracker:         tracker,
	}
	deps := &gatewayCloseDependencies{
		GatewayDependencies: appCore,
		workflow:            workflow,
		runtime:             runtime,
	}
	gateway, err := NewGateway(deps, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptest.NewServer(gateway.Handler())
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	t.Cleanup(allowActivationEnd)

	sendGatewayRequest(t, conn, "workflow", protocol.MethodWorkflowTaskGet, serverapi.WorkflowTaskGetRequest{TaskID: "task-1"})
	sendGatewayRequest(t, conn, "runtime", protocol.MethodSessionRuntimeActivate, gatewayRuntimeActivateRequest(appCore, store.Meta().SessionID, "runtime"))
	for i := 0; i < 2; i++ {
		select {
		case <-tracker.entered:
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for admitted handler %d", i+1)
		}
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("close gateway connection: %v", err)
	}
	canceled := make(map[string]struct{}, 2)
	for len(canceled) < 2 {
		select {
		case name := <-tracker.canceled:
			canceled[name] = struct{}{}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for canceled handlers")
		}
	}
	if _, ok := canceled["workflow"]; !ok {
		t.Fatal("workflow handler context was not canceled")
	}
	if _, ok := canceled["runtime"]; !ok {
		t.Fatal("runtime handler context was not canceled")
	}
	waitForGatewayCloseEvent(t, tracker.exited, "workflow")
	if got := tracker.active.Load(); got != 1 {
		t.Fatalf("active handler count after cancellation = %d, want runtime handler still admitted", got)
	}

	allowActivationEnd()
	waitForGatewayCloseEvent(t, tracker.exited, "runtime")
	select {
	case release := <-runtime.releaseStarted:
		if release.request.Attachment.SessionID != store.Meta().SessionID || release.request.Attachment.Generation != 1 {
			t.Fatalf("cleanup attachment = %+v, want session %q generation 1", release.request.Attachment, store.Meta().SessionID)
		}
		if !release.request.DropOwner || release.request.ClosePolicy != serverapi.SessionRuntimeReleaseClosePolicyCloseIfIdle {
			t.Fatalf("cleanup release request = %+v, want owner drop close-if-idle", release.request)
		}
		if release.active != 0 {
			t.Fatalf("cleanup started with %d active handler(s), want 0", release.active)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runtime cleanup")
	}
	if got := tracker.active.Load(); got != 0 {
		t.Fatalf("active handler count after cleanup started = %d, want 0", got)
	}
}

func waitForGatewayCloseEvent(t *testing.T, events <-chan string, want string) {
	t.Helper()
	for {
		select {
		case got := <-events:
			if got == want {
				return
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s handler exit", want)
		}
	}
}

func TestGatewayAdmissionCapsOrdinaryUnaryRequests(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	defer func() { _ = appCore.Close() }()

	entered := make(chan string, 17)
	entered17BeforeRelease := make(chan struct{}, 1)
	release := make(map[string]chan struct{}, 17)
	for i := int32(1); i <= 17; i++ {
		release["task-"+stringID(i)] = make(chan struct{}, 1)
	}
	var capacityReleased atomic.Bool
	var calls atomic.Int32
	workflow := &gatewayConcurrencyWorkflowService{
		WorkflowService: appCore.WorkflowClient(),
		getWorkflowTask: func(ctx context.Context, req serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
			calls.Add(1)
			if req.TaskID == "task-"+stringID(17) && !capacityReleased.Load() {
				entered17BeforeRelease <- struct{}{}
			}
			entered <- req.TaskID
			select {
			case <-release[req.TaskID]:
			case <-ctx.Done():
				return serverapi.WorkflowTaskGetResponse{}, ctx.Err()
			}
			return serverapi.WorkflowTaskGetResponse{}, errors.New("blocked workflow task lookup")
		},
	}
	deps := &gatewayConcurrencyDependencies{
		GatewayDependencies: appCore,
		workflow:            workflow,
	}
	gateway, err := NewGateway(deps, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptest.NewServer(gateway.Handler())
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	releaseAll := func() {
		for id := int32(1); id <= 17; id++ {
			select {
			case release["task-"+stringID(id)] <- struct{}{}:
			default:
			}
		}
	}
	t.Cleanup(releaseAll)

	for i := int32(1); i <= 17; i++ {
		sendGatewayRequest(t, conn, stringID(i), protocol.MethodWorkflowTaskGet, serverapi.WorkflowTaskGetRequest{
			TaskID: "task-" + stringID(i),
		})
	}

	enteredIDs := make(map[string]struct{}, 16)
	for len(enteredIDs) < 16 {
		select {
		case got := <-entered:
			if got == "task-request-17" {
				t.Fatal("17th request entered before an admission slot was released")
			}
			enteredIDs[got] = struct{}{}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for the first 16 service calls")
		}
	}
	if got := calls.Load(); got != 16 {
		t.Fatalf("service call count = %d, want 16", got)
	}
	for i := int32(1); i <= 16; i++ {
		if _, ok := enteredIDs["task-"+stringID(i)]; !ok {
			t.Fatalf("missing admitted request %q", stringID(i))
		}
	}
	select {
	case got := <-entered:
		t.Fatalf("unexpected extra service call %q before capacity release", got)
	default:
	}

	capacityReleased.Store(true)
	release["task-"+stringID(1)] <- struct{}{}
	select {
	case got := <-entered:
		if got != "task-request-17" {
			t.Fatalf("service call after release = %q, want request-17", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the 17th request to enter")
	}
	select {
	case <-entered17BeforeRelease:
		t.Fatal("17th request entered before capacity release")
	default:
	}

	for id := range enteredIDs {
		release[id] <- struct{}{}
	}
	release["task-"+stringID(17)] <- struct{}{}
	responses := make(map[string]struct{}, 17)
	for i := 0; i < 17; i++ {
		var response protocol.Response
		if err := websocket.JSON.Receive(conn, &response); err != nil {
			t.Fatalf("receive response %d: %v", i+1, err)
		}
		if response.Error == nil {
			t.Fatalf("response %q unexpectedly succeeded", response.ID)
		}
		responses[response.ID] = struct{}{}
	}
	for i := int32(1); i <= 17; i++ {
		if _, ok := responses[stringID(i)]; !ok {
			t.Fatalf("missing response for request %q", stringID(i))
		}
	}
}

func stringID(id int32) string {
	return "request-" + strconv.FormatInt(int64(id), 10)
}

func sendGatewayRequest(t *testing.T, conn *websocket.Conn, id, method string, params any) {
	t.Helper()
	if err := websocket.JSON.Send(conn, protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      id,
		Method:  method,
		Params:  mustJSON(t, params),
	}); err != nil {
		t.Fatalf("send %s: %v", id, err)
	}
}
