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

func (t *HTTPTransport) buildInputTokenCountParams(request OpenAIRequest, capabilities ProviderCapabilities) (responses.InputTokenCountParams, error) {
	builder := newOpenAIRequestPayloadBuilder(t.Store, t.ModelVerbosity, capabilities)
	return builder.BuildInputTokenCount(request)
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

func (b openAIRequestPayloadBuilder) BuildInputTokenCount(request OpenAIRequest) (responses.InputTokenCountParams, error) {
	input, err := buildResponsesInput(request.Items)
	if err != nil {
		return responses.InputTokenCountParams{}, err
	}
	toolControls, err := b.prepareToolControls(request)
	if err != nil {
		return responses.InputTokenCountParams{}, err
	}

	out := responses.InputTokenCountParams{
		Model: param.NewOpt(strings.TrimSpace(request.Model)),
		ToolChoice: responses.InputTokenCountParamsToolChoiceUnion{
			OfToolChoiceMode: openai.Opt(toolControls.choice),
		},
	}
	if len(input) > 0 {
		out.Input = responses.InputTokenCountParamsInputUnion{OfResponseInputItemArray: input}
	}
	if instructions := strings.TrimSpace(request.SystemPrompt); instructions != "" {
		out.Instructions = param.NewOpt(instructions)
	}
	if len(toolControls.tools) > 0 {
		out.Tools = toolControls.tools
		out.ParallelToolCalls = param.NewOpt(true)
	}
	if shouldApplyReasoningEffort(request.SupportsReasoningEffort, request.Model, request.ReasoningEffort) {
		out.Reasoning = buildReasoningParam(request.Model, request.ReasoningEffort)
	}
	textConfig, ok, err := buildInputTokenCountTextConfig(request.StructuredOutput, configuredTextVerbosity(request.Model, b.modelVerbosity, b.capabilities))
	if err != nil {
		return responses.InputTokenCountParams{}, err
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

func (openAIRequestPayloadBuilder) BuildCompact(request OpenAICompactionRequest) (responses.ResponseCompactParams, error) {
	if strings.TrimSpace(request.Model) == "" {
		return responses.ResponseCompactParams{}, fmt.Errorf("compaction model is required")
	}
	input, err := buildResponsesInput(request.InputItems)
	if err != nil {
		return responses.ResponseCompactParams{}, err
	}
	out := responses.ResponseCompactParams{Model: responses.ResponseCompactParamsModel(request.Model)}
	if len(input) > 0 {
		out.Input = responses.ResponseCompactParamsInputUnion{OfResponseInputItemArray: input}
	}
	if instructions := strings.TrimSpace(request.Instructions); instructions != "" {
		out.Instructions = param.NewOpt(instructions)
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
	if len(tool.Schema) > 0 && !json.Valid(tool.Schema) {
		return responses.ToolUnionParam{}, fmt.Errorf("invalid tool schema for %s", tool.Name)
	}
	params := map[string]any{"type": "object", "properties": map[string]any{}}
	if len(tool.Schema) > 0 {
		if err := json.Unmarshal(tool.Schema, &params); err != nil {
			return responses.ToolUnionParam{}, fmt.Errorf("invalid tool schema for %s", tool.Name)
		}
	}
	normalizeSchemaNode(params)
	toolParam := responses.ToolParamOfFunction(tool.Name, params, false)
	if description := strings.TrimSpace(tool.Description); description != "" && toolParam.OfFunction != nil {
		toolParam.OfFunction.Description = openai.String(description)
	}
	return toolParam, nil
}

func buildResponseTextConfig(output *StructuredOutput, verbosity string) (responses.ResponseTextConfigParam, bool, error) {
	text := responses.ResponseTextConfigParam{}
	if verbosity != "" {
		text.Verbosity = responses.ResponseTextConfigVerbosity(verbosity)
	}
	if output == nil {
		return text, text.Verbosity != "", nil
	}
	schema, err := parseStructuredOutputSchema(output.Schema)
	if err != nil {
		return responses.ResponseTextConfigParam{}, false, err
	}
	text.Format = responses.ResponseFormatTextConfigParamOfJSONSchema(strings.TrimSpace(output.Name), schema)
	if text.Format.OfJSONSchema != nil {
		if output.Strict {
			text.Format.OfJSONSchema.Strict = param.NewOpt(true)
		}
		if description := strings.TrimSpace(output.Description); description != "" {
			text.Format.OfJSONSchema.Description = param.NewOpt(description)
		}
	}
	return text, true, nil
}

func buildInputTokenCountTextConfig(output *StructuredOutput, verbosity string) (responses.InputTokenCountParamsText, bool, error) {
	text := responses.InputTokenCountParamsText{}
	if verbosity != "" {
		text.Verbosity = verbosity
	}
	if output == nil {
		return text, text.Verbosity != "", nil
	}
	schema, err := parseStructuredOutputSchema(output.Schema)
	if err != nil {
		return responses.InputTokenCountParamsText{}, false, err
	}
	text.Format = responses.ResponseFormatTextConfigParamOfJSONSchema(strings.TrimSpace(output.Name), schema)
	if text.Format.OfJSONSchema != nil {
		if output.Strict {
			text.Format.OfJSONSchema.Strict = param.NewOpt(true)
		}
		if description := strings.TrimSpace(output.Description); description != "" {
			text.Format.OfJSONSchema.Description = param.NewOpt(description)
		}
	}
	return text, true, nil
}

func parseStructuredOutputSchema(raw json.RawMessage) (map[string]any, error) {
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("invalid structured output schema")
	}
	return schema, nil
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

func normalizeSchemaNode(node any) {
	obj, ok := node.(map[string]any)
	if ok {
		if isJSONObjectSchema(obj) {
			if _, exists := obj["additionalProperties"]; !exists {
				obj["additionalProperties"] = false
			}
		}
		if props, ok := obj["properties"].(map[string]any); ok {
			for _, prop := range props {
				normalizeSchemaNode(prop)
			}
		}
		if defs, ok := obj["$defs"].(map[string]any); ok {
			for _, def := range defs {
				normalizeSchemaNode(def)
			}
		}
		if defs, ok := obj["definitions"].(map[string]any); ok {
			for _, def := range defs {
				normalizeSchemaNode(def)
			}
		}
		if items, exists := obj["items"]; exists {
			normalizeSchemaNode(items)
		}
		for _, key := range []string{"allOf", "anyOf", "oneOf"} {
			if list, ok := obj[key].([]any); ok {
				for _, item := range list {
					normalizeSchemaNode(item)
				}
			}
		}
		for _, key := range []string{"not", "if", "then", "else"} {
			if child, exists := obj[key]; exists {
				normalizeSchemaNode(child)
			}
		}
		return
	}

	if list, ok := node.([]any); ok {
		for _, item := range list {
			normalizeSchemaNode(item)
		}
	}
}

func isJSONObjectSchema(schema map[string]any) bool {
	if len(schema) == 0 {
		return false
	}
	if typeField, ok := schema["type"]; ok {
		switch value := typeField.(type) {
		case string:
			return strings.TrimSpace(value) == "object"
		case []any:
			for _, item := range value {
				if stringValue, ok := item.(string); ok && strings.TrimSpace(stringValue) == "object" {
					return true
				}
			}
		}
	}
	_, hasProps := schema["properties"]
	return hasProps
}
