package app

import (
	"strings"

	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
)

type chatSettingsMutationResult struct {
	response   serverapi.ChatSettingsMutationResponse
	projection chatSettingsMutationProjection
}

type chatSettingsMutationProjection struct {
	modelName             string
	agentRole             *string
	thinkingLevel         string
	fastModeAvailable     bool
	fastModeEnabled       bool
	reviewerMode          string
	reviewerEnabled       bool
	questionsEnabled      bool
	autoCompactionEnabled bool
	compactionMode        string
	compactionCount       int
	modelContractLocked   bool
	contextUsage          clientui.RuntimeContextUsage
}

func projectChatSettingsMutation(
	response serverapi.ChatSettingsMutationResponse,
	current clientui.RuntimeMainView,
) chatSettingsMutationProjection {
	agentRole := chatSettingsAgentRole(response.Settings.SelectedAgent.Role)
	contextUsage := runtimeContextUsageFromChatContext(response.Context)
	if chatSettingsAgentRolesEqual(current.Session.AgentRole, agentRole) {
		contextUsage.CacheHitPercent = current.Status.ContextUsage.CacheHitPercent
		contextUsage.HasCacheHitPercentage = current.Status.ContextUsage.HasCacheHitPercentage
	}
	return chatSettingsMutationProjection{
		modelName:             response.Settings.SelectedAgent.Model,
		agentRole:             agentRole,
		thinkingLevel:         response.Settings.SelectedAgent.Thinking,
		fastModeAvailable:     response.Settings.Fast != nil,
		fastModeEnabled:       response.Settings.Fast != nil && response.Settings.Fast.Value,
		reviewerMode:          string(response.Settings.Supervisor.Value),
		reviewerEnabled:       response.Settings.Supervisor.Value != serverapi.ChatSettingsSupervisorOff,
		questionsEnabled:      response.Settings.Questions.Enabled,
		autoCompactionEnabled: response.Context.AutoCompactionEnabled,
		compactionMode:        string(response.Context.CompactionMode),
		compactionCount:       int(response.Context.CompletedCompactionCount),
		modelContractLocked:   response.Settings.AgentLocked,
		contextUsage:          contextUsage,
	}
}

func (p chatSettingsMutationProjection) applyToRuntimeMainView(view *clientui.RuntimeMainView) {
	view.Session.AgentRole = p.agentRole
	view.Status.ThinkingLevel = p.thinkingLevel
	view.Status.ReviewerFrequency = p.reviewerMode
	view.Status.ReviewerEnabled = p.reviewerEnabled
	view.Status.FastModeAvailable = p.fastModeAvailable
	view.Status.FastModeEnabled = p.fastModeEnabled
	view.Status.QuestionsEnabled = p.questionsEnabled
	view.Status.AutoCompactionEnabled = p.autoCompactionEnabled
	view.Status.CompactionMode = p.compactionMode
	view.Status.CompactionCount = p.compactionCount
	view.Status.ContextUsage = p.contextUsage
}

func (p chatSettingsMutationProjection) applyToUIModel(m *uiModel) {
	if m == nil {
		return
	}
	m.modelName = p.modelName
	m.agentRole = p.agentRole
	m.thinkingLevel = p.thinkingLevel
	m.fastModeAvailable = p.fastModeAvailable
	m.fastModeEnabled = p.fastModeEnabled
	m.reviewerMode = p.reviewerMode
	m.reviewerEnabled = p.reviewerEnabled
	m.questionsEnabled = p.questionsEnabled
	m.autoCompactionEnabled = p.autoCompactionEnabled
	m.compactionMode = p.compactionMode
	m.compactionCount = p.compactionCount
	m.modelContractLocked = p.modelContractLocked
	m.status.snapshot.CompactionCount = p.compactionCount
	m.setRuntimeContextUsage(m.currentRuntimeSessionID(), p.contextUsage)
}

func chatSettingsAgentRole(role string) *string {
	role = strings.TrimSpace(role)
	if role == "" || strings.EqualFold(role, config.DefaultSubagentRole) {
		return nil
	}
	return &role
}

func chatSettingsAgentRolesEqual(current, next *string) bool {
	currentRole := chatSettingsAgentRoleValue(current)
	nextRole := chatSettingsAgentRoleValue(next)
	if currentRole == nil || nextRole == nil {
		return currentRole == nil && nextRole == nil
	}
	return strings.EqualFold(*currentRole, *nextRole)
}

func chatSettingsAgentRoleValue(role *string) *string {
	if role == nil {
		return nil
	}
	return chatSettingsAgentRole(*role)
}

func runtimeContextUsageFromChatContext(contextFacts serverapi.ChatContext) clientui.RuntimeContextUsage {
	return clientui.RuntimeContextUsage{
		UsedTokens:               int(contextFacts.UsedTokens),
		WindowTokens:             int(contextFacts.ContextWindowTokens),
		AutomaticThresholdTokens: int(contextFacts.AutomaticThresholdTokens),
		HasAutomaticThreshold:    true,
	}
}
