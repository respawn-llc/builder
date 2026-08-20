package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"core/internal/testharness/testsetup"
	"core/server/auth"
	serverbootstrap "core/server/bootstrap"
	"core/server/core"
	"core/server/metadata"
	"core/server/session"
	shelltool "core/server/tools/shell"
	"core/shared/apicontract"
	remoteclient "core/shared/client"
	"core/shared/clientui"
	"core/shared/llmerrors"
	"core/shared/protoapi"
	authpb "core/shared/protoapi/gen/kent/api/auth"
	connectionpb "core/shared/protoapi/gen/kent/api/connection"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"
	"google.golang.org/protobuf/types/known/emptypb"
)

func gatewaySessionExecutionTarget(t *testing.T, conn *websocket.Conn, requestID, sessionID string) clientui.SessionExecutionTarget {
	t.Helper()
	var response serverapi.SessionMainViewResponse
	callGateway(
		t,
		conn,
		requestID,
		protocol.MethodSessionGetMainView,
		serverapi.SessionMainViewRequest{SessionID: sessionID},
		&response,
	)
	return response.MainView.Session.ExecutionTarget
}

func registerGatewayWorkspace(t *testing.T, workspace string) {
	t.Helper()
	configureGatewayTestServerPort(t)
	resolved := resolveGatewayTestConfig(t, workspace)
	registerGatewayTestBinding(t, resolved.Config)
}

func configureGatewayTestServerPort(t *testing.T) {
	t.Helper()
	port := 56000 + int(gatewayTestPortCounter.Add(1))
	t.Setenv("KENT_SERVER_HOST", "127.0.0.1")
	t.Setenv("KENT_SERVER_PORT", strconv.Itoa(port))
}

var gatewayTestPortCounter atomic.Uint32

func reportGatewayHandlerError(errs chan<- error, format string, args ...any) {
	select {
	case errs <- fmt.Errorf(format, args...):
	default:
	}
}

func requireNoGatewayHandlerError(t *testing.T, errs <-chan error) {
	t.Helper()
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
}

func TestProtocolErrorMapsRuntimeUnavailable(t *testing.T) {
	code, _ := protocolError(serverapi.ErrRuntimeUnavailable)
	if code != protocol.ErrCodeRuntimeUnavailable {
		t.Fatalf("protocol error code = %d, want %d", code, protocol.ErrCodeRuntimeUnavailable)
	}
}

func TestProtocolErrorMapsWorkflowTaskNotFound(t *testing.T) {
	code, _ := protocolError(serverapi.ErrWorkflowTaskNotFound)
	if code != protocol.ErrCodeWorkflowTaskNotFound {
		t.Fatalf("protocol error code = %d, want %d", code, protocol.ErrCodeWorkflowTaskNotFound)
	}
}

func TestProtocolErrorMapsModelStreamStalled(t *testing.T) {
	code, _ := protocolError(fmt.Errorf("model generation failed after retries: %w", llmerrors.ErrModelStreamStalled))
	if code != protocol.ErrCodeModelStreamStalled {
		t.Fatalf("protocol error code = %d, want %d", code, protocol.ErrCodeModelStreamStalled)
	}
}

func TestProtocolErrorMapsContextCanceled(t *testing.T) {
	code, message := protocolError(context.Canceled)
	if code != protocol.ErrCodeRequestCanceled {
		t.Fatalf("protocol error code = %d, want %d", code, protocol.ErrCodeRequestCanceled)
	}
	if message != canceledByClientMessage {
		t.Fatalf("protocol error message = %q, want %q", message, canceledByClientMessage)
	}
}

func TestProtocolErrorMapsStreamFailureAsStreamFailure(t *testing.T) {
	source := serverapi.ErrStreamFailed
	code, message := protocolError(source)
	if code != protocol.ErrCodeStreamFailed {
		t.Fatalf("protocol error code = %d, want %d", code, protocol.ErrCodeStreamFailed)
	}
	if message != source.Error() {
		t.Fatalf("protocol error message = %q, want %q", message, source.Error())
	}
}

func TestResponseForErrorMapsJoinedWorktreeBlocked(t *testing.T) {
	source := errors.Join(
		fmt.Errorf("delete target: %w", serverapi.ErrWorktreeBlocked),
		errors.New("target has a live run"),
	)
	response := responseForError("worktree-blocked", source)
	if response.Error == nil ||
		response.Error.Code != protocol.ErrCodeWorktreeBlocked ||
		response.Error.Message != source.Error() ||
		len(response.Error.Data) != 0 {
		t.Fatalf("worktree-blocked response = %+v, want code/message without data", response.Error)
	}
}

func TestResponseForErrorPreservesRuntimeCommandNotAcceptedCause(t *testing.T) {
	command := "prompt:review"
	cause := &serverapi.PromptCommandError{
		Kind:    serverapi.PromptCommandErrorKindCommandNotFound,
		Command: &command,
	}
	source := serverapi.NewRuntimeCommandNotAcceptedError(cause)
	response := responseForError("runtime-command", source)
	if response.Error == nil || response.Error.Code != protocol.ErrCodeRuntimeCommandNotAccepted {
		t.Fatalf("runtime command response = %+v, want structured not-accepted error", response.Error)
	}
	var payload struct {
		Cause protocol.ResponseError `json:"cause"`
	}
	if err := json.Unmarshal(response.Error.Data, &payload); err != nil {
		t.Fatalf("decode nested cause: %v", err)
	}
	if payload.Cause.Code != protocol.ErrCodePromptCommands {
		t.Fatalf("nested cause code = %d, want %d", payload.Cause.Code, protocol.ErrCodePromptCommands)
	}
	decoded := serverapi.DecodePromptCommandError(payload.Cause.Data, payload.Cause.Message)
	var promptErr *serverapi.PromptCommandError
	if !errors.As(decoded, &promptErr) || promptErr.Kind != cause.Kind || promptErr.Command == nil || *promptErr.Command != command {
		t.Fatalf("nested cause = %T %+v, want %+v", decoded, promptErr, cause)
	}
}

func TestResponseForErrorPreservesRuntimeCommandNotAcceptedUnavailableCause(t *testing.T) {
	source := serverapi.NewRuntimeCommandNotAcceptedError(errors.Join(
		serverapi.ErrRuntimeUnavailable,
		errors.New("session has no Ready runtime"),
	))
	response := responseForError("runtime-command", source)
	if response.Error == nil || response.Error.Code != protocol.ErrCodeRuntimeCommandNotAccepted {
		t.Fatalf("runtime command response = %+v, want structured not-accepted error", response.Error)
	}
	var payload struct {
		Cause protocol.ResponseError `json:"cause"`
	}
	if err := json.Unmarshal(response.Error.Data, &payload); err != nil {
		t.Fatalf("decode nested cause: %v", err)
	}
	if payload.Cause.Code != protocol.ErrCodeRuntimeUnavailable {
		t.Fatalf("nested cause code = %d, want %d", payload.Cause.Code, protocol.ErrCodeRuntimeUnavailable)
	}
}

func TestResponseForErrorSurfacesIrreconcilableRecoveryEvidence(t *testing.T) {
	detail := &session.IrreconcilableRecoveryDetail{
		SessionID:             "session-1",
		Operation:             "recover_append_transaction",
		RecoveryPath:          "/sessions/session-1/append-recovery.json",
		EventsPath:            "/sessions/session-1/events.jsonl",
		CurrentMetadataSHA256: "current",
		PreMetadataSHA256:     "pre",
		PostMetadataSHA256:    "post",
		Phase:                 "committed",
		Conflict:              session.IrreconcilableRecoveryConflictCommittedSuffix,
		Suffix: &session.IrreconcilableRecoverySuffixIdentity{
			StartOffset:   101,
			EndOffset:     202,
			EventCount:    2,
			FirstSequence: 7,
			LastSequence:  8,
			SHA256:        "suffix",
		},
	}

	response := responseForError("recovery-conflict", detail)
	if response.Error == nil {
		t.Fatal("recovery-conflict response did not include an error")
	}
	if response.Error.Message != detail.Error() {
		t.Fatalf("recovery-conflict message = %q, want projected detail %q", response.Error.Message, detail.Error())
	}
}

func TestStreamCompleteParamsMapsTerminalErrors(t *testing.T) {
	for _, err := range []error{nil, io.EOF, context.Canceled, context.DeadlineExceeded} {
		params := streamCompleteParams(err)
		if params.Code != 0 || params.Message != "" {
			t.Fatalf("streamCompleteParams(%v) = %+v, want empty completion", err, params)
		}
	}
	params := streamCompleteParams(serverapi.ErrStreamFailed)
	if params.Code != protocol.ErrCodeStreamFailed || params.Message != serverapi.ErrStreamFailed.Error() {
		t.Fatalf("streamCompleteParams(stream failed) = %+v, want stream-failed code/message", params)
	}
	transcriptErr := serverapi.NewTranscriptStreamError(serverapi.TranscriptCloseReasonSubscriberOverflow, serverapi.ErrStreamGap)
	params = streamCompleteParams(transcriptErr)
	if params.Code != protocol.ErrCodeStreamGap || params.TranscriptCloseReason != string(serverapi.TranscriptCloseReasonSubscriberOverflow) {
		t.Fatalf("streamCompleteParams(transcript overflow) = %+v, want stream gap plus typed transcript reason", params)
	}
}

func TestNewGatewayRejectsTypedNilDependencies(t *testing.T) {
	var appCore *core.Core

	gateway, err := NewGateway(appCore, gatewayTestIdentity())
	if err == nil {
		t.Fatal("expected typed nil dependencies to be rejected")
	}
	if gateway != nil {
		t.Fatalf("gateway = %+v, want nil", gateway)
	}
	if !errors.Is(err, ErrGatewayDependenciesRequired) {
		t.Fatalf("error = %q, want ErrGatewayDependenciesRequired", err.Error())
	}
}

func TestCancellationMessageRoundTripsThroughRemoteClient(t *testing.T) {
	code, message := protocolError(&shelltool.PollingCanceledError{SessionID: "1000", Active: true})
	if code != protocol.ErrCodeRequestCanceled {
		t.Fatalf("protocol error code = %d, want %d", code, protocol.ErrCodeRequestCanceled)
	}

	handlerErrs := make(chan error, 8)
	server := httptest.NewServer(rpcwire.NewWebSocketTransport().Handler(func(ctx context.Context, conn rpcwire.Conn) {
		for event := range conn.Events() {
			if event.Err != nil {
				return
			}
			if event.Frame.Kind == rpcwire.FrameBinary {
				if err := serveGatewayRemoteTestHandshake(ctx, conn, event.Frame); err != nil {
					reportGatewayHandlerError(handlerErrs, "send handshake: %v", err)
					return
				}
				continue
			}
			req, err := event.Frame.DecodeRequest()
			if err != nil {
				reportGatewayHandlerError(handlerErrs, "decode request: %v", err)
				return
			}
			switch req.Method {
			case protocol.MethodChatContextGet:
				resp := protocol.NewErrorResponse(req.ID, code, message)
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(resp)); err != nil {
					reportGatewayHandlerError(handlerErrs, "send project list error: %w", err)
				}
				return
			default:
				reportGatewayHandlerError(handlerErrs, "unexpected method %q", req.Method)
				return
			}
		}
	}))
	defer server.Close()

	remote, err := remoteclient.DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()

	_, err = remote.GetChatContext(context.Background(), serverapi.NewWorkspaceChatContextRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("GetChatContext error = %v, want context.Canceled", err)
	}
	if err == nil || err.Error() != message {
		t.Fatalf("expected cancellation message %q, got %v", message, err)
	}
	if message == context.Canceled.Error() {
		t.Fatalf("test precondition failed: expected normalized message, got %q", message)
	}
	requireNoGatewayHandlerError(t, handlerErrs)
}

func newGatewayTestAuthSupport(t *testing.T, ready bool) serverbootstrap.AuthSupport {
	t.Helper()
	store := auth.NewMemoryStore(auth.EmptyState())
	authSupport, err := serverbootstrap.BuildAuthSupport(store, nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthSupport: %v", err)
	}
	if ready {
		if _, err := authSupport.AuthManager.SwitchMethod(context.Background(), auth.Method{
			Type:   auth.MethodAPIKey,
			APIKey: &auth.APIKeyMethod{Key: "test-key"},
		}, true); err != nil {
			t.Fatalf("SwitchMethod: %v", err)
		}
	}
	return authSupport
}

func activateGatewayController(t *testing.T, appCore *core.Core, sessionID string) serverapi.SessionRuntimeAttachment {
	t.Helper()
	settings := appCore.Config().Settings
	if strings.TrimSpace(settings.Model) == "" {
		settings.Model = "gpt-5"
	}
	if strings.TrimSpace(settings.ProviderOverride) == "" && strings.TrimSpace(settings.OpenAIBaseURL) == "" {
		settings.ProviderOverride = "openai"
	}
	response, err := appCore.SessionRuntimeClient().ActivateSessionRuntime(context.Background(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID:       "activate-" + strings.TrimSpace(sessionID),
		SessionID:             strings.TrimSpace(sessionID),
		OwnerID:               "gateway-test-owner",
		ActiveSettings:        settings,
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
		Source:                appCore.Config().Source,
	})
	if err != nil {
		t.Fatalf("ActivateSessionRuntime: %v", err)
	}
	if err := response.ValidateForSession(sessionID); err != nil {
		t.Fatalf("validate activation response: %v", err)
	}
	return response.Attachment
}

func releaseGatewayController(t *testing.T, appCore *core.Core, attachment serverapi.SessionRuntimeAttachment) {
	t.Helper()
	if _, err := appCore.SessionRuntimeClient().ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "release-" + strings.TrimSpace(attachment.SessionID),
		Attachment:      attachment,
		OwnerID:         "gateway-test-owner",
	}); err != nil {
		t.Fatalf("ReleaseSessionRuntime: %v", err)
	}
}

func gatewayRuntimeActivateRequest(appCore *core.Core, sessionID string, requestID string) serverapi.SessionRuntimeActivateRequest {
	settings := appCore.Config().Settings
	if strings.TrimSpace(settings.Model) == "" {
		settings.Model = "gpt-5"
	}
	if strings.TrimSpace(settings.ProviderOverride) == "" && strings.TrimSpace(settings.OpenAIBaseURL) == "" {
		settings.ProviderOverride = "openai"
	}
	return serverapi.SessionRuntimeActivateRequest{
		ClientRequestID:       strings.TrimSpace(requestID),
		SessionID:             strings.TrimSpace(sessionID),
		ActiveSettings:        settings,
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
		Source:                appCore.Config().Source,
	}
}

func waitForGatewayCondition(t *testing.T, label string, condition func() bool) {
	t.Helper()
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, condition, "timed out waiting for %s", label)
}

type countingSessionRuntimeClient struct {
	apicontract.SessionRuntimeService
	releaseCount        atomic.Int32
	activateRequests    chan serverapi.SessionRuntimeActivateRequest
	releaseRequests     chan serverapi.SessionRuntimeReleaseRequest
	activateAttachments []*serverapi.SessionRuntimeAttachment
	releaseResponse     *serverapi.SessionRuntimeReleaseResponse
}

func (c *countingSessionRuntimeClient) ActivateSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
	if c.activateRequests != nil {
		c.activateRequests <- req
	}
	if len(c.activateAttachments) != 0 {
		attachment := c.activateAttachments[0]
		c.activateAttachments = c.activateAttachments[1:]
		if attachment == nil {
			return serverapi.SessionRuntimeActivateResponse{}, nil
		}
		value := *attachment
		value.SessionID = req.SessionID
		return serverapi.SessionRuntimeActivateResponse{Attachment: value}, nil
	}
	return c.SessionRuntimeService.ActivateSessionRuntime(ctx, req)
}

func (c *countingSessionRuntimeClient) ReleaseSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
	c.releaseCount.Add(1)
	if c.releaseRequests != nil {
		c.releaseRequests <- req
	}
	if c.releaseResponse != nil {
		return *c.releaseResponse, nil
	}
	return c.SessionRuntimeService.ReleaseSessionRuntime(ctx, req)
}

type gatewayRuntimeClientOverride struct {
	*core.Core
	runtimeClient apicontract.SessionRuntimeService
}

func (d *gatewayRuntimeClientOverride) SessionRuntimeClient() apicontract.SessionRuntimeService {
	return d.runtimeClient
}

func TestGatewayConnectionCloseReleasesOwnedIdleRuntime(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	counter := &countingSessionRuntimeClient{
		SessionRuntimeService: appCore.SessionRuntimeClient(),
		activateRequests:      make(chan serverapi.SessionRuntimeActivateRequest, 2),
		releaseRequests:       make(chan serverapi.SessionRuntimeReleaseRequest, 3),
		activateAttachments:   []*serverapi.SessionRuntimeAttachment{{Generation: 1}, {Generation: 2}},
		releaseResponse:       &serverapi.SessionRuntimeReleaseResponse{Released: true},
	}
	gateway, err := NewGateway(&gatewayRuntimeClientOverride{Core: appCore, runtimeClient: counter}, gatewayTestIdentity())
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptest.NewServer(gateway.Handler())
	defer func() { _ = appCore.Close() }()
	defer server.Close()
	store := createGatewayAuthoritativeSession(t, appCore)

	conn := dialGateway(t, server)
	handshakeGateway(t, conn)
	var activation serverapi.SessionRuntimeActivateResponse
	request := gatewayRuntimeActivateRequest(appCore, store.Meta().SessionID, "activate-runtime")
	request.OwnerID = "client-spoof"
	callGateway(t, conn, "activate-runtime", protocol.MethodSessionRuntimeActivate, request, &activation)
	var activationRequest serverapi.SessionRuntimeActivateRequest
	select {
	case activationRequest = <-counter.activateRequests:
		if activationRequest.OwnerID == "" || activationRequest.OwnerID == "client-spoof" {
			t.Fatalf("gateway did not inject connection owner id: %+v", activationRequest)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for activation request")
	}
	var successor serverapi.SessionRuntimeActivateResponse
	callGateway(t, conn, "activate-runtime-2", protocol.MethodSessionRuntimeActivate, gatewayRuntimeActivateRequest(appCore, store.Meta().SessionID, "activate-runtime-2"), &successor)
	callGateway(t, conn, "release-runtime-1", protocol.MethodSessionRuntimeRelease, serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "release-runtime-1",
		Attachment:      activation.Attachment,
		OwnerID:         "client-spoof",
		DropOwner:       true,
		ClosePolicy:     serverapi.SessionRuntimeReleaseClosePolicyDetachOnly,
	}, nil)
	select {
	case request := <-counter.releaseRequests:
		if request.Attachment != activation.Attachment || request.OwnerID != activationRequest.OwnerID {
			t.Fatalf("explicit stale release request = %+v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stale explicit release")
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close gateway connection: %v", err)
	}
	select {
	case request := <-counter.releaseRequests:
		if request.Attachment != successor.Attachment {
			t.Fatalf("disconnect release attachment = %+v, want successor %+v", request.Attachment, successor.Attachment)
		}
		if request.OwnerID != activationRequest.OwnerID || !request.DropOwner || request.ClosePolicy != serverapi.SessionRuntimeReleaseClosePolicyCloseIfIdle {
			t.Fatalf("disconnect release request = %+v, want exact close-if-idle owner drop", request)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for disconnect runtime release")
	}
	select {
	case request := <-counter.releaseRequests:
		t.Fatalf("disconnect released stale attachment too: %+v", request.Attachment)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestGatewayDetachOnlyReleaseInjectsOwnerAndSkipsDisconnectRelease(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	counter := &countingSessionRuntimeClient{
		SessionRuntimeService: appCore.SessionRuntimeClient(),
		releaseRequests:       make(chan serverapi.SessionRuntimeReleaseRequest, 4),
		activateAttachments:   []*serverapi.SessionRuntimeAttachment{{Generation: 1}},
		releaseResponse:       &serverapi.SessionRuntimeReleaseResponse{Active: true},
	}
	gateway, err := NewGateway(&gatewayRuntimeClientOverride{Core: appCore, runtimeClient: counter}, gatewayTestIdentity())
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptest.NewServer(gateway.Handler())
	defer func() { _ = appCore.Close() }()
	defer server.Close()
	store := createGatewayAuthoritativeSession(t, appCore)

	conn := dialGateway(t, server)
	handshakeGateway(t, conn)
	var activation serverapi.SessionRuntimeActivateResponse
	callGateway(t, conn, "activate-runtime", protocol.MethodSessionRuntimeActivate, gatewayRuntimeActivateRequest(appCore, store.Meta().SessionID, "activate-runtime"), &activation)
	var release serverapi.SessionRuntimeReleaseResponse
	callGateway(t, conn, "release-runtime", protocol.MethodSessionRuntimeRelease, serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "release-runtime",
		Attachment:      activation.Attachment,
		OwnerID:         "client-spoof",
		DropOwner:       true,
		ClosePolicy:     serverapi.SessionRuntimeReleaseClosePolicyDetachOnly,
	}, &release)
	if release.Released || !release.Active {
		t.Fatalf("detach-only release response = %+v, want active unreleased response", release)
	}
	select {
	case req := <-counter.releaseRequests:
		if req.OwnerID == "" || req.OwnerID == "client-spoof" {
			t.Fatalf("gateway did not inject connection owner id: %+v", req)
		}
		if !req.DropOwner || req.ClosePolicy != serverapi.SessionRuntimeReleaseClosePolicyDetachOnly {
			t.Fatalf("gateway release request = %+v, want detach-only drop owner", req)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for explicit detach-only release")
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close gateway connection: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := counter.releaseCount.Load(); got != 1 {
		t.Fatalf("runtime release call count = %d, want only explicit detach-only release", got)
	}
}

func TestGatewayCloseIfIdleReleasePropagatesPolicy(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	counter := &countingSessionRuntimeClient{
		SessionRuntimeService: appCore.SessionRuntimeClient(),
		releaseRequests:       make(chan serverapi.SessionRuntimeReleaseRequest, 4),
		activateAttachments:   []*serverapi.SessionRuntimeAttachment{{Generation: 1}},
		releaseResponse:       &serverapi.SessionRuntimeReleaseResponse{Released: true},
	}
	gateway, err := NewGateway(&gatewayRuntimeClientOverride{Core: appCore, runtimeClient: counter}, gatewayTestIdentity())
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptest.NewServer(gateway.Handler())
	defer func() { _ = appCore.Close() }()
	defer server.Close()
	store := createGatewayAuthoritativeSession(t, appCore)

	conn := dialGateway(t, server)
	handshakeGateway(t, conn)
	var activation serverapi.SessionRuntimeActivateResponse
	callGateway(t, conn, "activate-runtime", protocol.MethodSessionRuntimeActivate, gatewayRuntimeActivateRequest(appCore, store.Meta().SessionID, "activate-runtime"), &activation)
	var release serverapi.SessionRuntimeReleaseResponse
	callGateway(t, conn, "release-runtime", protocol.MethodSessionRuntimeRelease, serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "release-runtime",
		Attachment:      activation.Attachment,
		DropOwner:       true,
		ClosePolicy:     serverapi.SessionRuntimeReleaseClosePolicyCloseIfIdle,
	}, &release)
	if !release.Released {
		t.Fatalf("close-if-idle release response = %+v, want released", release)
	}
	select {
	case req := <-counter.releaseRequests:
		if req.OwnerID == "" {
			t.Fatalf("gateway did not inject owner id: %+v", req)
		}
		if req.Attachment != activation.Attachment || !req.DropOwner || req.ClosePolicy != serverapi.SessionRuntimeReleaseClosePolicyCloseIfIdle {
			t.Fatalf("gateway release request = %+v, want explicit close-if-idle drop owner", req)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for explicit close-if-idle release")
	}
}

func TestGatewayMissingActivationAttachmentDoesNotRecordRuntimeOwnership(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	counter := &countingSessionRuntimeClient{
		SessionRuntimeService: appCore.SessionRuntimeClient(),
		activateAttachments:   []*serverapi.SessionRuntimeAttachment{nil},
	}
	gateway, err := NewGateway(&gatewayRuntimeClientOverride{Core: appCore, runtimeClient: counter}, gatewayTestIdentity())
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptest.NewServer(gateway.Handler())
	defer func() { _ = appCore.Close() }()
	defer server.Close()
	store := createGatewayAuthoritativeSession(t, appCore)

	conn := dialGateway(t, server)
	handshakeGateway(t, conn)
	_ = callGatewayExpectError(t, conn, "activate-runtime", protocol.MethodSessionRuntimeActivate, gatewayRuntimeActivateRequest(appCore, store.Meta().SessionID, "activate-runtime"))
	if err := conn.Close(); err != nil {
		t.Fatalf("close gateway connection: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := counter.releaseCount.Load(); got != 0 {
		t.Fatalf("runtime release call count after invalid activation response = %d, want 0", got)
	}
}

func TestGatewayHandshakeAndProjectList(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()

	handshakeGateway(t, conn)
	if protocol.Version != "126" {
		t.Fatalf("protocol version = %q, want 126", protocol.Version)
	}

	projectCatalog := projectpb.File_kent_api_project_project_proto.Services().ByName("ProjectCatalogService")
	if projectCatalog == nil {
		t.Fatal("Project Catalog descriptor is required")
	}
	projectList := projectCatalog.Methods().ByName("List")
	if projectList == nil {
		t.Fatal("Project List descriptor is required")
	}
	result := &projectpb.ProjectListResult{}
	callGatewayDescriptor(t, conn, "2", projectList, &emptypb.Empty{}, result)
	if result.GetError() != nil {
		t.Fatalf("Project List error: %+v", result.GetError())
	}
	projects := result.GetSuccess().GetProjects()
	if len(projects) != 1 || projects[0].ProjectId != appCore.ProjectID() {
		t.Fatalf("unexpected project list: %+v", projects)
	}
}

func TestGatewayHandshakeRejectsProtocolVersionMismatch(t *testing.T) {
	_, server := newGatewayTestServer(t)
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()

	result := handshakeGatewayVersion(t, conn, "46")
	failure := result.GetError()
	if failure == nil ||
		failure.Code != "protocol_version_mismatch" ||
		failure.GetProtocolVersionMismatch().RequiredProtocolVersion != protocol.Version {
		t.Fatalf("expected unsupported protocol version error, got %+v", failure)
	}
}

func TestGatewayTaskSearchDispatchesIndexedResponseAndTypedValidationError(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer func() { _ = appCore.Close() }()
	defer server.Close()
	task := createGatewaySearchableTask(t, appCore)

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)

	request := serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeLiteral,
		Query:    "needle",
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: serverapi.TaskSearchDefaultPageSize,
	}
	var response serverapi.TaskSearchResponse
	callGateway(t, conn, "search", protocol.MethodWorkflowTaskSearch, request, &response)
	if err := response.Validate(); err != nil {
		t.Fatalf("search response validation: %v", err)
	}
	if response.Mode != request.Mode ||
		len(response.Groups) != 1 ||
		response.Groups[0].TaskID != task.ID ||
		response.Groups[0].TotalHitCount != 1 ||
		len(response.Groups[0].Hits) != 1 ||
		response.Groups[0].Hits[0].Source.Kind != serverapi.TaskSearchSourceKindBody ||
		response.Groups[0].Hits[0].Literal == nil ||
		response.NextOffset != nil {
		t.Fatalf("indexed search response = %+v", response)
	}

	responseError := callGatewayExpectError(t, conn, "short", protocol.MethodWorkflowTaskSearch, serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeLiteral,
		Query:    "ab",
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: serverapi.TaskSearchDefaultPageSize,
	})
	if responseError.Code != protocol.ErrCodeWorkflowTaskSearch {
		t.Fatalf("short literal error = %+v, want task search code", responseError)
	}
	decoded := serverapi.DecodeTaskSearchError(responseError.Data, responseError.Message)
	var typed *serverapi.TaskSearchError
	if !errors.As(decoded, &typed) || typed.Reason != serverapi.TaskSearchErrorReasonNormalizedTooShort {
		t.Fatalf("short literal decoded error = %T %v", decoded, decoded)
	}
}

func TestGatewayRemoteTaskSearchRoundsTripIndexedResponse(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer func() { _ = appCore.Close() }()
	defer server.Close()
	task := createGatewaySearchableTask(t, appCore)

	remote, err := remoteclient.DialRemoteURLForProject(
		context.Background(),
		"ws"+server.URL[len("http"):],
		appCore.ProjectID(),
	)
	if err != nil {
		t.Fatalf("DialRemoteURLForProject: %v", err)
	}
	defer func() { _ = remote.Close() }()

	response, err := remote.SearchWorkflowTasks(context.Background(), serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeFTS5,
		Query:    "body:needle",
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: serverapi.TaskSearchDefaultPageSize,
	})
	if err != nil {
		t.Fatalf("SearchWorkflowTasks: %v", err)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("validate remote task-search response: %v", err)
	}
	if response.Mode != serverapi.TaskSearchModeFTS5 ||
		len(response.Groups) != 1 ||
		response.Groups[0].TaskID != task.ID ||
		len(response.Groups[0].Hits) != 1 ||
		response.Groups[0].Hits[0].Source.Kind != serverapi.TaskSearchSourceKindBody ||
		response.Groups[0].Hits[0].FTS5 == nil {
		t.Fatalf("remote indexed task-search response = %+v", response)
	}
}

func TestGatewayRemoteWorkflowTaskSessionsRoundsTripPage(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer func() { _ = appCore.Close() }()
	defer server.Close()
	task := createGatewaySearchableTask(t, appCore)

	remote, err := remoteclient.DialRemoteURLForProject(
		context.Background(),
		"ws"+server.URL[len("http"):],
		appCore.ProjectID(),
	)
	if err != nil {
		t.Fatalf("DialRemoteURLForProject: %v", err)
	}
	defer func() { _ = remote.Close() }()

	response, err := remote.ListWorkflowTaskSessions(context.Background(), serverapi.WorkflowTaskOffsetPageRequest{
		TaskID: task.ID,
	})
	if err != nil {
		t.Fatalf("ListWorkflowTaskSessions: %v", err)
	}
	if response.TaskID != task.ID || response.Items == nil || len(response.Items) != 0 || response.NextOffset != nil {
		t.Fatalf("response = %+v", response)
	}
}

func createGatewaySearchableTask(t *testing.T, appCore *core.Core) serverapi.WorkflowTaskSummary {
	t.Helper()
	ctx := context.Background()
	workflows := appCore.WorkflowClient()
	created, err := workflows.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{Name: "Search Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	definition, err := workflows.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: created.Workflow.ID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	startID, terminalID := "", ""
	for _, node := range definition.Definition.Nodes {
		switch node.Kind {
		case "start":
			startID = node.ID
		case "terminal":
			terminalID = node.ID
		}
	}
	if startID == "" || terminalID == "" {
		t.Fatalf("workflow definition lacks start/terminal Nodes: %+v", definition.Definition.Nodes)
	}
	agentID := runtimeids.NewGraphEntityID()
	reviewID := runtimeids.NewGraphEntityID()
	startGroupID := runtimeids.NewGraphEntityID()
	doneGroupID := runtimeids.NewGraphEntityID()
	finishGroupID := runtimeids.NewGraphEntityID()
	graph := serverapi.WorkflowGraphDraftFromDefinition(definition.Definition)
	graph.Nodes = append(graph.Nodes,
		serverapi.WorkflowGraphDraftNode{ID: agentID, Key: "agent", Kind: "agent", DisplayName: "Agent", SubagentRole: "coder"},
		serverapi.WorkflowGraphDraftNode{ID: reviewID, Key: "review", Kind: "agent", DisplayName: "Review", SubagentRole: "coder"},
	)
	graph.TransitionGroups = append(graph.TransitionGroups,
		serverapi.WorkflowGraphDraftTransitionGroup{ID: startGroupID, SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: doneGroupID, SourceNodeID: agentID, TransitionID: "done", DisplayName: "Done"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: finishGroupID, SourceNodeID: reviewID, TransitionID: "finish", DisplayName: "Finish"},
	)
	graph.Edges = append(graph.Edges,
		serverapi.WorkflowGraphDraftEdge{ID: runtimeids.NewGraphEntityID(), TransitionGroupID: startGroupID, Key: "start", TargetNodeID: agentID, AssigneeSelection: "configured", ThinkingSelection: "configured", ContextMode: "new_session", PromptTemplate: "Search work."},
		serverapi.WorkflowGraphDraftEdge{ID: runtimeids.NewGraphEntityID(), TransitionGroupID: doneGroupID, Key: "done", TargetNodeID: reviewID, AssigneeSelection: "configured", ThinkingSelection: "configured", ContextMode: "new_session", PromptTemplate: "Review the search work."},
		serverapi.WorkflowGraphDraftEdge{ID: runtimeids.NewGraphEntityID(), TransitionGroupID: finishGroupID, Key: "finish", TargetNodeID: terminalID, AssigneeSelection: "configured", ThinkingSelection: "configured", ContextMode: "new_session"},
	)
	saved, err := workflows.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID: created.Workflow.ID, ExpectedVersion: definition.Definition.Workflow.Version, Graph: graph,
	})
	if err != nil || !saved.Saved {
		t.Fatalf("SaveWorkflowGraph searchable task fixture = %+v, err = %v", saved, err)
	}
	if _, err := workflows.LinkWorkflowToProject(ctx, serverapi.WorkflowLinkProjectRequest{
		ProjectID:     appCore.ProjectID(),
		WorkflowID:    created.Workflow.ID,
		DefaultPolicy: serverapi.WorkflowProjectLinkDefaultAlways,
	}); err != nil {
		t.Fatalf("LinkWorkflowToProject: %v", err)
	}
	task, err := workflows.CreateWorkflowTask(ctx, serverapi.WorkflowTaskCreateRequest{
		ProjectID: appCore.ProjectID(),
		Title:     "Search Task",
		Body:      "needle body",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowTask: %v", err)
	}
	return task.Task
}

func TestGatewayRejectsMethodsBeforeHandshake(t *testing.T) {
	_, server := newGatewayTestServer(t)
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()

	sendGatewayRequest(t, conn, "1", protocol.MethodProcessList, serverapi.ProcessListRequest{})
	var response protocol.Response
	if err := websocket.JSON.Receive(conn, &response); err == nil {
		t.Fatalf("pre-handshake application traffic unexpectedly received %+v", response)
	}
}

func TestGatewayPreAuthMethodPolicy(t *testing.T) {
	registration, err := productionGatewayRegistration()
	if err != nil {
		t.Fatalf("production Gateway registration: %v", err)
	}
	if err := registration.Validate(); err != nil {
		t.Fatalf("validate production Gateway registration: %v", err)
	}
	executor := newRoutePolicyExecutor(&Gateway{registration: registration})
	for name, operation := range registration.operations {
		activeIdentity := name
		if route, legacy := registration.legacy[name]; legacy {
			activeIdentity = route.Method
		}
		got := executor.requiresServerAuth(activeIdentity)
		want := operation.Options.AuthenticationStage == sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_SERVER
		if got != want {
			t.Fatalf("requiresServerAuth(%q) = %t, want %t from descriptor", activeIdentity, got, want)
		}
	}
}

func TestGatewayAuthBootstrapAPIKeyCompletionEnablesAuthRequiredMethods(t *testing.T) {
	appCore, server, authSupport := newGatewayTestServerWithAuth(t, false)
	defer func() { _ = appCore.Close() }()
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)

	requireGatewayProjectAttachment(t, conn, "attach-project", &connectionpb.AttachProjectRequest{ProjectId: appCore.ProjectID()})
	if respErr := callGatewayExpectError(t, conn, "run-1", protocol.MethodRunPrompt, serverapi.RunPromptRequest{ClientRequestID: "run-1", Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()), Prompt: "test"}); respErr.Code != protocol.ErrCodeAuthRequired {
		t.Fatalf("run.prompt error = %+v, want auth required", respErr)
	}

	apiKey := "server-key"
	callGatewayAuthCompleteBootstrap(t, conn, "complete-1", &authpb.CompleteBootstrapRequest{
		Mode:   authpb.BootstrapMode_BOOTSTRAP_MODE_API_KEY,
		ApiKey: &apiKey,
	})
	status := callGatewayAuthBootstrapStatus(t, conn, "status-2")
	if !status.AuthReady {
		t.Fatal("expected bootstrap completion to configure server auth")
	}
	state, err := authSupport.AuthManager.StoredState(context.Background())
	if err != nil {
		t.Fatalf("StoredState: %v", err)
	}
	if state.Method.APIKey == nil || state.Method.APIKey.Key != "server-key" {
		t.Fatalf("unexpected stored auth method: %+v", state.Method)
	}

	secondAPIKey := "server-key-2"
	secondComplete := callGatewayAuthCompleteBootstrap(t, conn, "complete-2", &authpb.CompleteBootstrapRequest{Mode: authpb.BootstrapMode_BOOTSTRAP_MODE_API_KEY, ApiKey: &secondAPIKey})
	if !secondComplete.AuthReady || secondComplete.GetMethodType() != string(auth.MethodAPIKey) {
		t.Fatalf("unexpected second CompleteBootstrap result: %+v", secondComplete)
	}
	state, err = authSupport.AuthManager.StoredState(context.Background())
	if err != nil {
		t.Fatalf("StoredState after second complete: %v", err)
	}
	if state.Method.APIKey == nil || state.Method.APIKey.Key != "server-key" {
		t.Fatalf("unexpected stored auth method after retry: %+v", state.Method)
	}
}

func TestGatewayAuthBootstrapNoneAuthorizesSameConnectionOnly(t *testing.T) {
	appCore, server, _ := newGatewayTestServerWithAuth(t, false)
	defer func() { _ = appCore.Close() }()
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	requireGatewayProjectAttachment(t, conn, "attach-project", &connectionpb.AttachProjectRequest{ProjectId: appCore.ProjectID()})
	if result := callGatewaySessionPlanResult(t, conn, "plan-before-no-auth", gatewaySessionPlanRequest(t)); result.GetError().Code != "auth_required" {
		t.Fatalf("Session Plan before no-auth = %+v, want auth_required", result.GetError())
	}

	complete := callGatewayAuthCompleteBootstrap(t, conn, "complete-no-auth", &authpb.CompleteBootstrapRequest{Mode: authpb.BootstrapMode_BOOTSTRAP_MODE_NONE})
	if complete.AuthReady || !complete.NoAuthSelected {
		t.Fatalf("CompleteBootstrap none = %+v, want not ready and no-auth selected", complete)
	}

	plan := callGatewaySessionPlan(t, conn, "plan-after-no-auth", gatewaySessionPlanRequest(t))
	target := gatewaySessionExecutionTarget(t, conn, "main-view-after-no-auth", plan.Plan.SessionId)
	if strings.TrimSpace(target.EffectiveWorkdir) == "" {
		t.Fatalf("typed session target after no-auth has empty effective workdir: %+v", target)
	}
}

func TestGatewayPersistedNoAuthDoesNotAuthorizeFreshConnectionsWithoutAck(t *testing.T) {
	appCore, server, authSupport := newGatewayTestServerWithAuth(t, false)
	defer func() { _ = appCore.Close() }()
	store := createGatewayAuthoritativeSession(t, appCore)
	defer server.Close()
	if _, err := authSupport.AuthManager.SwitchMethodAndSetEnvAPIKeyPreference(context.Background(), auth.Method{Type: auth.MethodNone}, auth.EnvAPIKeyPreferencePreferSaved, true, true); err != nil {
		t.Fatalf("SwitchMethodAndSetEnvAPIKeyPreference: %v", err)
	}

	control := dialGateway(t, server)
	defer func() { _ = control.Close() }()
	handshakeGateway(t, control)
	requireGatewayProjectAttachment(t, control, "attach-project", &connectionpb.AttachProjectRequest{ProjectId: appCore.ProjectID()})
	if result := callGatewaySessionPlanResult(t, control, "plan-fresh-no-ack", gatewaySessionPlanRequest(t)); result.GetError().Code != "auth_required" {
		t.Fatalf("fresh Session Plan = %+v, want auth_required", result.GetError())
	}

	subscription := dialGateway(t, server)
	defer func() { _ = subscription.Close() }()
	handshakeGateway(t, subscription)
	requireGatewayProjectAttachment(t, subscription, "attach-project", &connectionpb.AttachProjectRequest{ProjectId: appCore.ProjectID()})
	requireGatewaySessionAttachment(t, subscription, "attach-session", store.Meta().SessionID)
	if respErr := callGatewayExpectError(t, subscription, "subscribe-fresh-no-ack", protocol.MethodSessionSubscribeTranscript, serverapi.TranscriptSubscribeRequest{SessionID: store.Meta().SessionID}); respErr.Code != protocol.ErrCodeAuthRequired {
		t.Fatalf("fresh session transcript subscribe = %+v, want auth required", respErr)
	}
}

func TestGatewayRejectsProjectWorkspaceMutationBeforeServerAuthReady(t *testing.T) {
	appCore, server, _ := newGatewayTestServerWithAuth(t, false)
	defer func() { _ = appCore.Close() }()
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	requireGatewayProjectAttachment(t, conn, "attach-project", &connectionpb.AttachProjectRequest{ProjectId: appCore.ProjectID()})

	method := projectpb.File_kent_api_project_project_proto.Services().
		ByName("ProjectCatalogService").Methods().ByName("AttachWorkspace")
	var result projectpb.AttachWorkspaceResult
	callGatewayDescriptor(t, conn, "attach-workspace", method, &projectpb.AttachWorkspaceRequest{
		ProjectId: appCore.ProjectID(), WorkspaceRoot: "/tmp/workspace",
	}, &result)
	if failure := result.GetError(); failure == nil || failure.Code != "auth_required" {
		t.Fatalf("Project AttachWorkspace result = %+v, want auth_required", &result)
	}
}

func TestGatewaySessionTranscriptSubscriptionReturnsHydrationOnDedicatedRoute(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer func() { _ = appCore.Close() }()
	store := createGatewayAuthoritativeSession(t, appCore)
	attachment := activateGatewayController(t, appCore, store.Meta().SessionID)
	defer releaseGatewayController(t, appCore, attachment)
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	requireGatewayProjectAttachment(t, conn, "attach-project", &connectionpb.AttachProjectRequest{ProjectId: appCore.ProjectID()})
	requireGatewaySessionAttachment(t, conn, "attach-session", store.Meta().SessionID)
	callGateway(t, conn, "subscribe-transcript", protocol.MethodSessionSubscribeTranscript, serverapi.TranscriptSubscribeRequest{SessionID: store.Meta().SessionID}, nil)

	var notification protocol.Request
	if err := websocket.JSON.Receive(conn, &notification); err != nil {
		t.Fatalf("receive transcript notification: %v", err)
	}
	if notification.Method != protocol.MethodSessionTranscriptEvent {
		t.Fatalf("notification method = %q, want transcript event", notification.Method)
	}
	var params protocol.SessionTranscriptEventParams
	if err := json.Unmarshal(notification.Params, &params); err != nil {
		t.Fatalf("decode transcript event: %v", err)
	}
	if params.Message.Sequence != 1 || params.Message.Kind() != clientui.TranscriptMessageHydration {
		t.Fatalf("transcript message = %+v, want seq=1 hydration", params.Message)
	}
	var paramsWire map[string]json.RawMessage
	if err := json.Unmarshal(notification.Params, &paramsWire); err != nil {
		t.Fatalf("decode transcript event envelope: %v", err)
	}
	var messageWire map[string]json.RawMessage
	if err := json.Unmarshal(paramsWire["message"], &messageWire); err != nil {
		t.Fatalf("decode transcript message wire envelope: %v", err)
	}
	for _, field := range []string{"sequence", "kind", "payload"} {
		if _, ok := messageWire[field]; !ok {
			t.Fatalf("transcript wire envelope missing %q: %s", field, notification.Params)
		}
	}
	if string(messageWire["kind"]) != `"hydration"` || string(messageWire["payload"]) == "null" {
		t.Fatalf("transcript wire envelope = %s, want hydration with payload", notification.Params)
	}
}

func TestGatewayQuestionHistorySubscriptionPassesAttachedSessionPreflight(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer func() { _ = appCore.Close() }()
	defer server.Close()
	store := createGatewayAuthoritativeSession(t, appCore)
	if _, err := store.MaterializeEventLog(); err != nil {
		t.Fatalf("materialize Question-history event log: %v", err)
	}

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	requireGatewayProjectAttachment(t, conn, "attach-project", &connectionpb.AttachProjectRequest{ProjectId: appCore.ProjectID()})
	requireGatewaySessionAttachment(t, conn, "attach-session", store.Meta().SessionID)
	callGateway(t, conn, "subscribe-question-history", protocol.MethodSessionQuestionHistorySubscribe, serverapi.QuestionHistorySubscribeRequest{
		SessionID: store.Meta().SessionID, MaxHandoffs: 1,
	}, nil)

	for _, wantKind := range []serverapi.QuestionHistoryEventKind{
		serverapi.QuestionHistoryEventStarted,
		serverapi.QuestionHistoryEventCompleted,
	} {
		var notification protocol.Request
		if err := websocket.JSON.Receive(conn, &notification); err != nil {
			t.Fatalf("receive Question-history notification: %v", err)
		}
		if notification.Method != protocol.MethodSessionQuestionHistoryEvent {
			t.Fatalf("notification method = %q, want Question-history event", notification.Method)
		}
		var params protocol.SessionQuestionHistoryEventParams
		if err := json.Unmarshal(notification.Params, &params); err != nil {
			t.Fatalf("decode Question-history event: %v", err)
		}
		if params.Event.Kind != string(wantKind) {
			t.Fatalf("Question-history event kind = %q, want %q", params.Event.Kind, wantKind)
		}
	}
	var complete protocol.Request
	if err := websocket.JSON.Receive(conn, &complete); err != nil {
		t.Fatalf("receive Question-history completion: %v", err)
	}
	if complete.Method != protocol.MethodSessionQuestionHistoryComplete {
		t.Fatalf("completion method = %q, want Question-history complete", complete.Method)
	}
}

func gatewaySessionPlanRequest(t *testing.T) *sessionlaunchpb.SessionPlanRequest {
	t.Helper()
	intent, err := protoapi.SessionLaunchIntentToProto(
		serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
	)
	if err != nil {
		t.Fatalf("encode Session launch intent: %v", err)
	}
	return &sessionlaunchpb.SessionPlanRequest{
		Mode:   sessionlaunchpb.SessionLaunchMode_SESSION_LAUNCH_MODE_INTERACTIVE,
		Intent: intent,
	}
}

func callGatewaySessionPlanResult(
	t *testing.T,
	conn *websocket.Conn,
	correlation string,
	request *sessionlaunchpb.SessionPlanRequest,
) *sessionlaunchpb.SessionPlanResult {
	t.Helper()
	method := sessionlaunchpb.File_kent_api_session_launch_session_launch_proto.Services().
		ByName("SessionLaunchService").Methods().ByName("Plan")
	result := &sessionlaunchpb.SessionPlanResult{}
	callGatewayDescriptor(t, conn, correlation, method, request, result)
	return result
}

func callGatewaySessionPlan(
	t *testing.T,
	conn *websocket.Conn,
	correlation string,
	request *sessionlaunchpb.SessionPlanRequest,
) *sessionlaunchpb.SessionPlanSuccess {
	t.Helper()
	result := callGatewaySessionPlanResult(t, conn, correlation, request)
	if failure := result.GetError(); failure != nil {
		t.Fatalf("Session Plan failed: %+v", failure)
	}
	return result.GetSuccess()
}

func TestGatewayRejectsSessionAccessOutsideAttachedProject(t *testing.T) {
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	t.Setenv("HOME", home)
	configureGatewayTestServerPort(t)

	resolvedA := resolveGatewayTestConfig(t, workspaceA)
	bindingA := registerGatewayTestBinding(t, resolvedA.Config)
	resolvedB := resolveGatewayTestConfig(t, workspaceB)
	bindingB := registerGatewayTestBinding(t, resolvedB.Config)
	metadataStore, err := metadata.Open(resolvedA.Config.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	defer func() { _ = metadataStore.Close() }()
	foreignSession, err := session.Create(
		filepath.Join(filepath.Join(resolvedB.Config.PersistenceRoot, "projects"), bindingB.ProjectID, "sessions"),
		"workspace-b",
		resolvedB.Config.WorkspaceRoot,
		sessioncontract.SessionCategoryMain,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create foreign: %v", err)
	}
	if err := foreignSession.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable foreign: %v", err)
	}
	if _, err := metadataStore.ResolveSessionExecutionTarget(context.Background(), foreignSession.Meta().SessionID); err != nil {
		t.Fatalf("ResolveSessionExecutionTarget precondition: %v", err)
	}
	record, err := metadataStore.ResolvePersistedSession(context.Background(), foreignSession.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession precondition: %v", err)
	}
	opened, err := session.Open(record.SessionDir, metadataStore.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Open precondition: %v", err)
	}
	_ = opened

	_, server := newGatewayTestServerForConfig(t, resolvedA.Config)

	remote, err := remoteclient.DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], bindingA.ProjectID)
	if err != nil {
		t.Fatalf("DialRemoteURLForProject: %v", err)
	}
	defer func() { _ = remote.Close() }()

	if _, err := remote.GetSessionMainView(context.Background(), serverapi.SessionMainViewRequest{SessionID: foreignSession.Meta().SessionID}); err == nil {
		t.Fatal("expected foreign-project session view access to be rejected")
	}
	if _, err := remote.GetLatestCommittedAssistantFinalAnswer(context.Background(), serverapi.SessionLatestCommittedAssistantFinalAnswerRequest{SessionID: foreignSession.Meta().SessionID}); err == nil {
		t.Fatal("expected foreign-project final answer access to be rejected")
	}
	if _, err := remote.PersistInputDraft(context.Background(), serverapi.SessionPersistInputDraftRequest{ClientRequestID: "persist-foreign", SessionID: foreignSession.Meta().SessionID, Input: "should fail"}); err == nil {
		t.Fatal("expected foreign-project session mutation to be rejected")
	}
	if _, err := remote.RetargetSessionWorkspace(context.Background(), serverapi.SessionRetargetWorkspaceRequest{ClientRequestID: "retarget-foreign", SessionID: foreignSession.Meta().SessionID, WorkspaceRoot: resolvedA.Config.WorkspaceRoot}); err == nil {
		t.Fatal("expected foreign-project session retarget to be rejected")
	}
	foreignSessionID, err := runtimeids.ParseSessionID(foreignSession.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID foreign: %v", err)
	}
	if _, err := remote.ReadChatSettings(context.Background(), serverapi.ChatSettingsReadRequest{
		Target: serverapi.SessionChatSettingsTarget(foreignSessionID),
	}); err == nil {
		t.Fatal("expected foreign-project Chat settings access to be rejected")
	}
	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	requireGatewayProjectAttachment(t, conn, "attach-project-for-foreign-goal-checks", &connectionpb.AttachProjectRequest{ProjectId: bindingA.ProjectID})
	assertForeignGoalAccessRejected(t, conn, foreignSession.Meta().SessionID)
	if bindingA.ProjectID == bindingB.ProjectID {
		t.Fatalf("expected distinct project ids, both=%q", bindingA.ProjectID)
	}
}

func TestGatewayAuthorizesBothChatSettingsTargetArms(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer func() { _ = appCore.Close() }()
	defer server.Close()
	store := createGatewayAuthoritativeSession(t, appCore)
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	workspace, err := appCore.MetadataStore().ResolveProjectSourceWorkspace(
		t.Context(),
		appCore.ProjectID(),
	)
	if err != nil {
		t.Fatalf("ResolveProjectSourceWorkspace: %v", err)
	}
	remote, err := remoteclient.DialRemoteURLForProject(
		t.Context(),
		"ws"+server.URL[len("http"):],
		appCore.ProjectID(),
	)
	if err != nil {
		t.Fatalf("DialRemoteURLForProject: %v", err)
	}
	defer func() { _ = remote.Close() }()

	lazy, err := remote.ReadChatSettings(t.Context(), serverapi.ChatSettingsReadRequest{
		Target: serverapi.LazyChatSettingsTarget(appCore.ProjectID(), workspace.ID),
	})
	if err != nil {
		t.Fatalf("ReadChatSettings lazy: %v", err)
	}
	if lazy.Session != nil {
		t.Fatalf("lazy response has Session facts: %+v", lazy.Session)
	}
	materialized, err := remote.ReadChatSettings(t.Context(), serverapi.ChatSettingsReadRequest{
		Target: serverapi.SessionChatSettingsTarget(sessionID),
	})
	if err != nil {
		t.Fatalf("ReadChatSettings Session: %v", err)
	}
	if materialized.Session == nil || materialized.Session.SessionID != sessionID {
		t.Fatalf("materialized response = %+v", materialized.Session)
	}

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	requireGatewayProjectAttachment(t, conn, "attach-project-chat-settings", &connectionpb.AttachProjectRequest{ProjectId: appCore.ProjectID()})
	mismatched := callGatewayExpectError(
		t,
		conn,
		"chat-settings-project-mismatch",
		protocol.MethodChatSettingsRead,
		serverapi.ChatSettingsReadRequest{
			Target: serverapi.LazyChatSettingsTarget("project-foreign", workspace.ID),
		},
	)
	if mismatched.Code == 0 {
		t.Fatalf("lazy project mismatch unexpectedly succeeded")
	}
}

func assertForeignGoalAccessRejected(t *testing.T, conn *websocket.Conn, sessionID string) {
	t.Helper()
	wantMessage := "session " + strconv.Quote(sessionID) + " not available"
	for _, tc := range []struct {
		name   string
		method string
		params any
	}{
		{name: "show", method: protocol.MethodRuntimeGoalShow, params: serverapi.RuntimeGoalShowRequest{SessionID: sessionID}},
		{name: "set", method: protocol.MethodRuntimeGoalSet, params: serverapi.RuntimeGoalSetRequest{ClientRequestID: "foreign-goal-set", SessionID: sessionID, Objective: "ship", Actor: "user"}},
		{name: "pause", method: protocol.MethodRuntimeGoalPause, params: serverapi.RuntimeGoalStatusRequest{ClientRequestID: "foreign-goal-pause", SessionID: sessionID, Actor: "user"}},
		{name: "resume", method: protocol.MethodRuntimeGoalResume, params: serverapi.RuntimeGoalStatusRequest{ClientRequestID: "foreign-goal-resume", SessionID: sessionID, Actor: "user"}},
		{name: "complete", method: protocol.MethodRuntimeGoalComplete, params: serverapi.RuntimeGoalStatusRequest{ClientRequestID: "foreign-goal-complete", SessionID: sessionID, Actor: "agent"}},
		{name: "clear", method: protocol.MethodRuntimeGoalClear, params: serverapi.RuntimeGoalClearRequest{ClientRequestID: "foreign-goal-clear", SessionID: sessionID, Actor: "user"}},
	} {
		err := callGatewayExpectError(t, conn, "foreign-goal-"+tc.name, tc.method, tc.params)
		if err.Code != protocol.ErrCodeInternalError || err.Message != wantMessage {
			t.Fatalf("foreign goal %s error = code %d message %q, want code %d message %q", tc.name, err.Code, err.Message, protocol.ErrCodeInternalError, wantMessage)
		}
	}
}

func TestGatewayAllowsUnscopedSessionRetargetOutsideServerDefaultProject(t *testing.T) {
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	t.Setenv("HOME", home)
	configureGatewayTestServerPort(t)

	resolvedA := resolveGatewayTestConfig(t, workspaceA)
	bindingA := registerGatewayTestBinding(t, resolvedA.Config)
	resolvedB := resolveGatewayTestConfig(t, workspaceB)
	bindingB := registerGatewayTestBinding(t, resolvedB.Config)
	metadataStore, err := metadata.Open(resolvedA.Config.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	defer func() { _ = metadataStore.Close() }()
	foreignSession, err := session.Create(
		filepath.Join(filepath.Join(resolvedB.Config.PersistenceRoot, "projects"), bindingB.ProjectID, "sessions"),
		"workspace-b",
		resolvedB.Config.WorkspaceRoot, sessioncontract.SessionCategoryMain, metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create foreign: %v", err)
	}
	if err := foreignSession.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable foreign: %v", err)
	}

	authSupport := newGatewayTestAuthSupport(t, true)
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(resolvedA.Config)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}
	defer func() { _ = runtimeSupport.Background.Close() }()
	appCore, err := core.New(resolvedA.Config, authSupport, runtimeSupport)
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	defer func() { _ = appCore.Close() }()
	gateway, err := NewGateway(appCore, gatewayTestIdentity())
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	if err := gateway.requireSessionInAttachedProject(context.Background(), &connectionState{}, foreignSession.Meta().SessionID); err != nil {
		t.Fatalf("requireSessionInAttachedProject unscoped: %v", err)
	}
	if err := gateway.requireSessionInAttachedProject(context.Background(), &connectionState{attachedProject: bindingA.ProjectID}, foreignSession.Meta().SessionID); err == nil {
		t.Fatal("expected attached project scope to reject foreign session retarget")
	}
}

func TestGatewayAllowsOptionalSessionLifecycleRequestsWithoutSessionID(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	registerGatewayWorkspace(t, workspace)

	resolved := resolveGatewayTestConfig(t, workspace)
	binding, err := metadata.ResolveBinding(context.Background(), resolved.Config.PersistenceRoot, resolved.Config.WorkspaceRoot)
	if err != nil {
		t.Fatalf("ResolveBinding: %v", err)
	}
	_, server := newGatewayTestServerForConfig(t, resolved.Config)

	remote, err := remoteclient.DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], binding.ProjectID)
	if err != nil {
		t.Fatalf("DialRemoteURLForProject: %v", err)
	}
	defer func() { _ = remote.Close() }()

	initialInput, err := remote.GetInitialInput(context.Background(), serverapi.SessionInitialInputRequest{TransitionInput: "draft text"})
	if err != nil {
		t.Fatalf("GetInitialInput: %v", err)
	}
	if initialInput.Input != "draft text" {
		t.Fatalf("initial input = %q, want draft text", initialInput.Input)
	}

	resolvedTransition, err := remote.ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{
		ClientRequestID: "new-session-no-current-session",
		Transition: serverapi.SessionTransition{
			Action:        "new_session",
			InitialPrompt: "hello",
		},
	})
	if err != nil {
		t.Fatalf("ResolveTransition: %v", err)
	}
	intent, ok := resolvedTransition.LaunchIntent()
	if !ok || intent.Kind() != serverapi.SessionLaunchIntentCreateNew {
		t.Fatalf("unexpected transition response: %+v", resolvedTransition)
	}
	preparation, ok := resolvedTransition.LaunchPreparation()
	if !ok {
		t.Fatal("transition response omitted launch preparation")
	}
	prompt, ok := preparation.InitialPrompt()
	if !ok || prompt.Text != "hello" {
		t.Fatalf("initial prompt = %+v/%v, want hello", prompt, ok)
	}
}

func TestGatewayComposerDraftRoundTripKeepsServerAvailable(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	store := createGatewayAuthoritativeSession(t, appCore)

	remote, err := remoteclient.DialRemoteURLForProject(
		context.Background(),
		"ws"+server.URL[len("http"):],
		appCore.ProjectID(),
	)
	if err != nil {
		t.Fatalf("DialRemoteURLForProject: %v", err)
	}
	defer func() { _ = remote.Close() }()

	if _, err := remote.PersistInputDraft(context.Background(), serverapi.SessionPersistInputDraftRequest{
		ClientRequestID: "gateway-composer-draft",
		SessionID:       store.Meta().SessionID,
		Input:           "visible draft",
	}); err != nil {
		t.Fatalf("PersistInputDraft: %v", err)
	}
	initialInput, err := remote.GetInitialInput(context.Background(), serverapi.SessionInitialInputRequest{
		SessionID: store.Meta().SessionID,
	})
	if err != nil {
		t.Fatalf("GetInitialInput: %v", err)
	}
	if initialInput.Input != "visible draft" {
		t.Fatalf("initial input = %+v, want visible draft", initialInput)
	}
	projects, err := remote.ListProjects(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("follow-up ListProjects: %v", err)
	}
	if len(projects.Projects) == 0 {
		t.Fatal("follow-up ListProjects returned no projects")
	}
}

func TestGatewayProjectReattachClearsStaleSessionAttachment(t *testing.T) {
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	t.Setenv("HOME", home)
	configureGatewayTestServerPort(t)

	resolvedA := resolveGatewayTestConfig(t, workspaceA)
	bindingA := registerGatewayTestBinding(t, resolvedA.Config)
	resolvedB := resolveGatewayTestConfig(t, workspaceB)
	bindingB := registerGatewayTestBinding(t, resolvedB.Config)

	appCore, server := newGatewayTestServerForConfig(t, resolvedA.Config)
	storeA := createGatewayAuthoritativeSession(t, appCore)

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	requireGatewayProjectAttachment(t, conn, "attach-project-a", &connectionpb.AttachProjectRequest{ProjectId: bindingA.ProjectID})
	requireGatewaySessionAttachment(t, conn, "attach-session-a", storeA.Meta().SessionID)
	requireGatewayProjectAttachment(t, conn, "attach-project-b", &connectionpb.AttachProjectRequest{ProjectId: bindingB.ProjectID})

	if respErr := callGatewayExpectError(t, conn, "subscribe", protocol.MethodSessionSubscribeTranscript, serverapi.TranscriptSubscribeRequest{SessionID: storeA.Meta().SessionID}); respErr.Code != protocol.ErrCodeInvalidRequest {
		t.Fatalf("expected session-attach-required error after project reattach, got %+v", respErr)
	}
}

func TestGatewayPreservesAttachProjectWorkspaceNotRegistered(t *testing.T) {
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	t.Setenv("HOME", home)
	configureGatewayTestServerPort(t)

	resolvedA := resolveGatewayTestConfig(t, workspaceA)
	bindingA := registerGatewayTestBinding(t, resolvedA.Config)
	resolvedB := resolveGatewayTestConfig(t, workspaceB)

	_, server := newGatewayTestServerForConfig(t, resolvedA.Config)

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	result := attachGatewayProject(t, conn, "attach-project", &connectionpb.AttachProjectRequest{
		ProjectId: bindingA.ProjectID,
		Workspace: &connectionpb.AttachProjectRequest_WorkspaceRoot{WorkspaceRoot: resolvedB.Config.WorkspaceRoot},
	})
	failure := result.GetError()
	if failure == nil || failure.Code != "workspace_not_registered" || failure.GetWorkspaceNotRegistered() == nil {
		t.Fatalf("workspace-not-registered attachment result = %+v", result)
	}
}
