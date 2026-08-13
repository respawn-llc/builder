package client

import (
	"context"
	"errors"
	"io"
	"testing"

	"core/shared/clientui"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/transcript"

	"golang.org/x/net/websocket"
)

func TestRemoteTranscriptPageRejectsMalformedLocatorPayload(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		acceptRemoteHandshake(t, ws)
		var request protocol.Request
		if err := websocket.JSON.Receive(ws, &request); err != nil {
			t.Fatalf("receive transcript page request: %v", err)
		}
		if request.Method != protocol.MethodSessionGetTranscriptPage {
			t.Fatalf("method = %q, want transcript page", request.Method)
		}
		response := serverapi.SessionTranscriptPageResponse{
			Transcript: clientui.TranscriptPage{
				SessionID: "12345678-1234-4234-8234-123456789012",
				Entries: []clientui.TranscriptCommittedRow{{
					Visibility: clientui.EntryVisibilityOngoing,
					Kind:       clientui.TranscriptRowAssistant,
					Assistant: &clientui.TranscriptAssistantRow{
						StepID: transcriptRemoteTestStepID(t),
						Text:   "done",
						Phase:  "final_answer",
					},
				}},
			},
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(request.ID, response)); err != nil {
			t.Fatalf("send transcript page response: %v", err)
		}
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("dial remote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	if _, err := remote.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{
		SessionID: "12345678-1234-4234-8234-123456789012",
	}); err == nil {
		t.Fatal("accepted transcript page with missing locator")
	}
}

func TestRemoteTranscriptSubscriptionRejectsMalformedLocatorPayload(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			t.Fatalf("receive attach request: %v", err)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, testSessionAttachResponse(t, "project-1", "workspace-1", "/workspace", "session-1"))); err != nil {
			t.Fatalf("send attach response: %v", err)
		}
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			t.Fatalf("receive subscribe request: %v", err)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.SubscribeResponse{})); err != nil {
			t.Fatalf("send subscribe response: %v", err)
		}
		message := clientui.NewTranscriptMessage(2, clientui.NewTranscriptEvent(clientui.TranscriptCommittedRow{
			Visibility: clientui.EntryVisibilityOngoing,
			Kind:       clientui.TranscriptRowAssistant,
			Assistant: &clientui.TranscriptAssistantRow{
				StepID: transcriptRemoteTestStepID(t),
				Text:   "done",
				Phase:  "final_answer",
			},
		}))
		if err := websocket.JSON.Send(ws, protocol.Request{
			JSONRPC: protocol.JSONRPCVersion,
			Method:  protocol.MethodSessionTranscriptEvent,
			Params:  mustJSON(t, protocol.SessionTranscriptEventParams{Message: message}),
		}); err != nil {
			t.Fatalf("send transcript event: %v", err)
		}
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("dial remote: %v", err)
	}
	defer func() { _ = remote.Close() }()
	sub, err := remote.SubscribeSessionTranscript(context.Background(), serverapi.TranscriptSubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("subscribe transcript: %v", err)
	}
	defer func() { _ = sub.Close() }()

	if _, err := sub.Next(context.Background()); err == nil || !errors.Is(err, serverapi.ErrStreamFailed) {
		t.Fatalf("malformed transcript event error = %v, want stream failure", err)
	}
}

func TestRemoteTranscriptSubscriptionDecodesProviderModelMismatchNotice(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			t.Fatalf("receive attach request: %v", err)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, testSessionAttachResponse(t, "project-1", "workspace-1", "/workspace", "session-1"))); err != nil {
			t.Fatalf("send attach response: %v", err)
		}
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			t.Fatalf("receive subscribe request: %v", err)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.SubscribeResponse{})); err != nil {
			t.Fatalf("send subscribe response: %v", err)
		}
		stepID := transcriptRemoteTestStepID(t)
		message := clientui.NewTranscriptMessage(2, clientui.NewTranscriptEvent(clientui.TranscriptCommittedRow{
			Locator: transcript.CommittedRowLocator{
				EventSequence: 1,
				RowOrdinal:    1,
			},
			Visibility: clientui.EntryVisibilityOngoing,
			Kind:       clientui.TranscriptRowNotice,
			Notice: &clientui.TranscriptNoticeRow{
				StepID:   &stepID,
				Reason:   clientui.TranscriptNoticeProviderModelMismatch,
				Severity: clientui.TranscriptNoticeWarning,
				ProviderModelMismatch: &transcript.ProviderModelMismatchNotice{
					RequestedModel: "requested-model",
					ServedModel:    "served-model",
				},
			},
		}))
		if err := websocket.JSON.Send(ws, protocol.Request{
			JSONRPC: protocol.JSONRPCVersion,
			Method:  protocol.MethodSessionTranscriptEvent,
			Params:  mustJSON(t, protocol.SessionTranscriptEventParams{Message: message}),
		}); err != nil {
			t.Fatalf("send transcript event: %v", err)
		}
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("dial remote: %v", err)
	}
	defer func() { _ = remote.Close() }()
	sub, err := remote.SubscribeSessionTranscript(context.Background(), serverapi.TranscriptSubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("subscribe transcript: %v", err)
	}
	defer func() { _ = sub.Close() }()

	message, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("next transcript message: %v", err)
	}
	row, ok := message.Payload().(clientui.TranscriptCommittedRow)
	if !ok || row.Notice == nil || row.Notice.ProviderModelMismatch == nil {
		t.Fatalf("transcript message = %+v, want provider-model mismatch notice", message)
	}
	mismatch := row.Notice.ProviderModelMismatch
	if mismatch.RequestedModel != "requested-model" || mismatch.ServedModel != "served-model" {
		t.Fatalf("mismatch = %+v", mismatch)
	}
}

func transcriptRemoteTestStepID(t *testing.T) runtimeids.StepID {
	t.Helper()
	id, err := runtimeids.ParseStepID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("parse step id: %v", err)
	}
	return id
}
