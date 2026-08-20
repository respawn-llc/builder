package transport

import (
	"core/shared/apicontract"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	"google.golang.org/protobuf/proto"
)

func registerSessionCatalogGatewayBinaryBindings(bindings map[string]gatewayBinaryBinding) error {
	service := projectpb.File_kent_api_project_session_catalog_proto.Services().ByName("SessionCatalogService")
	return registerProjectViewUnary(
		bindings,
		service,
		"Page",
		func() *projectpb.SessionPageRequest { return &projectpb.SessionPageRequest{} },
		apicontract.ProjectViewService.ListSessionPage,
		binarySessionPageFailure,
	)
}

func binarySessionPageFailure(_ *projectpb.SessionPageRequest, err error) proto.Message {
	return binaryInternalFailure(err)
}
