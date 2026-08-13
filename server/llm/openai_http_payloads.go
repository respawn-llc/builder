package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// ErrCustomToolNameRequired is returned when a custom tool param omits its name.
// Callers match it via errors.Is.
var ErrCustomToolNameRequired = errors.New("custom tool name is required")

type openAIRequestPayloadBuilder struct {
	store          bool
	modelVerbosity string
	capabilities   ProviderCapabilities
}

type openAIPayloadToolControls struct {
	tools  []responses.ToolUnionParam
	choice responses.ToolChoiceOptions
}

func newOpenAIRequestPayloadBuilder(store bool, modelVerbosity string, capabilities ProviderCapabilities) openAIRequestPayloadBuilder {
	return openAIRequestPayloadBuilder{store: store, modelVerbosity: strings.ToLower(strings.TrimSpace(modelVerbosity)), capabilities: capabilities}
}

func (t *HTTPTransport) buildPayload(request OpenAIRequest, mode OpenAIAuthMode, capabilities ProviderCapabilities) (responses.ResponseNewParams, error) {
	builder := newOpenAIRequestPayloadBuilder(t.Store, t.ModelVerbosity, capabilities)
	return builder.BuildResponse(request, mode)
}

func (b openAIRequestPayloadBuilder) BuildResponse(request OpenAIRequest, mode OpenAIAuthMode) (responses.ResponseNewParams, error) {
	input, err := buildResponsesInput(request.Items)
	if err != nil {
		return responses.ResponseNewParams{}, err
	}
	toolControls, err := b.prepareToolControls(request)
	if err != nil {
		return responses.ResponseNewParams{}, err
	}

	out := responses.ResponseNewParams{
		Model: request.Model,
		Store: openai.Bool(b.store),
		ToolChoice: responses.ResponseNewParamsToolChoiceUnion{
			OfToolChoiceMode: openai.Opt(toolControls.choice),
		},
	}
	if cacheKey := strings.TrimSpace(request.PromptCacheKey); cacheKey != "" && SupportsPromptCacheKeyProvider(b.capabilities) {
		out.PromptCacheKey = openai.String(cacheKey)
	}
	if len(input) > 0 {
		out.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: input}
	}
	if instructions := strings.TrimSpace(request.SystemPrompt); instructions != "" {
		out.Instructions = openai.String(instructions)
	}
	if len(toolControls.tools) > 0 {
		out.Tools = toolControls.tools
		out.ParallelToolCalls = openai.Bool(true)
	}
	if shouldApplyReasoningEffort(request.SupportsReasoningEffort, request.Model, request.ReasoningEffort) {
		out.Reasoning = buildReasoningParam(request.Model, request.ReasoningEffort)
		out.Include = append(out.Include, responses.ResponseIncludableReasoningEncryptedContent)
	}
	if request.FastMode && SupportsFastModeProvider(b.capabilities) {
		out.ServiceTier = responses.ResponseNewParamsServiceTierPriority
	}
	if request.MaxTokens > 0 && !mode.IsOAuth {
		out.MaxOutputTokens = openai.Int(int64(request.MaxTokens))
	}
	if request.Temperature != 0 && !mode.IsOAuth {
		out.Temperature = openai.Float(request.Temperature)
	}
	textConfig, ok, err := buildResponseTextConfig(request.StructuredOutput, configuredTextVerbosity(request.Model, b.modelVerbosity, b.capabilities))
	if err != nil {
		return responses.ResponseNewParams{}, err
	}
	if ok {
		out.Text = textConfig
	}
	return out, nil
}

func (b openAIRequestPayloadBuilder) prepareToolControls(request OpenAIRequest) (openAIPayloadToolControls, error) {
	if err := ValidateToolChoiceSupport(b.capabilities, request.ToolChoiceMode); err != nil {
		return openAIPayloadToolControls{}, err
	}
	tools, err := b.buildTools(request.Tools, request.EnableNativeWebSearch)
	if err != nil {
		return openAIPayloadToolControls{}, err
	}
	if request.ToolChoiceMode == ToolChoiceModeRequired && len(tools) == 0 {
		return openAIPayloadToolControls{}, fmt.Errorf("%w: required tool choice needs at least one materialized tool", ErrInvalidRequest)
	}
	choice, err := openAIToolChoice(request.ToolChoiceMode)
	if err != nil {
		return openAIPayloadToolControls{}, err
	}
	return openAIPayloadToolControls{tools: tools, choice: choice}, nil
}

func openAIToolChoice(mode ToolChoiceMode) (responses.ToolChoiceOptions, error) {
	switch mode {
	case ToolChoiceModeAutomatic:
		return responses.ToolChoiceOptionsAuto, nil
	case ToolChoiceModeRequired:
		return responses.ToolChoiceOptionsRequired, nil
	default:
		return "", fmt.Errorf("%w: unknown tool choice mode %q", ErrInvalidRequest, mode)
	}
}

func (b openAIRequestPayloadBuilder) BuildCompactV2(request OpenAICompactionRequest) (responses.ResponseNewParams, error) {
	if strings.TrimSpace(request.Model) == "" {
		return responses.ResponseNewParams{}, fmt.Errorf("compaction model is required")
	}
	input, err := buildResponsesInput(request.InputItems)
	if err != nil {
		return responses.ResponseNewParams{}, err
	}
	trigger := responses.NewResponseInputItemCompactionTriggerParam()
	input = append(input, responses.ResponseInputItemUnionParam{OfCompactionTrigger: &trigger})
	out := responses.ResponseNewParams{
		Model:             request.Model,
		Store:             openai.Bool(b.store),
		ParallelToolCalls: openai.Bool(false),
		ToolChoice: responses.ResponseNewParamsToolChoiceUnion{
			OfToolChoiceMode: openai.Opt(responses.ToolChoiceOptionsAuto),
		},
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: input},
	}
	if cacheKey := strings.TrimSpace(request.PromptCacheKey); cacheKey != "" && SupportsPromptCacheKeyProvider(b.capabilities) {
		out.PromptCacheKey = openai.String(cacheKey)
	}
	if instructions := strings.TrimSpace(request.Instructions); instructions != "" {
		out.Instructions = openai.String(instructions)
	}
	return out, nil
}

func (b openAIRequestPayloadBuilder) buildTools(requestTools []Tool, enableNativeWebSearch bool) ([]responses.ToolUnionParam, error) {
	tools := make([]responses.ToolUnionParam, 0, len(requestTools)+1)
	for _, tool := range requestTools {
		toolParam, err := buildFunctionToolParam(tool)
		if err != nil {
			return nil, err
		}
		tools = append(tools, toolParam)
	}
	if enableNativeWebSearch {
		tools = append(tools, responses.ToolParamOfWebSearch(responses.WebSearchToolTypeWebSearch))
	}
	return tools, nil
}

func buildFunctionToolParam(tool Tool) (responses.ToolUnionParam, error) {
	if tool.Custom != nil {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			return responses.ToolUnionParam{}, ErrCustomToolNameRequired
		}
		format := shared.CustomToolInputFormatUnionParam{}
		switch strings.TrimSpace(tool.Custom.Type) {
		case "grammar":
			definition := tool.Custom.Definition
			if strings.TrimSpace(definition) == "" {
				return responses.ToolUnionParam{}, fmt.Errorf("custom tool grammar definition is required for %s", name)
			}
			syntax := strings.TrimSpace(tool.Custom.Syntax)
			if syntax == "" {
				syntax = "lark"
			}
			format = shared.CustomToolInputFormatParamOfGrammar(definition, syntax)
		case "", "text":
			text := shared.NewCustomToolInputFormatTextParam()
			format = shared.CustomToolInputFormatUnionParam{OfText: &text}
		default:
			return responses.ToolUnionParam{}, fmt.Errorf("unsupported custom tool format %q for %s", tool.Custom.Type, name)
		}
		custom := responses.CustomToolParam{Name: name, Format: format}
		if description := strings.TrimSpace(tool.Description); description != "" {
			custom.Description = openai.String(description)
		}
		return responses.ToolUnionParam{OfCustom: &custom}, nil
	}
	if !tool.Schema.Prepared() {
		return responses.ToolUnionParam{}, fmt.Errorf("tool schema is not prepared for %s", tool.Name)
	}
	raw, err := json.Marshal(struct {
		Type        string          `json:"type"`
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
		Strict      bool            `json:"strict"`
	}{
		Type:        "function",
		Name:        tool.Name,
		Description: strings.TrimSpace(tool.Description),
		Parameters:  json.RawMessage(tool.Schema.JSON()),
		Strict:      tool.Schema.Strict(),
	})
	if err != nil {
		return responses.ToolUnionParam{}, fmt.Errorf("serialize tool schema for %s: %w", tool.Name, err)
	}
	function := param.Override[responses.FunctionToolParam](json.RawMessage(raw))
	return responses.ToolUnionParam{OfFunction: &function}, nil
}

func buildResponseTextConfig(output *StructuredOutput, verbosity string) (responses.ResponseTextConfigParam, bool, error) {
	text := responses.ResponseTextConfigParam{}
	if verbosity != "" {
		text.Verbosity = responses.ResponseTextConfigVerbosity(verbosity)
	}
	if output == nil {
		return text, text.Verbosity != "", nil
	}
	if !output.Schema.Prepared() {
		return responses.ResponseTextConfigParam{}, false, errors.New("structured output schema is not prepared")
	}
	format, err := buildStructuredOutputFormat(output)
	if err != nil {
		return responses.ResponseTextConfigParam{}, false, err
	}
	text.Format = format
	return text, true, nil
}

func buildStructuredOutputFormat(
	output *StructuredOutput,
) (responses.ResponseFormatTextConfigUnionParam, error) {
	raw, err := json.Marshal(struct {
		Type        string          `json:"type"`
		Name        string          `json:"name"`
		Schema      json.RawMessage `json:"schema"`
		Strict      bool            `json:"strict"`
		Description string          `json:"description,omitempty"`
	}{
		Type:        "json_schema",
		Name:        strings.TrimSpace(output.Name),
		Schema:      json.RawMessage(output.Schema.JSON()),
		Strict:      output.Schema.Strict(),
		Description: strings.TrimSpace(output.Description),
	})
	if err != nil {
		return responses.ResponseFormatTextConfigUnionParam{}, fmt.Errorf(
			"serialize structured output schema for %s: %w",
			output.Name,
			err,
		)
	}
	format := param.Override[responses.ResponseFormatTextJSONSchemaConfigParam](json.RawMessage(raw))
	return responses.ResponseFormatTextConfigUnionParam{OfJSONSchema: &format}, nil
}

func shouldApplyReasoningEffort(contractSupport bool, model, effort string) bool {
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return false
	}
	if contractSupport {
		return true
	}
	return SupportsReasoningEffortModel(model)
}

func buildReasoningParam(model, effort string) shared.ReasoningParam {
	param := shared.ReasoningParam{Effort: shared.ReasoningEffort(strings.TrimSpace(effort))}
	if SupportsReasoningSummaryModel(model) {
		param.Summary = shared.ReasoningSummaryConcise
	}
	return param
}

func configuredTextVerbosity(model, configured string, providerCaps ProviderCapabilities) string {
	normalized := strings.ToLower(strings.TrimSpace(configured))
	switch normalized {
	case "low", "medium", "high":
	default:
		return ""
	}
	support := VerbositySupportForModelAndProvider(model, providerCaps)
	if !support.Supported {
		return ""
	}
	for _, level := range support.Levels {
		if level == normalized {
			return normalized
		}
	}
	return ""
}
