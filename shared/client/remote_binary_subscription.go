package client

import (
	"context"
	"errors"
	"fmt"
	"io"

	"core/shared/protoapi"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/serverapi"

	"google.golang.org/protobuf/reflect/protoreflect"
)

func subscribeWorktreeSetupBinary(
	c *Remote,
	ctx context.Context,
	method protoreflect.MethodDescriptor,
	request *worktreepb.SetupSubscribeRequest,
) (*remoteSubscription[*worktreepb.SetupEvent], error) {
	operations, err := protoapi.ResolveSubscriptionOperations(method)
	if err != nil {
		return nil, err
	}
	conn, cleanup, err := c.openRPCConn(ctx)
	if err != nil {
		return nil, err
	}
	startResult := &worktreepb.SetupStartResult{}
	if err := callBinaryRPC(ctx, conn, operations.Subscribe.Name, method, request, startResult); err != nil {
		cleanup()
		return nil, errors.Join(serverapi.ErrStreamFailed, err)
	}
	if _, err := decodeGeneratedResult(method, startResult, worktreeError[*worktreepb.SetupStartError]); err != nil {
		cleanup()
		return nil, err
	}
	return &remoteSubscription[*worktreepb.SetupEvent]{
		conn: conn,
		next: func(ctx context.Context, conn rpcwire.Conn) (*worktreepb.SetupEvent, error) {
			return nextWorktreeSetupEvent(ctx, conn, operations)
		},
	}, nil
}

func nextWorktreeSetupEvent(
	ctx context.Context,
	conn rpcwire.Conn,
	operations protoapi.SubscriptionOperations,
) (*worktreepb.SetupEvent, error) {
	frame, err := receiveFrame(ctx, conn)
	if err != nil {
		return nil, serverapi.NormalizeStreamError(err)
	}
	if frame.Kind != rpcwire.FrameBinary {
		return nil, errors.Join(serverapi.ErrStreamFailed,
			fmt.Errorf("operation %s received a JSON frame", operations.Subscribe.Name))
	}
	envelope, err := protoapi.DecodeEnvelope(frame.Payload)
	if err != nil {
		return nil, errors.Join(serverapi.ErrStreamFailed, err)
	}
	notification := envelope.GetNotificationEvent()
	if notification == nil || notification.Payload == nil {
		return nil, errors.Join(serverapi.ErrStreamFailed,
			fmt.Errorf("operation %s received an unexpected envelope", operations.Subscribe.Name))
	}
	switch notification.Operation {
	case operations.Event.Name:
		event := &worktreepb.SetupEvent{}
		if err := protoapi.Decode(notification.Payload, event); err != nil {
			return nil, errors.Join(serverapi.ErrStreamFailed, err)
		}
		return event, nil
	case operations.Completion.Name:
		completion := &worktreepb.SetupCompletion{}
		if err := protoapi.Decode(notification.Payload, completion); err != nil {
			return nil, errors.Join(serverapi.ErrStreamFailed, err)
		}
		_ = conn.Close()
		if completion.Code == nil {
			return nil, io.EOF
		}
		return nil, protocolError(&protocol.ResponseError{
			Code: int(completion.GetCode()), Message: completion.GetDiagnostic(),
		})
	default:
		return nil, errors.Join(serverapi.ErrStreamFailed, fmt.Errorf(
			"operation %s received unexpected notification %s", operations.Subscribe.Name, notification.Operation))
	}
}
