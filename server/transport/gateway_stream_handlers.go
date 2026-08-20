package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	rpccontract "core/shared/apicontract"
	"core/shared/clientui"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
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
	if err := g.requireCoreActive(); err != nil {
		return sendResponse(ctx, conn, responseForError(req.ID, err))
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
	operation, route, ok := g.registration.LegacyOperation(req.Method)
	if !ok || operation.Options.Kind != sharedpb.OperationKind_OPERATION_KIND_SUBSCRIPTION {
		_ = sendResponse(ctx, conn, protocol.NewErrorResponse(req.ID, protocol.ErrCodeMethodNotFound, fmt.Sprintf("method %q not found", req.Method)))
		return
	}
	if err := g.requireCoreActive(); err != nil {
		_ = sendResponse(ctx, conn, responseForError(req.ID, err))
		return
	}
	if err := newRoutePolicyExecutor(g).requireAuthenticationStage(
		ctx,
		state,
		operation.Options.AuthenticationStage,
	); err != nil {
		_ = sendResponse(ctx, conn, responseForError(req.ID, err))
		return
	}
	route.Scope = routeScopePolicy(operation.Options.ScopePolicy)
	if _, resp, failed := g.preflightRouteRequest(ctx, state, route, req); failed {
		_ = sendResponse(ctx, conn, resp)
		return
	}
	gatewaySubscriptionHandlers[req.Method](g, conn, ctx, state, route, req)
}

func (g *Gateway) serveSessionTranscriptSubscription(conn rpcwire.Conn, ctx context.Context, state *connectionState, route rpccontract.Route, req protocol.Request) {
	serveGatewaySubscription(conn, ctx, route, req, g.deps.SessionTranscriptClient().SubscribeSessionTranscript, func(message clientui.TranscriptMessage) protocol.SessionTranscriptEventParams {
		return protocol.SessionTranscriptEventParams{Message: message}
	})
}

func (g *Gateway) serveQuestionHistorySubscription(conn rpcwire.Conn, ctx context.Context, _ *connectionState, route rpccontract.Route, req protocol.Request) {
	serveGatewaySubscription(
		conn,
		ctx,
		route,
		req,
		g.deps.SessionViewClient().SubscribeQuestionHistory,
		func(event serverapi.QuestionHistoryEvent) protocol.SessionQuestionHistoryEventParams {
			wire := protocol.SessionQuestionHistoryEvent{
				Kind:           string(event.Kind),
				LargeHistory:   event.LargeHistory,
				HistoryOmitted: event.HistoryOmitted,
			}
			if event.Question != nil {
				wire.Question = &protocol.SessionQuestionHistoryQuestion{
					Question:             event.Question.Question,
					Answer:               event.Question.Answer,
					SelectedOptionNumber: event.Question.SelectedOptionNumber,
					Commentary:           event.Question.Commentary,
					At:                   event.Question.At,
				}
			}
			return protocol.SessionQuestionHistoryEventParams{Event: wire}
		},
	)
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

func (g *Gateway) servePromptFollowUpSubscription(conn rpcwire.Conn, ctx context.Context, _ *connectionState, route rpccontract.Route, req protocol.Request) {
	serveGatewaySubscription(conn, ctx, route, req, g.deps.PromptControlClient().SubscribeFollowUp, func(evt serverapi.PromptFollowUpEvent) protocol.PromptFollowUpEventParams {
		return protocol.PromptFollowUpEventParams{Event: protocol.PromptFollowUpEvent{Kind: string(evt.Kind)}}
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
	return protocol.WorkflowProjectEventParams{Event: protocol.WorkflowProjectEvent{
		ProjectID:        evt.ProjectID,
		WorkflowID:       evt.WorkflowID,
		Resource:         protocol.WorkflowProjectEventResource(evt.Resource),
		Action:           protocol.WorkflowProjectEventAction(evt.Action),
		PrimaryEntityID:  evt.PrimaryEntityID,
		RelatedIDs:       append([]string(nil), evt.RelatedIDs...),
		OccurredAtUnixMs: evt.OccurredAtUnixMs,
	}}
}

func worktreeSetupEventParams(evt serverapi.WorktreeSetupEvent) protocol.WorktreeSetupEventParams {
	if err := evt.Validate(); err != nil {
		panic(fmt.Sprintf("serialize invalid worktree setup event: %v; event=%+v", err, evt))
	}
	payload := func(value any) json.RawMessage {
		encoded, err := json.Marshal(value)
		if err != nil {
			panic(fmt.Sprintf("marshal validated worktree setup event payload: %v", err))
		}
		return encoded
	}
	var started json.RawMessage
	if evt.Started != nil {
		started = payload(evt.Started)
	}
	var completed json.RawMessage
	if evt.Completed != nil {
		completed = payload(evt.Completed)
	}
	var notRequired json.RawMessage
	if evt.NotRequired != nil {
		notRequired = payload(evt.NotRequired)
	}
	var failed json.RawMessage
	if evt.Failed != nil {
		failed = payload(evt.Failed)
	}
	return protocol.WorktreeSetupEventParams{Event: protocol.WorktreeSetupEvent{
		SetupOperationID: evt.SetupOperationID.String(),
		Phase:            string(evt.Phase),
		Started:          started,
		Completed:        completed,
		NotRequired:      notRequired,
		Failed:           failed,
	}}
}
