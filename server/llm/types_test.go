package llm

import (
	"encoding/json"
	"testing"

	"core/server/session"
)

func TestRequestFromLockedContract_UsesBinaryPromptAndExplicitTools(t *testing.T) {
	locked := session.LockedContract{
		Model:          "gpt-5",
		Temperature:    1,
		MaxOutputToken: 0,
	}
	tool := Tool{Name: "shell", Schema: []byte(`{"type":"object"}`)}

	req, err := RequestFromLockedContract(locked, "sys", []ResponseItem{{Type: ResponseItemTypeMessage, Role: RoleUser, Content: "hi"}}, []Tool{tool})
	if err != nil {
		t.Fatalf("request from contract: %v", err)
	}
	if req.SystemPrompt != "sys" {
		t.Fatalf("system prompt mismatch: %q", req.SystemPrompt)
	}
	if req.ReasoningEffort != "" {
		t.Fatalf("reasoning effort mismatch: %q", req.ReasoningEffort)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "shell" {
		t.Fatalf("tools mismatch: %+v", req.Tools)
	}
}

func TestRequestFromLockedContract_RespectsExplicitToolDisable(t *testing.T) {
	locked := session.LockedContract{
		Model:          "gpt-5",
		Temperature:    1,
		MaxOutputToken: 0,
	}
	req, err := RequestFromLockedContract(locked, "sys", []ResponseItem{{Type: ResponseItemTypeMessage, Role: RoleUser, Content: "hi"}}, []Tool{})
	if err != nil {
		t.Fatalf("request from contract: %v", err)
	}
	if len(req.Tools) != 0 {
		t.Fatalf("expected tools disabled, got %+v", req.Tools)
	}
}

func TestMessagesFromItems_PreservesAssistantPhase(t *testing.T) {
	items := []ResponseItem{
		{
			Type:    ResponseItemTypeMessage,
			Role:    RoleAssistant,
			Phase:   MessagePhaseCommentary,
			Content: "progress",
		},
	}
	msgs := MessagesFromItems(items)
	if len(msgs) != 1 {
		t.Fatalf("expected one message, got %d", len(msgs))
	}
	if msgs[0].Phase != MessagePhaseCommentary {
		t.Fatalf("expected commentary phase, got %q", msgs[0].Phase)
	}
}

func TestCustomToolCallItemsRoundTripThroughMessages(t *testing.T) {
	patchInput := "*** Begin Patch\n*** Add File: a.txt\n+hi\n*** End Patch\n"
	items := []ResponseItem{
		{Type: ResponseItemTypeCustomToolCall, ID: "ct_1", CallID: "call_1", Name: "patch", CustomInput: patchInput},
		{Type: ResponseItemTypeCustomToolOutput, CallID: "call_1", Name: "patch", Output: json.RawMessage(`{"ok":true}`)},
	}

	msgs := MessagesFromItems(items)
	if len(msgs) != 2 {
		t.Fatalf("expected assistant and tool messages, got %+v", msgs)
	}
	if len(msgs[0].ToolCalls) != 1 || !msgs[0].ToolCalls[0].Custom || msgs[0].ToolCalls[0].CustomInput != patchInput {
		t.Fatalf("unexpected custom tool call message: %+v", msgs[0])
	}
	if msgs[1].MessageType != MessageTypeCustomToolCallOutput || msgs[1].ToolCallID != "call_1" {
		t.Fatalf("unexpected custom tool output message: %+v", msgs[1])
	}

	roundTrip := ItemsFromMessages(msgs)
	if len(roundTrip) != 2 {
		t.Fatalf("expected two round-trip items, got %+v", roundTrip)
	}
	if roundTrip[0].Type != ResponseItemTypeCustomToolCall || roundTrip[0].CustomInput != patchInput {
		t.Fatalf("unexpected round-trip custom call item: %+v", roundTrip[0])
	}
	if roundTrip[1].Type != ResponseItemTypeCustomToolOutput || string(roundTrip[1].Output) != `{"ok":true}` {
		t.Fatalf("unexpected round-trip custom output item: %+v", roundTrip[1])
	}
}

func TestBackgroundExitCodeRoundTripsThroughResponseItems(t *testing.T) {
	exitCode := 5
	items := ItemsFromMessages([]Message{{
		Role:               RoleDeveloper,
		MessageType:        MessageTypeBackgroundNotice,
		Content:            "background failed",
		BackgroundExitCode: &exitCode,
	}})
	if len(items) != 1 || items[0].BackgroundExitCode == nil || *items[0].BackgroundExitCode != exitCode {
		t.Fatalf("response items = %+v, want typed background exit code", items)
	}

	messages := MessagesFromItems(items)
	if len(messages) != 1 || messages[0].BackgroundExitCode == nil || *messages[0].BackgroundExitCode != exitCode {
		t.Fatalf("messages = %+v, want typed background exit code", messages)
	}
}

func TestMessagesFromItemsStartsNewAssistantAfterFunctionToolOutput(t *testing.T) {
	items := []ResponseItem{
		{Type: ResponseItemTypeFunctionCall, ID: "fc_1", CallID: "call_1", Name: "shell", Arguments: json.RawMessage(`{"cmd":"pwd"}`)},
		{Type: ResponseItemTypeFunctionCallOutput, CallID: "call_1", Name: "shell", Output: json.RawMessage(`{"output":"/tmp"}`)},
		{Type: ResponseItemTypeReasoning, ID: "rs_1", EncryptedContent: "enc_1"},
	}

	msgs := MessagesFromItems(items)
	if len(msgs) != 3 {
		t.Fatalf("expected assistant, tool, assistant messages, got %+v", msgs)
	}
	if len(msgs[0].ToolCalls) != 1 {
		t.Fatalf("expected first assistant to contain tool call, got %+v", msgs[0])
	}
	if msgs[1].Role != RoleTool || msgs[1].ToolCallID != "call_1" {
		t.Fatalf("expected tool output message, got %+v", msgs[1])
	}
	if msgs[2].Role != RoleAssistant || len(msgs[2].ReasoningItems) != 1 {
		t.Fatalf("expected reasoning on new assistant message, got %+v", msgs[2])
	}
}

func TestMessagesFromItems_PreservesMessageType(t *testing.T) {
	items := []ResponseItem{
		{
			Type:        ResponseItemTypeMessage,
			Role:        RoleDeveloper,
			MessageType: MessageTypeEnvironment,
			Content:     "env",
		},
	}
	msgs := MessagesFromItems(items)
	if len(msgs) != 1 {
		t.Fatalf("expected one message, got %d", len(msgs))
	}
	if msgs[0].MessageType != MessageTypeEnvironment {
		t.Fatalf("expected message type to round-trip, got %q", msgs[0].MessageType)
	}
	roundTrip := ItemsFromMessages(msgs)
	if len(roundTrip) != 1 {
		t.Fatalf("expected one round-trip item, got %d", len(roundTrip))
	}
	if roundTrip[0].MessageType != MessageTypeEnvironment {
		t.Fatalf("expected round-trip item message type, got %q", roundTrip[0].MessageType)
	}
}

func TestMessagesFromItems_PreservesSkillsMessageType(t *testing.T) {
	items := []ResponseItem{
		{
			Type:        ResponseItemTypeMessage,
			Role:        RoleDeveloper,
			MessageType: MessageTypeSkills,
			Content:     "## Skills\n### Available skills",
		},
	}
	msgs := MessagesFromItems(items)
	if len(msgs) != 1 {
		t.Fatalf("expected one message, got %d", len(msgs))
	}
	if msgs[0].MessageType != MessageTypeSkills {
		t.Fatalf("expected message type to round-trip, got %q", msgs[0].MessageType)
	}
	roundTrip := ItemsFromMessages(msgs)
	if len(roundTrip) != 1 {
		t.Fatalf("expected one round-trip item, got %d", len(roundTrip))
	}
	if roundTrip[0].MessageType != MessageTypeSkills {
		t.Fatalf("expected round-trip item message type, got %q", roundTrip[0].MessageType)
	}
}

func TestMessagesFromItems_PreservesHeadlessExitMessageType(t *testing.T) {
	items := []ResponseItem{
		{
			Type:        ResponseItemTypeMessage,
			Role:        RoleDeveloper,
			MessageType: MessageTypeHeadlessModeExit,
			Content:     "interactive mode instructions",
		},
	}
	msgs := MessagesFromItems(items)
	if len(msgs) != 1 {
		t.Fatalf("expected one message, got %d", len(msgs))
	}
	if msgs[0].MessageType != MessageTypeHeadlessModeExit {
		t.Fatalf("expected message type to round-trip, got %q", msgs[0].MessageType)
	}
	roundTrip := ItemsFromMessages(msgs)
	if len(roundTrip) != 1 {
		t.Fatalf("expected one round-trip item, got %d", len(roundTrip))
	}
	if roundTrip[0].MessageType != MessageTypeHeadlessModeExit {
		t.Fatalf("expected round-trip item message type, got %q", roundTrip[0].MessageType)
	}
}

func TestMessagesFromItems_PreservesWorktreeExitMessageType(t *testing.T) {
	items := []ResponseItem{
		{
			Type:        ResponseItemTypeMessage,
			Role:        RoleDeveloper,
			MessageType: MessageTypeWorktreeModeExit,
			Content:     "returned to main workspace",
		},
	}
	msgs := MessagesFromItems(items)
	if len(msgs) != 1 {
		t.Fatalf("expected one message, got %d", len(msgs))
	}
	if msgs[0].MessageType != MessageTypeWorktreeModeExit {
		t.Fatalf("expected message type to round-trip, got %q", msgs[0].MessageType)
	}
	roundTrip := ItemsFromMessages(msgs)
	if len(roundTrip) != 1 {
		t.Fatalf("expected one round-trip item, got %d", len(roundTrip))
	}
	if roundTrip[0].MessageType != MessageTypeWorktreeModeExit {
		t.Fatalf("expected round-trip item message type, got %q", roundTrip[0].MessageType)
	}
}

func TestUsageCacheHitPercent(t *testing.T) {
	usage := Usage{InputTokens: 200, CachedInputTokens: 50, HasCachedInputTokens: true}
	pct, ok := usage.CacheHitPercent()
	if !ok {
		t.Fatal("expected cache hit percentage to be available")
	}
	if pct != 25 {
		t.Fatalf("cache hit percent=%d, want 25", pct)
	}

	unknown := Usage{InputTokens: 200}
	if pct, ok := unknown.CacheHitPercent(); ok || pct != 0 {
		t.Fatalf("expected unknown cache hit percentage, got pct=%d ok=%t", pct, ok)
	}
}
