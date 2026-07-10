package config

import (
	"core/shared/theme"
	"core/shared/toolspec"
	"sort"
	"strconv"
	"strings"
)

const (
	defaultModel                         = "gpt-5.6-sol"
	defaultThinkingLevel                 = "medium"
	defaultModelVerbosity                = ModelVerbosityLow
	defaultTheme                         = theme.Auto
	defaultModelContextWindow            = 372_000
	minimumModelContextWindow            = 40_000
	defaultModelTimeoutSeconds           = 400
	defaultMinimumExecToBgSec            = 15
	defaultShellOutputMaxChars           = 16_000
	defaultBGShellsOutput                = "default"
	defaultShellPostprocessingMode       = ShellPostprocessingModeBuiltin
	defaultCacheWarningMode              = CacheWarningModeDefault
	defaultWorkflowCompletionMode        = WorkflowCompletionModeAuto
	defaultWorkflowConcurrency           = 5
	defaultWorkflowInvalidCompletionCap  = 5
	defaultCompactionThreshold           = defaultModelContextWindow * 95 / 100
	defaultPreSubmitCompactionLeadTokens = DefaultPreSubmitRunwayTokens
	defaultReviewerFrequency             = "edits"
	defaultReviewerTimeoutSec            = 120
	defaultCompactionMode                = "local"
	defaultServerHost                    = "127.0.0.1"
	defaultServerPort                    = 53082
	defaultWorktreeSetupTimeoutSeconds   = 60
)

func settingsTOMLForOnboarding(settings Settings, preservedDefaults map[string]bool) string {
	preserved := map[string]bool{"theme": true}
	for key, preserve := range preservedDefaults {
		if preserve {
			preserved[key] = true
		}
	}
	if strings.TrimSpace(settings.ProviderOverride) != "" {
		// provider_override must round-trip with an explicit model line.
		preserved["model"] = true
	}
	return settingsTOMLWithRenderingOptions(settings, true, preserved, map[string]bool{"debug": true})
}

func onboardingDefaultSettingsTOML(selectedTheme string) string {
	settings := configRegistry.defaultState().Settings
	if normalized := theme.Normalize(selectedTheme); normalized != "" {
		settings.Theme = normalized
	}
	return settingsTOMLWithRenderingOptions(settings, true, nil, map[string]bool{"debug": true})
}

func settingsTOMLWithRenderingOptions(settings Settings, includeToolSection bool, preservedDefaults map[string]bool, omittedKeys map[string]bool) string {
	state := configRegistry.defaultState()
	state.Settings = settings
	rawLines := configRegistry.defaultLines(state)
	inheritReviewerDefaultsWithSources(&state.Settings, nil)
	lines := configRegistry.defaultLines(state)
	defaultLines := configRegistry.defaultLines(configRegistry.defaultState())
	rootLines := annotateRenderedLines(filterDefaultLines(lines, ""), filterDefaultLines(defaultLines, ""), preservedDefaults)
	if len(omittedKeys) > 0 {
		rootLines = filterRenderedLines(rootLines, omittedKeys)
	}
	timeoutLines := annotateRenderedLines(filterDefaultLines(lines, "timeouts"), filterDefaultLines(defaultLines, "timeouts"), nil)
	worktreeLines := annotateRenderedLines(filterDefaultLines(lines, "worktrees"), filterDefaultLines(defaultLines, "worktrees"), nil)
	workflowLines := annotateRenderedLines(filterDefaultLines(lines, "workflow"), filterDefaultLines(defaultLines, "workflow"), nil)
	reviewerLines := annotateRenderedLines(filterExactSectionLines(lines, "reviewer"), filterExactSectionLines(defaultLines, "reviewer"), nil)

	var out strings.Builder
	out.WriteString("# Edit and restart to apply changes.\n")
	out.WriteString("# Config reference: " + DocsURL + "/config/\n\n")
	writeRootConfigLines(&out, rootLines)
	modelCapabilityLines := activeOptionalSectionLines(filterDefaultLines(lines, "model_capabilities"), filterDefaultLines(defaultLines, "model_capabilities"))
	if len(modelCapabilityLines) > 0 {
		out.WriteString("\n[model_capabilities]\n")
		writeDefaultLines(&out, modelCapabilityLines)
	}
	providerCapabilityLines := activeOptionalSectionLines(filterDefaultLines(lines, "provider_capabilities"), filterDefaultLines(defaultLines, "provider_capabilities"))
	if len(providerCapabilityLines) > 0 {
		out.WriteString("\n[provider_capabilities]\n")
		writeDefaultLines(&out, providerCapabilityLines)
	}
	if includeToolSection {
		out.WriteString("\n[tools]\n")
		out.WriteString("# Leave both patch/edit commented to use Kent's model-based default:\n")
		out.WriteString("# patch for first-party OpenAI or gpt-* models, edit otherwise.\n")
		writeToolLines(&out, state.Settings.EnabledTools)
	}
	shellLines := annotateRenderedLines(filterDefaultLines(lines, "shell"), filterDefaultLines(defaultLines, "shell"), nil)
	if len(shellLines) > 0 {
		out.WriteString("\n[shell]\n")
		writeDefaultLines(&out, shellLines)
	}
	if len(worktreeLines) > 0 {
		out.WriteString("\n[worktrees]\n")
		writeDefaultLines(&out, worktreeLines)
	}
	if len(workflowLines) > 0 {
		out.WriteString("\n[workflow]\n")
		writeDefaultLines(&out, workflowLines)
	}
	if len(timeoutLines) > 0 {
		out.WriteString("\n[timeouts]\n")
		writeDefaultLines(&out, timeoutLines)
	}
	if len(reviewerLines) > 0 || shouldRenderReviewerModel(settings, preservedDefaults) || shouldRenderReviewerThinking(settings, preservedDefaults) {
		out.WriteString("\n[reviewer]\n")
		writeReviewerInheritanceLines(&out, settings, state.Settings, preservedDefaults)
		for _, line := range reviewerLines {
			writeDefaultLines(&out, []defaultConfigLine{line})
		}
	}
	reviewerModelCapabilityLines := activeOptionalSectionLines(filterDefaultLines(rawLines, "reviewer.model_capabilities"), filterDefaultLines(lines, "reviewer.model_capabilities"))
	if len(reviewerModelCapabilityLines) > 0 {
		out.WriteString("\n[reviewer.model_capabilities]\n")
		writeDefaultLines(&out, reviewerModelCapabilityLines)
	}
	reviewerProviderCapabilityDefaultState := state
	reviewerProviderCapabilityDefaultState.Settings.Reviewer.ProviderCapabilities = settings.ProviderCapabilities
	reviewerProviderCapabilityLines := activeOptionalSectionLines(filterDefaultLines(rawLines, "reviewer.provider_capabilities"), filterDefaultLines(configRegistry.defaultLines(reviewerProviderCapabilityDefaultState), "reviewer.provider_capabilities"))
	if len(reviewerProviderCapabilityLines) > 0 {
		out.WriteString("\n[reviewer.provider_capabilities]\n")
		writeDefaultLines(&out, reviewerProviderCapabilityLines)
	}
	writeBuiltInSubagentSections(&out)
	writeSkillTogglesSection(&out, state.Settings.SkillToggles)
	return out.String()
}

func writeBuiltInSubagentSections(builder *strings.Builder) {
	if builder == nil {
		return
	}
	builder.WriteString("\n[subagents.fast]\n")
	builder.WriteString("# inherits all main settings unless overridden\n")
	builder.WriteString("# agent_callable = true # set false to hide/block this role from Kent-session subagent calls\n")
	builder.WriteString("# description = \"\" # model-visible role description for future/catalog uses\n")
	builder.WriteString("# model = \"gpt-5.6-terra\" # built-in heuristic on exact OpenAI first-party setups\n")
	builder.WriteString("# thinking_level = \"low\" # built-in heuristic on exact OpenAI first-party setups\n")
	builder.WriteString("# priority_request_mode = true # built-in heuristic on exact OpenAI first-party setups\n")
	builder.WriteString("# model_context_window = 372000 # built-in heuristic on exact OpenAI first-party setups\n")
}

func DefaultModel() string {
	return defaultModel
}

func filterRenderedLines(lines []defaultConfigLine, omittedKeys map[string]bool) []defaultConfigLine {
	if len(lines) == 0 || len(omittedKeys) == 0 {
		return lines
	}
	filtered := make([]defaultConfigLine, 0, len(lines))
	for _, line := range lines {
		if omittedKeys[strings.Join(line.Path, ".")] {
			continue
		}
		filtered = append(filtered, line)
	}
	return filtered
}

func annotateRenderedLines(lines []defaultConfigLine, defaults []defaultConfigLine, preserved map[string]bool) []defaultConfigLine {
	if len(lines) == 0 {
		return nil
	}
	defaultValues := make(map[string]string, len(defaults))
	for _, line := range defaults {
		defaultValues[strings.Join(line.Path, ".")] = renderTOMLValue(line.Value)
	}
	annotated := make([]defaultConfigLine, 0, len(lines))
	for _, line := range lines {
		key := strings.Join(line.Path, ".")
		commented := false
		if preserved == nil || !preserved[key] {
			if defaultValue, ok := defaultValues[key]; ok && defaultValue == renderTOMLValue(line.Value) {
				commented = true
			}
		}
		annotated = append(annotated, defaultConfigLine{Path: line.Path, Value: line.Value, Commented: commented})
	}
	return annotated
}

func activeOptionalSectionLines(lines []defaultConfigLine, defaults []defaultConfigLine) []defaultConfigLine {
	annotated := annotateRenderedLines(lines, defaults, nil)
	active := make([]defaultConfigLine, 0, len(annotated))
	for _, line := range annotated {
		if !line.Commented {
			active = append(active, defaultConfigLine{Path: line.Path, Value: line.Value})
		}
	}
	return active
}

func writeRootConfigLines(builder *strings.Builder, lines []defaultConfigLine) {
	for _, line := range lines {
		if len(line.Path) > 0 && line.Path[0] == "model" {
			builder.WriteString("# model changes are applied only when creating a new session\n")
		}
		writeDefaultLines(builder, []defaultConfigLine{line})
	}
}

func writeToolLines(builder *strings.Builder, enabledTools map[toolspec.ID]bool) {
	defaults := defaultEnabledToolMap()
	ids := toolspec.CatalogIDs()
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		configured, ok := enabledTools[id]
		if !ok {
			configured = defaults[id]
		}
		writeDefaultLines(builder, []defaultConfigLine{{Path: []string{"tools", toolspec.ConfigName(id)}, Value: configured, Commented: configured == defaults[id]}})
	}
}

func shouldRenderReviewerModel(settings Settings, preserved map[string]bool) bool {
	return (preserved != nil && preserved["reviewer.model"]) || strings.TrimSpace(settings.Reviewer.Model) != ""
}

func shouldRenderReviewerThinking(settings Settings, preserved map[string]bool) bool {
	return (preserved != nil && preserved["reviewer.thinking_level"]) || strings.TrimSpace(settings.Reviewer.ThinkingLevel) != ""
}

func writeReviewerInheritanceLines(builder *strings.Builder, raw Settings, effective Settings, preserved map[string]bool) {
	modelCommented := !(preserved != nil && preserved["reviewer.model"]) && strings.TrimSpace(raw.Reviewer.Model) == ""
	writeCommentedAssignment(builder, "model", effective.Reviewer.Model, modelCommented, "# inherited from main model unless overridden")
	thinkingCommented := !(preserved != nil && preserved["reviewer.thinking_level"]) && strings.TrimSpace(raw.Reviewer.ThinkingLevel) == ""
	thinkingValue := any(effective.Reviewer.ThinkingLevel)
	if preserved != nil && preserved["reviewer.thinking_level"] && strings.TrimSpace(raw.Reviewer.ThinkingLevel) == "" {
		thinkingValue = raw.Reviewer.ThinkingLevel
	}
	writeCommentedAssignment(builder, "thinking_level", thinkingValue, thinkingCommented, "# inherited from main thinking_level unless overridden")
	verbosityCommented := !(preserved != nil && preserved["reviewer.model_verbosity"]) && strings.TrimSpace(string(raw.Reviewer.ModelVerbosity)) == ""
	writeCommentedAssignment(builder, "model_verbosity", effective.Reviewer.ModelVerbosity, verbosityCommented, "# inherited from main model_verbosity unless overridden")
	providerCommented := !(preserved != nil && preserved["reviewer.provider_override"]) && strings.TrimSpace(raw.Reviewer.ProviderOverride) == ""
	writeCommentedAssignment(builder, "provider_override", effective.Reviewer.ProviderOverride, providerCommented, "# inherited from main provider_override unless overridden")
	baseURLCommented := !(preserved != nil && preserved["reviewer.openai_base_url"]) && strings.TrimSpace(raw.Reviewer.OpenAIBaseURL) == ""
	writeCommentedAssignment(builder, "openai_base_url", effective.Reviewer.OpenAIBaseURL, baseURLCommented, "# inherited from main openai_base_url for OpenAI-family reviewer providers")
	contextCommented := !(preserved != nil && preserved["reviewer.model_context_window"]) && raw.Reviewer.ModelContextWindow <= 0
	writeCommentedAssignment(builder, "model_context_window", effective.Reviewer.ModelContextWindow, contextCommented, "# inherited from main model_context_window unless overridden")
}

func writeCommentedAssignment(builder *strings.Builder, key string, value any, commented bool, trailingComment string) {
	if commented {
		builder.WriteString("# ")
	}
	builder.WriteString(key)
	builder.WriteString(" = ")
	builder.WriteString(renderTOMLValue(value))
	if strings.TrimSpace(trailingComment) != "" {
		builder.WriteByte(' ')
		builder.WriteString(trailingComment)
	}
	builder.WriteByte('\n')
}

func writeSkillTogglesSection(builder *strings.Builder, skillToggles map[string]bool) {
	if len(skillToggles) == 0 {
		return
	}
	keys := make([]string, 0, len(skillToggles))
	for key := range skillToggles {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	builder.WriteString("[skills]\n")
	for _, key := range keys {
		builder.WriteString(strconv.Quote(key))
		builder.WriteString(" = ")
		builder.WriteString(renderTOMLValue(skillToggles[key]))
		builder.WriteByte('\n')
	}
}
