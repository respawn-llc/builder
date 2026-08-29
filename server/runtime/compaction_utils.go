package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"core/server/llm"
)

const (
	estimatedInlineImagePayloadTokens = 256
	estimatedInlineFilePayloadTokens  = 512
	encryptedReasoningEnvelopeBytes   = 650
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
	providerCaps, err := e.llm.capabilities(ctx)
	if err != nil {
		return llm.ProviderCapabilities{}, fmt.Errorf("resolve provider capabilities: %w", err)
	}
	return providerCaps, nil
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
		totalTokens += estimateItemTokens(item)
	}
	if totalTokens <= 0 {
		return 0
	}
	return totalTokens
}

func estimateItemTokens(item llm.ResponseItem) int {
	switch item.Type {
	case llm.ResponseItemTypeReasoning, llm.ResponseItemTypeCompaction:
		if item.EncryptedContent != nil {
			return estimateEncryptedReasoningTokens(len(*item.EncryptedContent))
		}
	}

	totalTokens := 0
	for _, value := range []*string{
		item.Content,
		item.ID,
		item.Name,
		item.CallID,
		item.EncryptedContent,
	} {
		if value != nil {
			totalTokens += estimateTokensFromBytes(len(*value))
		}
	}
	totalTokens += estimateTokensFromBytes(len(item.Arguments))
	if outputTokens, ok := estimateStructuredOutputTokens(item.Output); ok {
		totalTokens += outputTokens
	} else {
		totalTokens += estimateTokensFromBytes(len(item.Output))
	}
	for _, summary := range item.ReasoningSummary {
		if summary.Role != nil {
			totalTokens += estimateTokensFromBytes(len(*summary.Role))
		}
		totalTokens += estimateTokensFromBytes(len(summary.Text))
	}
	return totalTokens
}

func estimateEncryptedReasoningTokens(encodedBytes int) int {
	if encodedBytes <= 0 {
		return 0
	}
	// Encrypted reasoning is base64 with a fixed decoded envelope that is not
	// model-visible. Match the provider client's context accounting.
	decodedBytes := encodedBytes/4*3 + encodedBytes%4*3/4
	modelVisibleBytes := decodedBytes - encryptedReasoningEnvelopeBytes
	return estimateTokensFromBytes(modelVisibleBytes)
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
