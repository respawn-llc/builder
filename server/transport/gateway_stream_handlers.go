package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	rpccontract "core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/serverapi"
)

type gatewaySubscription[Event any] interface {
	Next(context.Context) (Event, error)
	Close() error
}

func (g *Gateway) serveRunPrompt(conn rpcwire.Conn, ctx context.Context, state *connectionState, route rpccontract.Route, req protocol.Request) bool {
	if err := req.Validate(); err != nil {
		return sendResponse(ctx, conn, protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidRequest, err.Error()))
	}
	if !state.handshakeDone {
		return sendResponse(ctx, conn, protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidRequest, "handshake is required before other methods"))
	}
	if availability, ok := g.deps.(GatewayDependencyAvailability); ok {
		if err := availability.RouteDependencyAvailable(route.Dependency); err != nil {
			return sendResponse(ctx, conn, responseForError(req.ID, err))
		}
	}
	if err := newRoutePolicyExecutor(g).requireAuth(ctx, state, req.Method); err != nil {
		return sendResponse(ctx, conn, responseForError(req.ID, err))
	}
	decoded, preflightResp, failed := g.preflightRouteRequest(ctx, state, route, req)
	if failed {
		return sendResponse(ctx, conn, preflightResp)
	}
	params, ok := decoded.(serverapi.RunPromptRequest)
	if !ok {
		return sendResponse(ctx, conn, protocol.NewErrorResponse(req.ID, protocol.ErrCodeInternalError, "run prompt route contract mismatch"))
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var progressBroken atomic.Bool
	progress := serverapi.RunPromptProgressFunc(func(update serverapi.RunPromptProgress) {
		if progressBroken.Load() {
			return
		}
		err := update.Validate()
		var data []byte
		if err == nil {
			data, err = json.Marshal(update)
		}
		if err == nil {
			err = conn.Send(runCtx, rpcwire.FrameFromRequest(protocol.Request{JSONRPC: protocol.JSONRPCVersion, Method: route.EventMethod, Params: data}))
		}
		if err != nil {
			if progressBroken.CompareAndSwap(false, true) {
				cancel()
			}
		}
	})
	runClient, err := g.runPromptClientForState(runCtx, state)
	if err != nil {
		return sendResponse(ctx, conn, responseForError(req.ID, err))
	}
	resp, err := runClient.RunPrompt(runCtx, params, progress)
	if err != nil {
		return sendResponse(ctx, conn, responseForError(req.ID, err))
	}
	return sendResponse(ctx, conn, protocol.NewSuccessResponse(req.ID, resp))
}

func (g *Gateway) serveSubscription(conn rpcwire.Conn, ctx context.Context, state *connectionState, req protocol.Request) {
	if err := req.Validate(); err != nil {
		_ = sendResponse(ctx, conn, protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidRequest, err.Error()))
		return
	}
	if !state.handshakeDone {
		_ = sendResponse(ctx, conn, protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidRequest, "handshake is required before other methods"))
		return
	}
	route, ok := rpccontract.RouteByMethod(req.Method)
	if !ok {
		_ = sendResponse(ctx, conn, protocol.NewErrorResponse(req.ID, protocol.ErrCodeMethodNotFound, fmt.Sprintf("method %q not found", req.Method)))
		return
	}
	if availability, ok := g.deps.(GatewayDependencyAvailability); ok {
		if err := availability.RouteDependencyAvailable(route.Dependency); err != nil {
			_ = sendResponse(ctx, conn, responseForError(req.ID, err))
			return
		}
	}
	if err := newRoutePolicyExecutor(g).requireAuth(ctx, state, req.Method); err != nil {
		_ = sendResponse(ctx, conn, responseForError(req.ID, err))
		return
	}
	handler, ok := gatewaySubscriptionHandlers[req.Method]
	if !ok {
		_ = sendResponse(ctx, conn, protocol.NewErrorResponse(req.ID, protocol.ErrCodeMethodNotFound, fmt.Sprintf("method %q not found", req.Method)))
		return
	}
	if _, resp, failed := g.preflightRouteRequest(ctx, state, route, req); failed {
		_ = sendResponse(ctx, conn, resp)
		return
	}
	handler(g, conn, ctx, state, route, req)
}

func (g *Gateway) serveSessionActivitySubscription(conn rpcwire.Conn, ctx context.Context, _ *connectionState, route rpccontract.Route, req protocol.Request) {
	serveGatewaySubscription(conn, ctx, route, req, g.deps.SessionActivityClient().SubscribeSessionActivity, func(evt clientui.Event) protocol.SessionActivityEventParams {
		return protocol.SessionActivityEventParams{Event: evt}
	})
}

func (g *Gateway) serveSessionTranscriptSubscription(conn rpcwire.Conn, ctx context.Context, _ *connectionState, route rpccontract.Route, req protocol.Request) {
	serveGatewaySubscription(conn, ctx, route, req, g.deps.SessionTranscriptClient().SubscribeSessionTranscript, func(message clientui.TranscriptSubscriptionMessage) protocol.SessionTranscriptEventParams {
		return protocol.SessionTranscriptEventParams{Message: message}
	})
}

func serveGatewaySubscription[Req interface{ Validate() error }, Event any, Wire any, Sub gatewaySubscription[Event]](
	conn rpcwire.Conn,
	ctx context.Context,
	route rpccontract.Route,
	req protocol.Request,
	subscribe func(context.Context, Req) (Sub, error),
	wire func(Event) Wire,
) {
	params, err := decodeParams[Req](req.Params)
	if err != nil {
		_ = sendResponse(ctx, conn, protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidParams, err.Error()))
		return
	}
	if err := params.Validate(); err != nil {
		_ = sendResponse(ctx, conn, protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidParams, err.Error()))
		return
	}
	sub, err := subscribe(ctx, params)
	if err != nil {
		_ = sendResponse(ctx, conn, responseForError(req.ID, err))
		return
	}
	defer func() { _ = sub.Close() }()
	if !sendResponse(ctx, conn, protocol.NewSuccessResponse(req.ID, protocol.SubscribeResponse{Stream: route.EventMethod})) {
		return
	}
	for {
		evt, err := sub.Next(ctx)
		if err != nil {
			if data, marshalErr := json.Marshal(streamCompleteParams(err)); marshalErr == nil {
				_ = conn.Send(ctx, rpcwire.FrameFromRequest(protocol.Request{JSONRPC: protocol.JSONRPCVersion, Method: route.CompleteMethod, Params: data}))
			}
			return
		}
		data, err := json.Marshal(wire(evt))
		if err == nil {
			err = conn.Send(ctx, rpcwire.FrameFromRequest(protocol.Request{JSONRPC: protocol.JSONRPCVersion, Method: route.EventMethod, Params: data}))
		}
		if err != nil {
			return
		}
	}
}

func (g *Gateway) serveProcessOutputSubscription(conn rpcwire.Conn, ctx context.Context, _ *connectionState, route rpccontract.Route, req protocol.Request) {
	serveGatewaySubscription(conn, ctx, route, req, g.deps.ProcessOutputClient().SubscribeProcessOutput, func(chunk clientui.ProcessOutputChunk) protocol.ProcessOutputEventParams {
		return protocol.ProcessOutputEventParams{Chunk: chunk}
	})
}

func (g *Gateway) servePromptActivitySubscription(conn rpcwire.Conn, ctx context.Context, _ *connectionState, route rpccontract.Route, req protocol.Request) {
	serveGatewaySubscription(conn, ctx, route, req, g.deps.PromptActivityClient().SubscribePromptActivity, func(evt clientui.PendingPromptEvent) protocol.PromptActivityEventParams {
		return protocol.PromptActivityEventParams{Event: evt}
	})
}

func (g *Gateway) serveAttentionNotificationSubscription(conn rpcwire.Conn, ctx context.Context, _ *connectionState, route rpccontract.Route, req protocol.Request) {
	serveGatewaySubscription(conn, ctx, route, req, g.deps.AttentionNotificationClient().SubscribeAttentionNotifications, func(evt clientui.AttentionNotificationEvent) protocol.AttentionNotificationEventParams {
		return protocol.AttentionNotificationEventParams{Event: evt}
	})
}

func (g *Gateway) serveSessionAttentionNotificationSubscription(conn rpcwire.Conn, ctx context.Context, _ *connectionState, route rpccontract.Route, req protocol.Request) {
	serveGatewaySubscription(conn, ctx, route, req, g.deps.AttentionNotificationClient().SubscribeSessionAttentionNotifications, func(evt clientui.AttentionNotificationEvent) protocol.AttentionNotificationEventParams {
		return protocol.AttentionNotificationEventParams{Event: evt}
	})
}

func (g *Gateway) serveWorkflowProjectSubscription(conn rpcwire.Conn, ctx context.Context, _ *connectionState, route rpccontract.Route, req protocol.Request) {
	serveGatewaySubscription(conn, ctx, route, req, g.deps.WorkflowClient().SubscribeWorkflowProject, workflowProjectEventParams)
}

func (g *Gateway) serveWorkflowSubscription(conn rpcwire.Conn, ctx context.Context, _ *connectionState, route rpccontract.Route, req protocol.Request) {
	serveGatewaySubscription(conn, ctx, route, req, g.deps.WorkflowClient().SubscribeWorkflow, workflowProjectEventParams)
}

func (g *Gateway) serveWorktreeSetupSubscription(conn rpcwire.Conn, ctx context.Context, _ *connectionState, route rpccontract.Route, req protocol.Request) {
	serveGatewaySubscription(conn, ctx, route, req, g.deps.WorktreeClient().SubscribeWorktreeSetup, worktreeSetupEventParams)
}

func workflowProjectEventParams(evt serverapi.WorkflowProjectEvent) protocol.WorkflowProjectEventParams {
	return protocol.WorkflowProjectEventParams{Event: protocol.WorkflowProjectEvent{ProjectID: evt.ProjectID, WorkflowID: evt.WorkflowID, Resource: evt.Resource, Action: evt.Action, ChangedIDs: evt.ChangedIDs, OccurredAtUnixMs: evt.OccurredAtUnixMs}}
}

func worktreeSetupEventParams(evt serverapi.WorktreeSetupEvent) protocol.WorktreeSetupEventParams {
	return protocol.WorktreeSetupEventParams{Event: protocol.WorktreeSetupEvent{
		SetupOperationID:    evt.SetupOperationID.String(),
		SourceWorkspaceRoot: evt.SourceWorkspaceRoot,
		WorktreeRoot:        evt.WorktreeRoot,
		ScriptPath:          evt.ScriptPath,
		Phase:               string(evt.Phase),
		Timeout:             evt.Timeout,
		Canceled:            evt.Canceled,
		ExitCode:            evt.ExitCode,
		Stdout:              evt.Stdout,
		Stderr:              evt.Stderr,
		Error:               evt.Error,
	}}
}
