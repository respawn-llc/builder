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
	event func(Wire) (Event, error)
	once  sync.Once
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
		return serverapi.PromptFollowUpEvent{Kind: serverapi.PromptFollowUpEventKind(params.Event.Kind)}, nil
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
		if frame.Method == route.EventMethod {
			if progress != nil {
				var update serverapi.RunPromptProgress
				if err := decodeValidatedJSON(frame.Params, &update); err != nil {
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
		if err := decodeResponseFrame(resp, &result); err != nil {
			return serverapi.RunPromptResponse{}, err
		}
		return result, nil
	}
}

func (c *Remote) SubscribeSessionTranscript(ctx context.Context, req serverapi.TranscriptSubscribeRequest) (serverapi.TranscriptSubscription, error) {
	conn, route, err := c.subscribeRPC(ctx, protocol.MethodSessionSubscribeTranscript, "subscribe-session-transcript", req, req.SessionID, true)
	if err != nil {
		return nil, err
	}
	return newRemoteSubscriptionWithError(conn, route, func(params protocol.SessionTranscriptEventParams) (clientui.TranscriptMessage, error) {
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
	return decoded, nil
}

func (c *Remote) SubscribeWorktreeSetup(ctx context.Context, req serverapi.WorktreeSetupSubscribeRequest) (serverapi.WorktreeSetupSubscription, error) {
	conn, route, err := c.subscribeRPC(ctx, protocol.MethodWorktreeSetupSubscribe, "subscribe-worktree-setup", req, "", false)
	if err != nil {
		return nil, err
	}
	return newRemoteSubscriptionWithError(conn, route, func(params protocol.WorktreeSetupEventParams) (serverapi.WorktreeSetupEvent, error) {
		id, err := serverapi.ParseWorktreeSetupOperationID(params.Event.SetupOperationID)
		if err != nil {
			return serverapi.WorktreeSetupEvent{}, err
		}
		decoded := serverapi.WorktreeSetupEvent{
			SetupOperationID: id,
			Phase:            serverapi.WorktreeSetupPhase(params.Event.Phase),
		}
		decodePayload := func(raw json.RawMessage, target any) error {
			if len(raw) == 0 {
				return nil
			}
			if err := protocol.DecodeStrictJSON([]byte(raw), target); err != nil {
				return err
			}
			return nil
		}
		if err := decodePayload(params.Event.Started, &decoded.Started); err != nil {
			return serverapi.WorktreeSetupEvent{}, err
		}
		if err := decodePayload(params.Event.Completed, &decoded.Completed); err != nil {
			return serverapi.WorktreeSetupEvent{}, err
		}
		if err := decodePayload(params.Event.NotRequired, &decoded.NotRequired); err != nil {
			return serverapi.WorktreeSetupEvent{}, err
		}
		if err := decodePayload(params.Event.Failed, &decoded.Failed); err != nil {
			return serverapi.WorktreeSetupEvent{}, err
		}
		return decoded, nil
	}), nil
}

func (c *Remote) subscribeRPC(ctx context.Context, method string, requestID string, req any, sessionID string, attachSession bool) (rpcwire.Conn, rpccontract.Route, error) {
	route := mustRemoteRoute(method)
	conn, cleanup, err := c.openRPCConn(ctx)
	if err != nil {
		return nil, rpccontract.Route{}, err
	}
	if attachSession {
		attachedSessionID, attachedToSession := c.attachIntent.sessionID()
		if attachedToSession && attachedSessionID != strings.TrimSpace(sessionID) {
			cleanup()
			return nil, rpccontract.Route{}, fmt.Errorf(
				"remote is attached to session %q, cannot subscribe to session %q",
				attachedSessionID,
				strings.TrimSpace(sessionID),
			)
		}
		if !attachedToSession {
			intent, err := newRemoteSessionAttachmentIntent(sessionID)
			if err != nil {
				cleanup()
				return nil, rpccontract.Route{}, err
			}
			if _, err := attachSessionRPC(ctx, conn, intent); err != nil {
				cleanup()
				return nil, rpccontract.Route{}, err
			}
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
	return newRemoteSubscriptionWithError(conn, route, func(wire Wire) (Event, error) {
		return event(wire), nil
	})
}

func newRemoteSubscriptionWithError[Wire any, Event any](conn rpcwire.Conn, route rpccontract.Route, event func(Wire) (Event, error)) *remoteSubscription[Wire, Event] {
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
		if err := decodeValidatedJSON(frame.Params, &params); err != nil {
			var zero Event
			return zero, errors.Join(serverapi.ErrStreamFailed, err)
		}
		event, err := s.event(params)
		if err != nil {
			var zero Event
			return zero, errors.Join(serverapi.ErrStreamFailed, err)
		}
		if validator, ok := any(event).(interface{ Validate() error }); ok {
			if err := validator.Validate(); err != nil {
				var zero Event
				return zero, errors.Join(serverapi.ErrStreamFailed, err)
			}
		}
		return event, nil
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
		terminalErr := protocolError(&protocol.ResponseError{Code: params.Code, Message: params.Message})
		if reason := strings.TrimSpace(params.TranscriptCloseReason); reason != "" {
			return zero, serverapi.NewTranscriptStreamError(serverapi.TranscriptCloseReason(reason), terminalErr)
		}
		return zero, terminalErr
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
