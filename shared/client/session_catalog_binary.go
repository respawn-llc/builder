package client

import (
	"context"
	"fmt"

	projectpb "core/shared/protoapi/gen/kent/api/project"

	"google.golang.org/protobuf/reflect/protoreflect"
)

func sessionCatalogMethod(name string) protoreflect.MethodDescriptor {
	return bootstrapMethod(
		projectpb.File_kent_api_project_session_catalog_proto,
		"SessionCatalogService",
		protoreflect.Name(name),
	)
}

func (c *Remote) ListSessionPage(ctx context.Context, request *projectpb.SessionPageRequest) (*projectpb.SessionPageSuccess, error) {
	response, err := callGeneratedBinary(c, ctx, sessionCatalogMethod("Page"), request,
		&projectpb.SessionPageResult{},
		func(failure *projectpb.SessionPageError) error {
			return projectInternalGeneratedError(failure.Code, failure.GetInternalFailure())
		})
	if err != nil {
		return nil, err
	}
	if response.ProjectId != request.ProjectId {
		return nil, fmt.Errorf(
			"session page response project %q does not match request project %q",
			response.ProjectId,
			request.ProjectId,
		)
	}
	if response.Category != request.Category {
		return nil, fmt.Errorf(
			"session page response category %s does not match request category %s",
			response.Category,
			request.Category,
		)
	}
	return response, nil
}
