package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"core/server/llm"
)

const (
	estimatedInlineImagePayloadTokens = 256
	estimatedInlineFilePayloadTokens  = 512
)

func (e *Engine) providerCapabilities(ctx context.Context) (llm.ProviderCapabilities, error) {
	if e.cfg.ProviderCapabilitiesOverride != nil {
		return *e.cfg.ProviderCapabilitiesOverride, nil
	}
	if caps, ok := llm.ProviderCapabilitiesFromLocked(e.store.Meta().Locked); ok {
		return caps, nil
	}
	return e.currentProviderCapabilities(ctx)
}

func (e *Engine) currentProviderCapabilities(ctx context.Context) (llm.ProviderCapabilities, error) {
	if e.cfg.ProviderCapabilitiesOverride != nil {
		return *e.cfg.ProviderCapabilitiesOverride, nil
	}
	provider, ok := e.llm.(llm.ProviderCapabilitiesClient)
	if !ok {
		return llm.ProviderCapabilities{}, fmt.Errorf("provider capabilities are unavailable for client %T", e.llm)
	}
	providerCaps, err := provider.ProviderCapabilities(ctx)
	if err != nil {
		return llm.ProviderCapabilities{}, err
	}
	return providerCaps, nil
}

func ordinal(v int) string {
	if v <= 0 {
		return "0th"
	}
	if v%100 >= 11 && v%100 <= 13 {
		return fmt.Sprintf("%dth", v)
	}
	switch v % 10 {
	case 1:
		return fmt.Sprintf("%dst", v)
	case 2:
		return fmt.Sprintf("%dnd", v)
	case 3:
		return fmt.Sprintf("%drd", v)
	default:
		return fmt.Sprintf("%dth", v)
	}
}

func (e *Engine) inputTokensForItems(ctx context.Context, model string, instructions string, items []llm.ResponseItem) (int, bool) {
	req, ok := buildTokenCountRequestForItems(model, instructions, items)
	if !ok {
		return 0, false
	}
	return e.requestInputTokensPreciselyTracked(ctx, req, false)
}

func buildTokenCountRequestForItems(model string, instructions string, items []llm.ResponseItem) (llm.Request, bool) {
	trimmedModel := strings.TrimSpace(model)
	if trimmedModel == "" {
		return llm.Request{}, false
	}
	req := llm.Request{
		Model:          trimmedModel,
		SystemPrompt:   strings.TrimSpace(instructions),
		Items:          llm.CloneResponseItems(items),
		Tools:          []llm.Tool{},
		ToolChoiceMode: llm.ToolChoiceModeAutomatic,
	}
	if err := req.Validate(); err != nil {
		return llm.Request{}, false
	}
	return req, true
}

func sanitizeRemoteCompactionOutput(output []llm.ResponseItem) ([]llm.ResponseItem, error) {
	filtered := make([]llm.ResponseItem, 0, len(output))
	hasCheckpoint := false
	typeCounts := make(map[string]int)
	for _, item := range output {
		typeCounts[outputItemTypeLabel(item)]++
		switch item.Type {
		case llm.ResponseItemTypeMessage:
			if item.Role == llm.RoleUser && strings.TrimSpace(item.Content) != "" {
				filtered = append(filtered, item)
			}
		case llm.ResponseItemTypeCompaction:
			if strings.TrimSpace(item.EncryptedContent) == "" {
				continue
			}
			filtered = append(filtered, item)
			hasCheckpoint = true
		case llm.ResponseItemTypeReasoning:
			if strings.TrimSpace(item.EncryptedContent) == "" {
				continue
			}
			filtered = append(filtered, item)
			hasCheckpoint = true
		case llm.ResponseItemTypeOther:
			if !itemHasEncryptedCheckpoint(item) {
				continue
			}
			filtered = append(filtered, item)
			hasCheckpoint = true
		}
	}
	if !hasCheckpoint {
		return nil, fmt.Errorf("%w (types=%s)", errRemoteCompactionMissingCheckpoint, formatOutputTypeCounts(typeCounts))
	}
	return filtered, nil
}

func outputItemTypeLabel(item llm.ResponseItem) string {
	if v := strings.TrimSpace(string(item.Type)); v != "" {
		return v
	}
	if len(item.Raw) > 0 {
		var decoded struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(item.Raw, &decoded); err == nil {
			if v := strings.TrimSpace(decoded.Type); v != "" {
				return v
			}
		}
	}
	return "unknown"
}

func itemHasEncryptedCheckpoint(item llm.ResponseItem) bool {
	if strings.TrimSpace(item.EncryptedContent) != "" {
		return true
	}
	if len(item.Raw) == 0 || !json.Valid(item.Raw) {
		return false
	}
	var decoded struct {
		EncryptedContent string `json:"encrypted_content"`
	}
	if err := json.Unmarshal(item.Raw, &decoded); err != nil {
		return false
	}
	return strings.TrimSpace(decoded.EncryptedContent) != ""
}

func formatOutputTypeCounts(counts map[string]int) string {
	if len(counts) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", key, counts[key]))
	}
	return strings.Join(parts, ",")
}

// estimateTokensFromBytes approximates the token cost of a UTF-8 string of the
// given byte length using the ~4-bytes-per-token rule of thumb that the GPT-4
// family follows in practice. Used everywhere we need a deterministic estimate
// without calling the provider's tokenizer.
func estimateTokensFromBytes(byteLen int) int {
	if byteLen <= 0 {
		return 0
	}
	return (byteLen + 3) / 4
}

func estimateItemsTokens(items []llm.ResponseItem) int {
	totalTokens := 0
	for _, item := range items {
		totalTokens += estimateTokensFromBytes(len(item.Content))
		totalTokens += estimateTokensFromBytes(len(item.ID))
		totalTokens += estimateTokensFromBytes(len(item.Name))
		totalTokens += estimateTokensFromBytes(len(item.CallID))
		totalTokens += estimateTokensFromBytes(len(item.EncryptedContent))
		totalTokens += estimateTokensFromBytes(len(item.Arguments))
		if outputTokens, ok := estimateStructuredOutputTokens(item.Output); ok {
			totalTokens += outputTokens
		} else {
			totalTokens += estimateTokensFromBytes(len(item.Output))
		}
		for _, summary := range item.ReasoningSummary {
			totalTokens += estimateTokensFromBytes(len(summary.Role))
			totalTokens += estimateTokensFromBytes(len(summary.Text))
		}
	}
	if totalTokens <= 0 {
		return 0
	}
	return totalTokens
}

func estimateStructuredOutputTokens(raw json.RawMessage) (int, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || !strings.HasPrefix(trimmed, "[") {
		return 0, false
	}

	var items []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL string `json:"image_url"`
		Detail   string `json:"detail"`
		FileID   string `json:"file_id"`
		FileData string `json:"file_data"`
		FileURL  string `json:"file_url"`
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return 0, false
	}
	if len(items) == 0 {
		return 0, false
	}

	total := 0
	for _, item := range items {
		switch strings.ToLower(strings.TrimSpace(item.Type)) {
		case "input_text":
			total += estimateTokensFromBytes(len(item.Text))
		case "input_image":
			total += estimatedInlineImagePayloadTokens
			total += estimateReferenceTokens(item.ImageURL)
			total += estimateReferenceTokens(item.FileID)
			total += estimateTokensFromBytes(len(item.Detail))
		case "input_file":
			total += estimatedInlineFilePayloadTokens
			total += estimateReferenceTokens(item.FileData)
			total += estimateReferenceTokens(item.FileID)
			total += estimateReferenceTokens(item.FileURL)
			total += estimateTokensFromBytes(len(item.Filename))
		default:
			return 0, false
		}
	}
	return total, true
}

func estimateReferenceTokens(value string) int {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "data:") {
		return 0
	}
	return estimateTokensFromBytes(len(trimmed))
}
