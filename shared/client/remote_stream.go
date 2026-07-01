package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	rpccontract "core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/serverapi"
)

type remoteSubscription[Wire any, Event any] struct {
	conn  rpcwire.Conn
	route rpccontract.Route
	event func(Wire) Event
	once  sync.Once
}

func (c *Remote) SubscribePromptActivity(ctx context.Context, req serverapi.PromptActivitySubscribeRequest) (serverapi.PromptActivitySubscription, error) {
	conn, route, err := c.subscribeRPC(ctx, protocol.MethodPromptSubscribeActivity, "subscribe-prompt-activity", req, req.SessionID, true)
	if err != nil {
		return nil, err
	}
	return newRemoteSubscription(conn, route, func(params protocol.PromptActivityEventParams) clientui.PendingPromptEvent { return params.Event }), nil
}

func (c *Remote) RunPrompt(ctx context.Context, req serverapi.RunPromptRequest, progress serverapi.RunPromptProgressSink) (serverapi.RunPromptResponse, error) {
	route := mustRemoteRoute(protocol.MethodRunPrompt)
	conn, cleanup, err := c.openRPCConn(ctx)
	if err != nil {
		return serverapi.RunPromptResponse{}, err
	}
	defer cleanup()
	params, err := json.Marshal(req)
	if err != nil {
		return serverapi.RunPromptResponse{}, err
	}
	const requestID = "run-prompt"
	request := protocol.Request{JSONRPC: protocol.JSONRPCVersion, ID: requestID, Method: protocol.MethodRunPrompt, Params: params}
	if err := conn.Send(ctx, rpcwire.FrameFromRequest(request)); err != nil {
		return serverapi.RunPromptResponse{}, err
	}
	for {
		frame, err := receiveFrame(ctx, conn)
		if err != nil {
			return serverapi.RunPromptResponse{}, err
		}
		if frame.Method == route.EventMethod {
			if progress != nil {
				var update serverapi.RunPromptProgress
				if err := json.Unmarshal(frame.Params, &update); err != nil {
					return serverapi.RunPromptResponse{}, err
				}
				progress.PublishRunPromptProgress(update)
			}
			continue
		}
		if frame.ID != requestID {
			return serverapi.RunPromptResponse{}, fmt.Errorf("unexpected rpc frame id %q", frame.ID)
		}
		resp := frame.Response()
		if resp.Error != nil {
			return serverapi.RunPromptResponse{}, protocolError(resp.Error)
		}
		var result serverapi.RunPromptResponse
		if len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, &result); err != nil {
				return serverapi.RunPromptResponse{}, err
			}
		}
		return result, nil
	}
}

func (c *Remote) SubscribeSessionActivity(ctx context.Context, req serverapi.SessionActivitySubscribeRequest) (serverapi.SessionActivitySubscription, error) {
	conn, route, err := c.subscribeRPC(ctx, protocol.MethodSessionSubscribeActivity, "subscribe-session-activity", req, req.SessionID, true)
	if err != nil {
		return nil, err
	}
	return newRemoteSubscription(conn, route, func(params protocol.SessionActivityEventParams) clientui.Event { return params.Event }), nil
}

func (c *Remote) SubscribeProcessOutput(ctx context.Context, req serverapi.ProcessOutputSubscribeRequest) (serverapi.ProcessOutputSubscription, error) {
	conn, route, err := c.subscribeRPC(ctx, protocol.MethodProcessSubscribeOutput, "subscribe-process-output", req, "", false)
	if err != nil {
		return nil, err
	}
	return newRemoteSubscription(conn, route, func(params protocol.ProcessOutputEventParams) clientui.ProcessOutputChunk { return params.Chunk }), nil
}

func (c *Remote) SubscribeWorkflowProject(ctx context.Context, req serverapi.WorkflowProjectSubscribeRequest) (serverapi.WorkflowProjectSubscription, error) {
	conn, route, err := c.subscribeRPC(ctx, protocol.MethodWorkflowSubscribeProject, "subscribe-workflow-project", req, "", false)
	if err != nil {
		return nil, err
	}
	return newRemoteSubscription(conn, route, func(params protocol.WorkflowProjectEventParams) serverapi.WorkflowProjectEvent {
		return serverapi.WorkflowProjectEvent{ProjectID: params.Event.ProjectID, WorkflowID: params.Event.WorkflowID, Resource: params.Event.Resource, Action: params.Event.Action, ChangedIDs: params.Event.ChangedIDs, OccurredAtUnixMs: params.Event.OccurredAtUnixMs}
	}), nil
}

func (c *Remote) SubscribeWorkflow(ctx context.Context, req serverapi.WorkflowSubscribeRequest) (serverapi.WorkflowSubscription, error) {
	conn, route, err := c.subscribeRPC(ctx, protocol.MethodWorkflowSubscribe, "subscribe-workflow", req, "", false)
	if err != nil {
		return nil, err
	}
	return newRemoteSubscription(conn, route, func(params protocol.WorkflowProjectEventParams) serverapi.WorkflowProjectEvent {
		return serverapi.WorkflowProjectEvent{ProjectID: params.Event.ProjectID, WorkflowID: params.Event.WorkflowID, Resource: params.Event.Resource, Action: params.Event.Action, ChangedIDs: params.Event.ChangedIDs, OccurredAtUnixMs: params.Event.OccurredAtUnixMs}
	}), nil
}

func (c *Remote) subscribeRPC(ctx context.Context, method string, requestID string, req any, sessionID string, attachSession bool) (rpcwire.Conn, rpccontract.Route, error) {
	route := mustRemoteRoute(method)
	conn, cleanup, err := c.openRPCConn(ctx)
	if err != nil {
		return nil, rpccontract.Route{}, err
	}
	if attachSession {
		if err := callRPC(ctx, conn, "attach-session", protocol.MethodAttachSession, protocol.AttachSessionRequest{SessionID: sessionID}, nil); err != nil {
			cleanup()
			return nil, rpccontract.Route{}, err
		}
	}
	var ack protocol.SubscribeResponse
	if err := callRPC(ctx, conn, requestID, method, req, &ack); err != nil {
		cleanup()
		return nil, rpccontract.Route{}, err
	}
	return conn, route, nil
}

func newRemoteSubscription[Wire any, Event any](conn rpcwire.Conn, route rpccontract.Route, event func(Wire) Event) *remoteSubscription[Wire, Event] {
	return &remoteSubscription[Wire, Event]{conn: conn, route: route, event: event}
}

func mustRemoteRoute(method string) rpccontract.Route {
	route, ok := rpccontract.RouteByMethod(method)
	if !ok {
		panic(fmt.Sprintf("remote route %q is missing route contract", method))
	}
	return route
}

func (s *remoteSubscription[Wire, Event]) Next(ctx context.Context) (Event, error) {
	frame, err := receiveFrame(ctx, s.conn)
	if err != nil {
		var zero Event
		return zero, serverapi.NormalizeStreamError(err)
	}
	switch frame.Method {
	case s.route.EventMethod:
		var params Wire
		if err := json.Unmarshal(frame.Params, &params); err != nil {
			var zero Event
			return zero, errors.Join(serverapi.ErrStreamFailed, err)
		}
		return s.event(params), nil
	case s.route.CompleteMethod:
		var params protocol.StreamCompleteParams
		if err := json.Unmarshal(frame.Params, &params); err != nil {
			var zero Event
			return zero, errors.Join(serverapi.ErrStreamFailed, err)
		}
		_ = s.Close()
		var zero Event
		if params.Code == 0 && strings.TrimSpace(params.Message) == "" {
			return zero, io.EOF
		}
		return zero, protocolError(&protocol.ResponseError{Code: params.Code, Message: params.Message})
	default:
		var zero Event
		return zero, errors.Join(serverapi.ErrStreamFailed, fmt.Errorf("unexpected notification method %q", frame.Method))
	}
}

func (s *remoteSubscription[Wire, Event]) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		if s.conn != nil {
			_ = s.conn.Close()
		}
	})
	return nil
}
