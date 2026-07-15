package client

import (
	"context"
	"encoding/json"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
	"golang.org/x/net/websocket"
)

type recordingLifecycleResultService struct {
	last   serverapi.SessionResolveTransitionRequest
	result serverapi.SessionLifecycleResult
}

func (s *recordingLifecycleResultService) GetInitialInput(context.Context, serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error) {
	return serverapi.SessionInitialInputResponse{}, nil
}

func (s *recordingLifecycleResultService) PersistInputDraft(context.Context, serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error) {
	return serverapi.SessionPersistInputDraftResponse{}, nil
}

func (s *recordingLifecycleResultService) RetargetSessionWorkspace(context.Context, serverapi.SessionRetargetWorkspaceRequest) (serverapi.SessionRetargetWorkspaceResponse, error) {
	return serverapi.SessionRetargetWorkspaceResponse{}, nil
}

func (s *recordingLifecycleResultService) ResolveTransition(_ context.Context, req serverapi.SessionResolveTransitionRequest) (serverapi.SessionLifecycleResult, error) {
	s.last = req
	return s.result, nil
}

func TestSessionLifecycleResultLoopbackRoundTrip(t *testing.T) {
	service := &recordingLifecycleResultService{
		result: serverapi.SelectSessionLifecycleResult(serverapi.SessionAuthPreparationKeepCurrent),
	}
	lifecycle := NewLoopbackSessionLifecycleClient(service)
	got, err := lifecycle.ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{
		ClientRequestID: "loopback-lifecycle-result",
		Transition:      serverapi.SessionTransition{Action: serverapi.SessionTransitionActionResume},
	})
	if err != nil {
		t.Fatalf("ResolveTransition: %v", err)
	}
	if service.last.Transition.Action != serverapi.SessionTransitionActionResume {
		t.Fatalf("transition action = %q, want resume", service.last.Transition.Action)
	}
	if !got.Equal(service.result) {
		t.Fatalf("result = %+v, want %+v", got, service.result)
	}
}

func TestSessionLifecycleResultRemoteRoundTrip(t *testing.T) {
	want := serverapi.SelectSessionLifecycleResult(serverapi.SessionAuthPreparationReauthenticate)
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			t.Errorf("receive session.resolve_transition: %v", err)
			return
		}
		if req.Method != protocol.MethodSessionResolveTransition {
			t.Errorf("method = %q, want %q", req.Method, protocol.MethodSessionResolveTransition)
			return
		}
		var transition serverapi.SessionResolveTransitionRequest
		if err := json.Unmarshal(req.Params, &transition); err != nil {
			t.Errorf("decode transition: %v", err)
			return
		}
		if transition.Transition.Action != serverapi.SessionTransitionActionLogout {
			t.Errorf("transition action = %q, want logout", transition.Transition.Action)
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, want)); err != nil {
			t.Errorf("send lifecycle result: %v", err)
		}
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()

	got, err := remote.ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{
		ClientRequestID: "remote-lifecycle-result",
		Transition:      serverapi.SessionTransition{Action: serverapi.SessionTransitionActionLogout},
	})
	if err != nil {
		t.Fatalf("ResolveTransition: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("result = %+v, want %+v", got, want)
	}
}
