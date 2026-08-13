package transport

import (
	"context"
	"encoding/json"
	"errors"
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
	return executeRunPrompt(g, conn, ctx, state, route, req)
}

func executeRunPrompt(g *Gateway, conn rpcwire.Conn, ctx context.Context, state *connectionState, route rpccontract.Route, req protocol.Request) bool {
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
	params, err := decodeInboundRequest[serverapi.RunPromptRequest](g, route, requestDecoderDefault, req.Params)
	if err != nil {
		return sendResponse(ctx, conn, protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidParams, err.Error()))
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
	resp, err := rpccontract.WithValidated(params, rpccontract.SemanticValidationRequired, func(validated rpccontract.Validated[serverapi.RunPromptRequest]) (serverapi.RunPromptResponse, error) {
		if err := newRoutePolicyExecutor(g).authorizeScope(runCtx, state, route, validated.Value()); err != nil {
			return serverapi.RunPromptResponse{}, validatedOwnerError{cause: err}
		}
		runClient, err := g.runPromptClientForState(runCtx, state)
		if err != nil {
			return serverapi.RunPromptResponse{}, validatedOwnerError{cause: err}
		}
		trusted, ok := runClient.(rpccontract.RunPromptTrustedService)
		if !ok {
			return serverapi.RunPromptResponse{}, validatedOwnerError{cause: errors.New("Run Prompt trusted service is required")}
		}
		response, err := trusted.RunPromptValidated(runCtx, validated, progress)
		if err != nil {
			return serverapi.RunPromptResponse{}, validatedOwnerError{cause: err}
		}
		return response, nil
	})
	if err != nil {
		return sendResponse(ctx, conn, responseForValidationOrOwnerError(req.ID, err))
	}
	return sendResponse(ctx, conn, handlerSuccessResponse(req.ID, resp))
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
	executable, ok := inboundExecutableRoutes[req.Method]
	if !ok {
		_ = sendResponse(ctx, conn, protocol.NewErrorResponse(req.ID, protocol.ErrCodeMethodNotFound, fmt.Sprintf("method %q not found", req.Method)))
		return
	}
	if executable.executeSubscription != nil {
		executable.executeSubscription(g, conn, ctx, state, route, req)
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

func executeAttentionNotificationSubscription(g *Gateway, conn rpcwire.Conn, ctx context.Context, _ *connectionState, route rpccontract.Route, req protocol.Request) {
	params, err := decodeInboundRequest[serverapi.AttentionNotificationSubscribeRequest](g, route, requestDecoderDefault, req.Params)
	if err != nil {
		_ = sendResponse(ctx, conn, protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidParams, err.Error()))
		return
	}
	_, err = rpccontract.WithValidated(params, rpccontract.SemanticValidationRequired, func(validated rpccontract.Validated[serverapi.AttentionNotificationSubscribeRequest]) (struct{}, error) {
		trusted, ok := g.deps.AttentionNotificationClient().(rpccontract.AttentionNotificationTrustedService)
		if !ok {
			return struct{}{}, validatedOwnerError{cause: errors.New("Attention Notification trusted service is required")}
		}
		subscription, err := trusted.SubscribeAttentionNotificationsValidated(ctx, validated)
		if err != nil {
			return struct{}{}, validatedOwnerError{cause: err}
		}
		serveInstalledGatewaySubscription(conn, ctx, route, req, subscription, func(event clientui.AttentionNotificationEvent) protocol.AttentionNotificationEventParams {
			return protocol.AttentionNotificationEventParams{Event: event}
		})
		return struct{}{}, nil
	})
	if err != nil {
		_ = sendResponse(ctx, conn, responseForValidationOrOwnerError(req.ID, err))
	}
}

func executeProcessOutputSubscription(g *Gateway, conn rpcwire.Conn, ctx context.Context, state *connectionState, route rpccontract.Route, req protocol.Request) {
	params, err := decodeInboundRequest[serverapi.ProcessOutputSubscribeRequest](g, route, requestDecoderDefault, req.Params)
	if err != nil {
		_ = sendResponse(ctx, conn, protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidParams, err.Error()))
		return
	}
	_, err = rpccontract.WithValidated(params, rpccontract.SemanticValidationRequired, func(validated rpccontract.Validated[serverapi.ProcessOutputSubscribeRequest]) (struct{}, error) {
		authorization, err := authorizeProcessActiveProject(func(request serverapi.ProcessOutputSubscribeRequest) string {
			return request.ProcessID
		})(ctx, g, state, validated)
		if err != nil {
			return struct{}{}, validatedOwnerError{cause: err}
		}
		trusted, ok := g.deps.ProcessOutputClient().(rpccontract.ProcessOutputTrustedService)
		if !ok {
			return struct{}{}, validatedOwnerError{cause: errors.New("Process Output trusted service is required")}
		}
		subscription, err := trusted.SubscribeProcessOutputValidated(ctx, validated, authorization)
		if err != nil {
			return struct{}{}, validatedOwnerError{cause: err}
		}
		serveInstalledGatewaySubscription(conn, ctx, route, req, subscription, func(chunk clientui.ProcessOutputChunk) protocol.ProcessOutputEventParams {
			return protocol.ProcessOutputEventParams{Chunk: chunk}
		})
		return struct{}{}, nil
	})
	if err != nil {
		_ = sendResponse(ctx, conn, responseForValidationOrOwnerError(req.ID, err))
	}
}

func executePromptFollowUpSubscription(g *Gateway, conn rpcwire.Conn, ctx context.Context, state *connectionState, route rpccontract.Route, req protocol.Request) {
	params, err := decodeInboundRequest[serverapi.PromptFollowUpWatchRequest](g, route, requestDecoderDefault, req.Params)
	if err != nil {
		_ = sendResponse(ctx, conn, protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidParams, err.Error()))
		return
	}
	_, err = rpccontract.WithValidated(params, rpccontract.SemanticValidationRequired, func(validated rpccontract.Validated[serverapi.PromptFollowUpWatchRequest]) (struct{}, error) {
		if _, err := authorizeSessionActiveProject(
			func(request serverapi.PromptFollowUpWatchRequest) string { return request.SessionID.String() },
		)(ctx, g, state, validated); err != nil {
			return struct{}{}, validatedOwnerError{cause: err}
		}
		subscription, err := g.deps.PromptControlClient().SubscribeFollowUp(ctx, validated.Value())
		if err != nil {
			return struct{}{}, validatedOwnerError{cause: err}
		}
		serveInstalledGatewaySubscription(conn, ctx, route, req, subscription, func(evt serverapi.PromptFollowUpEvent) protocol.PromptFollowUpEventParams {
			return protocol.PromptFollowUpEventParams{Event: protocol.PromptFollowUpEvent{Kind: string(evt.Kind)}}
		})
		return struct{}{}, nil
	})
	if err != nil {
		_ = sendResponse(ctx, conn, responseForValidationOrOwnerError(req.ID, err))
	}
}

func (g *Gateway) serveSessionTranscriptSubscription(conn rpcwire.Conn, ctx context.Context, state *connectionState, route rpccontract.Route, req protocol.Request) {
	subscribe := g.deps.SessionTranscriptClient().SubscribeSessionTranscript
	if !state.clientCapabilities.TranscriptLiveRunFinished {
		subscribe = func(ctx context.Context, req serverapi.TranscriptSubscribeRequest) (serverapi.TranscriptSubscription, error) {
			subscription, err := g.deps.SessionTranscriptClient().SubscribeSessionTranscript(ctx, req)
			if err != nil {
				return nil, err
			}
			return &legacyTranscriptSubscription{inner: subscription}, nil
		}
	}
	serveGatewaySubscription(conn, ctx, route, req, subscribe, func(message clientui.TranscriptMessage) protocol.SessionTranscriptEventParams {
		return protocol.SessionTranscriptEventParams{Message: message}
	})
}

type legacyTranscriptSubscription struct {
	inner      serverapi.TranscriptSubscription
	suppressed uint64
}

type legacyTranscriptSequenceError struct {
	Sequence   uint64
	Suppressed uint64
}

func (e *legacyTranscriptSequenceError) Error() string {
	return fmt.Sprintf(
		"legacy transcript sequence %d is below suppressed message count %d",
		e.Sequence,
		e.Suppressed,
	)
}

func (s *legacyTranscriptSubscription) Next(ctx context.Context) (clientui.TranscriptMessage, error) {
	for {
		message, err := s.inner.Next(ctx)
		if err != nil {
			return clientui.TranscriptMessage{}, err
		}
		if message.Kind() == clientui.TranscriptMessageLiveRunFinished {
			s.suppressed++
			continue
		}
		if message.Sequence < s.suppressed {
			return clientui.TranscriptMessage{}, &legacyTranscriptSequenceError{
				Sequence:   message.Sequence,
				Suppressed: s.suppressed,
			}
		}
		message.Sequence -= s.suppressed
		return message, nil
	}
}

func (s *legacyTranscriptSubscription) Close() error {
	return s.inner.Close()
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
	serveInstalledGatewaySubscription(conn, ctx, route, req, sub, wire)
}

func serveInstalledGatewaySubscription[Event any, Wire any, Sub gatewaySubscription[Event]](
	conn rpcwire.Conn,
	ctx context.Context,
	route rpccontract.Route,
	req protocol.Request,
	sub Sub,
	wire func(Event) Wire,
) {
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
