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

type remoteSubscription[Event any] struct {
	conn rpcwire.Conn
	next func(context.Context, rpcwire.Conn) (Event, error)
	once sync.Once
}

func (c *Remote) SubscribeAttentionNotifications(ctx context.Context, req serverapi.AttentionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error) {
	conn, route, err := c.subscribeRPC(ctx, protocol.MethodAttentionNotificationSubscribe, "subscribe-attention-notification", req, "", false)
	if err != nil {
		return nil, err
	}
	return newRemoteSubscription(conn, route, func(params protocol.AttentionNotificationEventParams) clientui.AttentionNotificationEvent {
		return params.Event
	}), nil
}

func (c *Remote) SubscribeSessionAttentionNotifications(ctx context.Context, req serverapi.AttentionSessionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error) {
	conn, route, err := c.subscribeRPC(ctx, protocol.MethodAttentionSessionNotificationSubscribe, "subscribe-session-attention-notification", req, req.SessionID, true)
	if err != nil {
		return nil, err
	}
	return newRemoteSubscription(conn, route, func(params protocol.AttentionNotificationEventParams) clientui.AttentionNotificationEvent {
		return params.Event
	}), nil
}

func (c *Remote) SubscribeFollowUp(ctx context.Context, req serverapi.PromptFollowUpWatchRequest) (serverapi.PromptFollowUpSubscription, error) {
	conn, route, err := c.subscribeRPC(ctx, protocol.MethodPromptFollowUpWatch, "subscribe-prompt-follow-up", req, req.SessionID.String(), true)
	if err != nil {
		return nil, err
	}
	return newRemoteSubscriptionWithError(conn, route, func(params protocol.PromptFollowUpEventParams) (serverapi.PromptFollowUpEvent, error) {
		event := serverapi.PromptFollowUpEvent{Kind: serverapi.PromptFollowUpEventKind(params.Event.Kind)}
		if err := event.Validate(); err != nil {
			return serverapi.PromptFollowUpEvent{}, err
		}
		return event, nil
	}), nil
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
		message, err := frame.DecodeRequest()
		if err != nil {
			return serverapi.RunPromptResponse{}, err
		}
		if message.Method == route.EventMethod {
			if progress != nil {
				var update serverapi.RunPromptProgress
				if err := json.Unmarshal(message.Params, &update); err != nil {
					return serverapi.RunPromptResponse{}, err
				}
				if err := update.Validate(); err != nil {
					return serverapi.RunPromptResponse{}, err
				}
				progress.PublishRunPromptProgress(update)
			}
			continue
		}
		if message.ID != requestID {
			return serverapi.RunPromptResponse{}, fmt.Errorf("unexpected rpc frame id %q", message.ID)
		}
		resp, err := frame.DecodeResponse()
		if err != nil {
			return serverapi.RunPromptResponse{}, err
		}
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

func (c *Remote) SubscribeSessionTranscript(ctx context.Context, req serverapi.TranscriptSubscribeRequest) (serverapi.TranscriptSubscription, error) {
	handoff, installHandoff, err := c.prepareDraftHandoff(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}
	subscriptionRemote := c
	if handoff != nil {
		subscriptionRemote = handoff.remote
	}
	conn, route, err := subscriptionRemote.subscribeRPC(ctx, protocol.MethodSessionSubscribeTranscript, "subscribe-session-transcript", req, req.SessionID, true)
	if err != nil {
		if installHandoff {
			_ = handoff.remote.Close()
		}
		return nil, err
	}
	if installHandoff {
		err = c.installDraftHandoff(handoff)
	}
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return newRemoteSubscriptionWithError(conn, route, func(params protocol.SessionTranscriptEventParams) (clientui.TranscriptMessage, error) {
		if err := params.Message.Validate(); err != nil {
			return clientui.TranscriptMessage{}, err
		}
		return params.Message, nil
	}), nil
}

func (c *Remote) SubscribeQuestionHistory(ctx context.Context, req serverapi.QuestionHistorySubscribeRequest) (serverapi.QuestionHistorySubscription, error) {
	conn, route, err := c.subscribeRPC(ctx, protocol.MethodSessionQuestionHistorySubscribe, "subscribe-question-history", req, req.SessionID, true)
	if err != nil {
		return nil, err
	}
	return newRemoteSubscriptionWithError(conn, route, func(params protocol.SessionQuestionHistoryEventParams) (serverapi.QuestionHistoryEvent, error) {
		event := serverapi.QuestionHistoryEvent{
			Kind:           serverapi.QuestionHistoryEventKind(params.Event.Kind),
			LargeHistory:   params.Event.LargeHistory,
			HistoryOmitted: params.Event.HistoryOmitted,
		}
		if params.Event.Question != nil {
			event.Question = &serverapi.QuestionHistoryQuestion{
				Question:             params.Event.Question.Question,
				Answer:               params.Event.Question.Answer,
				SelectedOptionNumber: params.Event.Question.SelectedOptionNumber,
				Commentary:           params.Event.Question.Commentary,
				At:                   params.Event.Question.At,
			}
		}
		return event, event.Validate()
	}), nil
}

func (c *Remote) SubscribeWorkflowProject(ctx context.Context, req serverapi.WorkflowProjectSubscribeRequest) (serverapi.WorkflowProjectSubscription, error) {
	conn, route, err := c.subscribeRPC(ctx, protocol.MethodWorkflowSubscribeProject, "subscribe-workflow-project", req, "", false)
	if err != nil {
		return nil, err
	}
	return newRemoteSubscriptionWithError(conn, route, func(params protocol.WorkflowProjectEventParams) (serverapi.WorkflowProjectEvent, error) {
		return workflowProjectEventFromProtocol(params.Event)
	}), nil
}

func (c *Remote) SubscribeWorkflow(ctx context.Context, req serverapi.WorkflowSubscribeRequest) (serverapi.WorkflowSubscription, error) {
	conn, route, err := c.subscribeRPC(ctx, protocol.MethodWorkflowSubscribe, "subscribe-workflow", req, "", false)
	if err != nil {
		return nil, err
	}
	return newRemoteSubscriptionWithError(conn, route, func(params protocol.WorkflowProjectEventParams) (serverapi.WorkflowProjectEvent, error) {
		return workflowProjectEventFromProtocol(params.Event)
	}), nil
}

func workflowProjectEventFromProtocol(event protocol.WorkflowProjectEvent) (serverapi.WorkflowProjectEvent, error) {
	decoded := serverapi.WorkflowProjectEvent{
		ProjectID:        event.ProjectID,
		WorkflowID:       event.WorkflowID,
		Resource:         serverapi.WorkflowProjectEventResource(event.Resource),
		Action:           serverapi.WorkflowProjectEventAction(event.Action),
		PrimaryEntityID:  event.PrimaryEntityID,
		RelatedIDs:       append([]string(nil), event.RelatedIDs...),
		OccurredAtUnixMs: event.OccurredAtUnixMs,
	}
	if err := decoded.Validate(); err != nil {
		return serverapi.WorkflowProjectEvent{}, err
	}
	return decoded, nil
}

func (c *Remote) subscribeRPC(ctx context.Context, method string, requestID string, req any, sessionID string, attachSession bool) (rpcwire.Conn, rpccontract.Route, error) {
	route := mustRemoteRoute(method)
	var additionalAttachmentIntent *remoteAttachmentIntent
	if attachSession {
		c.mu.Lock()
		attachmentIntent := c.attachIntent
		c.mu.Unlock()
		attachedSessionID, attachedToSession := attachmentIntent.sessionID()
		if attachedToSession && attachedSessionID != strings.TrimSpace(sessionID) {
			return nil, rpccontract.Route{}, fmt.Errorf(
				"remote is attached to session %q, cannot subscribe to session %q",
				attachedSessionID,
				strings.TrimSpace(sessionID),
			)
		}
		if !attachedToSession {
			intent, err := newRemoteSessionAttachmentIntent(sessionID)
			if err != nil {
				return nil, rpccontract.Route{}, err
			}
			additionalAttachmentIntent = intent
		}
	}
	conn, cleanup, err := c.openRPCConnWithAdditionalAttachment(ctx, additionalAttachmentIntent)
	if err != nil {
		return nil, rpccontract.Route{}, err
	}
	var ack protocol.SubscribeResponse
	if err := callRPC(ctx, conn, requestID, method, req, &ack); err != nil {
		cleanup()
		return nil, rpccontract.Route{}, err
	}
	return conn, route, nil
}

func newRemoteSubscription[Wire any, Event any](conn rpcwire.Conn, route rpccontract.Route, event func(Wire) Event) *remoteSubscription[Event] {
	return newRemoteSubscriptionWithError(conn, route, func(wire Wire) (Event, error) {
		return event(wire), nil
	})
}

func newRemoteSubscriptionWithError[Wire any, Event any](conn rpcwire.Conn, route rpccontract.Route, event func(Wire) (Event, error)) *remoteSubscription[Event] {
	return &remoteSubscription[Event]{
		conn: conn,
		next: func(ctx context.Context, conn rpcwire.Conn) (Event, error) {
			return nextJSONSubscriptionEvent(ctx, conn, route, event)
		},
	}
}

func mustRemoteRoute(method string) rpccontract.Route {
	route, ok := rpccontract.RouteByMethod(method)
	if !ok {
		panic(fmt.Sprintf("remote route %q is missing route contract", method))
	}
	return route
}

func nextJSONSubscriptionEvent[Wire any, Event any](
	ctx context.Context,
	conn rpcwire.Conn,
	route rpccontract.Route,
	event func(Wire) (Event, error),
) (Event, error) {
	frame, err := receiveFrame(ctx, conn)
	if err != nil {
		var zero Event
		return zero, serverapi.NormalizeStreamError(err)
	}
	message, err := frame.DecodeRequest()
	if err != nil {
		var zero Event
		return zero, errors.Join(serverapi.ErrStreamFailed, err)
	}
	switch message.Method {
	case route.EventMethod:
		var params Wire
		if err := json.Unmarshal(message.Params, &params); err != nil {
			var zero Event
			return zero, errors.Join(serverapi.ErrStreamFailed, err)
		}
		decoded, err := event(params)
		if err != nil {
			var zero Event
			return zero, errors.Join(serverapi.ErrStreamFailed, err)
		}
		return decoded, nil
	case route.CompleteMethod:
		var params protocol.StreamCompleteParams
		if err := json.Unmarshal(message.Params, &params); err != nil {
			var zero Event
			return zero, errors.Join(serverapi.ErrStreamFailed, err)
		}
		_ = conn.Close()
		var zero Event
		if params.Code == 0 && strings.TrimSpace(params.Message) == "" {
			return zero, io.EOF
		}
		terminalErr := protocolError(&protocol.ResponseError{Code: params.Code, Message: params.Message})
		if reason := strings.TrimSpace(params.TranscriptCloseReason); reason != "" {
			return zero, serverapi.NewTranscriptStreamError(serverapi.TranscriptCloseReason(reason), terminalErr)
		}
		return zero, terminalErr
	default:
		var zero Event
		return zero, errors.Join(serverapi.ErrStreamFailed, fmt.Errorf("unexpected notification method %q", message.Method))
	}
}

func (s *remoteSubscription[Event]) Next(ctx context.Context) (Event, error) {
	event, err := s.next(ctx, s.conn)
	if errors.Is(err, io.EOF) {
		_ = s.Close()
	}
	return event, err
}

func (s *remoteSubscription[Event]) Close() error {
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
