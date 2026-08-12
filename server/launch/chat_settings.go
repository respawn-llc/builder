package launch

import (
	"errors"
	"fmt"
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

func PrepareChatSettingsForAgent(app config.App, authState auth.State, agent string) (PreparedChatSettings, error) {
	target, err := prepareChatSettingsTargetForAgent(app, authState, agent)
	if err != nil {
		return PreparedChatSettings{}, err
	}
	return PrepareChatSettingsForTarget(authState, target)
}

func PrepareSessionChatSettingsForAgent(app config.App, authState auth.State, agent string, promptFacing PreparedBaseTarget) (PreparedChatSettings, error) {
	baselineTarget, err := prepareChatSettingsTargetForAgent(app, authState, agent)
	if err != nil {
		return PreparedChatSettings{}, err
	}
	baseline, err := PrepareChatSettingsForTarget(authState, baselineTarget)
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

func prepareChatSettingsTargetForAgent(app config.App, authState auth.State, agent string) (PreparedBaseTarget, error) {
	var valid bool
	agent, valid = session.NormalizeChatAgent(agent)
	if !valid {
		return PreparedBaseTarget{}, errors.New("Chat Agent is required")
	}
	prepared, err := PrepareRunPromptOverridesWithContext(
		app,
		serverapi.RunPromptOverrides{AgentRole: &agent},
		authState,
		RunPromptPreparationContext{},
	)
	if err != nil {
		return PreparedBaseTarget{}, err
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
		return PreparedBaseTarget{}, fmt.Errorf("prepare Chat Agent %q returned no target", agent)
	}
	return *target, nil
}

func PrepareChatSettingsForTarget(authState auth.State, target PreparedBaseTarget) (PreparedChatSettings, error) {
	capabilities, err := llm.ProviderCapabilitiesForSettings(authState, target.Settings)
	if err != nil {
		return PreparedChatSettings{}, err
	}
	return prepareChatSettingsForTarget(target, llm.SupportsFastModeProvider(capabilities))
}

func PrepareChatSettingsForTargetWithoutProviderReadiness(target PreparedBaseTarget) (PreparedChatSettings, error) {
	return prepareChatSettingsForTarget(target, true)
}

func prepareChatSettingsForTarget(target PreparedBaseTarget, fastAvailable bool) (PreparedChatSettings, error) {
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
	return applyResolvedSessionChatSettings(active, settings, nil)
}

func applyResolvedSessionChatSettings(
	active config.Settings,
	settings session.ChatSettings,
	thinkingOverride *string,
) (config.Settings, session.ChatSettings, error) {
	thinking := settings.Thinking
	if thinkingOverride != nil {
		thinking = *thinkingOverride
	}
	if !slices.Contains(supportedChatThinkingValues(active.Model, thinking), thinking) {
		return config.Settings{}, session.ChatSettings{}, fmt.Errorf(
			"Session Chat Thinking %q is unsupported by model %q",
			thinking,
			active.Model,
		)
	}
	active.Reviewer.Frequency = settings.Supervisor
	active.ThinkingLevel = thinking
	active.PriorityRequestMode = settings.Fast
	return active, settings, nil
}
