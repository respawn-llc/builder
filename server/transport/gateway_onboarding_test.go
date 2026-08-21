package transport

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"core/server/core"
	"core/shared/apicontract"
	"core/shared/protoapi"
	onboardingpb "core/shared/protoapi/gen/kent/api/onboarding"
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

type gatewayOnboardingService func(context.Context, *onboardingpb.FinalizeRequest) (*onboardingpb.FinalizeSuccess, error)

func (service gatewayOnboardingService) Finalize(
	ctx context.Context,
	request *onboardingpb.FinalizeRequest,
) (*onboardingpb.FinalizeSuccess, error) {
	return service(ctx, request)
}

func TestGatewayOnboardingFinalizeErrorContracts(t *testing.T) {
	call := func(payload []byte, service gatewayOnboardingService) *onboardingpb.FinalizeResult {
		t.Helper()
		appCore, _ := newGatewayTestCore(t, true, false)
		defer func() { _ = appCore.Close() }()
		gateway, err := NewGateway(
			&gatewayOnboardingOverride{Core: appCore, finalize: service},
			gatewayTestIdentity(),
		)
		if err != nil {
			t.Fatalf("NewGateway: %v", err)
		}
		server := httptest.NewServer(gateway.Handler())
		defer server.Close()
		conn := dialGateway(t, server)
		defer func() { _ = conn.Close() }()
		handshakeGateway(t, conn)
		method := onboardingpb.File_kent_api_onboarding_onboarding_proto.Services().
			ByName("OnboardingService").Methods().ByName("Finalize")
		envelope := callGatewayDescriptorPayload(t, conn, "finalize-invalid", method, payload)
		result := &onboardingpb.FinalizeResult{}
		if response := envelope.GetResult(); response == nil {
			t.Fatalf("finalize result is required: %+v", envelope)
		} else if err := protoapi.Decode(response.Payload, result); err != nil {
			t.Fatalf("decode Finalize result: %v", err)
		}
		return result
	}

	requestPayload, err := proto.Marshal(&onboardingpb.FinalizeRequest{})
	if err != nil {
		t.Fatalf("marshal Finalize request: %v", err)
	}
	result := call(requestPayload, func(
		context.Context,
		*onboardingpb.FinalizeRequest,
	) (*onboardingpb.FinalizeSuccess, error) {
		return nil, serverapi.NewOnboardingFinalizeError(
			serverapi.OnboardingFinalizeInvalidRequest,
			serverapi.OnboardingInvalidRequestDetails{
				FieldErrors: []serverapi.OnboardingFinalizeFieldError{{Field: "theme", Code: "unsupported"}},
			},
			nil,
		)
	})
	if decoded := protoapi.OnboardingFinalizeErrorFromProto(result.GetError()); !errors.Is(decoded, serverapi.ErrOnboardingFinalizeInvalidRequest) {
		t.Fatalf("decoded error = %v, want invalid_request", decoded)
	}
}
