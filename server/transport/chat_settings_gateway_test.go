package transport

import (
	"net/http/httptest"
	"testing"

	"core/shared/protoapi"
	settingspb "core/shared/protoapi/gen/kent/api/chat_settings"
	connectionpb "core/shared/protoapi/gen/kent/api/connection"
	"core/shared/runtimeids"
)

func TestGatewayDescriptorNewChatSettingsCatalog(t *testing.T) {
	fixture := newRoutePolicyFixture(t)
	server := httptest.NewServer(fixture.gateway.Handler())
	t.Cleanup(server.Close)
	conn := dialGateway(t, server)
	t.Cleanup(func() { _ = conn.Close() })
	handshakeGateway(t, conn)
	requireGatewayProjectAttachment(t, conn, "attach-settings-project", &connectionpb.AttachProjectRequest{ProjectId: fixture.bindingA.ProjectID})
	method := settingspb.File_kent_api_chat_settings_chat_settings_proto.Services().
		ByName("ChatSettingsService").Methods().ByName("Read")
	result := &settingspb.ReadResult{}
	callGatewayDescriptor(t, conn, "new-chat-settings", method, &settingspb.ReadRequest{
		Target: &settingspb.ReadRequest_NewChat{NewChat: &settingspb.NewChatTarget{
			ProjectId: fixture.bindingA.ProjectID, WorkspaceId: fixture.bindingA.WorkspaceID,
		}},
	}, result)
	catalog := result.GetSuccess().GetNewChat()
	if catalog == nil || len(catalog.Choices) == 0 || catalog.InitialSettings == nil {
		t.Fatalf("New Chat catalog = %v", result)
	}
	for _, choice := range catalog.Choices {
		if choice.Baseline.AgentRole != choice.Agent.Role ||
			choice.Baseline.Supervisor != choice.Supervisor.Value ||
			choice.Baseline.GetQuestionsEnabled() != choice.Questions.Enabled ||
			choice.Baseline.GetAutoCompactionEnabled() != choice.AutoCompaction.Stored {
			t.Fatalf("incomplete Agent baseline: %v", choice)
		}
	}
}

func TestGatewayDescriptorSettingsMissingSessionFailure(t *testing.T) {
	fixture := newRoutePolicyFixture(t)
	server := httptest.NewServer(fixture.gateway.Handler())
	t.Cleanup(server.Close)
	conn := dialGateway(t, server)
	t.Cleanup(func() { _ = conn.Close() })
	handshakeGateway(t, conn)
	id := runtimeids.NewSessionID().String()
	method := settingspb.File_kent_api_chat_settings_chat_settings_proto.Services().
		ByName("ChatSettingsService").Methods().ByName("Read")
	result := &settingspb.ReadResult{}
	callGatewayDescriptor(t, conn, "missing-settings", method, &settingspb.ReadRequest{
		Target: &settingspb.ReadRequest_Session{Session: &settingspb.SessionTarget{SessionId: id}},
	}, result)
	if result.GetError().GetSessionNotFound().GetSessionId() != id {
		t.Fatalf("missing Session failure = %v", result)
	}
}

func TestGatewayDescriptorSettingsRequiresAuthentication(t *testing.T) {
	appCore, server, _ := newGatewayTestServerWithAuth(t, false)
	t.Cleanup(func() { _ = appCore.Close() })
	t.Cleanup(server.Close)
	conn := dialGateway(t, server)
	t.Cleanup(func() { _ = conn.Close() })
	handshakeGateway(t, conn)
	session := &settingspb.SessionTarget{SessionId: runtimeids.NewSessionID().String()}
	service := settingspb.File_kent_api_chat_settings_chat_settings_proto.Services().ByName("ChatSettingsService")
	read := &settingspb.ReadResult{}
	callGatewayDescriptor(t, conn, "unauthenticated-read", service.Methods().ByName("Read"),
		&settingspb.ReadRequest{Target: &settingspb.ReadRequest_Session{Session: session}}, read)
	if read.GetError().GetAuthRequired() == nil {
		t.Fatalf("read authentication failure = %v", read)
	}
	mutation := &settingspb.MutationResponse{}
	callGatewayDescriptor(t, conn, "unauthenticated-mutation", service.Methods().ByName("Mutate"),
		&settingspb.MutationRequest{
			Session: session, Operation: &settingspb.MutationOperation{Operation: &settingspb.MutationOperation_QuestionsEnabled{QuestionsEnabled: false}},
		}, mutation)
	if mutation.GetError().GetAuthRequired() == nil {
		t.Fatalf("mutation authentication failure = %v", mutation)
	}
}

func TestGatewayDescriptorSessionSettingsMutations(t *testing.T) {
	fixture := newRoutePolicyFixture(t)
	server := httptest.NewServer(fixture.gateway.Handler())
	t.Cleanup(server.Close)
	conn := dialGateway(t, server)
	t.Cleanup(func() { _ = conn.Close() })
	handshakeGateway(t, conn)
	method := settingspb.File_kent_api_chat_settings_chat_settings_proto.Services().
		ByName("ChatSettingsService").Methods().ByName("Mutate")
	for _, operation := range []*settingspb.MutationOperation{
		{Operation: &settingspb.MutationOperation_AgentRole{AgentRole: "missing-agent"}},
		{Operation: &settingspb.MutationOperation_Supervisor{Supervisor: settingspb.SupervisorValue_SUPERVISOR_VALUE_OFF}},
		{Operation: &settingspb.MutationOperation_Thinking{Thinking: " high "}},
		{Operation: &settingspb.MutationOperation_FastEnabled{FastEnabled: false}},
		{Operation: &settingspb.MutationOperation_QuestionsEnabled{QuestionsEnabled: false}},
		{Operation: &settingspb.MutationOperation_AutoCompactionEnabled{AutoCompactionEnabled: false}},
	} {
		result := &settingspb.MutationResponse{}
		callGatewayDescriptor(t, conn, "mutate-settings", method, &settingspb.MutationRequest{
			Session: &settingspb.SessionTarget{SessionId: fixture.ownSessionID}, Operation: operation,
		}, result)
		success := result.GetSuccess()
		if success == nil || success.Session.SessionId != fixture.ownSessionID || success.Settings == nil || success.Context == nil {
			t.Fatalf("mutation did not return authoritative Session projections: %v", result)
		}
		if err := protoapi.Validate(success); err != nil {
			t.Fatal(err)
		}
		if operation.GetAgentRole() == "missing-agent" && success.Result.GetRejected() == nil {
			t.Fatalf("missing Agent was not rejected: %v", success.Result)
		}
		if _, ok := operation.Operation.(*settingspb.MutationOperation_QuestionsEnabled); ok {
			if success.Result.GetApplied() == nil || success.Settings.Questions.Enabled {
				t.Fatalf("Questions mutation not applied: %v", success)
			}
		}
	}
}
