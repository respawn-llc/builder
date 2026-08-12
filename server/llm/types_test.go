package llm

import (
	"encoding/json"
	"errors"
	"testing"

	"core/server/session"
	"core/shared/textutil"
)

func messageContent(message Message) string {
	if message.Content == nil {
		panic("test expected message content to be present")
	}
	return *message.Content
}

func TestRequestValidateRejectsMissingToolChoiceMode(t *testing.T) {
	err := (Request{Model: "gpt-5"}).Validate()
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Validate() error = %v, want ErrInvalidRequest", err)
	}
}

func TestRequestValidateRejectsUnknownToolChoiceMode(t *testing.T) {
	err := (Request{Model: "gpt-5", ToolChoiceMode: ToolChoiceMode("sometimes")}).Validate()
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Validate() error = %v, want ErrInvalidRequest", err)
	}
}

func TestRequestValidateAcceptsRequiredToolChoiceWithLocalTool(t *testing.T) {
	err := (Request{
		Model:          "gpt-5",
		ToolChoiceMode: ToolChoiceModeRequired,
		Tools:          []Tool{{Name: "shell", Schema: mustTestFunctionSchema(t, struct{}{})}},
	}).Validate()
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestRequestValidateRejectsUnpreparedFunctionSchema(t *testing.T) {
	err := (Request{
		Model:          "gpt-5",
		ToolChoiceMode: ToolChoiceModeAutomatic,
		Tools:          []Tool{{Name: "shell"}},
	}).Validate()
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Validate() error = %v, want ErrInvalidRequest", err)
	}
}

func TestRequestValidateRejectsUnpreparedStructuredOutputSchema(t *testing.T) {
	err := (Request{
		Model:            "gpt-5",
		ToolChoiceMode:   ToolChoiceModeAutomatic,
		StructuredOutput: &StructuredOutput{Name: "reviewer_suggestions"},
	}).Validate()
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Validate() error = %v, want ErrInvalidRequest", err)
	}
}

func TestRequestValidateAcceptsRequiredToolChoiceWithHostedWebSearchOnly(t *testing.T) {
	err := (Request{
		Model:                 "gpt-5",
		ToolChoiceMode:        ToolChoiceModeRequired,
		EnableNativeWebSearch: true,
	}).Validate()
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestRequestValidateRejectsRequiredToolChoiceWithoutAdvertisedTools(t *testing.T) {
	err := (Request{Model: "gpt-5", ToolChoiceMode: ToolChoiceModeRequired}).Validate()
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Validate() error = %v, want ErrInvalidRequest", err)
	}
}

func TestRequestFromLockedContract_UsesBinaryPromptAndExplicitTools(t *testing.T) {
	locked := session.LockedContract{
		Model:          "gpt-5",
		Temperature:    1,
		MaxOutputToken: 0,
	}
	tool := Tool{Name: "shell", Schema: mustTestFunctionSchema(t, struct{}{})}

	req, err := RequestFromLockedContract(locked, "sys", []ResponseItem{{Type: ResponseItemTypeMessage, Role: textutil.Value(RoleUser), Content: textutil.Value("hi")}}, []Tool{tool}, ToolControls{ChoiceMode: ToolChoiceModeAutomatic})
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
	if req.ToolChoiceMode != ToolChoiceModeAutomatic {
		t.Fatalf("tool choice mode = %q, want automatic", req.ToolChoiceMode)
	}
}

func TestRequestFromLockedContract_RespectsExplicitToolDisable(t *testing.T) {
	locked := session.LockedContract{
		Model:          "gpt-5",
		Temperature:    1,
		MaxOutputToken: 0,
	}
	req, err := RequestFromLockedContract(locked, "sys", []ResponseItem{{Type: ResponseItemTypeMessage, Role: textutil.Value(RoleUser), Content: textutil.Value("hi")}}, []Tool{}, ToolControls{ChoiceMode: ToolChoiceModeAutomatic})
	if err != nil {
		t.Fatalf("request from contract: %v", err)
	}
	if len(req.Tools) != 0 {
		t.Fatalf("expected tools disabled, got %+v", req.Tools)
	}
}

func TestRequestFromLockedContractValidatesHostedToolsAfterControlsAreApplied(t *testing.T) {
	locked := session.LockedContract{Model: "gpt-5"}
	req, err := RequestFromLockedContract(locked, "sys", nil, nil, ToolControls{
		ChoiceMode:            ToolChoiceModeRequired,
		EnableNativeWebSearch: true,
	})
	if err != nil {
		t.Fatalf("request from contract: %v", err)
	}
	if !req.EnableNativeWebSearch || req.ToolChoiceMode != ToolChoiceModeRequired {
		t.Fatalf("request controls = web_search:%t mode:%q", req.EnableNativeWebSearch, req.ToolChoiceMode)
	}
}

func TestMessagesFromItems_PreservesAssistantPhase(t *testing.T) {
	items := []ResponseItem{
		{
			Type:    ResponseItemTypeMessage,
			Role:    textutil.Value(RoleAssistant),
			Phase:   textutil.Value(MessagePhaseCommentary),
			Content: textutil.Value("progress"),
		},
	}
	msgs := MessagesFromItems(items)
	if len(msgs) != 1 {
		t.Fatalf("expected one message, got %d", len(msgs))
	}
	if msgs[0].Phase == nil || *msgs[0].Phase != MessagePhaseCommentary {
		t.Fatalf("expected commentary phase, got %v", msgs[0].Phase)
	}
}

func TestCustomToolCallItemsRoundTripThroughMessages(t *testing.T) {
	patchInput := "*** Begin Patch\n*** Add File: a.txt\n+hi\n*** End Patch\n"
	items := []ResponseItem{
		{Type: ResponseItemTypeCustomToolCall, ID: textutil.Value("ct_1"), CallID: textutil.Value("call_1"), Name: textutil.Value("patch"), CustomInput: textutil.Value(patchInput)},
		{Type: ResponseItemTypeCustomToolOutput, CallID: textutil.Value("call_1"), Name: textutil.Value("patch"), Output: json.RawMessage(`{"ok":true}`)},
	}

	msgs := MessagesFromItems(items)
	if len(msgs) != 2 {
		t.Fatalf("expected assistant and tool messages, got %+v", msgs)
	}
	if len(msgs[0].ToolCalls) != 1 || !msgs[0].ToolCalls[0].Custom || msgs[0].ToolCalls[0].CustomInput == nil || *msgs[0].ToolCalls[0].CustomInput != patchInput {
		t.Fatalf("unexpected custom tool call message: %+v", msgs[0])
	}
	if msgs[1].MessageType == nil || *msgs[1].MessageType != MessageTypeCustomToolCallOutput || msgs[1].ToolCallID == nil || *msgs[1].ToolCallID != "call_1" {
		t.Fatalf("unexpected custom tool output message: %+v", msgs[1])
	}

	roundTrip := ItemsFromMessages(msgs)
	if len(roundTrip) != 2 {
		t.Fatalf("expected two round-trip items, got %+v", roundTrip)
	}
	if roundTrip[0].Type != ResponseItemTypeCustomToolCall || roundTrip[0].CustomInput == nil || *roundTrip[0].CustomInput != patchInput {
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
		MessageType:        textutil.Value(MessageTypeBackgroundNotice),
		Content:            textutil.Value("background failed"),
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
		{Type: ResponseItemTypeFunctionCall, ID: textutil.Value("fc_1"), CallID: textutil.Value("call_1"), Name: textutil.Value("shell"), Arguments: json.RawMessage(`{"cmd":"pwd"}`)},
		{Type: ResponseItemTypeFunctionCallOutput, CallID: textutil.Value("call_1"), Name: textutil.Value("shell"), Output: json.RawMessage(`{"output":"/tmp"}`)},
		{Type: ResponseItemTypeReasoning, ID: textutil.Value("rs_1"), EncryptedContent: textutil.Value("enc_1")},
	}

	msgs := MessagesFromItems(items)
	if len(msgs) != 3 {
		t.Fatalf("expected assistant, tool, assistant messages, got %+v", msgs)
	}
	if len(msgs[0].ToolCalls) != 1 {
		t.Fatalf("expected first assistant to contain tool call, got %+v", msgs[0])
	}
	if msgs[1].Role != RoleTool || msgs[1].ToolCallID == nil || *msgs[1].ToolCallID != "call_1" {
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
			Role:        textutil.Value(RoleDeveloper),
			MessageType: textutil.Value(MessageTypeEnvironment),
			Content:     textutil.Value("env"),
		},
	}
	msgs := MessagesFromItems(items)
	if len(msgs) != 1 {
		t.Fatalf("expected one message, got %d", len(msgs))
	}
	if msgs[0].MessageType == nil || *msgs[0].MessageType != MessageTypeEnvironment {
		t.Fatalf("expected message type to round-trip, got %v", msgs[0].MessageType)
	}
	roundTrip := ItemsFromMessages(msgs)
	if len(roundTrip) != 1 {
		t.Fatalf("expected one round-trip item, got %d", len(roundTrip))
	}
	if roundTrip[0].MessageType == nil || *roundTrip[0].MessageType != MessageTypeEnvironment {
		t.Fatalf("expected round-trip item message type, got %v", roundTrip[0].MessageType)
	}
}

func TestMessagesFromItems_PreservesSkillsMessageType(t *testing.T) {
	items := []ResponseItem{
		{
			Type:        ResponseItemTypeMessage,
			Role:        textutil.Value(RoleDeveloper),
			MessageType: textutil.Value(MessageTypeSkills),
			Content:     textutil.Value("## Skills\n### Available skills"),
		},
	}
	msgs := MessagesFromItems(items)
	if len(msgs) != 1 {
		t.Fatalf("expected one message, got %d", len(msgs))
	}
	if msgs[0].MessageType == nil || *msgs[0].MessageType != MessageTypeSkills {
		t.Fatalf("expected message type to round-trip, got %v", msgs[0].MessageType)
	}
	roundTrip := ItemsFromMessages(msgs)
	if len(roundTrip) != 1 {
		t.Fatalf("expected one round-trip item, got %d", len(roundTrip))
	}
	if roundTrip[0].MessageType == nil || *roundTrip[0].MessageType != MessageTypeSkills {
		t.Fatalf("expected round-trip item message type, got %v", roundTrip[0].MessageType)
	}
}

func TestMessagesFromItems_PreservesActiveGoalContinuationMessageType(t *testing.T) {
	items := []ResponseItem{{
		Type:        ResponseItemTypeMessage,
		Role:        textutil.Value(RoleDeveloper),
		MessageType: textutil.Value(MessageTypeActiveGoalContinuation),
		Content:     textutil.Value("active-goal continuation"),
	}}
	messages := MessagesFromItems(items)
	if len(messages) != 1 || messages[0].MessageType == nil || *messages[0].MessageType != MessageTypeActiveGoalContinuation {
		t.Fatalf("messages = %+v, want active-goal continuation type", messages)
	}
	roundTrip := ItemsFromMessages(messages)
	if len(roundTrip) != 1 || roundTrip[0].MessageType == nil || *roundTrip[0].MessageType != MessageTypeActiveGoalContinuation {
		t.Fatalf("round-trip items = %+v, want active-goal continuation type", roundTrip)
	}
}

func TestMessagesFromItems_PreservesHeadlessExitMessageType(t *testing.T) {
	items := []ResponseItem{
		{
			Type:        ResponseItemTypeMessage,
			Role:        textutil.Value(RoleDeveloper),
			MessageType: textutil.Value(MessageTypeHeadlessModeExit),
			Content:     textutil.Value("interactive mode instructions"),
		},
	}
	msgs := MessagesFromItems(items)
	if len(msgs) != 1 {
		t.Fatalf("expected one message, got %d", len(msgs))
	}
	if msgs[0].MessageType == nil || *msgs[0].MessageType != MessageTypeHeadlessModeExit {
		t.Fatalf("expected message type to round-trip, got %v", msgs[0].MessageType)
	}
	roundTrip := ItemsFromMessages(msgs)
	if len(roundTrip) != 1 {
		t.Fatalf("expected one round-trip item, got %d", len(roundTrip))
	}
	if roundTrip[0].MessageType == nil || *roundTrip[0].MessageType != MessageTypeHeadlessModeExit {
		t.Fatalf("expected round-trip item message type, got %v", roundTrip[0].MessageType)
	}
}

func TestMessagesFromItems_PreservesWorktreeExitMessageType(t *testing.T) {
	worktreeContext := &session.WorktreeContext{
		Branch:        session.OptionalWorktreeBranch("feature/typed-context"),
		WorktreePath:  "/tmp/worktree",
		WorkspaceRoot: "/tmp/workspace",
		EffectiveCwd:  "/tmp/workspace/pkg",
	}
	items := []ResponseItem{
		{
			Type:            ResponseItemTypeMessage,
			Role:            textutil.Value(RoleDeveloper),
			MessageType:     textutil.Value(MessageTypeWorktreeModeExit),
			WorktreeContext: worktreeContext,
			Content:         textutil.Value("returned to main workspace"),
		},
	}
	msgs := MessagesFromItems(items)
	if len(msgs) != 1 {
		t.Fatalf("expected one message, got %d", len(msgs))
	}
	if msgs[0].MessageType == nil || *msgs[0].MessageType != MessageTypeWorktreeModeExit {
		t.Fatalf("expected message type to round-trip, got %v", msgs[0].MessageType)
	}
	if msgs[0].WorktreeContext == nil || !session.WorktreeContextEqual(*msgs[0].WorktreeContext, *worktreeContext) {
		t.Fatalf("worktree context = %+v, want %+v", msgs[0].WorktreeContext, worktreeContext)
	}
	roundTrip := ItemsFromMessages(msgs)
	if len(roundTrip) != 1 {
		t.Fatalf("expected one round-trip item, got %d", len(roundTrip))
	}
	if roundTrip[0].MessageType == nil || *roundTrip[0].MessageType != MessageTypeWorktreeModeExit {
		t.Fatalf("expected round-trip item message type, got %v", roundTrip[0].MessageType)
	}
	if roundTrip[0].WorktreeContext == nil || !session.WorktreeContextEqual(*roundTrip[0].WorktreeContext, *worktreeContext) {
		t.Fatalf("round-trip worktree context = %+v, want %+v", roundTrip[0].WorktreeContext, worktreeContext)
	}

	cloned := CloneResponseItems(roundTrip)
	*roundTrip[0].WorktreeContext.Branch = "mutated"
	if cloned[0].WorktreeContext == nil ||
		cloned[0].WorktreeContext.Branch == nil ||
		worktreeContext.Branch == nil ||
		*cloned[0].WorktreeContext.Branch != *worktreeContext.Branch {
		t.Fatalf("cloned worktree context aliased source: %+v", cloned[0].WorktreeContext)
	}
}

func TestPreparedOpenAIItemKeepsWorktreeContextOutOfProviderPayload(t *testing.T) {
	target := &session.WorktreeContext{
		Branch:        session.OptionalWorktreeBranch("feature/internal-metadata"),
		WorktreePath:  "/tmp/worktree",
		WorkspaceRoot: "/tmp/workspace",
		EffectiveCwd:  "/tmp/worktree",
	}
	items := ItemsFromMessages([]Message{{
		Role:            RoleDeveloper,
		MessageType:     textutil.Value(MessageTypeWorktreeMode),
		WorktreeContext: target,
		Content:         textutil.Value("worktree context"),
	}})
	if len(items) != 1 ||
		items[0].WorktreeContext == nil ||
		!session.WorktreeContextEqual(*items[0].WorktreeContext, *target) {
		t.Fatalf("prepared canonical items lost worktree context: %+v", items)
	}

	var providerPayload map[string]json.RawMessage
	if err := json.Unmarshal(items[0].Raw, &providerPayload); err != nil {
		t.Fatalf("decode prepared provider payload: %v", err)
	}
	if _, exists := providerPayload["worktree_context"]; exists {
		t.Fatalf("provider payload contains internal worktree context: %s", items[0].Raw)
	}
}

func TestUsageCacheHitPercent(t *testing.T) {
	usage := Usage{InputTokens: 200, CachedInputTokens: textutil.Value(50)}
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
