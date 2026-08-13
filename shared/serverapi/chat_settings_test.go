package serverapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"core/shared/protocol"
	"core/shared/runtimeids"
)

func TestChatSettingsReadTargetRoundTripsLazyAndSessionTargets(t *testing.T) {
	sessionID, err := runtimeids.ParseSessionID("session-1")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	tests := []struct {
		name string
		want ChatSettingsReadTarget
		json string
	}{
		{
			name: "lazy",
			want: LazyChatSettingsTarget(
				"project-11111111-1111-4111-8111-111111111111",
				"workspace-22222222-2222-4222-8222-222222222222",
			),
			json: `{"kind":"lazy","project_id":"project-11111111-1111-4111-8111-111111111111","workspace_id":"workspace-22222222-2222-4222-8222-222222222222"}`,
		},
		{
			name: "session",
			want: SessionChatSettingsTarget(sessionID),
			json: `{"kind":"session","session_id":"session-1"}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.want)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(encoded) != test.json {
				t.Fatalf("JSON = %s, want %s", encoded, test.json)
			}
			var decoded ChatSettingsReadTarget
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			roundTrip, err := json.Marshal(decoded)
			if err != nil {
				t.Fatalf("Marshal decoded: %v", err)
			}
			if !bytes.Equal(roundTrip, encoded) {
				t.Fatalf("round trip = %s, want %s", roundTrip, encoded)
			}
		})
	}
}

func TestChatSettingsReadTargetRejectsContradictoryAndMalformedJSON(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`{"kind":"unknown"}`,
		`{"kind":"lazy","project_id":"","workspace_id":"workspace-1"}`,
		`{"kind":"lazy","project_id":" project-1 ","workspace_id":"workspace-1"}`,
		`{"kind":"lazy","project_id":"project-1","workspace_id":" "}`,
		`{"kind":"lazy","project_id":"project-1","workspace_id":"workspace-1","session_id":"session-1"}`,
		`{"kind":"session"}`,
		`{"kind":"session","session_id":" session-1 "}`,
		`{"kind":"session","session_id":"../escape"}`,
		`{"kind":"session","session_id":"session-1","project_id":"project-1"}`,
		`{"kind":"session","session_id":"session-1","unknown":true}`,
		`{"kind":"session","session_id":"session-1"}{"kind":"session","session_id":"session-2"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			var target ChatSettingsReadTarget
			if err := json.Unmarshal([]byte(raw), &target); err == nil {
				t.Fatalf("Unmarshal(%s) unexpectedly succeeded: %+v", raw, target)
			}
		})
	}
}

func TestChatSettingsReadTargetPreservesSupportedSessionIdentifiers(t *testing.T) {
	for _, raw := range []string{
		"11111111-1111-4111-8111-111111111111",
		"session-1",
		"legacy_session.2024",
	} {
		t.Run(raw, func(t *testing.T) {
			var target ChatSettingsReadTarget
			payload := []byte(`{"kind":"session","session_id":` + mustJSONChatSettings(t, raw) + `}`)
			if err := json.Unmarshal(payload, &target); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			sessionID, ok := target.SessionID()
			if !ok || sessionID.String() != raw {
				t.Fatalf("session ID = %q, %t; want %q", sessionID.String(), ok, raw)
			}
		})
	}
}

func TestChatSettingsReadRequestRoundTripsStrictTarget(t *testing.T) {
	request := ChatSettingsReadRequest{
		Target: LazyChatSettingsTarget("project-1", "workspace-1"),
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded ChatSettingsReadRequest
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for _, raw := range []string{
		`{}`,
		`{"target":{"kind":"lazy","project_id":"project-1","workspace_id":"workspace-1"},"unknown":true}`,
	} {
		var invalid ChatSettingsReadRequest
		if err := json.Unmarshal([]byte(raw), &invalid); err == nil {
			t.Fatalf("Unmarshal(%s) unexpectedly succeeded", raw)
		}
	}
}

func TestChatSettingsReadTargetRejectsUnsafeSessionIdentifiers(t *testing.T) {
	for _, raw := range []string{
		"",
		" session-1",
		"session-1 ",
		"/absolute",
		`path\segment`,
		"path/segment",
		".",
		"..",
		"../escape",
		"11111111-1111-1111-8111-111111111111",
	} {
		t.Run(raw, func(t *testing.T) {
			var target ChatSettingsReadTarget
			payload := []byte(`{"kind":"session","session_id":` + mustJSONChatSettings(t, raw) + `}`)
			if err := json.Unmarshal(payload, &target); err == nil {
				t.Fatalf("Unmarshal unexpectedly succeeded: %+v", target)
			}
		})
	}
}

func TestChatSettingsReadResponseRoundTripsCompleteContract(t *testing.T) {
	sessionID, err := runtimeids.ParseSessionID("legacy_session.2024")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	previousID, err := runtimeids.ParseSessionID("session-1")
	if err != nil {
		t.Fatalf("ParseSessionID previous: %v", err)
	}
	response := ChatSettingsReadResponse{
		Settings: ChatSettings{
			SelectedAgent: ChatSettingsAgentSummary{
				Role:     "default",
				Model:    "gpt-5",
				Thinking: "high",
			},
			AgentChoices: []ChatSettingsAgentChoice{
				{
					Role:               "default",
					Model:              "gpt-5",
					Thinking:           "high",
					Tools:              []string{"ask_question", "exec_command"},
					CustomSystemPrompt: false,
					CustomCapabilities: false,
					AgentCallable:      true,
				},
				{
					Role:               "reviewer",
					Model:              "gpt-5.1",
					Thinking:           "medium",
					Tools:              []string{"exec_command"},
					CustomSystemPrompt: true,
					CustomCapabilities: true,
					AgentCallable:      false,
				},
			},
			AgentEditability: ChatSettingsWorkflowLock,
			Supervisor: ChatSettingsSupervisor{
				Value:       ChatSettingsSupervisorAfterEdits,
				Editability: ChatSettingsEditable,
			},
			Thinking: &ChatSettingsThinking{
				Kind:          ChatSettingsThinkingEnumerated,
				Value:         "high",
				BaselineValue: "high",
				Values:        []string{"low", "medium", "high"},
				Editability:   ChatSettingsEditable,
			},
			Fast: &ChatSettingsFast{
				Value:       false,
				Editability: ChatSettingsEditable,
			},
			Questions: ChatSettingsQuestions{
				Capable:     true,
				Enabled:     false,
				Editability: ChatSettingsEditable,
			},
			AutoCompaction: ChatSettingsAutoCompaction{
				Policy:      ChatSettingsAutoCompactionRequired,
				Stored:      false,
				Effective:   true,
				Editability: ChatSettingsWorkflowLock,
			},
			AgentLocked:    true,
			WorkflowLocked: true,
			CachingLocked:  false,
		},
		Session: &ChatSettingsSessionFacts{
			SessionID:         sessionID,
			PreviousSessionID: &previousID,
			TaskID:            chatSettingsStringPtr("task-11111111-1111-4111-8111-111111111111"),
		},
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded ChatSettingsReadResponse
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("Validate decoded: %v", err)
	}
	roundTrip, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("Marshal decoded: %v", err)
	}
	if !bytes.Equal(roundTrip, encoded) {
		t.Fatalf("round trip = %s, want %s", roundTrip, encoded)
	}
	if !bytes.Contains(encoded, []byte(`"value":false`)) {
		t.Fatalf("explicit false was not preserved: %s", encoded)
	}
}

func chatSettingsStringPtr(value string) *string {
	return &value
}

func TestChatSettingsReadResponseRejectsInvalidContracts(t *testing.T) {
	valid := validChatSettingsReadResponse(t)
	tests := []struct {
		name   string
		mutate func(*ChatSettingsReadResponse)
	}{
		{
			name: "unknown Agent editability",
			mutate: func(response *ChatSettingsReadResponse) {
				response.Settings.AgentEditability = "agent_lock"
			},
		},
		{
			name: "contradictory Agent locks",
			mutate: func(response *ChatSettingsReadResponse) {
				response.Settings.AgentLocked = true
			},
		},
		{
			name: "workflow blocker loses precedence",
			mutate: func(response *ChatSettingsReadResponse) {
				response.Settings.AgentLocked = true
				response.Settings.WorkflowLocked = true
				response.Settings.CachingLocked = true
				response.Settings.AgentEditability = ChatSettingsCachingLock
				response.Settings.AutoCompaction.Policy = ChatSettingsAutoCompactionRequired
				response.Settings.AutoCompaction.Effective = true
				response.Settings.AutoCompaction.Editability = ChatSettingsWorkflowLock
			},
		},
		{
			name: "selected unlocked Agent absent from choices",
			mutate: func(response *ChatSettingsReadResponse) {
				response.Settings.SelectedAgent.Role = "missing"
			},
		},
		{
			name: "selected caching locked historical Agent may be absent",
			mutate: func(response *ChatSettingsReadResponse) {
				response.Settings.SelectedAgent.Role = "historical"
				response.Settings.AgentLocked = true
				response.Settings.CachingLocked = true
				response.Settings.AgentEditability = ChatSettingsCachingLock
			},
		},
		{
			name: "duplicate choices",
			mutate: func(response *ChatSettingsReadResponse) {
				response.Settings.AgentChoices = append(
					response.Settings.AgentChoices,
					response.Settings.AgentChoices[0],
				)
			},
		},
		{
			name: "unsorted choices",
			mutate: func(response *ChatSettingsReadResponse) {
				response.Settings.AgentChoices = []ChatSettingsAgentChoice{
					response.Settings.AgentChoices[0],
					{Role: "zeta", Model: "gpt-5", Thinking: "medium", Tools: []string{}},
					{Role: "alpha", Model: "gpt-5", Thinking: "medium", Tools: []string{}},
				}
			},
		},
		{
			name: "unsorted tools",
			mutate: func(response *ChatSettingsReadResponse) {
				response.Settings.AgentChoices[0].Tools = []string{"exec_command", "ask_question"}
			},
		},
		{
			name: "enumerated current absent",
			mutate: func(response *ChatSettingsReadResponse) {
				response.Settings.Thinking.Value = "ultra"
			},
		},
		{
			name: "custom Thinking contains values",
			mutate: func(response *ChatSettingsReadResponse) {
				response.Settings.Thinking.Kind = ChatSettingsThinkingCustom
			},
		},
		{
			name: "unsupported editability blocker",
			mutate: func(response *ChatSettingsReadResponse) {
				response.Settings.Fast.Editability = ChatSettingsCachingLock
			},
		},
		{
			name: "disabled compaction not forced off",
			mutate: func(response *ChatSettingsReadResponse) {
				response.Settings.AutoCompaction = ChatSettingsAutoCompaction{
					Policy:      ChatSettingsAutoCompactionDisabled,
					Stored:      true,
					Effective:   true,
					Editability: ChatSettingsPolicyDisabled,
				}
			},
		},
		{
			name: "required compaction not workflow blocked",
			mutate: func(response *ChatSettingsReadResponse) {
				response.Settings.AgentLocked = true
				response.Settings.WorkflowLocked = true
				response.Settings.AgentEditability = ChatSettingsWorkflowLock
				response.Settings.AutoCompaction = ChatSettingsAutoCompaction{
					Policy:      ChatSettingsAutoCompactionRequired,
					Stored:      false,
					Effective:   true,
					Editability: ChatSettingsEditable,
				}
			},
		},
		{
			name: "malformed Task ID",
			mutate: func(response *ChatSettingsReadResponse) {
				response.Session.TaskID = chatSettingsStringPtr("11111111-1111-4111-8111-111111111111")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := valid
			response.Settings.AgentChoices = append([]ChatSettingsAgentChoice(nil), valid.Settings.AgentChoices...)
			session := *valid.Session
			response.Session = &session
			test.mutate(&response)
			err := response.Validate()
			if test.name == "selected caching locked historical Agent may be absent" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate unexpectedly succeeded: %+v", response)
			}
		})
	}
}

func TestChatSettingsReadResponsePreservesStructuralAbsence(t *testing.T) {
	response := validChatSettingsReadResponse(t)
	response.Session = nil
	response.Settings.Thinking = nil
	response.Settings.Fast = nil
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("Unmarshal top level: %v", err)
	}
	if _, ok := wire["session"]; ok {
		t.Fatalf("session unexpectedly present in %s", encoded)
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(wire["settings"], &settings); err != nil {
		t.Fatalf("Unmarshal settings: %v", err)
	}
	for _, absent := range []string{"thinking", "fast"} {
		if _, ok := settings[absent]; ok {
			t.Fatalf("%s unexpectedly present in %s", absent, encoded)
		}
	}
	var decoded ChatSettingsReadResponse
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Session != nil || decoded.Settings.Thinking != nil || decoded.Settings.Fast != nil {
		t.Fatalf("structural absence was not preserved: %+v", decoded)
	}
}

func TestChatSettingsReadResponseValidatesFactsForTarget(t *testing.T) {
	response := validChatSettingsReadResponse(t)
	if err := response.ValidateForTarget(LazyChatSettingsTarget("project-1", "workspace-1")); err == nil {
		t.Fatal("lazy response accepted materialized Session facts")
	}
	response.Session = nil
	if err := response.ValidateForTarget(LazyChatSettingsTarget("project-1", "workspace-1")); err != nil {
		t.Fatalf("lazy response: %v", err)
	}

	sessionID, err := runtimeids.ParseSessionID("session-1")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	sessionTarget := SessionChatSettingsTarget(sessionID)
	if err := response.ValidateForTarget(sessionTarget); err == nil {
		t.Fatal("materialized response accepted absent Session facts")
	}
	response.Session = &ChatSettingsSessionFacts{SessionID: sessionID}
	if err := response.ValidateForTarget(sessionTarget); err != nil {
		t.Fatalf("materialized response: %v", err)
	}
	if err := response.ValidateForTarget(SessionChatSettingsTarget(mustChatSettingsSessionID(t, "session-2"))); err == nil {
		t.Fatal("materialized response accepted a different Session ID")
	}
}

func TestChatSettingsThinkingCustomModeRoundTripsExactValues(t *testing.T) {
	response := validChatSettingsReadResponse(t)
	response.Settings.Thinking = &ChatSettingsThinking{
		Kind:          ChatSettingsThinkingCustom,
		Value:         "provider-specific",
		BaselineValue: "provider-default",
		Editability:   ChatSettingsEditable,
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded ChatSettingsReadResponse
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Settings.Thinking == nil ||
		decoded.Settings.Thinking.Value != "provider-specific" ||
		decoded.Settings.Thinking.BaselineValue != "provider-default" ||
		decoded.Settings.Thinking.Values != nil {
		t.Fatalf("decoded custom Thinking = %+v", decoded.Settings.Thinking)
	}
}

func TestChatSettingsBlockerMatrix(t *testing.T) {
	tests := []struct {
		name                  string
		workflowLocked        bool
		cachingLocked         bool
		agentEditability      ChatSettingsEditability
		compactionPolicy      ChatSettingsAutoCompactionPolicy
		compactionStored      bool
		compactionEffective   bool
		compactionEditability ChatSettingsEditability
	}{
		{
			name:                  "editable",
			agentEditability:      ChatSettingsEditable,
			compactionPolicy:      ChatSettingsAutoCompactionOptional,
			compactionStored:      false,
			compactionEffective:   false,
			compactionEditability: ChatSettingsEditable,
		},
		{
			name:                  "workflow lock",
			workflowLocked:        true,
			agentEditability:      ChatSettingsWorkflowLock,
			compactionPolicy:      ChatSettingsAutoCompactionRequired,
			compactionStored:      false,
			compactionEffective:   true,
			compactionEditability: ChatSettingsWorkflowLock,
		},
		{
			name:                  "caching lock",
			cachingLocked:         true,
			agentEditability:      ChatSettingsCachingLock,
			compactionPolicy:      ChatSettingsAutoCompactionOptional,
			compactionStored:      true,
			compactionEffective:   true,
			compactionEditability: ChatSettingsEditable,
		},
		{
			name:                  "workflow wins Agent precedence",
			workflowLocked:        true,
			cachingLocked:         true,
			agentEditability:      ChatSettingsWorkflowLock,
			compactionPolicy:      ChatSettingsAutoCompactionRequired,
			compactionStored:      true,
			compactionEffective:   true,
			compactionEditability: ChatSettingsWorkflowLock,
		},
		{
			name:                  "disabled policy wins workflow requirement",
			workflowLocked:        true,
			agentEditability:      ChatSettingsWorkflowLock,
			compactionPolicy:      ChatSettingsAutoCompactionDisabled,
			compactionStored:      true,
			compactionEffective:   false,
			compactionEditability: ChatSettingsPolicyDisabled,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := validChatSettingsReadResponse(t)
			response.Settings.WorkflowLocked = test.workflowLocked
			response.Settings.CachingLocked = test.cachingLocked
			response.Settings.AgentLocked = test.workflowLocked || test.cachingLocked
			response.Settings.AgentEditability = test.agentEditability
			response.Settings.AutoCompaction = ChatSettingsAutoCompaction{
				Policy:      test.compactionPolicy,
				Stored:      test.compactionStored,
				Effective:   test.compactionEffective,
				Editability: test.compactionEditability,
			}
			if err := response.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if response.Settings.Supervisor.Editability != ChatSettingsEditable ||
				response.Settings.Thinking.Editability != ChatSettingsEditable ||
				response.Settings.Fast.Editability != ChatSettingsEditable ||
				response.Settings.Questions.Editability != ChatSettingsEditable {
				t.Fatal("lock leaked to an independently editable setting")
			}
		})
	}
}

func TestChatSettingsReadResponseRejectsUnknownNestedFieldsAndEnums(t *testing.T) {
	for _, raw := range []string{
		`{"settings":{"selected_agent":{"role":"default","model":"gpt-5","thinking":"medium","label":"Default"},"agent_choices":[{"role":"default","model":"gpt-5","thinking":"medium","tools":[],"custom_system_prompt":false,"custom_capabilities":false,"agent_callable":true}],"agent_editability":"editable","supervisor":{"value":"off","editability":"editable"},"questions":{"capable":false,"enabled":true,"editability":"editable"},"auto_compaction":{"policy":"optional","stored":true,"effective":true,"editability":"editable"},"agent_locked":false,"workflow_locked":false,"caching_locked":false}}`,
		`{"settings":{"selected_agent":{"role":"default","model":"gpt-5","thinking":"medium"},"agent_choices":[{"role":"default","model":"gpt-5","thinking":"medium","tools":[],"custom_system_prompt":false,"custom_capabilities":false,"agent_callable":true}],"agent_editability":"editable","supervisor":{"value":"sometimes","editability":"editable"},"questions":{"capable":false,"enabled":true,"editability":"editable"},"auto_compaction":{"policy":"optional","stored":true,"effective":true,"editability":"editable"},"agent_locked":false,"workflow_locked":false,"caching_locked":false}}`,
		`{"settings":{"selected_agent":{"role":"default","model":"gpt-5","thinking":"medium"},"agent_choices":[{"role":"default","model":"gpt-5","thinking":"medium","tools":[],"custom_system_prompt":false,"custom_capabilities":false,"agent_callable":true}],"agent_editability":"editable","supervisor":{"value":"off","editability":"editable"},"thinking":{"kind":"future","value":"medium","baseline_value":"medium","editability":"editable"},"questions":{"capable":false,"enabled":true,"editability":"editable"},"auto_compaction":{"policy":"optional","stored":true,"effective":true,"editability":"editable"},"agent_locked":false,"workflow_locked":false,"caching_locked":false}}`,
		`{"settings":{"selected_agent":{"role":"default","model":"gpt-5","thinking":"medium"},"agent_choices":[{"role":"default","model":"gpt-5","thinking":"medium","tools":[],"custom_system_prompt":false,"custom_capabilities":false,"agent_callable":true}],"agent_editability":"editable","supervisor":{"value":"off","editability":"editable"},"questions":{"capable":false,"enabled":true,"editability":"editable"},"auto_compaction":{"policy":"future","stored":true,"effective":true,"editability":"editable"},"agent_locked":false,"workflow_locked":false,"caching_locked":false}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			var response ChatSettingsReadResponse
			if err := json.Unmarshal([]byte(raw), &response); err == nil {
				t.Fatalf("Unmarshal unexpectedly succeeded: %+v", response)
			}
		})
	}
}

func TestChatSettingsAgentPreparationErrorRoundTripsCategories(t *testing.T) {
	for _, category := range []ChatSettingsAgentPreparationCategory{
		ChatSettingsAgentInvalidConfiguration,
		ChatSettingsAgentProviderUnavailable,
		ChatSettingsAgentInternalPreparation,
	} {
		t.Run(string(category), func(t *testing.T) {
			source := &ChatSettingsAgentPreparationError{
				Agent:    "reviewer",
				Category: category,
			}
			if source.RPCErrorCode() != protocol.ErrCodeChatSettingsAgentPreparation {
				t.Fatalf("RPCErrorCode = %d", source.RPCErrorCode())
			}
			decodedErr := DecodeChatSettingsAgentPreparationError(
				source.RPCErrorData(),
				"diagnostic detail",
			)
			var decoded *ChatSettingsAgentPreparationError
			if !errors.As(decodedErr, &decoded) {
				t.Fatalf("decoded error = %T %v", decodedErr, decodedErr)
			}
			if decoded.Agent != source.Agent || decoded.Category != source.Category {
				t.Fatalf("decoded = %+v, want %+v", decoded, source)
			}
		})
	}
}

func TestChatSettingsAgentPreparationErrorRejectsInvalidFacts(t *testing.T) {
	for _, source := range []*ChatSettingsAgentPreparationError{
		nil,
		{Agent: "", Category: ChatSettingsAgentInvalidConfiguration},
		{Agent: " reviewer ", Category: ChatSettingsAgentInvalidConfiguration},
		{Agent: "reviewer", Category: "future"},
	} {
		if source != nil && source.Validate() == nil {
			t.Fatalf("Validate unexpectedly succeeded: %+v", source)
		}
	}
	decoded := DecodeChatSettingsAgentPreparationError(
		json.RawMessage(`{"type":"chat_settings_agent_preparation","agent":"reviewer","category":"future"}`),
		"diagnostic detail",
	)
	var typed *ChatSettingsAgentPreparationError
	if errors.As(decoded, &typed) {
		t.Fatalf("invalid payload decoded as typed error: %+v", typed)
	}
}

func validChatSettingsReadResponse(t *testing.T) ChatSettingsReadResponse {
	t.Helper()
	sessionID, err := runtimeids.ParseSessionID("legacy_session.2024")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	return ChatSettingsReadResponse{
		Settings: ChatSettings{
			SelectedAgent: ChatSettingsAgentSummary{
				Role:     "default",
				Model:    "gpt-5",
				Thinking: "medium",
			},
			AgentChoices: []ChatSettingsAgentChoice{
				{
					Role:          "default",
					Model:         "gpt-5",
					Thinking:      "medium",
					Tools:         []string{"ask_question", "exec_command"},
					AgentCallable: true,
				},
			},
			AgentEditability: ChatSettingsEditable,
			Supervisor: ChatSettingsSupervisor{
				Value:       ChatSettingsSupervisorOff,
				Editability: ChatSettingsEditable,
			},
			Thinking: &ChatSettingsThinking{
				Kind:          ChatSettingsThinkingEnumerated,
				Value:         "medium",
				BaselineValue: "medium",
				Values:        []string{"low", "medium", "high"},
				Editability:   ChatSettingsEditable,
			},
			Fast: &ChatSettingsFast{
				Value:       false,
				Editability: ChatSettingsEditable,
			},
			Questions: ChatSettingsQuestions{
				Capable:     true,
				Enabled:     false,
				Editability: ChatSettingsEditable,
			},
			AutoCompaction: ChatSettingsAutoCompaction{
				Policy:      ChatSettingsAutoCompactionOptional,
				Stored:      true,
				Effective:   true,
				Editability: ChatSettingsEditable,
			},
		},
		Session: &ChatSettingsSessionFacts{
			SessionID: sessionID,
			TaskID:    chatSettingsStringPtr("task-legacy"),
		},
	}
}

func mustJSONChatSettings(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return string(encoded)
}

func mustChatSettingsSessionID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q): %v", raw, err)
	}
	return id
}
