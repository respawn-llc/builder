package status

import (
	"core/server/llm"
	"core/shared/config"
	"core/shared/textutil"
	"strings"
)

func ConfigOverrideSources(src config.SourceReport) []string {
	present := map[string]bool{}
	for _, source := range src.Sources {
		switch strings.TrimSpace(source) {
		case "env":
			present["ENV"] = true
		case "cli":
			present["CLI ARGS"] = true
		}
	}
	ordered := make([]string, 0, len(present))
	for _, label := range []string{"ENV", "CLI ARGS"} {
		if present[label] {
			ordered = append(ordered, label)
		}
	}
	return ordered
}

func ModelSummary(req Request) string {
	resolved := strings.TrimSpace(req.ModelName)
	configured, _ := textutil.OptionalTrimmed(req.ConfiguredModelName)
	modelName := resolved
	if modelName == "" {
		modelName = configured
	}
	if modelName == "" {
		modelName = "<unset>"
	}
	parts := []string{ModelDisplaySummary(modelName, req.ThinkingLevel)}
	if req.FastModeAvailable && req.FastModeEnabled {
		parts = append(parts, "fast")
	}
	return strings.Join(parts, " ")
}

func ModelDisplaySummary(modelName, thinkingLevel string) string {
	return llm.ModelDisplayLabel(strings.TrimSpace(modelName), strings.TrimSpace(thinkingLevel))
}

func SupervisorLabel(enabled bool, mode string) string {
	if !enabled {
		return "off"
	}
	trimmed := strings.TrimSpace(mode)
	if trimmed == "" || trimmed == "off" {
		return "on"
	}
	return trimmed
}
