package client

import (
	"context"
	"encoding/json"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
	"golang.org/x/net/websocket"
)

func TestRemoteReadChatSettingsRoundTripsLazyContract(t *testing.T) {
	target := serverapi.LazyChatSettingsTarget("project-1", "workspace-1")
	response := transportChatSettingsResponse()
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		request := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &request); err != nil {
			t.Errorf("receive Chat settings request: %v", err)
			return
		}
		if request.Method != protocol.MethodChatSettingsRead {
			t.Errorf("method = %q", request.Method)
			return
		}
		var params serverapi.ChatSettingsReadRequest
		if err := json.Unmarshal(request.Params, &params); err != nil {
			t.Errorf("decode params: %v", err)
			return
		}
		if params.Target.Kind() != serverapi.ChatSettingsReadTargetLazy {
			t.Errorf("target kind = %q", params.Target.Kind())
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(request.ID, response)); err != nil {
			t.Errorf("send response: %v", err)
		}
	})
	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()

	got, err := remote.ReadChatSettings(context.Background(), serverapi.ChatSettingsReadRequest{Target: target})
	if err != nil {
		t.Fatalf("ReadChatSettings: %v", err)
	}
	if got.Settings.SelectedAgent.Role != "default" || got.Session != nil {
		t.Fatalf("response = %+v", got)
	}
}

func transportChatSettingsResponse() serverapi.ChatSettingsReadResponse {
	return serverapi.ChatSettingsReadResponse{
		Settings: serverapi.ChatSettings{
			SelectedAgent: serverapi.ChatSettingsAgentSummary{
				Role: "default", Model: "gpt-5", Thinking: "medium",
			},
			AgentChoices: []serverapi.ChatSettingsAgentChoice{{
				Role: "default", Model: "gpt-5", Thinking: "medium",
				Tools: []string{}, AgentCallable: true,
			}},
			AgentEditability: serverapi.ChatSettingsEditable,
			Supervisor: serverapi.ChatSettingsSupervisor{
				Value: serverapi.ChatSettingsSupervisorOff, Editability: serverapi.ChatSettingsEditable,
			},
			Questions: serverapi.ChatSettingsQuestions{
				Enabled: true, Editability: serverapi.ChatSettingsEditable,
			},
			AutoCompaction: serverapi.ChatSettingsAutoCompaction{
				Policy: serverapi.ChatSettingsAutoCompactionOptional,
				Stored: true, Effective: true, Editability: serverapi.ChatSettingsEditable,
			},
		},
	}
}
