package transport

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"core/server/core"
	"core/shared/apicontract"
	remoteclient "core/shared/client"
	"core/shared/protoapi"
	onboardingpb "core/shared/protoapi/gen/kent/api/onboarding"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"core/shared/serverapi"

	"google.golang.org/protobuf/proto"
)

type gatewayOnboardingOverride struct {
	*core.Core
	finalize apicontract.OnboardingFinalizeService
}

func (d *gatewayOnboardingOverride) OnboardingFinalizeClient() apicontract.OnboardingFinalizeService {
	return d.finalize
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

func TestGatewayOnboardingFinalizeErrorContracts(t *testing.T) {
	tests := []struct {
		name       string
		authReady  bool
		request    *onboardingpb.FinalizeRequest
		payload    []byte
		structured bool
	}{
		{name: "unauthenticated domain invalid is typed", request: &onboardingpb.FinalizeRequest{}, structured: true},
		{name: "malformed params remain invalid params", authReady: true, payload: []byte{0x0a}},
		{name: "unspecified enum remains invalid params", authReady: true, request: &onboardingpb.FinalizeRequest{Theme: onboardingpb.Theme_THEME_UNSPECIFIED.Enum()}},
		{name: "invalid tool override remains invalid params", authReady: true, request: &onboardingpb.FinalizeRequest{ToolOverrides: []*onboardingpb.ToolOverride{{Id: onboardingpb.ToolID_TOOL_ID_ASK_QUESTION}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			appCore, _ := newGatewayTestCore(t, true, tt.authReady)
			defer func() { _ = appCore.Close() }()
			service := gatewayOnboardingService{}
			if tt.structured {
				service.handler = func(context.Context, serverapi.OnboardingFinalizeRequest) (serverapi.OnboardingFinalizeResponse, error) {
					return serverapi.OnboardingFinalizeResponse{}, serverapi.NewOnboardingFinalizeError(
						serverapi.OnboardingFinalizeInvalidRequest,
						serverapi.OnboardingInvalidRequestDetails{FieldErrors: []serverapi.OnboardingFinalizeFieldError{{Field: "theme", Code: "unsupported"}}},
						nil,
					)
				}
			}
			gateway, err := NewGateway(&gatewayOnboardingOverride{Core: appCore, finalize: service}, gatewayTestIdentity())
			if err != nil {
				t.Fatalf("NewGateway: %v", err)
			}
			server := httptestServerForGateway(t, gateway)
			defer server.Close()
			conn := dialGateway(t, server)
			defer func() { _ = conn.Close() }()
			handshakeGateway(t, conn)

			payload := tt.payload
			if tt.request != nil {
				payload, err = proto.Marshal(tt.request)
				if err != nil {
					t.Fatalf("marshal Finalize request: %v", err)
				}
			}
			method := onboardingpb.File_kent_api_onboarding_onboarding_proto.Services().ByName("OnboardingService").Methods().ByName("Finalize")
			envelope := callGatewayDescriptorPayload(t, conn, "finalize-invalid", method, payload)
			if !tt.structured {
				failure := envelope.GetTransportFailure()
				if failure == nil || failure.Code != sharedpb.TransportFailureCode_TRANSPORT_FAILURE_CODE_INVALID_PAYLOAD {
					t.Fatalf("malformed finalize response = %+v, want invalid payload transport failure", envelope)
				}
				return
			}
			result := &onboardingpb.FinalizeResult{}
			if response := envelope.GetResult(); response == nil {
				t.Fatalf("finalize result is required: %+v", envelope)
			} else if err := protoapi.Decode(response.Payload, result); err != nil {
				t.Fatalf("decode Finalize result: %v", err)
			}
			if decoded := protoapi.OnboardingFinalizeErrorFromProto(result.GetError()); !errors.Is(decoded, serverapi.ErrOnboardingFinalizeInvalidRequest) {
				t.Fatalf("decoded error = %v, want invalid_request", decoded)
			}
		})
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
	}, gatewayTestIdentity())
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
