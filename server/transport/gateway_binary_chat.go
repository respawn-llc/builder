package transport

import (
	"context"
	"errors"

	"core/server/session"
	"core/shared/apicontract"
	"core/shared/protoapi"
	authpb "core/shared/protoapi/gen/kent/api/auth"
	chatpb "core/shared/protoapi/gen/kent/api/chat"
	chatsettingspb "core/shared/protoapi/gen/kent/api/chat_settings"
	"core/shared/serverapi"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func registerChatGatewayBinaryBindings(bindings map[string]gatewayBinaryBinding) error {
	service := chatpb.File_kent_api_chat_chat_proto.Services().ByName("ChatService")
	if err := registerChatUnary(
		bindings,
		service,
		"Steer",
		func() *chatpb.SteerRequest { return &chatpb.SteerRequest{} },
		func(client apicontract.ChatMutationService, ctx context.Context, request *chatpb.SteerRequest) (*chatpb.InputMutationSuccess, error) {
			return client.Steer(ctx, request)
		},
	); err != nil {
		return err
	}
	if err := registerChatUnary(
		bindings,
		service,
		"Queue",
		func() *chatpb.QueueRequest { return &chatpb.QueueRequest{} },
		func(client apicontract.ChatMutationService, ctx context.Context, request *chatpb.QueueRequest) (*chatpb.InputMutationSuccess, error) {
			return client.Queue(ctx, request)
		},
	); err != nil {
		return err
	}
	return registerChatUnary(
		bindings,
		service,
		"Compact",
		func() *chatpb.CompactRequest { return &chatpb.CompactRequest{} },
		func(client apicontract.ChatMutationService, ctx context.Context, request *chatpb.CompactRequest) (*chatpb.CompactionMutationSuccess, error) {
			return client.Compact(ctx, request)
		},
	)
}

func registerChatUnary[
	Request protoapi.ChatTargetRequest,
	Success proto.Message,
](
	bindings map[string]gatewayBinaryBinding,
	service protoreflect.ServiceDescriptor,
	method protoreflect.Name,
	newRequest func() Request,
	invoke func(apicontract.ChatMutationService, context.Context, Request) (Success, error),
) error {
	return registerGatewayBinaryUnary(
		bindings,
		service,
		method,
		gatewayBinaryCoreActiveOrdinary,
		newRequest,
		chatTargetScope[Request],
		func(g *Gateway, ctx context.Context, _ *connectionState, request Request) (Success, error) {
			client := g.deps.ChatMutationClient()
			if client == nil {
				var zero Success
				return zero, errors.New("Chat mutation service is required")
			}
			return invoke(client, ctx, request)
		},
		binaryChatFailure[Request],
	)
}

func chatTargetScope[Request protoapi.ChatTargetRequest](request Request) (routeScopeParams, error) {
	target, err := protoapi.ChatTargetFromRequest(request)
	if err != nil {
		return routeScopeParams{}, err
	}
	return routeScopeParams{chatTarget: target}, nil
}

func binaryChatFailure[Request protoapi.ChatTargetRequest](
	_ *Gateway,
	_ *connectionState,
	request Request,
	err error,
) proto.Message {
	switch {
	case errors.Is(err, serverapi.ErrServerAuthRequired):
		return &authpb.AuthRequiredDetails{}
	case errors.Is(err, serverapi.ErrWorkspaceNotRegistered),
		errors.Is(err, errActiveProjectRequired):
		return &chatsettingspb.WorkspaceNotRegisteredDetails{}
	case errors.Is(err, session.ErrSessionNotFound),
		errors.Is(err, errSessionOutsideActiveProject):
		target, targetErr := protoapi.ChatTargetFromRequest(request)
		if targetErr == nil && target.GetSession() != nil {
			return &chatpb.SessionNotFoundDetails{SessionId: target.GetSession().SessionId}
		}
	}
	return binaryInternalFailure(err)
}
