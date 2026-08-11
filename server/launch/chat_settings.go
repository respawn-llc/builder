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
	Baseline           session.ChatSettings
	FastAvailable      bool
	QuestionsAvailable bool
}

func PrepareChatSettingsForAgent(app config.App, authState auth.State, agent string) (PreparedChatSettings, error) {
	agent = strings.TrimSpace(agent)
	if strings.EqualFold(agent, config.DefaultSubagentRole) {
		agent = config.DefaultSubagentRole
	} else {
		agent = config.NormalizeSubagentRole(agent)
	}
	if agent == "" {
		return PreparedChatSettings{}, errors.New("Chat Agent is required")
	}
	prepared, err := PrepareRunPromptOverridesWithContext(
		app,
		serverapi.RunPromptOverrides{AgentRole: &agent},
		authState,
		RunPromptPreparationContext{},
	)
	if err != nil {
		return PreparedChatSettings{}, err
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
		return PreparedChatSettings{}, fmt.Errorf("prepare Chat Agent %q returned no target", agent)
	}
	capabilities, err := llm.ProviderCapabilitiesForSettings(authState, target.Settings)
	if err != nil {
		return PreparedChatSettings{}, err
	}
	supervisor, valid := runtime.NormalizeReviewerFrequency(target.Settings.Reviewer.Frequency)
	thinking := strings.TrimSpace(target.Settings.ThinkingLevel)
	if !valid || thinking == "" {
		return PreparedChatSettings{}, errors.New("prepared Chat settings are invalid")
	}
	fastAvailable := llm.SupportsFastModeProvider(capabilities)
	questionsAvailable := slices.Contains(target.EnabledTools, toolspec.ToolAskQuestion)
	return PreparedChatSettings{
		Baseline: session.ChatSettings{
			Supervisor:     supervisor,
			Thinking:       thinking,
			Fast:           target.Settings.PriorityRequestMode && fastAvailable,
			Questions:      runtime.DefaultQuestionsEnabled && questionsAvailable,
			AutoCompaction: runtime.DefaultAutoCompactionEnabled,
		},
		FastAvailable:      fastAvailable,
		QuestionsAvailable: questionsAvailable,
	}, nil
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
