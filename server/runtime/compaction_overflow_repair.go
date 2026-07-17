package runtime

import (
	"encoding/json"
	"strings"

	"core/server/llm"
	"core/shared/toolspec"
)

const compactionOverflowCollapsedText = "<collapsed>"

var compactionOverflowCollapsedJSON = json.RawMessage(`"<collapsed>"`)

var compactionOverflowRepairTargetPercents = []int{10, 20, 40}

type compactionOverflowRepairStats struct {
	ShellOutputsCollapsed int
	PatchInputsCollapsed  int
	EstimatedSavedTokens  int
}

func (s compactionOverflowRepairStats) Collapsed() bool {
	return s.ShellOutputsCollapsed > 0 || s.PatchInputsCollapsed > 0 || s.EstimatedSavedTokens > 0
}

func (s compactionOverflowRepairStats) Add(other compactionOverflowRepairStats) compactionOverflowRepairStats {
	return compactionOverflowRepairStats{
		ShellOutputsCollapsed: s.ShellOutputsCollapsed + other.ShellOutputsCollapsed,
		PatchInputsCollapsed:  s.PatchInputsCollapsed + other.PatchInputsCollapsed,
		EstimatedSavedTokens:  s.EstimatedSavedTokens + other.EstimatedSavedTokens,
	}
}

func collapseCompactionOverflowToolPayloadsAfterSavings(items []llm.ResponseItem, targetSavedTokens int, existingSavedTokens int) ([]llm.ResponseItem, compactionOverflowRepairStats) {
	out := llm.CloneResponseItems(items)
	if targetSavedTokens <= 0 || len(out) == 0 {
		return out, compactionOverflowRepairStats{}
	}
	currentSavedTokens := existingSavedTokens
	if currentSavedTokens >= targetSavedTokens {
		return out, compactionOverflowRepairStats{}
	}

	callTools := compactionOverflowRepairCallTools(out)
	stats := compactionOverflowRepairStats{}
	for idx := range out {
		if currentSavedTokens >= targetSavedTokens {
			break
		}
		item := out[idx]
		switch item.Type {
		case llm.ResponseItemTypeFunctionCallOutput:
			if !isCompactionOverflowRepairShellOutputTool(compactionOverflowRepairToolID(item, callTools)) || isCollapsedCompactionOverflowShellOutput(item.Output) {
				continue
			}
			replacement, saved := collapsedCompactionOverflowShellOutput(item.Output)
			if saved <= 0 {
				continue
			}
			applyCompactionOverflowShellOutputCollapse(&out[idx], replacement)
			stats.ShellOutputsCollapsed++
			stats.EstimatedSavedTokens += saved
			currentSavedTokens += saved
		case llm.ResponseItemTypeCustomToolCall:
			if compactionOverflowRepairToolID(item, callTools) != toolspec.ToolPatch || item.CustomInput == compactionOverflowCollapsedText {
				continue
			}
			replacement, saved := collapsedCompactionOverflowPatchInput(item.CustomInput)
			if saved <= 0 {
				continue
			}
			applyCompactionOverflowPatchInputCollapse(&out[idx], replacement)
			stats.PatchInputsCollapsed++
			stats.EstimatedSavedTokens += saved
			currentSavedTokens += saved
		}
	}
	if stats.Collapsed() {
		out = llm.PrepareOpenAIInputItems(out)
	}
	return out, stats
}

func applyCompactionOverflowShellOutputCollapse(item *llm.ResponseItem, replacement json.RawMessage) {
	if item == nil {
		return
	}
	item.Output = replacement
	item.Raw = nil
}

func applyCompactionOverflowPatchInputCollapse(item *llm.ResponseItem, replacement string) {
	if item == nil {
		return
	}
	item.CustomInput = replacement
	item.Raw = nil
}

func compactionOverflowRepairTargetTokens(contextWindowTokens int, repairAttempt int) int {
	if repairAttempt <= 0 || repairAttempt > len(compactionOverflowRepairTargetPercents) {
		return 0
	}
	if contextWindowTokens <= 0 {
		contextWindowTokens = defaultContextWindowTokens
	}
	return (contextWindowTokens * compactionOverflowRepairTargetPercents[repairAttempt-1]) / 100
}

func compactionOverflowRepairCallTools(items []llm.ResponseItem) map[string]toolspec.ID {
	out := make(map[string]toolspec.ID, len(items))
	for _, item := range items {
		if item.Type != llm.ResponseItemTypeFunctionCall && item.Type != llm.ResponseItemTypeCustomToolCall {
			continue
		}
		id := compactionOverflowRepairCallID(item)
		toolID, ok := toolspec.ParseID(item.Name)
		if id == "" || !ok {
			continue
		}
		out[id] = toolID
	}
	return out
}

func compactionOverflowRepairCallID(item llm.ResponseItem) string {
	if callID := strings.TrimSpace(item.CallID); callID != "" {
		return callID
	}
	return strings.TrimSpace(item.ID)
}

func compactionOverflowRepairToolID(item llm.ResponseItem, callTools map[string]toolspec.ID) toolspec.ID {
	if toolID, ok := toolspec.ParseID(item.Name); ok {
		return toolID
	}
	if callTools == nil {
		return ""
	}
	return callTools[compactionOverflowRepairCallID(item)]
}

func isCompactionOverflowRepairShellOutputTool(toolID toolspec.ID) bool {
	return toolID == toolspec.ToolExecCommand || toolID == toolspec.ToolWriteStdin
}

func collapsedCompactionOverflowShellOutput(output json.RawMessage) (json.RawMessage, int) {
	before := estimateTokensFromBytes(len(output))
	if outputTokens, ok := estimateStructuredOutputTokens(output); ok {
		before = outputTokens
	}
	replacement, err := json.Marshal(compactionOverflowCollapsedText)
	if err != nil {
		replacement = compactionOverflowCollapsedJSON
	}
	after := estimateTokensFromBytes(len(replacement))
	saved := before - after
	if saved <= 0 {
		return nil, 0
	}
	return replacement, saved
}

func isCollapsedCompactionOverflowShellOutput(output json.RawMessage) bool {
	var payload string
	return json.Unmarshal(output, &payload) == nil && payload == compactionOverflowCollapsedText
}

func collapsedCompactionOverflowPatchInput(input string) (string, int) {
	before := estimateTokensFromBytes(len(input))
	replacement := compactionOverflowCollapsedText
	after := estimateTokensFromBytes(len(replacement))
	saved := before - after
	if saved <= 0 {
		return "", 0
	}
	return replacement, saved
}
