package transcript

import (
	"encoding/json"
	"strings"
)

func EncodeToolCallMeta(meta ToolCallMeta) json.RawMessage {
	normalized := NormalizeToolCallMeta(meta)
	if isEmptyToolCallMeta(normalized) {
		return nil
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return nil
	}
	return raw
}

func DecodeToolCallMeta(raw json.RawMessage) (*ToolCallMeta, bool) {
	if len(raw) == 0 || !json.Valid(raw) {
		return nil, false
	}
	var meta ToolCallMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, false
	}
	normalized := NormalizeToolCallMeta(meta)
	if isEmptyToolCallMeta(normalized) {
		return nil, false
	}
	return &normalized, true
}

func isEmptyToolCallMeta(meta ToolCallMeta) bool {
	return strings.TrimSpace(meta.ToolName) == "" &&
		strings.TrimSpace(meta.Command) == "" &&
		strings.TrimSpace(meta.CompactText) == "" &&
		strings.TrimSpace(meta.Question) == "" &&
		len(meta.Suggestions) == 0 &&
		meta.RecommendedOptionIndex == 0 &&
		!meta.RawOutputRequested &&
		!meta.OutputTruncated &&
		!meta.MovedToBackground &&
		meta.ShellExitCode == nil &&
		meta.RenderHint == nil &&
		meta.PatchRender == nil
}
