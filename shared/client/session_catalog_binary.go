package client

import (
	"context"
	"fmt"

	"core/shared/protoapi"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	"core/shared/serverapi"

	"google.golang.org/protobuf/reflect/protoreflect"
)

func sessionCatalogMethod(name string) protoreflect.MethodDescriptor {
	return bootstrapMethod(
		projectpb.File_kent_api_project_session_catalog_proto,
		"SessionCatalogService",
		protoreflect.Name(name),
	)
}

func (c *Remote) ListSessionPage(ctx context.Context, req serverapi.SessionPageRequest) (serverapi.SessionPageResponse, error) {
	request, err := protoapi.SessionPageRequestToProto(req)
	if err != nil {
		return serverapi.SessionPageResponse{}, err
	}
	result := &projectpb.SessionPageResult{}
	if err := c.callBinary(ctx, sessionCatalogMethod("Page"), request, result); err != nil {
		return serverapi.SessionPageResponse{}, err
	}
	if failure := result.GetError(); failure != nil {
		return serverapi.SessionPageResponse{}, projectInternalGeneratedError(failure.Code, failure.GetInternalFailure())
	}
	response, err := protoapi.SessionPageFromProto(result.GetSuccess())
	if err != nil {
		return serverapi.SessionPageResponse{}, err
	}
	if response.ProjectID != req.ProjectID {
		return serverapi.SessionPageResponse{}, fmt.Errorf(
			"session page response project %q does not match request project %q",
			response.ProjectID,
			req.ProjectID,
		)
	}
	if response.Category != req.Category {
		return serverapi.SessionPageResponse{}, fmt.Errorf(
			"session page response category %q does not match request category %q",
			response.Category,
			req.Category,
		)
	}
	return response, nil
}
