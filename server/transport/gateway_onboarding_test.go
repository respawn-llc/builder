package transport

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"core/server/core"
	"core/shared/apicontract"
	remoteclient "core/shared/client"
	"core/shared/protocol"
	"core/shared/serverapi"
)

type gatewayOnboardingOverride struct {
	*core.Core
	finalize apicontract.OnboardingFinalizeService
}

func (d *gatewayOnboardingOverride) OnboardingFinalizeClient() apicontract.OnboardingFinalizeService {
	return d.finalize
}

type gatewayOnboardingUnavailableOverride struct {
	*core.Core
	unavailable apicontract.Dependency
}

func (d *gatewayOnboardingUnavailableOverride) RouteDependencyAvailable(dep apicontract.Dependency) error {
	if dep == d.unavailable {
		return serverapi.NewServerNotReadyError(serverapi.ServerNotReadyOnboardingRequired, nil, nil)
	}
	return nil
}

type gatewayOnboardingService struct {
	handler func(context.Context, serverapi.OnboardingFinalizeRequest) (serverapi.OnboardingFinalizeResponse, error)
}

func (s gatewayOnboardingService) FinalizeOnboarding(ctx context.Context, req serverapi.OnboardingFinalizeRequest) (serverapi.OnboardingFinalizeResponse, error) {
	if s.handler != nil {
		return s.handler(ctx, req)
	}
	if err := serverapi.ValidateOnboardingFinalizeRequest(req); err != nil {
		return serverapi.OnboardingFinalizeResponse{}, err
	}
	return serverapi.OnboardingFinalizeResponse{Completed: true, SettingsPath: "/tmp/config.toml"}, nil
}

func TestGatewayOnboardingFinalizeIsUnauthenticatedAndDomainInvalidIsTyped(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, false)
	defer func() { _ = appCore.Close() }()
	gateway, err := NewGateway(&gatewayOnboardingOverride{
		Core:     appCore,
		finalize: gatewayOnboardingService{},
	}, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptestServerForGateway(t, gateway)
	defer server.Close()
	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)

	blue := serverapi.OnboardingTheme("blue")
	errResp := callGatewayExpectError(t, conn, "finalize-invalid", protocol.MethodOnboardingFinalize, serverapi.OnboardingFinalizeRequest{Theme: &blue})
	if errResp.Code != protocol.ErrCodeOnboardingFinalizeFailed {
		t.Fatalf("error code = %d, want onboarding finalize failed", errResp.Code)
	}
	decoded := serverapi.DecodeOnboardingFinalizeError(errResp.Data, errResp.Message)
	if !errors.Is(decoded, serverapi.ErrOnboardingFinalizeInvalidRequest) {
		t.Fatalf("decoded error = %v, want invalid_request", decoded)
	}
}

func TestGatewayOnboardingFinalizeMalformedParamsRemainInvalidParams(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	defer func() { _ = appCore.Close() }()
	gateway, err := NewGateway(&gatewayOnboardingOverride{
		Core:     appCore,
		finalize: gatewayOnboardingService{},
	}, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptestServerForGateway(t, gateway)
	defer server.Close()
	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)

	errResp := callGatewayExpectError(t, conn, "bad-params", protocol.MethodOnboardingFinalize, "not an object")
	if errResp.Code != protocol.ErrCodeInvalidParams || len(errResp.Data) != 0 {
		t.Fatalf("malformed finalize response = %+v, want invalid params without structured data", errResp)
	}
}

func TestGatewaySubscriptionChecksDependencyAvailabilityBeforeClientLookup(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	defer func() { _ = appCore.Close() }()
	gateway, err := NewGateway(&gatewayOnboardingUnavailableOverride{
		Core:        appCore,
		unavailable: apicontract.DependencyAttentionNotification,
	}, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptestServerForGateway(t, gateway)
	defer server.Close()
	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)

	errResp := callGatewayExpectError(t, conn, "attention-subscribe", protocol.MethodAttentionNotificationSubscribe, serverapi.AttentionNotificationSubscribeRequest{})
	if errResp.Code != protocol.ErrCodeServerNotReady {
		t.Fatalf("error code = %d, want server not ready", errResp.Code)
	}
	if decoded := serverapi.DecodeServerNotReadyError(errResp.Data, errResp.Message); !errors.Is(decoded, serverapi.ErrServerNotReadyOnboardingRequired) {
		t.Fatalf("decoded error = %v, want onboarding_required", decoded)
	}
}

func TestGatewayProgressChecksDependencyAvailabilityBeforeAuthAndPreflight(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, false)
	defer func() { _ = appCore.Close() }()
	gateway, err := NewGateway(&gatewayOnboardingUnavailableOverride{
		Core:        appCore,
		unavailable: apicontract.DependencyRunPrompt,
	}, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptestServerForGateway(t, gateway)
	defer server.Close()
	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)

	errResp := callGatewayExpectError(t, conn, "run-prompt", protocol.MethodRunPrompt, serverapi.RunPromptRequest{})
	if errResp.Code != protocol.ErrCodeServerNotReady {
		t.Fatalf("error code = %d, want server not ready", errResp.Code)
	}
	if decoded := serverapi.DecodeServerNotReadyError(errResp.Data, errResp.Message); !errors.Is(decoded, serverapi.ErrServerNotReadyOnboardingRequired) {
		t.Fatalf("decoded error = %v, want onboarding_required", decoded)
	}
}

func TestGatewayAttachChecksDependencyAvailabilitySeparatelyFromHandshake(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, false)
	defer func() { _ = appCore.Close() }()
	gateway, err := NewGateway(&gatewayOnboardingUnavailableOverride{
		Core:        appCore,
		unavailable: apicontract.DependencyProtocolAttach,
	}, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptestServerForGateway(t, gateway)
	defer server.Close()
	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)

	errResp := callGatewayExpectError(t, conn, "attach-project", protocol.MethodAttachProject, protocol.AttachProjectRequest{ProjectID: appCore.ProjectID()})
	if errResp.Code != protocol.ErrCodeServerNotReady {
		t.Fatalf("error code = %d, want server not ready", errResp.Code)
	}
	if decoded := serverapi.DecodeServerNotReadyError(errResp.Data, errResp.Message); !errors.Is(decoded, serverapi.ErrServerNotReadyOnboardingRequired) {
		t.Fatalf("decoded error = %v, want onboarding_required", decoded)
	}
}

func TestRemoteOnboardingFinalizePreservesStructuredSentinels(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	defer func() { _ = appCore.Close() }()
	gateway, err := NewGateway(&gatewayOnboardingOverride{
		Core: appCore,
		finalize: gatewayOnboardingService{
			handler: func(context.Context, serverapi.OnboardingFinalizeRequest) (serverapi.OnboardingFinalizeResponse, error) {
				return serverapi.OnboardingFinalizeResponse{}, serverapi.NewOnboardingFinalizeError(serverapi.OnboardingFinalizeConfigAlreadyExists, serverapi.OnboardingConfigAlreadyExistsDetails{SettingsPath: "/tmp/config.toml"}, nil)
			},
		},
	}, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptestServerForGateway(t, gateway)
	defer server.Close()

	remote, err := remoteclient.DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()
	_, err = remote.FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{})
	if !errors.Is(err, serverapi.ErrOnboardingFinalizeConfigAlreadyExists) {
		t.Fatalf("FinalizeOnboarding error = %v, want config_already_exists sentinel", err)
	}
	var finalizeErr *serverapi.OnboardingFinalizeError
	if !errors.As(err, &finalizeErr) {
		t.Fatalf("FinalizeOnboarding error = %T %v, want typed finalize error", err, err)
	}
	if _, ok := finalizeErr.Details.(serverapi.OnboardingConfigAlreadyExistsDetails); !ok {
		t.Fatalf("remote details = %T, want typed config_already_exists details", finalizeErr.Details)
	}
}

func TestConfiguredCoreOnboardingFinalizeReturnsConfigAlreadyExists(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer func() { _ = appCore.Close() }()
	defer server.Close()

	remote, err := remoteclient.DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()
	_, err = remote.FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{Theme: ptrThemeForTransport("blue")})
	if !errors.Is(err, serverapi.ErrOnboardingFinalizeConfigAlreadyExists) {
		t.Fatalf("FinalizeOnboarding error = %v, want config_already_exists", err)
	}
}

func ptrThemeForTransport(value serverapi.OnboardingTheme) *serverapi.OnboardingTheme {
	return &value
}

func httptestServerForGateway(t *testing.T, gateway *Gateway) *httptest.Server {
	t.Helper()
	return httptest.NewServer(gateway.Handler())
}
