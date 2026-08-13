package transport

import (
	"context"
	"encoding/json"
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
	handler      func(context.Context, serverapi.OnboardingFinalizeRequest) (serverapi.OnboardingFinalizeResponse, error)
	rawCalls     int
	trustedCalls int
}

func (s *gatewayOnboardingService) FinalizeOnboarding(ctx context.Context, req serverapi.OnboardingFinalizeRequest) (serverapi.OnboardingFinalizeResponse, error) {
	s.rawCalls++
	if s.handler != nil {
		return s.handler(ctx, req)
	}
	if err := serverapi.ValidateOnboardingFinalizeRequest(req); err != nil {
		return serverapi.OnboardingFinalizeResponse{}, err
	}
	return serverapi.OnboardingFinalizeResponse{Completed: true, SettingsPath: "/tmp/config.toml"}, nil
}

func (s *gatewayOnboardingService) FinalizeOnboardingValidated(ctx context.Context, req apicontract.Validated[serverapi.OnboardingFinalizeRequest]) (serverapi.OnboardingFinalizeResponse, error) {
	s.trustedCalls++
	if s.handler != nil {
		return s.handler(ctx, req.Value())
	}
	return serverapi.OnboardingFinalizeResponse{Completed: true, SettingsPath: "/tmp/config.toml"}, nil
}

func TestGatewayOnboardingFinalizeErrorContracts(t *testing.T) {
	blue := serverapi.OnboardingTheme("blue")
	tests := []struct {
		name       string
		authReady  bool
		params     any
		code       int
		structured bool
	}{
		{name: "unauthenticated domain invalid is typed", params: serverapi.OnboardingFinalizeRequest{Theme: &blue}, code: protocol.ErrCodeOnboardingFinalizeFailed, structured: true},
		{name: "malformed params remain invalid params", authReady: true, params: "not an object", code: protocol.ErrCodeInvalidParams},
		{name: "null params remain invalid params", authReady: true, params: json.RawMessage(`null`), code: protocol.ErrCodeInvalidParams},
		{name: "extra params remain invalid params", authReady: true, params: json.RawMessage(`{"unknown":true}`), code: protocol.ErrCodeInvalidParams},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			appCore, _ := newGatewayTestCore(t, true, tt.authReady)
			defer func() { _ = appCore.Close() }()
			gateway, err := NewGateway(&gatewayOnboardingOverride{Core: appCore, finalize: &gatewayOnboardingService{}}, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
			if err != nil {
				t.Fatalf("NewGateway: %v", err)
			}
			server := httptestServerForGateway(t, gateway)
			defer server.Close()
			conn := dialGateway(t, server)
			defer func() { _ = conn.Close() }()
			handshakeGateway(t, conn)

			errResp := callGatewayExpectError(t, conn, "finalize-invalid", protocol.MethodOnboardingFinalize, tt.params)
			if errResp.Code != tt.code {
				t.Fatalf("error code = %d, want %d", errResp.Code, tt.code)
			}
			if !tt.structured {
				if len(errResp.Data) != 0 {
					t.Fatalf("malformed finalize response = %+v, want no structured data", errResp)
				}
				return
			}
			if decoded := serverapi.DecodeOnboardingFinalizeError(errResp.Data, errResp.Message); !errors.Is(decoded, serverapi.ErrOnboardingFinalizeInvalidRequest) {
				t.Fatalf("decoded error = %v, want invalid_request", decoded)
			}
		})
	}
}

func TestGatewayOnboardingFinalizeCallsOnlyTrustedOwner(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	t.Cleanup(func() { _ = appCore.Close() })
	service := &gatewayOnboardingService{}
	gateway, err := NewGateway(&gatewayOnboardingOverride{Core: appCore, finalize: service}, protocol.ServerIdentity{
		ProtocolVersion: protocol.Version,
		ServerID:        "server-1",
	})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	response := gateway.dispatch(t.Context(), &connectionState{handshakeDone: true}, protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      "finalize",
		Method:  protocol.MethodOnboardingFinalize,
		Params:  []byte(`{}`),
	})
	if response.Error != nil {
		t.Fatalf("Onboarding Finalize response error = %+v", response.Error)
	}
	if service.rawCalls != 0 || service.trustedCalls != 1 {
		t.Fatalf("Onboarding Finalize calls raw=%d trusted=%d, want raw=0 trusted=1", service.rawCalls, service.trustedCalls)
	}
}

func TestGatewayChecksDependencyAvailabilityBeforeRouteSpecificWork(t *testing.T) {
	authReadyCore, _ := newGatewayTestCore(t, true, true)
	t.Cleanup(func() { _ = authReadyCore.Close() })
	authBlockedCore, _ := newGatewayTestCore(t, true, false)
	t.Cleanup(func() { _ = authBlockedCore.Close() })

	tests := []struct {
		name       string
		authReady  bool
		dependency apicontract.Dependency
		method     string
		params     func(*core.Core) any
	}{
		{name: "subscription client lookup", authReady: true, dependency: apicontract.DependencyAttentionNotification, method: protocol.MethodAttentionNotificationSubscribe, params: func(*core.Core) any {
			return serverapi.AttentionNotificationSubscribeRequest{}
		}},
		{name: "progress auth and preflight", dependency: apicontract.DependencyRunPrompt, method: protocol.MethodRunPrompt, params: func(*core.Core) any {
			return serverapi.RunPromptRequest{ClientRequestID: "run-prompt", Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()), Prompt: "test"}
		}},
		{name: "attach after handshake", dependency: apicontract.DependencyProtocolAttach, method: protocol.MethodAttachProject, params: func(appCore *core.Core) any {
			return protocol.AttachProjectRequest{ProjectID: appCore.ProjectID()}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			appCore := authBlockedCore
			if tt.authReady {
				appCore = authReadyCore
			}
			gateway, err := NewGateway(&gatewayOnboardingUnavailableOverride{Core: appCore, unavailable: tt.dependency}, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
			if err != nil {
				t.Fatalf("NewGateway: %v", err)
			}
			server := httptestServerForGateway(t, gateway)
			defer server.Close()
			conn := dialGateway(t, server)
			defer func() { _ = conn.Close() }()
			handshakeGateway(t, conn)

			errResp := callGatewayExpectError(t, conn, "dependency-unavailable", tt.method, tt.params(appCore))
			if errResp.Code != protocol.ErrCodeServerNotReady {
				t.Fatalf("error code = %d, want server not ready", errResp.Code)
			}
			if decoded := serverapi.DecodeServerNotReadyError(errResp.Data, errResp.Message); !errors.Is(decoded, serverapi.ErrServerNotReadyOnboardingRequired) {
				t.Fatalf("decoded error = %v, want onboarding_required", decoded)
			}
		})
	}
}

func TestRemoteOnboardingFinalizePreservesStructuredSentinels(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	defer func() { _ = appCore.Close() }()
	gateway, err := NewGateway(&gatewayOnboardingOverride{
		Core: appCore,
		finalize: &gatewayOnboardingService{
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
	dark := serverapi.OnboardingThemeDark
	_, err = remote.FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{Theme: &dark})
	if !errors.Is(err, serverapi.ErrOnboardingFinalizeConfigAlreadyExists) {
		t.Fatalf("FinalizeOnboarding error = %v, want config_already_exists", err)
	}
}

func httptestServerForGateway(t *testing.T, gateway *Gateway) *httptest.Server {
	t.Helper()
	return httptest.NewServer(gateway.Handler())
}
