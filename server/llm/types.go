package llm

import (
	"context"
	"core/server/session"
	"core/shared/clientui"
	"core/shared/modelcontract"
	"core/shared/transcript"
	"core/shared/valuecopy"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrInvalidRequest              = errors.New("invalid llm request")
	ErrMissingTransport            = errors.New("openai transport is required")
	ErrUnsupportedToolChoicePolicy = errors.New("unsupported tool choice policy")
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type MessagePhase = clientui.MessagePhase

const (
	MessagePhaseCommentary = clientui.MessagePhaseCommentary
	MessagePhaseFinal      = clientui.MessagePhaseFinal
)

type MessageType = clientui.MessageType

const (
	MessageTypeAgentsMD                  = clientui.MessageTypeAgentsMD
	MessageTypeSkills                    = clientui.MessageTypeSkills
	MessageTypeSubagents                 = clientui.MessageTypeSubagents
	MessageTypeEnvironment               = clientui.MessageTypeEnvironment
	MessageTypeCompactionSummary         = clientui.MessageTypeCompactionSummary
	MessageTypeInterruption              = clientui.MessageTypeInterruption
	MessageTypeErrorFeedback             = clientui.MessageTypeErrorFeedback
	MessageTypeCompactionSoonReminder    = clientui.MessageTypeCompactionSoonReminder
	MessageTypeHandoffFutureMessage      = clientui.MessageTypeHandoffFutureMessage
	MessageTypeReviewerFeedback          = clientui.MessageTypeReviewerFeedback
	MessageTypeBackgroundNotice          = clientui.MessageTypeBackgroundNotice
	MessageTypeCustomToolCallOutput      = clientui.MessageTypeCustomToolCallOutput
	MessageTypeManualCompactionCarryover = clientui.MessageTypeManualCompactionCarryover
	MessageTypeHeadlessMode              = clientui.MessageTypeHeadlessMode
	MessageTypeHeadlessModeExit          = clientui.MessageTypeHeadlessModeExit
	MessageTypeWorkflowMode              = clientui.MessageTypeWorkflowMode
	MessageTypeWorktreeMode              = clientui.MessageTypeWorktreeMode
	MessageTypeWorktreeModeExit          = clientui.MessageTypeWorktreeModeExit
	MessageTypeGoal                      = clientui.MessageTypeGoal
)

type Message struct {
	Role               Role                     `json:"role"`
	MessageType        MessageType              `json:"message_type,omitempty"`
	SourcePath         string                   `json:"source_path,omitempty"`
	WorktreeContext    *session.WorktreeContext `json:"worktree_context,omitempty"`
	Content            string                   `json:"content,omitempty"`
	CompactContent     string                   `json:"compact_content,omitempty"`
	Name               string                   `json:"name,omitempty"`
	ToolCallID         string                   `json:"tool_call_id,omitempty"`
	Phase              MessagePhase             `json:"phase,omitempty"`
	BackgroundExitCode *int                     `json:"background_exit_code,omitempty"`
	ToolCalls          []ToolCall               `json:"tool_calls,omitempty"`
	ReasoningItems     []ReasoningItem          `json:"reasoning_items,omitempty"`
}

type ResponseItemType string
type ResponseItemLinkKind string

const (
	ResponseItemTypeMessage            ResponseItemType = "message"
	ResponseItemTypeFunctionCall       ResponseItemType = "function_call"
	ResponseItemTypeFunctionCallOutput ResponseItemType = "function_call_output"
	ResponseItemTypeCustomToolCall     ResponseItemType = "custom_tool_call"
	ResponseItemTypeCustomToolOutput   ResponseItemType = "custom_tool_call_output"
	ResponseItemTypeReasoning          ResponseItemType = "reasoning"
	ResponseItemTypeCompaction         ResponseItemType = "compaction"
	ResponseItemTypeOther              ResponseItemType = "other"
)

const (
	ResponseItemLinkToolOutputAttachment ResponseItemLinkKind = "tool_output_attachment"
)

func ResponseItemTypeIsCustomToolCall(itemType ResponseItemType) bool {
	return itemType == ResponseItemTypeCustomToolCall
}

func ToolOutputItemType(custom bool) ResponseItemType {
	if custom {
		return ResponseItemTypeCustomToolOutput
	}
	return ResponseItemTypeFunctionCallOutput
}

func ToolOutputMessageType(custom bool) MessageType {
	if custom {
		return MessageTypeCustomToolCallOutput
	}
	return ""
}

type ResponseItem struct {
	Type               ResponseItemType         `json:"type"`
	OutputIndex        int64                    `json:"output_index,omitempty"`
	Role               Role                     `json:"role,omitempty"`
	MessageType        MessageType              `json:"message_type,omitempty"`
	SourcePath         string                   `json:"source_path,omitempty"`
	WorktreeContext    *session.WorktreeContext `json:"worktree_context,omitempty"`
	Phase              MessagePhase             `json:"phase,omitempty"`
	ID                 string                   `json:"id,omitempty"`
	Name               string                   `json:"name,omitempty"`
	CallID             string                   `json:"call_id,omitempty"`
	Content            string                   `json:"content,omitempty"`
	CompactContent     string                   `json:"compact_content,omitempty"`
	BackgroundExitCode *int                     `json:"background_exit_code,omitempty"`
	ToolPresentation   json.RawMessage          `json:"tool_presentation,omitempty"`
	Arguments          json.RawMessage          `json:"arguments,omitempty"`
	CustomInput        string                   `json:"custom_input,omitempty"`
	Output             json.RawMessage          `json:"output,omitempty"`
	ReasoningSummary   []ReasoningEntry         `json:"reasoning_summary,omitempty"`
	EncryptedContent   string                   `json:"encrypted_content,omitempty"`
	Raw                json.RawMessage          `json:"raw,omitempty"`
	LinkedCallID       string                   `json:"linked_call_id,omitempty"`
	LinkKind           ResponseItemLinkKind     `json:"link_kind,omitempty"`
}

func CloneResponseItems(items []ResponseItem) []ResponseItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]ResponseItem, 0, len(items))
	for _, item := range items {
		copyItem := item
		copyItem.BackgroundExitCode = valuecopy.Pointer(item.BackgroundExitCode)
		copyItem.WorktreeContext = session.CloneWorktreeContext(item.WorktreeContext)
		if len(item.Arguments) > 0 {
			copyItem.Arguments = append(json.RawMessage(nil), item.Arguments...)
		}
		if len(item.Output) > 0 {
			copyItem.Output = append(json.RawMessage(nil), item.Output...)
		}
		if len(item.ToolPresentation) > 0 {
			copyItem.ToolPresentation = append(json.RawMessage(nil), item.ToolPresentation...)
		}
		if len(item.Raw) > 0 {
			copyItem.Raw = append(json.RawMessage(nil), item.Raw...)
		}
		if len(item.ReasoningSummary) > 0 {
			copyItem.ReasoningSummary = append([]ReasoningEntry(nil), item.ReasoningSummary...)
		}
		out = append(out, copyItem)
	}
	return out
}

func ItemsFromMessages(messages []Message) []ResponseItem {
	out := make([]ResponseItem, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case RoleAssistant:
			if strings.TrimSpace(msg.Content) != "" {
				out = append(out, ResponseItem{
					Type:               ResponseItemTypeMessage,
					Role:               RoleAssistant,
					MessageType:        msg.MessageType,
					SourcePath:         msg.SourcePath,
					WorktreeContext:    session.CloneWorktreeContext(msg.WorktreeContext),
					Phase:              msg.Phase,
					Content:            msg.Content,
					CompactContent:     msg.CompactContent,
					BackgroundExitCode: valuecopy.Pointer(msg.BackgroundExitCode),
				})
			}
			for _, tc := range msg.ToolCalls {
				callID := strings.TrimSpace(tc.ID)
				if callID == "" && strings.TrimSpace(tc.Name) == "" {
					continue
				}
				if tc.Custom {
					customInput := tc.CustomInput
					if strings.TrimSpace(customInput) == "" {
						customInput = stringFromJSONRaw(tc.Input)
					}
					out = append(out, ResponseItem{
						Type:             ResponseItemTypeCustomToolCall,
						ID:               callID,
						CallID:           callID,
						Name:             tc.Name,
						ToolPresentation: append(json.RawMessage(nil), tc.Presentation...),
						CustomInput:      customInput,
					})
					continue
				}
				out = append(out, ResponseItem{
					Type:             ResponseItemTypeFunctionCall,
					ID:               callID,
					CallID:           callID,
					Name:             tc.Name,
					ToolPresentation: append(json.RawMessage(nil), tc.Presentation...),
					Arguments:        normalizeToolInput(string(tc.Input)),
				})
			}
			for _, ri := range msg.ReasoningItems {
				id := strings.TrimSpace(ri.ID)
				encrypted := strings.TrimSpace(ri.EncryptedContent)
				if id == "" || encrypted == "" {
					continue
				}
				out = append(out, ResponseItem{
					Type:             ResponseItemTypeReasoning,
					ID:               id,
					EncryptedContent: encrypted,
				})
			}
		case RoleTool:
			callID := strings.TrimSpace(msg.ToolCallID)
			if callID == "" {
				continue
			}
			itemType := ToolOutputItemType(msg.MessageType == MessageTypeCustomToolCallOutput)
			out = append(out, ResponseItem{Type: itemType, CallID: callID, Name: msg.Name, Output: normalizeToolInput(msg.Content)})
		default:
			if strings.TrimSpace(msg.Content) == "" {
				continue
			}
			out = append(out, ResponseItem{
				Type:               ResponseItemTypeMessage,
				Role:               msg.Role,
				MessageType:        msg.MessageType,
				SourcePath:         msg.SourcePath,
				WorktreeContext:    session.CloneWorktreeContext(msg.WorktreeContext),
				Content:            msg.Content,
				CompactContent:     msg.CompactContent,
				BackgroundExitCode: valuecopy.Pointer(msg.BackgroundExitCode),
				Name:               msg.Name,
			})
		}
	}
	return PrepareOpenAIInputItems(out)
}

func MessagesFromItems(items []ResponseItem) []Message {
	out := make([]Message, 0, len(items))
	appendAssistant := func() int {
		out = append(out, Message{Role: RoleAssistant})
		return len(out) - 1
	}
	lastAssistantIdx := -1

	for _, item := range items {
		switch item.Type {
		case ResponseItemTypeMessage:
			role := item.Role
			if role == "" {
				role = RoleUser
			}
			msg := Message{
				Role:               role,
				MessageType:        item.MessageType,
				SourcePath:         item.SourcePath,
				WorktreeContext:    session.CloneWorktreeContext(item.WorktreeContext),
				Phase:              item.Phase,
				Content:            item.Content,
				CompactContent:     item.CompactContent,
				BackgroundExitCode: valuecopy.Pointer(item.BackgroundExitCode),
				Name:               item.Name,
			}
			out = append(out, msg)
			if role == RoleAssistant {
				lastAssistantIdx = len(out) - 1
			}
		case ResponseItemTypeFunctionCall:
			if lastAssistantIdx < 0 || lastAssistantIdx >= len(out) || out[lastAssistantIdx].Role != RoleAssistant {
				lastAssistantIdx = appendAssistant()
			}
			callID := strings.TrimSpace(item.CallID)
			if callID == "" {
				callID = strings.TrimSpace(item.ID)
			}
			out[lastAssistantIdx].ToolCalls = append(out[lastAssistantIdx].ToolCalls, ToolCall{
				ID:           callID,
				Name:         item.Name,
				Presentation: append(json.RawMessage(nil), item.ToolPresentation...),
				Input:        normalizeToolInput(string(item.Arguments)),
			})
		case ResponseItemTypeCustomToolCall:
			if lastAssistantIdx < 0 || lastAssistantIdx >= len(out) || out[lastAssistantIdx].Role != RoleAssistant {
				lastAssistantIdx = appendAssistant()
			}
			callID := strings.TrimSpace(item.CallID)
			if callID == "" {
				callID = strings.TrimSpace(item.ID)
			}
			out[lastAssistantIdx].ToolCalls = append(out[lastAssistantIdx].ToolCalls, ToolCall{
				ID:           callID,
				Name:         item.Name,
				Presentation: append(json.RawMessage(nil), item.ToolPresentation...),
				Input:        normalizeToolInput(item.CustomInput),
				Custom:       true,
				CustomInput:  item.CustomInput,
			})
		case ResponseItemTypeFunctionCallOutput:
			callID := strings.TrimSpace(item.CallID)
			if callID == "" {
				continue
			}
			out = append(out, Message{
				Role:       RoleTool,
				ToolCallID: callID,
				Name:       item.Name,
				Content:    stringFromJSONRaw(item.Output),
			})
			lastAssistantIdx = -1
		case ResponseItemTypeCustomToolOutput:
			callID := strings.TrimSpace(item.CallID)
			if callID == "" {
				continue
			}
			out = append(out, Message{Role: RoleTool, MessageType: MessageTypeCustomToolCallOutput, ToolCallID: callID, Name: item.Name, Content: stringFromJSONRaw(item.Output)})
			lastAssistantIdx = -1
		case ResponseItemTypeReasoning:
			if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.EncryptedContent) == "" {
				continue
			}
			if lastAssistantIdx < 0 || lastAssistantIdx >= len(out) || out[lastAssistantIdx].Role != RoleAssistant {
				lastAssistantIdx = appendAssistant()
			}
			out[lastAssistantIdx].ReasoningItems = append(out[lastAssistantIdx].ReasoningItems, ReasoningItem{
				ID:               item.ID,
				EncryptedContent: item.EncryptedContent,
			})
		}
	}

	filtered := out[:0]
	for _, msg := range out {
		if strings.TrimSpace(msg.Content) == "" && len(msg.ToolCalls) == 0 && len(msg.ReasoningItems) == 0 {
			continue
		}
		filtered = append(filtered, msg)
	}
	return append([]Message(nil), filtered...)
}

func stringFromJSONRaw(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return trimmed
}

type Tool struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Schema      json.RawMessage   `json:"schema"`
	Custom      *CustomToolFormat `json:"custom,omitempty"`
}

type CustomToolFormat struct {
	Type       string `json:"type"`
	Syntax     string `json:"syntax,omitempty"`
	Definition string `json:"definition,omitempty"`
}

const PatchToolLarkGrammar = `start: begin_patch hunk+ end_patch
begin_patch: "*** Begin Patch" LF
end_patch: "*** End Patch" LF?

hunk: add_hunk | delete_hunk | update_hunk
add_hunk: "*** Add File: " filename LF add_line+
delete_hunk: "*** Delete File: " filename LF
update_hunk: "*** Update File: " filename LF change_move? change?

filename: /(.+)/
add_line: "+" /(.*)/ LF -> line

change_move: "*** Move to: " filename LF
change: ((change_context | change_line)+ eof_line? | eof_line)
change_context: ("@@" | "@@ " /(.+)/) LF
change_line: ("+" | "-" | " ") /(.*)/ LF
eof_line: "*** End of File" LF

%import common.LF
`

type ToolCall struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Presentation json.RawMessage `json:"presentation,omitempty"`
	Input        json.RawMessage `json:"input"`
	Custom       bool            `json:"custom,omitempty"`
	CustomInput  string          `json:"custom_input,omitempty"`
}

type ToolResult struct {
	CallID  string          `json:"call_id"`
	Name    string          `json:"name"`
	Output  json.RawMessage `json:"output"`
	IsError bool            `json:"is_error"`
}

type StructuredOutput struct {
	Name        string          `json:"name"`
	Schema      json.RawMessage `json:"schema"`
	Description string          `json:"description,omitempty"`
	Strict      bool            `json:"strict,omitempty"`
}

type ToolChoiceMode string

const (
	ToolChoiceModeAutomatic ToolChoiceMode = "automatic"
	ToolChoiceModeRequired  ToolChoiceMode = "required"
)

type ToolControls struct {
	ChoiceMode            ToolChoiceMode
	EnableNativeWebSearch bool
}

type UnsupportedToolChoicePolicyError struct {
	ProviderID string
	Mode       ToolChoiceMode
}

func (e *UnsupportedToolChoicePolicyError) Error() string {
	return fmt.Sprintf("provider %q cannot represent %q tool choice; select a Responses API provider", e.ProviderID, e.Mode)
}

func (e *UnsupportedToolChoicePolicyError) Unwrap() error {
	return ErrUnsupportedToolChoicePolicy
}

func ValidateToolChoiceSupport(capabilities ProviderCapabilities, mode ToolChoiceMode) error {
	switch mode {
	case ToolChoiceModeAutomatic:
		return nil
	case ToolChoiceModeRequired:
		if capabilities.SupportsResponsesAPI {
			return nil
		}
		return &UnsupportedToolChoicePolicyError{
			ProviderID: strings.TrimSpace(capabilities.ProviderID),
			Mode:       mode,
		}
	default:
		return fmt.Errorf("%w: unknown tool choice mode %q", ErrInvalidRequest, mode)
	}
}

func HasEffectiveAdvertisedTools(tools []Tool, enableNativeWebSearch bool) bool {
	return len(tools) > 0 || enableNativeWebSearch
}

type Request struct {
	Model                   string                       `json:"model"`
	Temperature             float64                      `json:"temperature"`
	MaxTokens               int                          `json:"max_tokens"`
	ReasoningEffort         string                       `json:"reasoning_effort,omitempty"`
	SupportsReasoningEffort bool                         `json:"supports_reasoning_effort,omitempty"`
	FastMode                bool                         `json:"fast_mode,omitempty"`
	EnableNativeWebSearch   bool                         `json:"enable_native_web_search,omitempty"`
	SystemPrompt            string                       `json:"system_prompt"`
	PromptCacheKey          string                       `json:"prompt_cache_key,omitempty"`
	PromptCacheScope        transcript.CacheWarningScope `json:"prompt_cache_scope,omitempty"`
	SessionID               string                       `json:"session_id,omitempty"`
	Items                   []ResponseItem               `json:"items,omitempty"`
	Tools                   []Tool                       `json:"tools,omitempty"`
	ToolChoiceMode          ToolChoiceMode               `json:"tool_choice_mode"`
	StructuredOutput        *StructuredOutput            `json:"structured_output,omitempty"`
}

func (r Request) Validate() error {
	if r.Model == "" {
		return fmt.Errorf("%w: model is required", ErrInvalidRequest)
	}
	if r.MaxTokens < 0 {
		return fmt.Errorf("%w: max_tokens must be >= 0", ErrInvalidRequest)
	}
	switch r.ToolChoiceMode {
	case ToolChoiceModeAutomatic, ToolChoiceModeRequired:
	default:
		return fmt.Errorf("%w: unknown tool choice mode %q", ErrInvalidRequest, r.ToolChoiceMode)
	}
	if r.ToolChoiceMode == ToolChoiceModeRequired && !HasEffectiveAdvertisedTools(r.Tools, r.EnableNativeWebSearch) {
		return fmt.Errorf("%w: required tool choice needs at least one advertised tool", ErrInvalidRequest)
	}
	for i := range r.Items {
		if strings.TrimSpace(string(r.Items[i].Type)) == "" {
			return fmt.Errorf("%w: item type is required at index %d", ErrInvalidRequest, i)
		}
	}
	for i := range r.Tools {
		if r.Tools[i].Name == "" {
			return fmt.Errorf("%w: tool name is required at index %d", ErrInvalidRequest, i)
		}
		if len(r.Tools[i].Schema) > 0 && !json.Valid(r.Tools[i].Schema) {
			return fmt.Errorf("%w: tool schema is invalid json at index %d", ErrInvalidRequest, i)
		}
		if r.Tools[i].Custom != nil && strings.TrimSpace(r.Tools[i].Custom.Type) == "grammar" {
			if strings.TrimSpace(r.Tools[i].Custom.Definition) == "" {
				return fmt.Errorf("%w: custom tool grammar definition is required at index %d", ErrInvalidRequest, i)
			}
		}
	}
	if r.StructuredOutput != nil {
		if strings.TrimSpace(r.StructuredOutput.Name) == "" {
			return fmt.Errorf("%w: structured_output.name is required", ErrInvalidRequest)
		}
		if len(r.StructuredOutput.Schema) == 0 || !json.Valid(r.StructuredOutput.Schema) {
			return fmt.Errorf("%w: structured_output.schema must be valid json", ErrInvalidRequest)
		}
	}
	return nil
}

func RequestFromLockedContract(locked session.LockedContract, systemPrompt string, items []ResponseItem, tools []Tool, controls ToolControls) (Request, error) {
	if locked.Model == "" {
		return Request{}, fmt.Errorf("%w: locked model is required", ErrInvalidRequest)
	}
	if locked.MaxOutputToken < 0 {
		return Request{}, fmt.Errorf("%w: locked max output token must be >= 0", ErrInvalidRequest)
	}

	req := Request{
		Model:                   locked.Model,
		Temperature:             locked.Temperature,
		MaxTokens:               locked.MaxOutputToken,
		SupportsReasoningEffort: LockedContractSupportsReasoningEffort(&locked, locked.Model),
		EnableNativeWebSearch:   controls.EnableNativeWebSearch,
		SystemPrompt:            systemPrompt,
		PromptCacheKey:          "",
		PromptCacheScope:        "",
		SessionID:               "",
		Items:                   CloneResponseItems(items),
		Tools:                   append([]Tool(nil), tools...),
		ToolChoiceMode:          controls.ChoiceMode,
	}
	if err := req.Validate(); err != nil {
		return Request{}, err
	}
	return req, nil
}

type Usage struct {
	InputTokens          int  `json:"input_tokens"`
	OutputTokens         int  `json:"output_tokens"`
	WindowTokens         int  `json:"window_tokens"`
	CachedInputTokens    int  `json:"cached_input_tokens,omitempty"`
	HasCachedInputTokens bool `json:"has_cached_input_tokens,omitempty"`
}

type ReasoningEntry struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type ReasoningItem struct {
	ID               string `json:"id"`
	EncryptedContent string `json:"encrypted_content,omitempty"`
}

func (u Usage) Percent() int {
	if u.WindowTokens <= 0 {
		return 0
	}
	total := u.InputTokens + u.OutputTokens
	if total <= 0 {
		return 0
	}
	pct := (total * 100) / u.WindowTokens
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

func (u Usage) CacheHitPercent() (int, bool) {
	if !u.HasCachedInputTokens || u.InputTokens <= 0 {
		return 0, false
	}
	cached := u.CachedInputTokens
	if cached < 0 {
		cached = 0
	}
	if cached > u.InputTokens {
		cached = u.InputTokens
	}
	pct := (cached * 100) / u.InputTokens
	if pct < 0 {
		return 0, false
	}
	if pct > 100 {
		return 100, true
	}
	return pct, true
}

type Response struct {
	Assistant      Message          `json:"assistant"`
	ProviderPhase  *ProviderPhase   `json:"-"`
	ToolCalls      []ToolCall       `json:"tool_calls,omitempty"`
	Reasoning      []ReasoningEntry `json:"reasoning,omitempty"`
	ReasoningItems []ReasoningItem  `json:"reasoning_items,omitempty"`
	OutputItems    []ResponseItem   `json:"output_items,omitempty"`
	Usage          Usage            `json:"usage"`
}

type CompactionRequest struct {
	Model        string
	Instructions string
	SessionID    string
	InputItems   []ResponseItem
}

type CompactionResponse struct {
	OutputItems       []ResponseItem
	Usage             Usage
	TrimmedItemsCount *int
}

type CompactionClient interface {
	Compact(ctx context.Context, request CompactionRequest) (CompactionResponse, error)
}

type ProviderCapabilities = modelcontract.ProviderCapabilities

type ProviderCapabilitiesClient interface {
	ProviderCapabilities(ctx context.Context) (ProviderCapabilities, error)
}

type Client interface {
	Generate(ctx context.Context, request Request) (Response, error)
}

type StreamClient interface {
	GenerateStream(ctx context.Context, request Request, onDelta func(text string)) (Response, error)
}

type AssistantDelta struct {
	Text  string
	Phase MessagePhase
}

type ReasoningSummaryDelta struct {
	Key  string
	Role string
	Text string
}

type StreamCallbacks struct {
	OnAssistantDelta        func(delta AssistantDelta)
	OnReasoningSummaryDelta func(delta ReasoningSummaryDelta)
	OnStreamActivity        func()
}

type StreamEventsClient interface {
	GenerateStreamWithEvents(ctx context.Context, request Request, callbacks StreamCallbacks) (Response, error)
}

type RequestInputTokenCountClient interface {
	CountRequestInputTokens(ctx context.Context, request Request) (int, error)
}

type RequestInputTokenCountSupportClient interface {
	SupportsRequestInputTokenCount(ctx context.Context) (bool, error)
}

type ModelContextWindowClient interface {
	ResolveModelContextWindow(ctx context.Context, model string) (int, error)
}
