package transport

import (
	"context"

	"core/shared/protoapi"
	projectpb "core/shared/protoapi/gen/kent/api/project"

	"google.golang.org/protobuf/proto"
)

func registerSessionCatalogGatewayBinaryBindings(bindings map[string]gatewayBinaryBinding) error {
	service := projectpb.File_kent_api_project_session_catalog_proto.Services().ByName("SessionCatalogService")
	return registerGatewayBinaryBinding(
		bindings,
		service,
		"Page",
		gatewayBinaryCoreActiveOrdinary,
		func() proto.Message { return &projectpb.SessionPageRequest{} },
		nil,
		invokeBinarySessionPage,
		binarySessionPageFailure,
	)
}

func invokeBinarySessionPage(g *Gateway, ctx context.Context, _ *connectionState, message proto.Message) (proto.Message, error) {
	request, err := protoapi.SessionPageRequestFromProto(message.(*projectpb.SessionPageRequest))
	if err != nil {
		return nil, err
	}
	client, err := projectViewClient(g)
	if err != nil {
		return nil, err
	}
	response, err := client.ListSessionPage(ctx, request)
	if err != nil {
		return nil, err
	}
	success, err := protoapi.SessionPageToProto(response)
	if err != nil {
		return nil, err
	}
	return &projectpb.SessionPageResult{Outcome: &projectpb.SessionPageResult_Success{Success: success}}, nil
}

func binarySessionPageFailure(err error) proto.Message {
	return &projectpb.SessionPageResult{Outcome: &projectpb.SessionPageResult_Error{Error: &projectpb.SessionPageError{
		Code: "internal_failure",
		Detail: &projectpb.SessionPageError_InternalFailure{
			InternalFailure: binaryInternalFailure(err),
		},
	}}}
}
