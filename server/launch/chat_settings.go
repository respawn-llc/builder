package launch

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"core/server/auth"
	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

type PreparedChatSettings struct {
	Baseline                session.ChatSettings
	SupportedThinkingValues []string
	FastAvailable           bool
	QuestionsAvailable      bool
}

type ChatAgentCatalogOptions struct {
	SkipProviderReadinessValidation bool
}

type PreparedChatAgentCatalogEntry struct {
	Choice               serverapi.ChatSettingsAgentChoice
	PreparedTarget       PreparedBaseTarget
	ProviderCapabilities llm.ProviderCapabilities
	Settings             PreparedChatSettings
	comparison           preparedChatAgentComparison
}

type PreparedChatAgentCatalog struct {
	entries []PreparedChatAgentCatalogEntry
}

type preparedChatAgentComparison struct {
	Settings             config.Settings
	Tools                []toolspec.ID
	ProviderCapabilities llm.ProviderCapabilities
	ChatSettings         PreparedChatSettings
}

func PrepareChatAgentCatalog(
	app config.App,
	authState auth.State,
	options ChatAgentCatalogOptions,
) (PreparedChatAgentCatalog, error) {
	selectors := append(
		[]string{config.DefaultSubagentRole},
		config.AvailableSubagentRoleNames(app.Settings, false)...,
	)
	entries := make([]PreparedChatAgentCatalogEntry, 0, len(selectors))
	for _, selector := range selectors {
		entry, err := prepareChatAgentCatalogEntry(app, authState, selector, options)
		if err != nil {
			return PreparedChatAgentCatalog{}, err
		}
		if len(entries) > 0 && reflect.DeepEqual(entries[0].comparison, entry.comparison) {
			continue
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 || entries[0].Choice.Role != config.DefaultSubagentRole {
		return PreparedChatAgentCatalog{}, errors.New("prepared Chat Agent catalog is missing default")
	}
	defaultPrompts := entries[0].PreparedTarget.Settings.SystemPromptFiles
	for index := range entries {
		entries[index].Choice.CustomSystemPrompt = !slices.Equal(
			entries[index].PreparedTarget.Settings.SystemPromptFiles,
			defaultPrompts,
		)
	}
	return PreparedChatAgentCatalog{entries: entries}, nil
}

func (c PreparedChatAgentCatalog) Choices() []serverapi.ChatSettingsAgentChoice {
	choices := make([]serverapi.ChatSettingsAgentChoice, 0, len(c.entries))
	for _, entry := range c.entries {
		choices = append(choices, cloneChatAgentCatalogEntry(entry).Choice)
	}
	return choices
}

func (c PreparedChatAgentCatalog) Entries() []PreparedChatAgentCatalogEntry {
	entries := make([]PreparedChatAgentCatalogEntry, 0, len(c.entries))
	for _, entry := range c.entries {
		entries = append(entries, cloneChatAgentCatalogEntry(entry))
	}
	return entries
}

func (c PreparedChatAgentCatalog) Lookup(agent string) (PreparedChatAgentCatalogEntry, bool) {
	agent = normalizeChatAgentCatalogSelector(agent)
	for _, entry := range c.entries {
		if entry.Choice.Role == agent {
			return cloneChatAgentCatalogEntry(entry), true
		}
	}
	return PreparedChatAgentCatalogEntry{}, false
}

func prepareChatAgentCatalogEntry(
	app config.App,
	authState auth.State,
	selector string,
	options ChatAgentCatalogOptions,
) (PreparedChatAgentCatalogEntry, error) {
	selector = normalizeChatAgentCatalogSelector(selector)
	if selector == "" {
		return PreparedChatAgentCatalogEntry{}, chatAgentPreparationError(
			selector,
			serverapi.ChatSettingsAgentInvalidConfiguration,
		)
	}
	role := selector
	prepared, err := PrepareRunPromptOverridesWithContext(
		app,
		serverapi.RunPromptOverrides{AgentRole: &role},
		authState,
		RunPromptPreparationContext{
			SkipProviderReadinessValidation: options.SkipProviderReadinessValidation,
		},
	)
	if err != nil {
		return PreparedChatAgentCatalogEntry{}, chatAgentPreparationError(
			selector,
			classifyChatAgentPreparationError(err),
		)
	}
	target := prepared.BaseTarget
	if selector != config.DefaultSubagentRole {
		target = nil
		if prepared.NamedTarget != nil {
			target = &PreparedBaseTarget{
				Settings:     prepared.NamedTarget.Settings,
				Source:       prepared.NamedTarget.Source,
				EnabledTools: prepared.NamedTarget.EnabledTools,
			}
		}
	}
	if target == nil {
		return PreparedChatAgentCatalogEntry{}, chatAgentPreparationError(
			selector,
			serverapi.ChatSettingsAgentInternalPreparation,
		)
	}
	var capabilities llm.ProviderCapabilities
	fastAvailable := prepared.FastAvailable
	if options.SkipProviderReadinessValidation {
		capabilities, _ = llm.ProviderCapabilitiesFromOverride(target.Settings.ProviderCapabilities)
		fastAvailable = true
	} else {
		if !prepared.ProviderReadinessValidated {
			return PreparedChatAgentCatalogEntry{}, chatAgentPreparationError(
				selector,
				serverapi.ChatSettingsAgentInternalPreparation,
			)
		}
		capabilities, err = llm.ProviderCapabilitiesForSettings(authState, target.Settings)
		if err != nil {
			return PreparedChatAgentCatalogEntry{}, chatAgentPreparationError(
				selector,
				classifyChatAgentPreparationError(err),
			)
		}
		fastAvailable = llm.SupportsFastModeProvider(capabilities)
	}
	settings, err := PrepareChatSettingsForPreparedTarget(*target, fastAvailable)
	if err != nil {
		return PreparedChatAgentCatalogEntry{}, chatAgentPreparationError(
			selector,
			serverapi.ChatSettingsAgentInvalidConfiguration,
		)
	}
	tools := append([]toolspec.ID(nil), target.EnabledTools...)
	entry := PreparedChatAgentCatalogEntry{
		Choice: serverapi.ChatSettingsAgentChoice{
			Role:               selector,
			Model:              strings.TrimSpace(target.Settings.Model),
			Thinking:           settings.Baseline.Thinking,
			Tools:              toolspec.IDStrings(tools),
			CustomCapabilities: chatAgentHasExplicitCapabilities(app.Settings, selector),
			AgentCallable:      chatAgentCallable(app.Settings, selector),
		},
		PreparedTarget: PreparedBaseTarget{
			Settings:     cloneSettings(target.Settings),
			Source:       cloneSourceReport(target.Source),
			EnabledTools: tools,
		},
		ProviderCapabilities: capabilities,
		Settings:             clonePreparedChatSettings(settings),
	}
	entry.comparison = preparedChatAgentComparison{
		Settings:             normalizeComparableSettings(target.Settings),
		Tools:                append([]toolspec.ID(nil), tools...),
		ProviderCapabilities: capabilities,
		ChatSettings:         clonePreparedChatSettings(settings),
	}
	return entry, nil
}

func normalizeChatAgentCatalogSelector(agent string) string {
	if strings.EqualFold(strings.TrimSpace(agent), config.DefaultSubagentRole) {
		return config.DefaultSubagentRole
	}
	return config.NormalizeSubagentRole(agent)
}

func classifyChatAgentPreparationError(err error) serverapi.ChatSettingsAgentPreparationCategory {
	var providerSelection *llm.ProviderSelectionError
	if errors.Is(err, llm.ErrUnsupportedProvider) || errors.As(err, &providerSelection) {
		return serverapi.ChatSettingsAgentProviderUnavailable
	}
	if errors.Is(err, errInvalidAgentRole) ||
		errors.Is(err, ErrPatchEditToolsConflict) {
		return serverapi.ChatSettingsAgentInvalidConfiguration
	}
	return serverapi.ChatSettingsAgentInternalPreparation
}

func chatAgentPreparationError(
	agent string,
	category serverapi.ChatSettingsAgentPreparationCategory,
) error {
	return &serverapi.ChatSettingsAgentPreparationError{
		Agent:    agent,
		Category: category,
	}
}

func chatAgentHasExplicitCapabilities(settings config.Settings, selector string) bool {
	if selector == config.DefaultSubagentRole {
		return false
	}
	lookup := config.LookupSubagentRole(settings, selector)
	if lookup.Status != config.SubagentRoleLookupPresent {
		return false
	}
	for key := range lookup.Role.Sources {
		if strings.HasPrefix(key, "model_capabilities.") ||
			strings.HasPrefix(key, "provider_capabilities.") {
			return true
		}
	}
	return false
}

func chatAgentCallable(settings config.Settings, selector string) bool {
	if selector == config.DefaultSubagentRole {
		return true
	}
	lookup := config.LookupSubagentRole(settings, selector)
	return lookup.Status == config.SubagentRoleLookupPresent &&
		config.SubagentRoleCallable(lookup.Role)
}

func cloneChatAgentCatalogEntry(entry PreparedChatAgentCatalogEntry) PreparedChatAgentCatalogEntry {
	entry.Choice.Tools = append([]string(nil), entry.Choice.Tools...)
	entry.PreparedTarget = PreparedBaseTarget{
		Settings:     cloneSettings(entry.PreparedTarget.Settings),
		Source:       cloneSourceReport(entry.PreparedTarget.Source),
		EnabledTools: append([]toolspec.ID(nil), entry.PreparedTarget.EnabledTools...),
	}
	entry.Settings = clonePreparedChatSettings(entry.Settings)
	entry.comparison = preparedChatAgentComparison{
		Settings:             cloneSettings(entry.comparison.Settings),
		Tools:                append([]toolspec.ID(nil), entry.comparison.Tools...),
		ProviderCapabilities: entry.comparison.ProviderCapabilities,
		ChatSettings:         clonePreparedChatSettings(entry.comparison.ChatSettings),
	}
	return entry
}

func clonePreparedChatSettings(settings PreparedChatSettings) PreparedChatSettings {
	settings.SupportedThinkingValues = append(
		[]string(nil),
		settings.SupportedThinkingValues...,
	)
	return settings
}

func PrepareChatSettingsForAgent(app config.App, authState auth.State, agent string) (PreparedChatSettings, error) {
	target, fastAvailable, err := prepareChatSettingsTargetForAgent(app, authState, agent)
	if err != nil {
		return PreparedChatSettings{}, err
	}
	return PrepareChatSettingsForPreparedTarget(target, fastAvailable)
}

func PrepareSessionChatSettingsForAgent(app config.App, authState auth.State, agent string, promptFacing PreparedBaseTarget) (PreparedChatSettings, error) {
	baselineTarget, fastAvailable, err := prepareChatSettingsTargetForAgent(app, authState, agent)
	if err != nil {
		return PreparedChatSettings{}, err
	}
	baseline, err := PrepareChatSettingsForPreparedTarget(baselineTarget, fastAvailable)
	if err != nil {
		return PreparedChatSettings{}, err
	}
	capabilities, err := PrepareChatSettingsForTarget(authState, promptFacing)
	if err != nil {
		return PreparedChatSettings{}, err
	}
	baseline.SupportedThinkingValues = capabilities.SupportedThinkingValues
	baseline.FastAvailable = capabilities.FastAvailable
	baseline.QuestionsAvailable = capabilities.QuestionsAvailable
	baseline.Baseline.Fast = baseline.Baseline.Fast && capabilities.FastAvailable
	baseline.Baseline.Questions = baseline.Baseline.Questions && capabilities.QuestionsAvailable
	return baseline, nil
}

func prepareChatSettingsTargetForAgent(app config.App, authState auth.State, agent string) (PreparedBaseTarget, bool, error) {
	var valid bool
	agent, valid = session.NormalizeChatAgent(agent)
	if !valid {
		return PreparedBaseTarget{}, false, errors.New("Chat Agent is required")
	}
	prepared, err := PrepareRunPromptOverridesWithContext(
		app,
		serverapi.RunPromptOverrides{AgentRole: &agent},
		authState,
		RunPromptPreparationContext{},
	)
	if err != nil {
		return PreparedBaseTarget{}, false, err
	}
	target := prepared.BaseTarget
	if agent != config.DefaultSubagentRole {
		target = nil
		if prepared.NamedTarget != nil {
			target = &PreparedBaseTarget{
				Settings:     prepared.NamedTarget.Settings,
				Source:       prepared.NamedTarget.Source,
				EnabledTools: prepared.NamedTarget.EnabledTools,
			}
		}
	}
	if target == nil {
		return PreparedBaseTarget{}, false, fmt.Errorf("prepare Chat Agent %q returned no target", agent)
	}
	return *target, prepared.FastAvailable, nil
}

func PrepareChatSettingsForTarget(authState auth.State, target PreparedBaseTarget) (PreparedChatSettings, error) {
	capabilities, err := llm.ProviderCapabilitiesForSettings(authState, target.Settings)
	if err != nil {
		return PreparedChatSettings{}, err
	}
	return PrepareChatSettingsForPreparedTarget(target, llm.SupportsFastModeProvider(capabilities))
}

func PrepareChatSettingsForTargetWithoutProviderReadiness(target PreparedBaseTarget) (PreparedChatSettings, error) {
	return PrepareChatSettingsForPreparedTarget(target, true)
}

func PrepareChatSettingsForPreparedTarget(target PreparedBaseTarget, fastAvailable bool) (PreparedChatSettings, error) {
	supervisor, valid := runtime.NormalizeReviewerFrequency(target.Settings.Reviewer.Frequency)
	thinking := strings.TrimSpace(target.Settings.ThinkingLevel)
	if !valid || thinking == "" {
		return PreparedChatSettings{}, errors.New("prepared Chat settings are invalid")
	}
	questionsAvailable := slices.Contains(target.EnabledTools, toolspec.ToolAskQuestion)
	return PreparedChatSettings{
		Baseline: session.ChatSettings{
			Supervisor:     supervisor,
			Thinking:       thinking,
			Fast:           target.Settings.PriorityRequestMode && fastAvailable,
			Questions:      runtime.DefaultQuestionsEnabled && questionsAvailable,
			AutoCompaction: runtime.DefaultAutoCompactionEnabled,
		},
		SupportedThinkingValues: supportedChatThinkingValues(target.Settings.Model, thinking),
		FastAvailable:           fastAvailable,
		QuestionsAvailable:      questionsAvailable,
	}, nil
}

func supportedChatThinkingValues(model string, configured string) []string {
	values := llm.SupportedThinkingLevelsModel(model)
	if _, known := llm.LookupModelCapabilityContract(model); known {
		return values
	}
	configured = strings.TrimSpace(configured)
	if configured != "" && !slices.Contains(values, configured) {
		values = append(values, configured)
	}
	return values
}

func SupportedChatThinkingValues(model string, configured string) []string {
	return supportedChatThinkingValues(model, configured)
}

func ResolveSessionChatSettings(meta session.Meta, current config.Settings) (session.ChatSettings, error) {
	defaultSettings := config.DefaultOnboardingSettings()
	currentOverrides := &session.ChatSettingsOverrides{
		Fast: &current.PriorityRequestMode,
	}
	if supervisor := strings.TrimSpace(current.Reviewer.Frequency); supervisor != "" {
		currentOverrides.Supervisor = &supervisor
	}
	if thinking := strings.TrimSpace(current.ThinkingLevel); thinking != "" {
		currentOverrides.Thinking = &thinking
	}
	return session.ResolveEffectiveChatSettings(
		meta.ChatSettings,
		currentOverrides,
		session.ChatSettings{
			Supervisor:     defaultSettings.Reviewer.Frequency,
			Thinking:       defaultSettings.ThinkingLevel,
			Fast:           defaultSettings.PriorityRequestMode,
			Questions:      runtime.DefaultQuestionsEnabled,
			AutoCompaction: runtime.DefaultAutoCompactionEnabled,
		},
	)
}

func applySessionChatSettings(meta session.Meta, active config.Settings) (config.Settings, session.ChatSettings, error) {
	settings, err := ResolveSessionChatSettings(meta, active)
	if err != nil {
		return config.Settings{}, session.ChatSettings{}, err
	}
	return applyResolvedSessionChatSettings(active, settings, nil, false, nil)
}

func applyResolvedSessionChatSettings(
	active config.Settings,
	settings session.ChatSettings,
	thinkingOverride *string,
	validateThinking bool,
	fastAvailable *bool,
) (config.Settings, session.ChatSettings, error) {
	thinking := settings.Thinking
	if thinkingOverride != nil {
		thinking = *thinkingOverride
	}
	if validateThinking && !slices.Contains(supportedChatThinkingValues(active.Model, thinking), thinking) {
		return config.Settings{}, session.ChatSettings{}, fmt.Errorf(
			"Session Chat Thinking %q is unsupported by model %q",
			thinking,
			active.Model,
		)
	}
	if settings.Fast && fastAvailable != nil && !*fastAvailable {
		return config.Settings{}, session.ChatSettings{}, errors.New(
			"Session Chat Fast mode is unsupported by the active provider",
		)
	}
	active.Reviewer.Frequency = settings.Supervisor
	active.ThinkingLevel = thinking
	active.PriorityRequestMode = settings.Fast
	return active, settings, nil
}
